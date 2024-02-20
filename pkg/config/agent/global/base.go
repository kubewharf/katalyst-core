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

package global

type BaseConfiguration struct {
	// Agents is the list of agent components to enable or disable
	// '*' means "all enabled by default agents"
	// 'foo' means "enable 'foo'"
	// '-foo' means "disable 'foo'"
	// first item for a particular name wins
	Agents []string

	NodeName    string
	NodeAddress string

	// LockFileName indicates the file used as unique lock
	LockFileName string
	// if LockWaitingEnabled set as true, will not panic and report agent as healthy instead
	LockWaitingEnabled bool

	// ReclaimRelativeRootCgroupPath is configurable since we may need to
	// specify a customized path for reclaimed-cores to enrich qos-management ways
	ReclaimRelativeRootCgroupPath string

	*MachineInfoConfiguration
	*KubeletConfiguration
	*RuntimeConfiguration
}

type MachineInfoConfiguration struct {
	// if NetMultipleNS set as true, we should collect network interfaces from multiple ns
	NetMultipleNS   bool
	NetNSDirAbsPath string
}

type KubeletConfiguration struct {
	KubeletReadOnlyPort      int
	KubeletSecurePort        int
	KubeletSecurePortEnabled bool

	KubeletConfigEndpoint  string
	KubeletPodsEndpoint    string
	KubeletSummaryEndpoint string

	APIAuthTokenFile string
}

type RuntimeConfiguration struct {
	RuntimeEndpoint string
}

func NewBaseConfiguration() *BaseConfiguration {
	return &BaseConfiguration{
		MachineInfoConfiguration: &MachineInfoConfiguration{},
		KubeletConfiguration:     &KubeletConfiguration{},
		RuntimeConfiguration:     &RuntimeConfiguration{},
	}
}
