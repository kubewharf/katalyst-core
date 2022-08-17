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

package types

import (
	"k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// QoSResourceName describes different resources under qos aware control
type QoSResourceName string

const (
	QoSResourceCPU    QoSResourceName = "cpu"
	QoSResourceMemory QoSResourceName = "memory"
)

// CPUAdvisorPolicyName describes qos aware policy names for cpu resource
type CPUAdvisorPolicyName string

const (
	CPUAdvisorPolicyCanonical CPUAdvisorPolicyName = "canonical"
)

// MemoryAdvisorPolicyName describes qos aware policy names for memory resource
type MemoryAdvisorPolicyName string

const (
	MemoryAdvisorPolicyCanonical MemoryAdvisorPolicyName = "canonical"
)

// ContainerInfo contains container infomation for sysadvisor plugins
type ContainerInfo struct {
	// Metadata unchanged during container's lifecycle
	PodUID         string
	PodNamespace   string
	PodName        string
	ContainerName  string
	ContainerType  v1alpha1.ContainerType
	ContainerIndex int
	Labels         map[string]string
	Annotations    map[string]string
	QoSLevel       string
	CPURequest     float64
	MemoryRequest  float64

	// Allocation infomation changing by list and watch
	RampUp                           bool
	OwnerPoolName                    string
	TopologyAwareAssignments         map[int]machine.CPUSet
	OriginalTopologyAwareAssignments map[int]machine.CPUSet
}

// PoolInfo contains pool infomation for sysadvisor plugins
type PoolInfo struct {
	PoolName                         string
	TopologyAwareAssignments         map[int]machine.CPUSet
	OriginalTopologyAwareAssignments map[int]machine.CPUSet
}

// ContainerEntries stores container info keyed by container name
type ContainerEntries map[string]*ContainerInfo

// PodEntries stores container info keyed by pod uid and container name
type PodEntries map[string]ContainerEntries

// PoolEntries stores pool info keyed by pool name
type PoolEntries map[string]*PoolInfo
