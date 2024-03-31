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

// IsBaselinePod check whether a pod is baseline pod
func IsBaselinePod(podMeta metav1.ObjectMeta, baselinePercent *int32, baselineSentinel *SPDBaselinePodMeta) (bool, error) {
	// if spd baseline percent not config means baseline is disabled
	if baselinePercent == nil {
		return false, nil
	} else if *baselinePercent >= consts.SPDBaselinePercentMax {
		return true, nil
	} else if *baselinePercent <= consts.SPDBaselinePercentMin {
		return false, nil
	}

	if baselineSentinel == nil {
		return false, fmt.Errorf("baseline percent is already set but baseline sentinel is nil")
	}

	pm := GetSPDBaselinePodMeta(podMeta)
	if pm.Cmp(*baselineSentinel) <= 0 {
		return true, nil
	}

	return false, nil
}

// IsExtendedBaselinePod check whether a pod is baseline pod by extended indicator
func IsExtendedBaselinePod(podMeta metav1.ObjectMeta, baselinePercent *int32, podMetaMap map[string]SPDBaselinePodMeta, name string) (bool, error) {
	var baselineSentinel *SPDBaselinePodMeta
	sentinel, ok := podMetaMap[name]
	if ok {
		baselineSentinel = &sentinel
	}

	isBaseline, err := IsBaselinePod(podMeta, baselinePercent, baselineSentinel)
	if err != nil {
		return false, err
	}

	return isBaseline, nil
}

// GetSPDBaselinePodMeta get the baseline coefficient of this pod
func GetSPDBaselinePodMeta(podMeta metav1.ObjectMeta) SPDBaselinePodMeta {
	return SPDBaselinePodMeta{
		TimeStamp: podMeta.CreationTimestamp,
		PodName:   podMeta.Name,
	}
}

// GetSPDBaselineSentinel get the baseline sentinel pod of this spd
func GetSPDBaselineSentinel(spd *v1alpha1.ServiceProfileDescriptor) (*SPDBaselinePodMeta, error) {
	s, ok := spd.Annotations[consts.SPDAnnotationBaselineSentinelKey]
	if !ok {
		return nil, nil
	}

	bs := SPDBaselinePodMeta{}
	err := json.Unmarshal([]byte(s), &bs)
	if err != nil {
		return nil, err
	}

	return &bs, err
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

// GetSPDExtendedBaselineSentinel get the extended baseline sentinel pod of this spd
func GetSPDExtendedBaselineSentinel(spd *v1alpha1.ServiceProfileDescriptor) (map[string]SPDBaselinePodMeta, error) {
	s, ok := spd.Annotations[consts.SPDAnnotationExtendedBaselineSentinelKey]
	if !ok {
		return nil, nil
	}

	bs := map[string]SPDBaselinePodMeta{}
	err := json.Unmarshal([]byte(s), &bs)
	if err != nil {
		return nil, err
	}

	return bs, err
}

// SetSPDExtendedBaselineSentinel set the extended baseline sentinel of this spd, if percentile is nil means delete it
func SetSPDExtendedBaselineSentinel(spd *v1alpha1.ServiceProfileDescriptor, podMetaMap map[string]SPDBaselinePodMeta) {
	if spd == nil {
		return
	}

	if podMetaMap == nil || len(podMetaMap) == 0 {
		delete(spd.Annotations, consts.SPDAnnotationExtendedBaselineSentinelKey)
		return
	}

	if spd.Annotations == nil {
		spd.Annotations = make(map[string]string)
	}

	extendedBaselineSentinel, err := json.Marshal(podMetaMap)
	if err != nil {
		spd.Annotations[consts.SPDAnnotationExtendedBaselineSentinelKey] = ""
	} else {
		spd.Annotations[consts.SPDAnnotationExtendedBaselineSentinelKey] = string(extendedBaselineSentinel)
	}

	return
}
