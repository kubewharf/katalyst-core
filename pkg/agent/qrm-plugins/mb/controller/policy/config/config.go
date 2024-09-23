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

package config

import "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/task"

var config = map[task.QoSLevel]map[string]int{
	task.QoSLevelDedicatedCores: {
		"lounge": 6_000,
	},
	task.QoSLevelSharedCores: {
		"lounge": 2_000,
		"min":    2_000,
	},
	task.QoSLevelSystemCores: {
		"lounge": 2_000,
		"min":    3_000,
	},
	task.QoSLevelReclaimedCores: {
		"min": 0,
	},
}

func GetMins(qos ...task.QoSLevel) int {
	return getValues("min", qos...)
}

func GetLounges(qos ...task.QoSLevel) int {
	return getValues("lounge", qos...)
}

func getValues(name string, qos ...task.QoSLevel) int {
	result := 0
	for _, q := range qos {
		if min, ok := config[q]["min"]; ok {
			result += min
		}
	}
	return result
}
