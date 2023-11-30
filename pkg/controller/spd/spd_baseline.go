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

	if spd.Spec.BaselinePercent == nil || *spd.Spec.BaselinePercent >= consts.SPDBaselinePercentMax || *spd.Spec.BaselinePercent <= consts.SPDBaselinePercentMin {
		return nil
	}

	podMeta, err := sc.calculateBaselineSentinel(spd)
	if err != nil {
		return err
	}
	util.SetSPDBaselineSentinel(spd, &podMeta)
	return nil
}

// calculateBaselineSentinel returns the sentinel one for a list of pods
// referenced by the SPD. If one pod's createTime is less than the sentinel pod
func (sc *SPDController) calculateBaselineSentinel(spd *v1alpha1.ServiceProfileDescriptor) (util.SPDBaselinePodMeta, error) {
	gvr, _ := meta.UnsafeGuessKindToResource(schema.FromAPIVersionAndKind(spd.Spec.TargetRef.APIVersion, spd.Spec.TargetRef.Kind))
	workloadLister, ok := sc.workloadLister[gvr]
	if !ok {
		return util.SPDBaselinePodMeta{}, fmt.Errorf("without workload lister for gvr %v", gvr)
	}

	podList, err := util.GetPodListForSPD(spd, sc.podIndexer, sc.conf.SPDPodLabelIndexerKeys, workloadLister, sc.podLister)
	if err != nil {
		return util.SPDBaselinePodMeta{}, err
	}

	podList = native.FilterPods(podList, func(pod *v1.Pod) (bool, error) {
		return native.PodIsActive(pod), nil
	})
	if len(podList) == 0 {
		return util.SPDBaselinePodMeta{}, nil
	}

	bcList := make([]util.SPDBaselinePodMeta, 0, len(podList))
	for _, p := range podList {
		bcList = append(bcList, util.GetPodMeta(p))
	}
	sort.SliceStable(bcList, func(i, j int) bool {
		return bcList[i].Cmp(bcList[j]) < 0
	})
	baselineIndex := int(math.Floor(float64(len(bcList)-1) * float64(*spd.Spec.BaselinePercent) / 100))
	return bcList[baselineIndex], nil
}
