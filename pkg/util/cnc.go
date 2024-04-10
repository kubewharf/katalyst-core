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
	configapi "github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
)

// RemoveUnusedTargetConfig delete those unused configurations from CNC status
func RemoveUnusedTargetConfig(configList []configapi.TargetConfig, needToDelete func(config configapi.TargetConfig) bool) []configapi.TargetConfig {
	if len(configList) == 0 {
		return configList
	}

	resultConfigList := make([]configapi.TargetConfig, 0, len(configList))
	for _, config := range configList {
		if needToDelete(config) {
			continue
		}
		resultConfigList = append(resultConfigList, config)
	}

	return resultConfigList
}
