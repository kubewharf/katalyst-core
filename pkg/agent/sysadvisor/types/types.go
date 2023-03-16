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

// CPUProvisionPolicyName defines policy names for cpu advisor resource provision
type CPUProvisionPolicyName string

const (
	CPUProvisionPolicyNone      CPUProvisionPolicyName = "none"
	CPUProvisionPolicyCanonical CPUProvisionPolicyName = "canonical"
	CPUProvisionPolicyRama      CPUProvisionPolicyName = "rama"
)

// CPUHeadroomPolicyName defines policy names for cpu advisor headroom estimation
type CPUHeadroomPolicyName string

const (
	CPUHeadroomPolicyNone CPUHeadroomPolicyName = "none"
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

// PoolInfo contains pool information for sysadvisor plugins
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

// ControlKnob holds tunable system entries affecting indicator metrics
type ControlKnob map[ControlKnobName]ControlKnobValue

// ControlKnobName defines available control knob key
type ControlKnobName string

const (
	ControlKnobCPUSetSize ControlKnobName = "cpuset-size"
)

// ControlKnobValue holds control knob value or action
type ControlKnobValue struct {
	Value  float64
	Action ControlKnobAction
}

// ControlKnobAction defines control knob adjustment actions
type ControlKnobAction string

const (
	ControlKnobActionNone ControlKnobAction = "none"
)

// Indicator holds system metrics related to service stability keyed by metric name
type Indicator map[string]IndicatorValue

// IndicatorValue holds indicator values of different levels
type IndicatorValue struct {
	Current float64
	Target  float64
	High    float64
	Low     float64
}
