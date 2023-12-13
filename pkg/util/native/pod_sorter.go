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

package native

import (
	v1 "k8s.io/api/core/v1"

	corev1helpers "k8s.io/component-helpers/scheduling/corev1"

	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

type PodSourceList struct {
	pods []*v1.Pod
}

var _ general.SourceList = &PodSourceList{}

func NewPodSourceImpList(pods []*v1.Pod) general.SourceList {
	return &PodSourceList{
		pods: pods,
	}
}

func (pl *PodSourceList) Len() int {
	return len(pl.pods)
}

func (pl *PodSourceList) GetSource(index int) interface{} {
	return pl.pods[index]
}

func (pl *PodSourceList) SetSource(index int, p interface{}) {
	pl.pods[index] = p.(*v1.Pod)
}

// PodPriorityCmpFunc sorts priority of pods with greater comparison
func PodPriorityCmpFunc(i1, i2 interface{}) int {
	priority1 := corev1helpers.PodPriority(i1.(*v1.Pod))
	priority2 := corev1helpers.PodPriority(i2.(*v1.Pod))

	return general.CmpInt32(priority1, priority2)
}

// PodCPURequestCmpFunc sorts cpu request of pods with less comparison
func PodCPURequestCmpFunc(i1, i2 interface{}) int {
	p1Request := SumUpPodRequestResources(i1.(*v1.Pod))
	p2Request := SumUpPodRequestResources(i2.(*v1.Pod))

	p1CPUQuantity := CPUQuantityGetter()(p1Request)
	p2CPUQuantity := CPUQuantityGetter()(p2Request)

	return p1CPUQuantity.Cmp(p2CPUQuantity)
}

// PodUniqKeyCmpFunc sorts uniq key of pod with greater comparison
func PodUniqKeyCmpFunc(i1, i2 interface{}) int {
	p1UniqKey := GenerateUniqObjectNameKey(i1.(*v1.Pod))
	p2UniqKey := GenerateUniqObjectNameKey(i2.(*v1.Pod))

	return general.CmpString(p1UniqKey, p2UniqKey)
}

var _ general.CmpFunc = PodPriorityCmpFunc
var _ general.CmpFunc = PodCPURequestCmpFunc
var _ general.CmpFunc = PodUniqKeyCmpFunc
