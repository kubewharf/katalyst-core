/*
Copyright 2022 The Katalyst Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package topology

import (
	"fmt"
	"sync"

	cadvisorapi "github.com/google/cadvisor/info/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
)

const (
	// maxAllowableNUMANodes specifies the maximum number of NUMA Nodes that
	// the TopologyManager supports on the underlying machine.
	//
	// At present, having more than this number of NUMA Nodes will result in a
	// state explosion when trying to enumerate possible NUMAAffinity masks and
	// generate hints for them. As such, if more NUMA Nodes than this are
	// present on a machine and the TopologyManager is enabled, an error will
	// be returned and the TopologyManager will not be loaded.
	maxAllowableNUMANodes = 8
	// defaultResourceKey is the key to store the default hint for those resourceNames
	// which don't specify hint.
	defaultResourceKey = "*"
)

type Manager interface {
	Admit(pod *v1.Pod) error

	AddHintProvider(provider HintProvider)

	GetAffinity(podUID string, containerName string, resourceName string) TopologyHint

	RemovePod(podUID string)
}

// HintProvider is an interface for components that want to collaborate to
// achieve globally optimal concrete resource alignment with respect to
// NUMA locality.
type HintProvider interface {
	// GetTopologyHints returns a map of resource names to a list of possible
	// concrete resource allocations in terms of NUMA locality hints. Each hint
	// is optionally marked "preferred" and indicates the set of NUMA nodes
	// involved in the hypothetical allocation. The topology manager calls
	// this function for each hint provider, and merges the hints to produce
	// a consensus "best" hint. The hint providers may subsequently query the
	// topology manager to influence actual resource assignment.
	GetTopologyHints(pod *v1.Pod, container *v1.Container) map[string][]TopologyHint
	// GetPodTopologyHints returns a map of resource names to a list of possible
	// concrete resource allocations per Pod in terms of NUMA locality hints.
	GetPodTopologyHints(pod *v1.Pod) map[string][]TopologyHint
	// Allocate triggers resource allocation to occur on the HintProvider after
	// all hints have been gathered and the aggregated Hint is available via a
	// call to GetAffinity().
	Allocate(pod *v1.Pod, container *v1.Container) error
}

type manager struct {
	mutex sync.Mutex
	// Mapping of a Pods mapping of Containers and their TopologyHints
	// Indexed by PodUID to ContainerName
	podTopologyHints map[string]podTopologyHints
	// The list of components registered with the Manager
	hintProviders []HintProvider
	// Topology Manager Policy
	policy Policy
}

func NewManager(topology []cadvisorapi.Node, topologyPolicyName string, alignResources []string) (Manager, error) {
	klog.InfoS("Creating topology manager with policy per scope", "topologyPolicyName", topologyPolicyName)

	var numaNodes []int
	for _, node := range topology {
		numaNodes = append(numaNodes, node.Id)
	}

	if topologyPolicyName != PolicyNone && len(numaNodes) > maxAllowableNUMANodes {
		return nil, fmt.Errorf("unsupported on machines with more than %v NUMA Nodes", maxAllowableNUMANodes)
	}

	var policy Policy
	switch topologyPolicyName {
	case PolicyNone:
		policy = NewNonePolicy()

	case PolicyBestEffort:
		policy = NewBestEffortPolicy(numaNodes)

	case PolicyRestricted:
		policy = NewRestrictedPolicy(numaNodes)

	case PolicySingleNumaNode:
		policy = NewSingleNumaNodePolicy(numaNodes)

	case PolicyNumeric:
		policy = NewNumericPolicy(alignResources)

	default:
		return nil, fmt.Errorf("unknown policy: \"%s\"", topologyPolicyName)
	}

	m := &manager{
		podTopologyHints: map[string]podTopologyHints{},
		hintProviders:    make([]HintProvider, 0),
		policy:           policy,
	}
	return m, nil
}

func (m *manager) Admit(pod *v1.Pod) error {
	if m.policy.Name() == PolicyNone {
		return m.admitPolicyNone(pod)
	}

	for _, container := range append(pod.Spec.InitContainers, pod.Spec.Containers...) {
		bestHint, admit := m.calculateAffinity(pod, &container)
		klog.V(3).Infof("Best TopologyHint, bestHint: %v, pod: %v, containerName: %v", bestHint, klog.KObj(pod), container.Name)

		if !admit {
			err := fmt.Errorf("pod: %v, containerName: %v not admit", pod.Name, container.Name)
			return err
		}
		klog.V(3).Infof("Topology Affinity, bestHint: %v, pod: %v, containerName: %v", bestHint, klog.KObj(pod), container.Name)
		m.setTopologyHints(string(pod.UID), container.Name, bestHint)

		err := m.allocateAlignedResources(pod, &container)
		if err != nil {
			klog.Errorf("allocateAlignedResources fail, pod: %v, containerName: %v, err: %v", klog.KObj(pod), container.Name, err)
			return err
		}
	}

	return nil
}

func (m *manager) admitPolicyNone(pod *v1.Pod) error {
	for _, container := range append(pod.Spec.InitContainers, pod.Spec.Containers...) {
		err := m.allocateAlignedResources(pod, &container)
		if err != nil {
			klog.Errorf("allocateAlignedResources fail, pod: %v, containerName: %v, err: %v", klog.KObj(pod), container.Name, err)
			return err
		}
	}

	return nil
}

func (m *manager) AddHintProvider(provider HintProvider) {
	m.hintProviders = append(m.hintProviders, provider)
}

func (m *manager) GetAffinity(podUID string, containerName string, resourceName string) TopologyHint {
	return m.getTopologyHints(podUID, containerName, resourceName)
}

func (m *manager) calculateAffinity(pod *v1.Pod, container *v1.Container) (map[string]TopologyHint, bool) {
	providersHints := m.accumulateProvidersHints(pod, container)
	bestHint, admit := m.policy.Merge(providersHints)
	klog.V(3).Infof("ContainerTopologyHint, bestHint: %v", bestHint)
	return bestHint, admit
}

func (m *manager) accumulateProvidersHints(pod *v1.Pod, container *v1.Container) []map[string][]TopologyHint {
	var providersHints []map[string][]TopologyHint

	for _, provider := range m.hintProviders {
		// Get the TopologyHints for a Container from a provider.
		hints := provider.GetTopologyHints(pod, container)
		providersHints = append(providersHints, hints)
		klog.V(3).Infof("TopologyHints, hints: %v, pod: %v, containerName: %v", hints, klog.KObj(pod), container.Name)
	}
	return providersHints
}

func (m *manager) allocateAlignedResources(pod *v1.Pod, container *v1.Container) error {
	for _, provider := range m.hintProviders {
		err := provider.Allocate(pod, container)
		if err != nil {
			return err
		}
	}
	return nil
}

func (m *manager) setTopologyHints(podUID string, containerName string, th map[string]TopologyHint) {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	if m.podTopologyHints[podUID] == nil {
		m.podTopologyHints[podUID] = make(map[string]map[string]TopologyHint)
	}
	m.podTopologyHints[podUID][containerName] = th
}

func (m *manager) getTopologyHints(podUID string, containerName string, resourceName string) TopologyHint {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	hint, ok := m.podTopologyHints[podUID][containerName][resourceName]
	if ok {
		return hint
	}
	return m.podTopologyHints[podUID][containerName][defaultResourceKey]
}

func (m *manager) RemovePod(podUID string) {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	klog.V(3).Infof("RemovePod, podUID: %v", podUID)
	delete(m.podTopologyHints, podUID)
}
