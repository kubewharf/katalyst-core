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
	"github.com/kubewharf/katalyst-core/pkg/util"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

// updateBaselinePercentile update baseline percentile annotation for spd
func (sc *SPDController) updateBaselinePercentile(spd *v1alpha1.ServiceProfileDescriptor) error {
	if spd == nil {
		return nil
	}

	if spd.Spec.BaselineRatio == nil {
		util.SetSPDBaselinePercentile(spd, nil)
		return nil
	} else if *spd.Spec.BaselineRatio == 1.0 {
		// if baseline ratio equals 100%, we set baselinePercentile to ""
		// which means all pod is baseline
		util.SetSPDBaselinePercentile(spd, &util.BaselineCoefficient{})
		return nil
	} else if *spd.Spec.BaselineRatio == 0 {
		// if baseline ratio equals 0%, we set baselinePercentile to "-1"
		// which means the baseline coefficient of all pods no less than the threshold,
		// and then without pod is baseline.
		util.SetSPDBaselinePercentile(spd, &util.BaselineCoefficient{-1})
		return nil
	}

	percentile, err := sc.calculateBaselinePercentile(spd)
	if err != nil {
		return err
	}

	if percentile != nil {
		util.SetSPDBaselinePercentile(spd, &percentile)
	}
	return nil
}

// calculateBaselinePercentile computes the baseline percentile for a list of pods
// referenced by the SPD. The baseline percentile represents the threshold value for
// the pod baseline coefficient. A pod is considered to be within the baseline if its
// coefficient is less than this threshold value.
func (sc *SPDController) calculateBaselinePercentile(spd *v1alpha1.ServiceProfileDescriptor) (util.BaselineCoefficient, error) {
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

	bcList := make([]util.BaselineCoefficient, 0, len(podList))
	for _, p := range podList {
		bcList = append(bcList, util.GetPodBaselineCoefficient(p))
	}
	sort.SliceStable(bcList, func(i, j int) bool {
		return bcList[i].Cmp(bcList[j]) < 0
	})
	baselineIndex := int(math.Floor(float64(len(bcList)-1) * float64(*spd.Spec.BaselineRatio)))
	return bcList[baselineIndex], nil
}
