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

package helper

import (
	"encoding/json"
	"sync"

	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

// EnhancementUpdateFunc provides a mechanism for user-specified enhancement kvs for specific key
// since we may need to set qos-level for some customized cases
type EnhancementUpdateFunc func(enhancementKVs, podAnnotations map[string]string) map[string]string

var enhancementUpdaters sync.Map

func RegisterEnhancementUpdateFunc(enhancementKey string, f EnhancementUpdateFunc) {
	enhancementUpdaters.Store(enhancementKey, f)
}

func GetRegisteredEnhancementUpdateFuncs() map[string]EnhancementUpdateFunc {
	updaters := make(map[string]EnhancementUpdateFunc)
	enhancementUpdaters.Range(func(key, value interface{}) bool {
		updaters[key.(string)] = value.(EnhancementUpdateFunc)
		return true
	})
	return updaters
}

func GetRegisteredEnhancementUpdateFunc(enhancementKey string) EnhancementUpdateFunc {
	value, ok := enhancementUpdaters.Load(enhancementKey)
	if !ok {
		return nil
	}
	return value.(EnhancementUpdateFunc)
}

// ParseKatalystQOSEnhancement parses enhancements from annotations by given key,
// since enhancement values are stored as k-v, so we should unmarshal it into maps.
func ParseKatalystQOSEnhancement(enhancements, podAnnotations map[string]string, enhancementKey string) (enhancementConfig map[string]string) {
	defer func() {
		updatedEnhancementConfig := enhancementConfig
		enhancementUpdateFunc := GetRegisteredEnhancementUpdateFunc(enhancementKey)
		if enhancementUpdateFunc != nil {
			updatedEnhancementConfig = enhancementUpdateFunc(enhancementConfig, podAnnotations)
			general.Infof("update enhancementConfig from %+v to %+v", enhancementConfig, updatedEnhancementConfig)
		}
		enhancementConfig = updatedEnhancementConfig
	}()

	enhancementValue, ok := enhancements[enhancementKey]
	if !ok {
		return nil
	}

	enhancementConfig = map[string]string{}
	err := json.Unmarshal([]byte(enhancementValue), &enhancementConfig)
	if err != nil {
		general.Errorf("parse enhancement %s failed: %v", enhancementKey, err)
		return nil
	}
	return enhancementConfig
}
