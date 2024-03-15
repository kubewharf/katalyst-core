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

package spd

import (
	"fmt"
	"math"
	"sort"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/kubewharf/katalyst-api/pkg/apis/workload/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/util"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

// updateBaselineSentinel update baseline sentinel annotation for spd
func (sc *SPDController) updateBaselineSentinel(spd *v1alpha1.ServiceProfileDescriptor) error {
	if spd == nil {
		return nil
	}

	// delete baseline sentinel annotation if baseline percent or extended indicator not set
	if spd.Spec.BaselinePercent == nil && len(spd.Spec.ExtendedIndicator) == 0 {
		util.SetSPDBaselineSentinel(spd, nil)
		util.SetSPDExtendedBaselineSentinel(spd, nil)
		return nil
	}

	podMetaList, err := sc.getSPDPodMetaList(spd)
	if err != nil {
		return err
	}

	// calculate baseline sentinel
	baselineSentinel := calculateBaselineSentinel(podMetaList, spd.Spec.BaselinePercent)

	// calculate extended baseline sentinel for each extended indicator
	extendedBaselineSentinel := make(map[string]util.SPDBaselinePodMeta)
	for _, indicator := range spd.Spec.ExtendedIndicator {
		sentinel := calculateBaselineSentinel(podMetaList, indicator.BaselinePercent)
		if sentinel == nil {
			continue
		}

		extendedBaselineSentinel[indicator.Name] = *sentinel
	}

	util.SetSPDBaselineSentinel(spd, baselineSentinel)
	util.SetSPDExtendedBaselineSentinel(spd, extendedBaselineSentinel)
	return nil
}

// getSPDPodMetaList get spd pod meta list in order
func (sc *SPDController) getSPDPodMetaList(spd *v1alpha1.ServiceProfileDescriptor) ([]util.SPDBaselinePodMeta, error) {
	gvr, _ := meta.UnsafeGuessKindToResource(schema.FromAPIVersionAndKind(spd.Spec.TargetRef.APIVersion, spd.Spec.TargetRef.Kind))
	workloadLister, ok := sc.workloadLister[gvr]
	if !ok {
		return nil, fmt.Errorf("without workload lister for gvr %v", gvr)
	}

	podList, err := util.GetPodListForSPD(spd, sc.podIndexer, sc.conf.SPDPodLabelIndexerKeys, workloadLister, sc.podLister)
	if err != nil {
		return nil, err
	}

	podList = native.FilterPods(podList, func(pod *v1.Pod) (bool, error) {
		return native.PodIsActive(pod), nil
	})
	if len(podList) == 0 {
		return nil, nil
	}

	podMetaList := make([]util.SPDBaselinePodMeta, 0, len(podList))
	for _, p := range podList {
		podMetaList = append(podMetaList, util.GetPodMeta(p))
	}
	sort.SliceStable(podMetaList, func(i, j int) bool {
		return podMetaList[i].Cmp(podMetaList[j]) < 0
	})

	return podMetaList, nil
}

// calculateBaselineSentinel returns the sentinel one for a list of pods
// referenced by the SPD. If one pod's createTime is less than the sentinel pod
func calculateBaselineSentinel(podMetaList []util.SPDBaselinePodMeta, baselinePercent *int32) *util.SPDBaselinePodMeta {
	if baselinePercent == nil || *baselinePercent >= consts.SPDBaselinePercentMax ||
		*baselinePercent <= consts.SPDBaselinePercentMin {
		return nil
	}

	if len(podMetaList) == 0 {
		return nil
	}

	baselineIndex := int(math.Floor(float64(len(podMetaList)-1) * float64(*baselinePercent) / 100))
	return &podMetaList[baselineIndex]
}
