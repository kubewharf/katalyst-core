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
	"math"
	"strconv"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kubewharf/katalyst-api/pkg/apis/workload/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/consts"
	core_consts "github.com/kubewharf/katalyst-core/pkg/consts"
)

const (
	LabelStatefulSetExtensionName    = "statefulset_extension_name"
	LabelStatefulSetExtensionReplica = "replica_id"
)

type (
	PodMetaCustomProcessor            func(podMeta metav1.ObjectMeta, spdBaselinePodMeta *SPDBaselinePodMeta) error
	PodMetaCustomSentinelKeyProcessor func(podMetaList []SPDBaselinePodMeta, baselinePercent *int32) *SPDBaselinePodMeta
)

type CustomCompareKey string

var (
	SPDBaselinePodMetaCustomCompareKeyShardID CustomCompareKey = "shard_id"
	PodMetaCustomCompareKeyProcessorMap                        = map[CustomCompareKey]PodMetaCustomProcessor{
		SPDBaselinePodMetaCustomCompareKeyShardID: GetStseCustomSPDBaselinePodMeta,
	}
	PodMetaCustomSentinelKeyProcessorMap = map[CustomCompareKey]PodMetaCustomSentinelKeyProcessor{
		SPDBaselinePodMetaCustomCompareKeyShardID: GetStseCustomSPDBaselinePodMetaSentinel,
	}
)

type SPDBaselinePodMeta struct {
	TimeStamp          metav1.Time       `json:"timeStamp"`
	PodName            string            `json:"podName"`
	CustomCompareKey   *CustomCompareKey `json:"customCompareKey"`
	CustomCompareValue interface{}       `json:"customCompareValue"`
}

func (c SPDBaselinePodMeta) Cmp(c1 *SPDBaselinePodMeta) int {
	if c.CustomCompareKey != nil && c.CustomCompareKey == c1.CustomCompareKey {
		v1 := c.CustomCompareValue
		v2 := c1.CustomCompareValue
		switch val := v1.(type) {
		case float64:
			val2 := v2.(float64)
			if val < val2 {
				return -1
			} else if val > val2 {
				return 1
			}
		case string:
			val2 := v2.(string)
			cmp := strings.Compare(val, val2)
			if cmp != 0 {
				return cmp
			}
		}
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
func IsBaselinePod(podMeta metav1.ObjectMeta, baselinePercent *int32, baselineSentinel *SPDBaselinePodMeta, spdCustomCompareKey *CustomCompareKey) (bool, error) {
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
	if pm.Cmp(baselineSentinel) <= 0 {
		return true, nil
	}

	return false, nil
}

// IsExtendedBaselinePod check whether a pod is baseline pod by extended indicator
func IsExtendedBaselinePod(podMeta metav1.ObjectMeta, baselinePercent *int32, podMetaMap map[string]*SPDBaselinePodMeta, name string, spdCustomCompareKey *CustomCompareKey) (bool, error) {
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
func GetSPDBaselinePodMeta(podMeta metav1.ObjectMeta, spdCustomCompareKey *CustomCompareKey) (*SPDBaselinePodMeta, error) {
	baselinePodMeta := SPDBaselinePodMeta{
		TimeStamp: podMeta.CreationTimestamp,
		PodName:   podMeta.Name,
	}
	if spdCustomCompareKey != nil {
		if customKeyFunc, ok := PodMetaCustomCompareKeyProcessorMap[*spdCustomCompareKey]; ok {
			err := customKeyFunc(podMeta, &baselinePodMeta)
			if err != nil {
				return nil, err
			}
			baselinePodMeta.CustomCompareKey = spdCustomCompareKey
		}
	}
	return &baselinePodMeta, nil
}

func GetStseCustomSPDBaselinePodMeta(podMeta metav1.ObjectMeta, spdBaselinePodMeta *SPDBaselinePodMeta) error {
	statefulsetExtensionName := podMeta.Labels[LabelStatefulSetExtensionName]
	parts := strings.Split(podMeta.Labels[LabelStatefulSetExtensionName], "-")
	if len(parts) != 3 {
		return fmt.Errorf("invalid statefulsetextension format: expected dp-{name}-{shard}, got %s", statefulsetExtensionName)
	}

	shardStr := parts[len(parts)-1]
	shardID, err := strconv.ParseFloat(shardStr, 64)
	if err != nil {
		return fmt.Errorf("invalid shard segment: %w", err)
	}

	spdBaselinePodMeta.CustomCompareValue = shardID
	return nil
}

func GetStseCustomSPDBaselinePodMetaSentinel(podMetaList []SPDBaselinePodMeta, baselinePercent *int32) *SPDBaselinePodMeta {
	lastPodMeta := podMetaList[len(podMetaList)-1]
	shardID, ok := lastPodMeta.CustomCompareValue.(float64)
	if !ok {
		return nil
	}
	baselineShard := int(math.Max(math.Floor((shardID+1.0)*float64(*baselinePercent)/100)-1.0, 0))
	parts := strings.Split(lastPodMeta.PodName, "-")
	replicaCount, _ := strconv.Atoi(parts[len(parts)-1])
	baselineIndex := baselineShard * (replicaCount + 1)
	return &podMetaList[baselineIndex]
}

func GetSPDCustomCompareKeys(spd *v1alpha1.ServiceProfileDescriptor) *CustomCompareKey {
	var spdCustomCompareKey CustomCompareKey
	key, ok := spd.Annotations[core_consts.ServiceProfileDescriptorAnnotationKeyCustomCompareKey]
	if !ok {
		return nil
	}
	spdCustomCompareKey = CustomCompareKey(key)
	return &spdCustomCompareKey
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
