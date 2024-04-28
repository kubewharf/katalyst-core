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

package cache

import (
	"fmt"
	"sync"

	v1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/features"
	"k8s.io/kubernetes/pkg/kubelet/cm/cpumanager"
	"k8s.io/kubernetes/pkg/scheduler/framework"

	"github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

var cache *overcommitCache

func init() {
	cache = &overcommitCache{
		nodeCaches: map[string]*NodeCache{},
	}
}

// cache stored node native topology providers and guaranteed requested resource.
// only used in overcommit scenario when kubelet uses native topology strategy.
type overcommitCache struct {
	sync.RWMutex
	nodeCaches map[string]*NodeCache
}

func GetCache() *overcommitCache {
	return cache
}

func (c *overcommitCache) GetNode(name string) (*NodeCache, error) {
	c.RLock()
	defer c.RUnlock()

	node, ok := c.nodeCaches[name]
	if !ok {
		return nil, fmt.Errorf("node %v not found", name)
	}

	return node, nil
}

func (c *overcommitCache) AddPod(pod *v1.Pod) error {
	key, err := framework.GetPodKey(pod)
	if err != nil {
		return err
	}

	c.Lock()
	defer c.Unlock()

	n, ok := c.nodeCaches[pod.Spec.NodeName]
	if !ok {
		n = New()
		c.nodeCaches[pod.Spec.NodeName] = n
	}
	n.AddPod(key, pod)

	return nil
}

func (c *overcommitCache) RemovePod(pod *v1.Pod) error {
	key, err := framework.GetPodKey(pod)
	if err != nil {
		return err
	}

	c.Lock()
	defer c.Unlock()

	n, ok := c.nodeCaches[pod.Spec.NodeName]
	if !ok {
		klog.ErrorS(nil, "Node not found when trying to remove pod", "node", klog.KRef("", pod.Spec.NodeName), "pod", klog.KObj(pod))
	} else {
		n.RemovePod(key, pod)
	}

	return nil
}

func (c *overcommitCache) AddOrUpdateCNR(cnr *v1alpha1.CustomNodeResource) {
	c.Lock()
	defer c.Unlock()

	n, ok := c.nodeCaches[cnr.Name]
	if !ok {
		n = New()
		c.nodeCaches[cnr.Name] = n
	}

	n.updateTopologyProvider(cnr)
}

func (c *overcommitCache) RemoveCNR(cnr *v1alpha1.CustomNodeResource) {
	c.Lock()
	defer c.Unlock()

	delete(c.nodeCaches, cnr.Name)
}

type NodeCache struct {
	sync.RWMutex

	PodResources map[string]int

	// kubelet topology hint providers from CNR annotation.
	// provider will be cached only if provider policy is available.
	// only used for node resource overcommitment.
	HintProviders map[string]struct{}

	// total guaranteed cpus on node
	GuaranteedCPUs int
}

func New() *NodeCache {
	return &NodeCache{
		PodResources:  map[string]int{},
		HintProviders: map[string]struct{}{},
	}
}

func (n *NodeCache) AddPod(key string, pod *v1.Pod) {
	n.RemovePod(key, pod)
	guaranteedCPUs := native.PodGuaranteedCPUs(pod)

	n.Lock()
	defer n.Unlock()

	n.PodResources[key] = guaranteedCPUs
	n.GuaranteedCPUs += guaranteedCPUs
}

func (n *NodeCache) RemovePod(key string, pod *v1.Pod) {
	n.Lock()
	defer n.Unlock()
	podResource, ok := n.PodResources[key]
	if !ok {
		return
	}

	n.GuaranteedCPUs -= podResource
	delete(n.PodResources, key)
}

func (n *NodeCache) updateTopologyProvider(cnr *v1alpha1.CustomNodeResource) {
	if len(cnr.Annotations) <= 0 {
		return
	}

	if CPUManagerPolicy, ok := cnr.Annotations[consts.KCNRAnnotationCPUManager]; ok {
		if CPUManagerPolicy == string(cpumanager.PolicyStatic) {
			n.HintProviders[string(features.CPUManager)] = struct{}{}
		}
	}

	if memoryManagerPolicy, ok := cnr.Annotations[consts.KCNRAnnotationMemoryManager]; ok {
		if memoryManagerPolicy == "Static" {
			n.HintProviders[string(features.MemoryManager)] = struct{}{}
		}
	}
}

func (n *NodeCache) HintProvidersAvailable() (CPUManager, MemoryManager bool) {
	n.RLock()
	defer n.RUnlock()

	_, ok := n.HintProviders[string(features.CPUManager)]
	if ok {
		CPUManager = true
	}

	_, ok = n.HintProviders[string(features.MemoryManager)]
	if ok {
		MemoryManager = true
	}

	return
}

func (n *NodeCache) GetGuaranteedCPUs() int {
	n.RLock()
	defer n.RUnlock()

	return n.GuaranteedCPUs
}
