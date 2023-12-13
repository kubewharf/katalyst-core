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
	"fmt"
	"sync"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/qos/helper"
)

// defaultQoSLevel willed b e use as default QoS Level if nothing in annotation
const defaultQoSLevel = apiconsts.PodAnnotationQoSLevelSharedCores

type qosValidationFunc func(map[string]string) (bool, error)

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
	QoSEnhancementAnnotationSelector map[string]string

	// qosCheckFunc is used as a syntactic sugar to easily walk through
	// all QoS Level validation functions
	qosCheckFuncMap map[string]qosValidationFunc

	// for different situation, there may be different default values for enhancement keys
	// we use options to control those different values
	// the key here is specific enhancement key such as "numa_binding", "numa_exclusive"
	// the value is the default value of the key
	EnhancementDefaultValues map[string]string
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
		QoSEnhancementAnnotationSelector: make(map[string]string),
		EnhancementDefaultValues:         make(map[string]string),
	}

	c.qosCheckFuncMap = map[string]qosValidationFunc{
		apiconsts.PodAnnotationQoSLevelSharedCores:    c.CheckSharedQoS,
		apiconsts.PodAnnotationQoSLevelDedicatedCores: c.CheckDedicatedQoS,
		apiconsts.PodAnnotationQoSLevelReclaimedCores: c.CheckReclaimedQoS,
		apiconsts.PodAnnotationQoSLevelSystemCores:    c.CheckSystemQoS,
	}
	return c
}

// FilterQoSAndEnhancement filter map that are related to katalyst QoS and katalyst Enhancement.
// for enhancements,we should unmarshal and store the unmarshal key-value.
// it works both for default katalyst QoS keys and expanded QoS keys.
func (c *QoSConfiguration) FilterQoSAndEnhancement(annotations map[string]string) (map[string]string, error) {
	filteredAnnotations := c.FilterQoSMap(annotations)
	wrappedEnhancements := c.GetQoSEnhancements(annotations)

	c.RLock()
	defer c.RUnlock()

	for _, enhancementKey := range validQosEnhancementKey.List() {
		enhancementKVs := helper.ParseKatalystQOSEnhancement(wrappedEnhancements, annotations, enhancementKey)
		for key, val := range enhancementKVs {
			if filteredAnnotations[key] != "" {
				general.Warningf("get enhancements %s:%s from %s, but the kv already exists: %s:%s",
					key, val, enhancementKey, key, filteredAnnotations[key])
			}
			filteredAnnotations[key] = val
		}
	}

	for enhancementKey, defaultValue := range c.EnhancementDefaultValues {
		if _, found := filteredAnnotations[enhancementKey]; !found {
			general.Infof("enhancementKey: %s isn't declared, set its value to defaultValue: %s",
				enhancementKey, defaultValue)
			filteredAnnotations[enhancementKey] = defaultValue
		}
	}

	return filteredAnnotations, nil
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

func (c *QoSConfiguration) SetExpandQoSLevelSelector(qosLevel string, selectorMap map[string]string) {
	if _, ok := c.qosCheckFuncMap[qosLevel]; !ok {
		return
	}

	c.Lock()
	defer c.Unlock()
	c.QoSClassAnnotationSelector[qosLevel] = general.MergeMap(c.QoSClassAnnotationSelector[qosLevel], selectorMap)
}

func (c *QoSConfiguration) GetQoSLevelForPod(pod *v1.Pod) (string, error) {
	if pod == nil {
		return "", fmt.Errorf("nil pod")
	}
	return c.GetQoSLevel(pod.Annotations)
}

// GetQoSLevel returns the standard katalyst QoS Level for given annotations;
// - returns error if there is conflict in qos level annotations or can't get valid qos level.
// - returns defaultQoSLevel if nothing matches and isNotDefaultQoSLevel is false.
func (c *QoSConfiguration) GetQoSLevel(annotations map[string]string) (qosLevel string, retErr error) {
	defer func() {
		if retErr != nil {
			return
		}

		// redirect qos-level according to user-specified qos judgement function
		qosLevelUpdater := helper.GetQoSLevelUpdateFunc()
		if qosLevelUpdater != nil {
			updatedQoSLevel := qosLevelUpdater(qosLevel, annotations)
			if updatedQoSLevel != qosLevel {
				general.Infof("update qosLevel from %s to %s", qosLevel, updatedQoSLevel)
			}
			qosLevel = updatedQoSLevel
		}
	}()

	isNotDefaultQoSLevel := false
	for qos := range c.QoSClassAnnotationSelector {
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

func (c *QoSConfiguration) SetExpandQoSEnhancementSelector(enhancementAdapter map[string]string) {
	c.Lock()
	defer c.Unlock()

	for defaultKey, expandedKey := range enhancementAdapter {
		if validQosEnhancementKey.Has(defaultKey) {
			c.QoSEnhancementAnnotationSelector[expandedKey] = defaultKey
		}
	}
}

func (c *QoSConfiguration) GetQoSEnhancementsForPod(pod *v1.Pod) map[string]string {
	if pod == nil {
		return map[string]string{}
	}
	return c.GetQoSEnhancements(pod.Annotations)
}

// GetQoSEnhancements returns the standard katalyst QoS Enhancement Map for given annotations;
// - ignore conflict cases: default enhancement key always prior to expand enhancement key
func (c *QoSConfiguration) GetQoSEnhancements(annotations map[string]string) map[string]string {
	res := make(map[string]string)

	c.RLock()
	defer c.RUnlock()
	for k, v := range annotations {
		if validQosEnhancementKey.Has(k) {
			res[k] = v
		} else if defaultK, ok := c.QoSEnhancementAnnotationSelector[k]; ok {
			if _, exist := res[defaultK]; !exist {
				res[defaultK] = v
			}
		}
	}

	return res
}

func (c *QoSConfiguration) CheckReclaimedQoSForPod(pod *v1.Pod) (bool, error) {
	if pod == nil {
		return false, nil
	}
	return c.CheckReclaimedQoS(pod.Annotations)
}

// CheckReclaimedQoS returns true if the annotation indicates for ReclaimedCores;
// - returns error if different QoS configurations conflict with each other.
func (c *QoSConfiguration) CheckReclaimedQoS(annotations map[string]string) (bool, error) {
	if qosLevel, err := c.GetQoSLevel(annotations); err != nil {
		return false, err
	} else {
		return qosLevel == apiconsts.PodAnnotationQoSLevelReclaimedCores, nil
	}
}

func (c *QoSConfiguration) CheckSharedQoSForPod(pod *v1.Pod) (bool, error) {
	if pod == nil {
		return false, nil
	}
	return c.CheckSharedQoS(pod.Annotations)
}

// CheckSharedQoS returns true if the annotation indicates for SharedCores;
// - returns error if different QoS configurations conflict with each other.
func (c *QoSConfiguration) CheckSharedQoS(annotations map[string]string) (bool, error) {
	if qosLevel, err := c.GetQoSLevel(annotations); err != nil {
		return false, err
	} else {
		return qosLevel == apiconsts.PodAnnotationQoSLevelSharedCores, nil
	}
}

func (c *QoSConfiguration) CheckDedicatedQoSForPod(pod *v1.Pod) (bool, error) {
	if pod == nil {
		return false, nil
	}
	return c.CheckDedicatedQoS(pod.Annotations)
}

// CheckDedicatedQoS returns true if the annotation indicates for DedicatedCores;
// - returns error if different QoS configurations conflict with each other.
func (c *QoSConfiguration) CheckDedicatedQoS(annotations map[string]string) (bool, error) {
	if qosLevel, err := c.GetQoSLevel(annotations); err != nil {
		return false, err
	} else {
		return qosLevel == apiconsts.PodAnnotationQoSLevelDedicatedCores, nil
	}
}

func (c *QoSConfiguration) CheckSystemQoSForPod(pod *v1.Pod) (bool, error) {
	if pod == nil {
		return false, nil
	}
	return c.CheckSystemQoS(pod.Annotations)
}

// CheckSystemQoS returns true if the annotation indicates for SystemCores;
// - returns error if different QoS configurations conflict with each other.
func (c *QoSConfiguration) CheckSystemQoS(annotations map[string]string) (bool, error) {
	if qosLevel, err := c.GetQoSLevel(annotations); err != nil {
		return false, err
	} else {
		return qosLevel == apiconsts.PodAnnotationQoSLevelSystemCores, nil
	}
}

// SetEnhancementDefaultValues set default values for enhancement keys
// because sometimes we need different default values for enhancement keys in different types of clusters
func (c *QoSConfiguration) SetEnhancementDefaultValues(enhancementDefaultValues map[string]string) {
	c.Lock()
	defer c.Unlock()
	c.EnhancementDefaultValues = general.MergeMap(c.EnhancementDefaultValues, enhancementDefaultValues)
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
			return false, false, fmt.Errorf("qos %v conflicts, matched count %v, mis matched count %v", qosValue, valueEqualCnt, valueNotEqualCnt)
		}
		// some key-value list match and some key may not exist
		return true, true, nil
	} else if valueNotEqualCnt == 0 {
		return false, false, nil
	}

	return true, false, nil
}

func (c *QoSConfiguration) GetSpecifiedPoolNameForPod(pod *v1.Pod) (string, error) {
	if pod == nil {
		return "", fmt.Errorf("nil pod")
	}
	return c.GetSpecifiedPoolName(c.GetQoSEnhancementsForPod(pod), pod.Annotations)
}

// GetSpecifiedPoolName returns the specified cpuset pool name for given enhancements and annotations;
func (c *QoSConfiguration) GetSpecifiedPoolName(enhancements, annotations map[string]string) (string, error) {
	qosLevel, err := c.GetQoSLevel(annotations)
	if err != nil {
		return "", fmt.Errorf("GetQoSLevel failed with error: %v", err)
	}

	enhancementKVs := helper.ParseKatalystQOSEnhancement(enhancements, annotations,
		apiconsts.PodAnnotationCPUEnhancementKey)
	return state.GetSpecifiedPoolName(qosLevel, enhancementKVs[apiconsts.PodAnnotationCPUEnhancementCPUSet]), nil
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
