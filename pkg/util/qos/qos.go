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

package qos

import (
	"encoding/json"

	"k8s.io/klog/v2"
)

// ParseKatalystQOSEnhancement parses enhancements from annotations by given key,
// since enhancement values are stored as k-v, so we should unmarshal it into maps.
func ParseKatalystQOSEnhancement(annotations map[string]string, enhancementKey string) map[string]string {
	enhancementValue, ok := annotations[enhancementKey]
	if !ok {
		return nil
	}

	enhancementConfig := map[string]string{}
	err := json.Unmarshal([]byte(enhancementValue), &enhancementConfig)
	if err != nil {
		klog.Errorf("GetPodQOSEnhancement parse enhancement %s failed: %v", enhancementKey, err)
		return nil
	}

	return enhancementConfig
}
