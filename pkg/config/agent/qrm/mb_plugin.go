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

package qrm

type MBQRMPluginConfig struct {
	// PolicyName is used to switch between several strategies
	PolicyName string
	// DomainQoSAwareCapacity keeps qos group customized mb upper capacity it allows
	DomainGroupAwareCapacity map[string]int
	// NoThrottleGroups are qos groups that should not be throttled with their mb usage
	NoThrottleGroups []string
}

func NewMBQRMPluginConfig() *MBQRMPluginConfig {
	return &MBQRMPluginConfig{
		// only generic is supported right now
		PolicyName: "generic",
	}
}
