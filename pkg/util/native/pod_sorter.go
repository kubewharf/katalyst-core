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

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"

	corev1helpers "k8s.io/component-helpers/scheduling/corev1"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

var (
	podQoSRank = map[string]int{
		apiconsts.PodAnnotationQoSLevelReclaimedCores: 0,
		apiconsts.PodAnnotationQoSLevelSharedCores:    1,
		apiconsts.PodAnnotationQoSLevelDedicatedCores: 2,
		apiconsts.PodAnnotationQoSLevelSystemCores:    3,
	}

	podSaleModeRank = map[string]int{
		apiconsts.PodSaleModeSpot:      0,
		apiconsts.PodSaleModeScheduled: 1,
		apiconsts.PodSaleModeReserved:  2,
		apiconsts.PodSaleModeUnknown:   3,
	}
)

type PodSourceList struct {
	pods []*v1.Pod
}

type PodSaleModeAnnotationKeyGetter interface {
	GetPodSaleModeAnnotationKey() string
}

var _ general.SourceList = &PodSourceList{}

func NewPodSourceList(pods []*v1.Pod) *PodSourceList {
	return &PodSourceList{
		pods: append([]*v1.Pod(nil), pods...),
	}
}

func NewPodSourceImpList(pods []*v1.Pod) general.SourceList {
	return &PodSourceList{
		pods: pods,
	}
}

func (pl *PodSourceList) Filter(filterFunc func(*v1.Pod) (bool, error)) *PodSourceList {
	if filterFunc == nil {
		return &PodSourceList{
			pods: append([]*v1.Pod(nil), pl.pods...),
		}
	}

	filteredPods := make([]*v1.Pod, 0, len(pl.pods))
	for _, pod := range pl.pods {
		if pod == nil {
			continue
		}

		if ok, err := filterFunc(pod); err != nil {
			klog.Errorf("filter pod %v err: %v", pod.Name, err)
		} else if ok {
			filteredPods = append(filteredPods, pod)
		}
	}

	return &PodSourceList{
		pods: filteredPods,
	}
}

func (pl *PodSourceList) Sort(cmp ...general.CmpFunc) *PodSourceList {
	if len(cmp) == 0 {
		return pl
	}

	general.NewMultiSorter(cmp...).Sort(pl)
	return pl
}

func (pl *PodSourceList) TopN(n uint64) []*v1.Pod {
	if n > uint64(len(pl.pods)) {
		n = uint64(len(pl.pods))
	}

	return append([]*v1.Pod(nil), pl.pods[:n]...)
}

func (pl *PodSourceList) Pods() []*v1.Pod {
	return append([]*v1.Pod(nil), pl.pods...)
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

// PodQoSCmpFunc sorts pods by eviction QoS priority.
func PodQoSCmpFunc(i1, i2 interface{}) int {
	p1, p2 := i1.(*v1.Pod), i2.(*v1.Pod)

	return cmpRank(getPodQoSRank(p1), getPodQoSRank(p2))
}

// PodSaleModeCmpFunc sorts pods by eviction sale mode priority.
func PodSaleModeCmpFunc(i1, i2 interface{}) int {
	p1, p2 := i1.(*v1.Pod), i2.(*v1.Pod)

	return cmpRank(getPodSaleModeRank(p1, apiconsts.PodAnnotationSaleModeKey), getPodSaleModeRank(p2, apiconsts.PodAnnotationSaleModeKey))
}

func NewPodSaleModeCmpFunc(annotationKey string) general.CmpFunc {
	if annotationKey == "" {
		annotationKey = apiconsts.PodAnnotationSaleModeKey
	}

	return func(i1, i2 interface{}) int {
		p1, p2 := i1.(*v1.Pod), i2.(*v1.Pod)

		return cmpRank(getPodSaleModeRank(p1, annotationKey), getPodSaleModeRank(p2, annotationKey))
	}
}

// PodUniqKeyCmpFunc sorts uniq key of pod with greater comparison
func PodUniqKeyCmpFunc(i1, i2 interface{}) int {
	p1UniqKey := GenerateUniqObjectNameKey(i1.(*v1.Pod))
	p2UniqKey := GenerateUniqObjectNameKey(i2.(*v1.Pod))

	return general.CmpString(p1UniqKey, p2UniqKey)
}

func getPodQoSRank(pod *v1.Pod) int {
	if pod == nil {
		return len(podQoSRank)
	}

	if rank, ok := podQoSRank[pod.Annotations[apiconsts.PodAnnotationQoSLevelKey]]; ok {
		return rank
	}

	return len(podQoSRank)
}

func getPodSaleModeRank(pod *v1.Pod, annotationKey string) int {
	if pod == nil {
		return len(podSaleModeRank)
	}

	if rank, ok := podSaleModeRank[pod.Annotations[annotationKey]]; ok {
		return rank
	}

	return podSaleModeRank[apiconsts.PodSaleModeUnknown]
}

func cmpRank(rank1, rank2 int) int {
	if rank1 == rank2 {
		return 0
	}
	if rank1 < rank2 {
		return -1
	}
	return 1
}

var (
	_ general.CmpFunc = PodPriorityCmpFunc
	_ general.CmpFunc = PodCPURequestCmpFunc
	_ general.CmpFunc = PodQoSCmpFunc
	_ general.CmpFunc = PodSaleModeCmpFunc
	_ general.CmpFunc = PodUniqKeyCmpFunc
)
