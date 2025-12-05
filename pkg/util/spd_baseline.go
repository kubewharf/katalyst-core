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
	"sync"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-api/pkg/apis/workload/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/consts"
)

type (
	CustomCompareKey       string
	PodMetaCustomProcessor struct {
		PodMetaCustomKeyProcessor      func(podMeta metav1.ObjectMeta, spdBaselinePodMeta *SPDBaselinePodMeta, customKey CustomCompareKey) error
		PodMetaCustomSentinelProcessor func(podMetaList []SPDBaselinePodMeta, baselinePercent *int32) *SPDBaselinePodMeta
		PodMetaCustomCmp               func(c1 SPDBaselinePodMeta, c2 SPDBaselinePodMeta) int
	}
)

var spdPodMetaCustomProcessor sync.Map

func RegisterSPDPodMetaCustomProcessor(key CustomCompareKey, processor *PodMetaCustomProcessor) {
	spdPodMetaCustomProcessor.Store(key, processor)
	klog.Infof("[spd] registered SPD pod meta custom processor: %v", key)
}

func GetSPDPodMetaCustomProcessor(key CustomCompareKey) (*PodMetaCustomProcessor, error) {
	value, ok := spdPodMetaCustomProcessor.Load(key)
	if !ok {
		return nil, fmt.Errorf("custom processor for %s not found", key)
	}

	customKeyProcessor, ok := value.(*PodMetaCustomProcessor)
	if !ok {
		return nil, fmt.Errorf("can't get customKeyProcessor for key: %v", key)
	}

	return customKeyProcessor, nil
}

type SPDBaselinePodMeta struct {
	TimeStamp          metav1.Time      `json:"timeStamp"`
	PodName            string           `json:"podName"`
	CustomCompareKey   CustomCompareKey `json:"customCompareKey"`
	CustomCompareValue interface{}      `json:"customCompareValue"`
}

func (c SPDBaselinePodMeta) Cmp(c1 SPDBaselinePodMeta) int {
	if c.CustomCompareKey != "" && c.CustomCompareKey == c1.CustomCompareKey {
		customKeyProcessor, _ := GetSPDPodMetaCustomProcessor(c.CustomCompareKey)
		customCmpFunc := customKeyProcessor.PodMetaCustomCmp
		return customCmpFunc(c, c1)
	}
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
func IsBaselinePod(podMeta metav1.ObjectMeta, baselinePercent *int32, baselineSentinel *SPDBaselinePodMeta, spdCustomCompareKey CustomCompareKey) (bool, error) {
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

	pm, err := GetSPDBaselinePodMeta(podMeta, spdCustomCompareKey)
	if err != nil {
		return false, fmt.Errorf("invalid pod meta %s: %v", podMeta.Name, err)
	}
	if pm.Cmp(*baselineSentinel) <= 0 {
		return true, nil
	}

	return false, nil
}

// IsExtendedBaselinePod check whether a pod is baseline pod by extended indicator
func IsExtendedBaselinePod(podMeta metav1.ObjectMeta, baselinePercent *int32, podMetaMap map[string]*SPDBaselinePodMeta, name string, spdCustomCompareKey CustomCompareKey) (bool, error) {
	var baselineSentinel *SPDBaselinePodMeta
	sentinel, ok := podMetaMap[name]
	if ok {
		baselineSentinel = sentinel
	}

	isBaseline, err := IsBaselinePod(podMeta, baselinePercent, baselineSentinel, spdCustomCompareKey)
	if err != nil {
		return false, err
	}

	return isBaseline, nil
}

// GetSPDBaselinePodMeta get the baseline coefficient of this pod
func GetSPDBaselinePodMeta(podMeta metav1.ObjectMeta, spdCustomCompareKey CustomCompareKey) (*SPDBaselinePodMeta, error) {
	baselinePodMeta := SPDBaselinePodMeta{
		TimeStamp: podMeta.CreationTimestamp,
		PodName:   podMeta.Name,
	}
	if spdCustomCompareKey == "" {
		return &baselinePodMeta, nil
	}

	customKeyProcessor, err := GetSPDPodMetaCustomProcessor(spdCustomCompareKey)
	if err != nil {
		return nil, err
	}
	customKeyFunc := customKeyProcessor.PodMetaCustomKeyProcessor
	err = customKeyFunc(podMeta, &baselinePodMeta, spdCustomCompareKey)
	if err != nil {
		return nil, err
	}

	return &baselinePodMeta, nil
}

func GetSPDCustomCompareKeys(spd *v1alpha1.ServiceProfileDescriptor) CustomCompareKey {
	var spdCustomCompareKey CustomCompareKey
	key, ok := spd.Annotations[consts.SPDAnnotationKeyCustomCompareKey]
	if !ok {
		return ""
	}
	spdCustomCompareKey = CustomCompareKey(key)
	klog.Infof("[spd] get SPD custom compare key %v for spd %v", spdCustomCompareKey, spd.Name)
	return spdCustomCompareKey
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
func GetSPDExtendedBaselineSentinel(spd *v1alpha1.ServiceProfileDescriptor) (map[string]*SPDBaselinePodMeta, error) {
	s, ok := spd.Annotations[consts.SPDAnnotationExtendedBaselineSentinelKey]
	if !ok {
		return nil, nil
	}

	bs := map[string]*SPDBaselinePodMeta{}
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
