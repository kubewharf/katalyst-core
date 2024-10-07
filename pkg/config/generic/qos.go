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

package generic

import (
	"encoding/json"
	"fmt"
	"sync"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

// defaultQoSLevel willed b e use as default QoS Level if nothing in annotation
const defaultQoSLevel = apiconsts.PodAnnotationQoSLevelSharedCores

type qosValidationFunc func(pod *v1.Pod, annotation map[string]string) (bool, error)

// validQosKey contains all the qos-level that Katalyst supports
var validQosKey = sets.NewString(
	apiconsts.PodAnnotationQoSLevelSharedCores,
	apiconsts.PodAnnotationQoSLevelDedicatedCores,
	apiconsts.PodAnnotationQoSLevelReclaimedCores,
	apiconsts.PodAnnotationQoSLevelSystemCores,
)

// validQosEnhancementKey contains all the enhancement that Katalyst supports
var validQosEnhancementKey = sets.NewString(
	apiconsts.PodAnnotationCPUEnhancementKey,
	apiconsts.PodAnnotationMemoryEnhancementKey,
	apiconsts.PodAnnotationNetworkEnhancementKey,
)

// QoSConfiguration stores the qos configurations needed by core katalyst components.
// since we may have legacy QoS judgement ways, we should map those legacy configs
// into standard katalyst QoS Level.
type QoSConfiguration struct {
	sync.RWMutex

	// QoSClassAnnotationSelector is used as an expanded way to match legacy specified
	// QoS annotations into standard katalyst QoS level
	// - if no expended selector is configured
	// --- only use the default key-value instead
	// - if multiple expended selectors are defined
	// --- returns true if anyone matches
	// - we should also do validation
	QoSClassAnnotationSelector map[string]map[string]string

	// QoSEnhancementAnnotationKey is used as an expanded way to match legacy specified
	// QoS annotations into standard katalyst QoS enhancement
	QoSEnhancementAnnotationKey map[string]string

	// for different situation, there may be different default values for enhancement keys
	// we use options to control those different values
	// the key here is specific enhancement key such as "numa_binding", "numa_exclusive"
	// the value is the default value of the key
	QoSEnhancementDefaultValues map[string]string

	// qosCheckFunc is used as a syntactic sugar to easily walk through
	// all QoS Level validation functions
	qosCheckFuncMap map[string]qosValidationFunc
}

// NewQoSConfiguration creates a new qos configuration.
func NewQoSConfiguration() *QoSConfiguration {
	c := &QoSConfiguration{
		QoSClassAnnotationSelector: map[string]map[string]string{
			apiconsts.PodAnnotationQoSLevelSharedCores: {
				apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelSharedCores,
			},
			apiconsts.PodAnnotationQoSLevelDedicatedCores: {
				apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelDedicatedCores,
			},
			apiconsts.PodAnnotationQoSLevelReclaimedCores: {
				apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelReclaimedCores,
			},
			apiconsts.PodAnnotationQoSLevelSystemCores: {
				apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelSystemCores,
			},
		},
		QoSEnhancementAnnotationKey: make(map[string]string),
		QoSEnhancementDefaultValues: make(map[string]string),
	}

	c.qosCheckFuncMap = map[string]qosValidationFunc{
		apiconsts.PodAnnotationQoSLevelSharedCores:    c.CheckSharedQoS,
		apiconsts.PodAnnotationQoSLevelDedicatedCores: c.CheckDedicatedQoS,
		apiconsts.PodAnnotationQoSLevelReclaimedCores: c.CheckReclaimedQoS,
		apiconsts.PodAnnotationQoSLevelSystemCores:    c.CheckSystemQoS,
	}
	return c
}

func (c *QoSConfiguration) SetExpandQoSLevelSelector(qosLevel string, selectorMap map[string]string) {
	if _, ok := c.qosCheckFuncMap[qosLevel]; !ok {
		return
	}

	c.Lock()
	defer c.Unlock()
	c.QoSClassAnnotationSelector[qosLevel] = general.MergeMap(c.QoSClassAnnotationSelector[qosLevel], selectorMap)
}

func (c *QoSConfiguration) SetExpandQoSEnhancementKey(enhancementKeys map[string]string) {
	c.Lock()
	defer c.Unlock()

	for defaultKey, expandedKey := range enhancementKeys {
		if validQosEnhancementKey.Has(defaultKey) {
			c.QoSEnhancementAnnotationKey[expandedKey] = defaultKey
		}
	}
}

// SetEnhancementDefaultValues set default values for enhancement keys
// because sometimes we need different default values for enhancement keys in different types of clusters
func (c *QoSConfiguration) SetEnhancementDefaultValues(enhancementDefaultValues map[string]string) {
	c.Lock()
	defer c.Unlock()

	c.QoSEnhancementDefaultValues = general.MergeMap(c.QoSEnhancementDefaultValues, enhancementDefaultValues)
}

// FilterQoSMap filter map that are related to katalyst QoS.
// it works both for default katalyst QoS keys and expanded QoS keys
func (c *QoSConfiguration) FilterQoSMap(annotations map[string]string) map[string]string {
	c.RLock()
	defer c.RUnlock()

	filteredAnnotations := make(map[string]string)
	for qos := range c.QoSClassAnnotationSelector {
		for qosExpand := range c.QoSClassAnnotationSelector[qos] {
			if val, ok := annotations[qosExpand]; ok {
				filteredAnnotations[qosExpand] = val
			}
		}
	}
	return filteredAnnotations
}

// FilterQoSEnhancementMap filter map that are related to katalyst Enhancement.
// for enhancements,we should unmarshal and store the unmarshal key-value.
// it works both for default katalyst QoS keys and expanded QoS keys.
func (c *QoSConfiguration) FilterQoSEnhancementMap(annotations map[string]string) map[string]string {
	c.RLock()
	defer c.RUnlock()

	filteredAnnotations := make(map[string]string)

	for _, enhancementKey := range validQosEnhancementKey.List() {
		enhancementKVs := c.GetQoSEnhancementKVs(nil, annotations, enhancementKey)
		for key, val := range enhancementKVs {
			if filteredAnnotations[key] != "" {
				general.Warningf("get enhancements %s:%s from %s, but the kv already exists: %s:%s",
					key, val, enhancementKey, key, filteredAnnotations[key])
			}
			filteredAnnotations[key] = val
		}
	}

	for enhancementKey, defaultValue := range c.QoSEnhancementDefaultValues {
		if _, found := filteredAnnotations[enhancementKey]; !found {
			general.Infof("enhancementKey: %s isn't declared, set its value to defaultValue: %s",
				enhancementKey, defaultValue)
			filteredAnnotations[enhancementKey] = defaultValue
		}
	}

	return filteredAnnotations
}

func (c *QoSConfiguration) FilterQoSAndEnhancementMap(annotations map[string]string) map[string]string {
	return general.MergeMap(c.FilterQoSMap(annotations), c.FilterQoSEnhancementMap(annotations))
}

func (c *QoSConfiguration) GetQoSLevelForPod(pod *v1.Pod) (string, error) {
	return c.GetQoSLevel(pod, map[string]string{})
}

// GetQoSLevel returns the standard katalyst QoS Level for given annotations;
// - returns error if there is conflict in qos level annotations or can't get valid qos level.
// - returns defaultQoSLevel if nothing matches and isNotDefaultQoSLevel is false.
func (c *QoSConfiguration) GetQoSLevel(pod *v1.Pod, expandedAnnotations map[string]string) (qosLevel string, retErr error) {
	annotations := MergeAnnotations(pod, expandedAnnotations)

	defer func() {
		if retErr != nil {
			return
		}
		// redirect qos-level according to user-specified qos judgement function
		overrideQoSLevel, ok := getQoSLevelExpander().Override(qosLevel, pod, annotations)
		if ok {
			general.Infof("update qosLevel from %s to %s", qosLevel, overrideQoSLevel)
			qosLevel = overrideQoSLevel
		}
	}()

	isNotDefaultQoSLevel := false
	for qos := range validQosKey {
		identified, matched, err := c.checkQosMatched(annotations, qos)
		if err != nil {
			general.Errorf("check qos level %v for annotation failed: %v", qos, err)
			return "", err
		} else if identified {
			if matched {
				return qos, nil
			} else if qos == defaultQoSLevel {
				isNotDefaultQoSLevel = true
			}
		}
	}

	if isNotDefaultQoSLevel {
		return "", fmt.Errorf("can't get valid qos level")
	}
	return defaultQoSLevel, nil
}

func (c *QoSConfiguration) CheckReclaimedQoSForPod(pod *v1.Pod) (bool, error) {
	return c.CheckReclaimedQoS(pod, map[string]string{})
}

// CheckReclaimedQoS returns true if the annotation indicates for ReclaimedCores;
// - returns error if different QoS configurations conflict with each other.
func (c *QoSConfiguration) CheckReclaimedQoS(pod *v1.Pod, expandedAnnotations map[string]string) (bool, error) {
	if qosLevel, err := c.GetQoSLevel(pod, expandedAnnotations); err != nil {
		return false, err
	} else {
		return qosLevel == apiconsts.PodAnnotationQoSLevelReclaimedCores, nil
	}
}

func (c *QoSConfiguration) CheckSharedQoSForPod(pod *v1.Pod) (bool, error) {
	return c.CheckSharedQoS(pod, map[string]string{})
}

// CheckSharedQoS returns true if the annotation indicates for SharedCores;
// - returns error if different QoS configurations conflict with each other.
func (c *QoSConfiguration) CheckSharedQoS(pod *v1.Pod, expandedAnnotations map[string]string) (bool, error) {
	if qosLevel, err := c.GetQoSLevel(pod, expandedAnnotations); err != nil {
		return false, err
	} else {
		return qosLevel == apiconsts.PodAnnotationQoSLevelSharedCores, nil
	}
}

func (c *QoSConfiguration) CheckDedicatedQoSForPod(pod *v1.Pod) (bool, error) {
	return c.CheckDedicatedQoS(pod, map[string]string{})
}

// CheckDedicatedQoS returns true if the annotation indicates for DedicatedCores;
// - returns error if different QoS configurations conflict with each other.
func (c *QoSConfiguration) CheckDedicatedQoS(pod *v1.Pod, expandedAnnotations map[string]string) (bool, error) {
	if qosLevel, err := c.GetQoSLevel(pod, expandedAnnotations); err != nil {
		return false, err
	} else {
		return qosLevel == apiconsts.PodAnnotationQoSLevelDedicatedCores, nil
	}
}

func (c *QoSConfiguration) CheckSystemQoSForPod(pod *v1.Pod) (bool, error) {
	return c.CheckSystemQoS(pod, map[string]string{})
}

// CheckSystemQoS returns true if the annotation indicates for SystemCores;
// - returns error if different QoS configurations conflict with each other.
func (c *QoSConfiguration) CheckSystemQoS(pod *v1.Pod, expandedAnnotations map[string]string) (bool, error) {
	if qosLevel, err := c.GetQoSLevel(pod, expandedAnnotations); err != nil {
		return false, err
	} else {
		return qosLevel == apiconsts.PodAnnotationQoSLevelSystemCores, nil
	}
}

// checkQosMatched is a unified helper function to judge whether annotation
// matches with the given QoS Level;
// return
// - identified: returning true if the function identifies the qos level matches according to QoSClassAnnotationSelector, else false.
// - matched: returning true if annotations match with qosValue, else false.
// - error: return err != nil if different QoS configurations conflict with each other
func (c *QoSConfiguration) checkQosMatched(annotations map[string]string, qosValue string) (identified bool, matched bool, err error) {
	c.RLock()
	defer c.RUnlock()

	valueNotEqualCnt, valueEqualCnt := 0, 0
	for key, value := range c.QoSClassAnnotationSelector[qosValue] {
		_, valueNotEqual, valueEqual := checkKeyValueMatched(annotations, key, value)
		valueNotEqualCnt, valueEqualCnt = valueNotEqualCnt+valueNotEqual, valueEqualCnt+valueEqual
	}

	if valueEqualCnt > 0 {
		// some key-value list match while others don't
		if valueNotEqualCnt > 0 {
			return false, false,
				fmt.Errorf("qos %v conflicts, matched count %v, mis matched count %v", qosValue, valueEqualCnt, valueNotEqualCnt)
		}
		// some key-value list match and some key may not exist
		return true, true, nil
	} else if valueNotEqualCnt == 0 {
		return false, false, nil
	}

	return true, false, nil
}

// GetQoSEnhancementKVs parses enhancements from annotations by given key,
// since enhancement values are stored as k-v, so we should unmarshal it into maps.
func (c *QoSConfiguration) GetQoSEnhancementKVs(pod *v1.Pod, expandedAnnotations map[string]string, enhancementKey string) (flattenedEnhancements map[string]string) {
	annotations := c.getQoSEnhancements(MergeAnnotations(pod, expandedAnnotations))

	defer func() {
		overrideFlattenedEnhancements, ok := getQoSEnhancementExpander().Override(flattenedEnhancements, pod, annotations)
		if ok {
			general.Infof("update enhancements from %+v to %+v", flattenedEnhancements, overrideFlattenedEnhancements)
			flattenedEnhancements = overrideFlattenedEnhancements
		}
	}()

	flattenedEnhancements = map[string]string{}
	enhancementValue, ok := annotations[enhancementKey]
	if !ok {
		return
	}

	err := json.Unmarshal([]byte(enhancementValue), &flattenedEnhancements)
	if err != nil {
		general.Errorf("parse enhancement %s failed: %v", enhancementKey, err)
		return
	}
	return flattenedEnhancements
}

// GetQoSEnhancements returns the standard katalyst QoS Enhancement Map for given annotations;
// - ignore conflict cases: default enhancement key always prior to expand enhancement key
func (c *QoSConfiguration) getQoSEnhancements(annotations map[string]string) map[string]string {
	res := make(map[string]string)

	c.RLock()
	defer c.RUnlock()
	for k, v := range annotations {
		if validQosEnhancementKey.Has(k) {
			res[k] = v
		} else if defaultK, ok := c.QoSEnhancementAnnotationKey[k]; ok {
			if _, exist := res[defaultK]; !exist {
				res[defaultK] = v
			}
		}
	}

	return res
}

func MergeAnnotations(pod *v1.Pod, expandAnnotations map[string]string) map[string]string {
	if pod == nil {
		if expandAnnotations == nil {
			return map[string]string{}
		}
		return expandAnnotations
	} else {
		return general.MergeMap(pod.Annotations, expandAnnotations)
	}
}

// checkKeyValueMatched checks whether the given key-value pair exists in the map
// if the returns value equals 1, it represents
// - key not exists
// - key exists, but value not equals
// - key exists, and value not equal
// returns 0 otherwise
func checkKeyValueMatched(m map[string]string, key, value string) (keyNotExist int, valueNotEqual int, valueEqual int) {
	v, ok := m[key]
	if !ok {
		keyNotExist = 1
	} else if v != value {
		valueNotEqual = 1
	} else {
		valueEqual = 1
	}
	return
}
