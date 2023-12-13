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

package util

import (
	"encoding/json"
	"fmt"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kubewharf/katalyst-api/pkg/apis/workload/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/consts"
)

type SPDBaselinePodMeta struct {
	TimeStamp metav1.Time `json:"timeStamp"`
	PodName   string      `json:"podName"`
}

func (c SPDBaselinePodMeta) Cmp(c1 SPDBaselinePodMeta) int {
	if c.TimeStamp.Time.Before(c1.TimeStamp.Time) {
		return -1
	}
	if c.TimeStamp.Time.After(c1.TimeStamp.Time) {
		return 1
	}

	if c.PodName < c1.PodName {
		return -1
	}
	if c.PodName > c1.PodName {
		return 1
	}
	return 0
}

func (c SPDBaselinePodMeta) String() string {
	d, err := json.Marshal(&c)
	if err != nil {
		return ""
	}
	return string(d)
}

// IsBaselinePod check whether a pod is baseline pod and whether
// the spd baseline is enabled
func IsBaselinePod(pod *v1.Pod, spd *v1alpha1.ServiceProfileDescriptor) (bool, bool, error) {
	if pod == nil || spd == nil {
		return false, false, fmt.Errorf("pod or spd is nil")
	}

	// if spd baseline percent not config means baseline is disabled
	if spd.Spec.BaselinePercent == nil {
		return false, false, nil
	}
	if *spd.Spec.BaselinePercent >= consts.SPDBaselinePercentMax {
		return true, true, nil
	} else if *spd.Spec.BaselinePercent <= consts.SPDBaselinePercentMin {
		return false, true, nil
	}

	bp, err := GetSPDBaselineSentinel(spd)
	if err != nil {
		return false, false, err
	}

	bc := GetPodMeta(pod)
	if bc.Cmp(bp) <= 0 {
		return true, true, nil
	}

	return false, true, nil
}

// GetPodMeta get the baseline coefficient of this pod
func GetPodMeta(pod *v1.Pod) SPDBaselinePodMeta {
	return SPDBaselinePodMeta{
		TimeStamp: pod.CreationTimestamp,
		PodName:   pod.Name,
	}
}

// GetSPDBaselineSentinel get the baseline sentinel pod of this spd
func GetSPDBaselineSentinel(spd *v1alpha1.ServiceProfileDescriptor) (SPDBaselinePodMeta, error) {
	s, ok := spd.Annotations[consts.SPDAnnotationBaselineSentinelKey]
	if !ok {
		return SPDBaselinePodMeta{}, fmt.Errorf("spd baseline percentile not found")
	}
	bc := SPDBaselinePodMeta{}
	err := json.Unmarshal([]byte(s), &bc)

	return bc, err
}

// SetSPDBaselineSentinel set the baseline percentile of this spd, if percentile is nil means delete it
func SetSPDBaselineSentinel(spd *v1alpha1.ServiceProfileDescriptor, podMeta *SPDBaselinePodMeta) {
	if spd == nil {
		return
	}

	if podMeta == nil {
		delete(spd.Annotations, consts.SPDAnnotationBaselineSentinelKey)
		return
	}

	if spd.Annotations == nil {
		spd.Annotations = make(map[string]string)
	}

	spd.Annotations[consts.SPDAnnotationBaselineSentinelKey] = podMeta.String()
	return
}
