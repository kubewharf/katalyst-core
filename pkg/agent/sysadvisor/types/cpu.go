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
	"time"

	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

type TopologyAwareAssignment map[int]machine.CPUSet

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
	CPUHeadroomPolicyNone          CPUHeadroomPolicyName = "none"
	CPUHeadroomPolicyCanonical     CPUHeadroomPolicyName = "canonical"
	CPUHeadroomPolicyNUMAExclusive CPUHeadroomPolicyName = "numa_exclusive"
)

// CPUProvisionAssemblerName defines assemblers for cpu advisor to generate node
// provision result from region control knobs
type CPUProvisionAssemblerName string

const (
	CPUProvisionAssemblerNone   CPUProvisionAssemblerName = "none"
	CPUProvisionAssemblerCommon CPUProvisionAssemblerName = "common"
)

// CPUHeadroomAssemblerName defines assemblers for cpu advisor to generate node
// headroom from region headroom or node level policy
type CPUHeadroomAssemblerName string

const (
	CPUHeadroomAssemblerNone      CPUHeadroomAssemblerName = "none"
	CPUHeadroomAssemblerCommon    CPUHeadroomAssemblerName = "common"
	CPUHeadroomAssemblerDedicated CPUHeadroomAssemblerName = "dedicated"
)

// QoSRegionType declares pre-defined region types
type QoSRegionType string

const (
	// QoSRegionTypeShare for each share pool
	QoSRegionTypeShare QoSRegionType = "share"

	// QoSRegionTypeIsolation for each isolation pool
	QoSRegionTypeIsolation QoSRegionType = "isolation"

	// QoSRegionTypeDedicatedNumaExclusive for each dedicated core with numa binding
	// and numa exclusive container
	QoSRegionTypeDedicatedNumaExclusive QoSRegionType = "dedicated-numa-exclusive"
)

// ControlKnob holds tunable system entries affecting indicator metrics
type ControlKnob map[ControlKnobName]ControlKnobValue

// ControlKnobName defines available control knob key for provision policy
type ControlKnobName string

const (
	// ControlKnobNonReclaimedCPUSize refers to cpu requirement of non-reclaimed workloads
	ControlKnobNonReclaimedCPUSize ControlKnobName = "non-reclaimed-cpu-size"

	// ControlKnobNonReclaimedCPUSizeUpper refers to the upper cpu size, for isolated pods now
	ControlKnobNonReclaimedCPUSizeUpper ControlKnobName = "non-reclaimed-cpu-size-upper"

	// ControlKnobNonReclaimedCPUSizeLower refers to the lower cpu size, for isolated pods now
	ControlKnobNonReclaimedCPUSizeLower ControlKnobName = "non-reclaimed-cpu-size-lower"
)

// ControlKnobValue holds control knob value and action
type ControlKnobValue struct {
	Value  float64
	Action ControlKnobAction
}

// ControlKnobAction defines control knob adjustment actions
type ControlKnobAction string

const (
	ControlKnobActionNone ControlKnobAction = "none"
)

// OvershootType declares overshoot type for region
type OvershootType string

const (
	// OvershootTrue indicates overshoot
	OvershootTrue OvershootType = "true"

	// OvershootFalse indicates not overshoot
	OvershootFalse OvershootType = "false"

	// OvershootUnknown indicates unknown overshoot status
	OvershootUnknown OvershootType = "unknown"
)

// BoundType declares bound types for region
type BoundType string

const (
	// BoundUpper indicates reaching resource upper bound, with highest priority
	BoundUpper BoundType = "upper"

	// BoundLower indicates reaching resource lower bound
	BoundLower BoundType = "lower"

	// BoundNone indicates between resource upper and lower bound
	BoundNone BoundType = "none"

	// BoundUnknown indicates unknown bound status
	BoundUnknown BoundType = "unknown"
)

// RegionStatus holds stability accounting info of region
type RegionStatus struct {
	OvershootStatus map[string]OvershootType `json:"overshoot_status"` // map[indicatorMetric]overshootType
	BoundType       BoundType                `json:"bound_type"`
}

// PoolInfo contains pool information for sysadvisor plugins
type PoolInfo struct {
	PoolName                         string
	TopologyAwareAssignments         TopologyAwareAssignment
	OriginalTopologyAwareAssignments TopologyAwareAssignment
	RegionNames                      sets.String
}

// PoolEntries stores pool info keyed by pool name
type PoolEntries map[string]*PoolInfo

// RegionEntries stores region info keyed by region name
type RegionEntries map[string]*RegionInfo

// RegionInfo contains region information generated by sysadvisor resource advisor
type RegionInfo struct {
	RegionName    string         `json:"region_name"`
	RegionType    QoSRegionType  `json:"region_type"`
	OwnerPoolName string         `json:"owner_pool_name"`
	BindingNumas  machine.CPUSet `json:"binding_numas"`
	RegionStatus  RegionStatus   `json:"region_status"`

	ControlKnobMap             ControlKnob            `json:"control_knob_map"`
	ProvisionPolicyTopPriority CPUProvisionPolicyName `json:"provision_policy_top_priority"`
	ProvisionPolicyInUse       CPUProvisionPolicyName `json:"provision_policy_in_use"`

	Headroom                  float64               `json:"headroom"`
	HeadroomPolicyTopPriority CPUHeadroomPolicyName `json:"headroom_policy_top_priority"`
	HeadroomPolicyInUse       CPUHeadroomPolicyName `json:"headroom_policy_in_use"`
}

// InternalCPUCalculationResult conveys minimal information to cpu server for composing
// calculation result
type InternalCPUCalculationResult struct {
	PoolEntries map[string]map[int]int // map[poolName][numaId]cpuSize
	TimeStamp   time.Time
}

// ControlEssentials defines essential metrics for cpu advisor feedback control
type ControlEssentials struct {
	ControlKnobs   ControlKnob
	Indicators     Indicator
	ReclaimOverlap bool
}

// Indicator holds system metrics related to service stability keyed by metric name
type Indicator map[string]IndicatorValue

// IndicatorValue holds indicator values of different levels
type IndicatorValue struct {
	Current float64
	Target  float64
}

// IndicatorCurrentGetter get pod indicator current value by podUID
type IndicatorCurrentGetter func() (float64, error)
