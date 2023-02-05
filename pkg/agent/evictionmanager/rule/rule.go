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

package rule

import (
	"k8s.io/klog/v2"
	kubelettypes "k8s.io/kubernetes/pkg/kubelet/types"

	pkgconfig "github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

var evictionScopePriority = map[string]int{
	EvictionScopeForce:  1,
	EvictionScopeMemory: 2,
	EvictionScopeCPU:    3,
}

type EvictionStrategy interface {
	// CandidateSort defines the eviction priority among different EvictPods
	CandidateSort(rpList RuledEvictPodList)

	// CandidateValidate defines whether the given pod is permitted for
	// eviction since some specific EvictPods should always keep running.
	CandidateValidate(rp *RuledEvictPod) bool
}

type EvictionStrategyImpl struct {
	conf     *generic.GenericConfiguration
	compares []general.CmpFunc
}

func NewEvictionStrategyImpl(conf *pkgconfig.Configuration) EvictionStrategy {
	e := &EvictionStrategyImpl{
		conf: conf.GenericConfiguration,
	}

	// if any compare function reach out with a result, returns immediately
	e.compares = []general.CmpFunc{
		e.CompareKatalystQoS,
		e.ComparePriority,
		e.CompareEvictionResource,
		e.ComparePodName,
	}
	return e
}

// CandidateSort defines the sorting rules will be as below
// - katalyst QoS: none-reclaimed > reclaimed
// - pod priority
// - predefined resource priority: e.g. memory > cpu > ...
// - pod names
func (e *EvictionStrategyImpl) CandidateSort(rpList RuledEvictPodList) {
	general.NewMultiSorter(e.compares...).Sort(rpList)
}

// CandidateValidate will try to filter out EvictPods from eviction
// - EvictPods with katalyst SystemQoS
// - EvictPods marked as critical
func (e *EvictionStrategyImpl) CandidateValidate(rp *RuledEvictPod) bool {
	pod := rp.EvictPod.Pod
	if pod == nil {
		return false
	}

	if ok, err := e.conf.CheckSystemQoSForPod(pod); err != nil {
		klog.Errorf("failed to get qos for pod %v, err: %v", pod.Name, err)
		return true
	} else if ok {
		return false
	}

	if kubelettypes.IsCriticalPod(pod) {
		return false
	}
	return true
}

// CompareKatalystQoS compares KatalystQoS for EvictPods, if we failed to
// parse qos level for a pod, we will consider it as none-reclaimed pod.
func (e *EvictionStrategyImpl) CompareKatalystQoS(s1, s2 interface{}) int {
	return covertBoolSortCompareToIntSort(func(s1, s2 interface{}) bool {
		c1, c2 := s1.(*RuledEvictPod), s2.(*RuledEvictPod)

		p1Reclaimed, err := e.conf.CheckReclaimedQoSForPod(c1.Pod)
		if err != nil {
			klog.Errorf("failed to get qos for pod %v, err: %v", c1.Pod.Name, err)
			p1Reclaimed = false
		}

		p2Reclaimed, err := e.conf.CheckReclaimedQoSForPod(c2.Pod)
		if err != nil {
			klog.Errorf("failed to get qos for pod %v, err: %v", c2.Pod.Name, err)
			p2Reclaimed = false
		}

		return !p1Reclaimed && p2Reclaimed
	}, s1, s2)
}

// ComparePriority compares pod priority for EvictPods, if any pod doesn't have
// nominated priority, it will always be inferior to those with priority nominated.
func (e *EvictionStrategyImpl) ComparePriority(s1, s2 interface{}) int {
	return covertBoolSortCompareToIntSort(func(s1, s2 interface{}) bool {
		c1, c2 := s1.(*RuledEvictPod), s2.(*RuledEvictPod)

		if c1.Pod.Spec.Priority == nil {
			return false
		}
		return c2.Pod.Spec.Priority == nil || *c1.Pod.Spec.Priority > *c2.Pod.Spec.Priority
	}, s1, s2)
}

// CompareEvictionResource compares the eviction scope defined in each plugin
func (e *EvictionStrategyImpl) CompareEvictionResource(s1, s2 interface{}) int {
	return covertBoolSortCompareToIntSort(func(s1, s2 interface{}) bool {
		c1, c2 := s1.(*RuledEvictPod), s2.(*RuledEvictPod)

		if _, ok := evictionScopePriority[c1.Scope]; !ok {
			return false
		}
		if _, ok := evictionScopePriority[c2.Scope]; !ok {
			return true
		}
		return evictionScopePriority[c2.Scope] > evictionScopePriority[c1.Scope]
	}, s1, s2)
}

func (e *EvictionStrategyImpl) ComparePodName(s1, s2 interface{}) int {
	return covertBoolSortCompareToIntSort(func(s1, s2 interface{}) bool {
		c1, c2 := s1.(*RuledEvictPod), s2.(*RuledEvictPod)

		return c1.Pod.Name > c2.Pod.Name
	}, s1, s2)
}

// covertBoolSortCompareToIntSort converts the bool-based sorting
// function to int-based sorting function
func covertBoolSortCompareToIntSort(f func(s1, s2 interface{}) bool, s1, s2 interface{}) int {
	if f(s1, s2) {
		return -1
	} else if f(s2, s1) {
		return 1
	}
	return 0
}
