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

package region

import (
	"context"
	"fmt"
	"math"
	"sync"

	"go.uber.org/atomic"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/region/headroompolicy"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/region/provisionpolicy"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/region/regulator"
	borweinctrl "github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/helper/modelctrl/borwein"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

const (
	metricCPUGetHeadroomFailed             = "get_cpu_headroom_failed"
	metricCPUGetProvisionFailed            = "get_cpu_provision_failed"
	metricRegionHeadroom                   = "region_headroom"
	metricCPUProvisionControlKnobRaw       = "cpu_provision_control_knob_raw"
	metricCPUProvisionControlKnobRegulated = "cpu_provision_control_knob_regulated"

	metricTagKeyPolicyName        = "policy_name"
	metricTagKeyRegionType        = "region_type"
	metricTagKeyRegionName        = "region_name"
	metricTagKeyRegionNUMAs       = "region_numas"
	metricTagKeyControlKnobName   = "control_knob_name"
	metricTagKeyControlKnobAction = "control_knob_action"
)

type internalPolicyState struct {
	updateStatus types.PolicyUpdateStatus
	initDoOnce   sync.Once
}

type internalProvisionPolicy struct {
	name   types.CPUProvisionPolicyName
	policy provisionpolicy.ProvisionPolicy
	internalPolicyState
}

type internalHeadroomPolicy struct {
	name   types.CPUHeadroomPolicyName
	policy headroompolicy.HeadroomPolicy
	internalPolicyState
}

type provisionPolicyResult struct {
	essentials                 types.ResourceEssentials
	controlKnobValueRegulators map[types.ControlKnobName]regulator.Regulator
}

func newProvisionPolicyResult(essentials types.ResourceEssentials) *provisionPolicyResult {
	return &provisionPolicyResult{
		essentials:                 essentials,
		controlKnobValueRegulators: make(map[types.ControlKnobName]regulator.Regulator),
	}
}

// setEssentials is to set essentials for each control knob
func (r *provisionPolicyResult) setEssentials(essentials types.ResourceEssentials) {
	r.essentials = essentials
	for _, reg := range r.controlKnobValueRegulators {
		reg.SetEssentials(essentials)
	}
}

// regulateControlKnob is to regulate control knob with current and last one
// todo: current only regulate control knob value, it will also regulate action in the future
func (r *provisionPolicyResult) regulateControlKnob(currentControlKnob types.ControlKnob, lastControlKnob *types.ControlKnob) {
	if lastControlKnob != nil {
		for name, knob := range *lastControlKnob {
			reg, ok := r.controlKnobValueRegulators[name]
			if !ok || reg == nil {
				reg = r.newRegulator(name)
				reg.SetEssentials(r.essentials)
			}

			reg.SetLatestRequirement(int(knob.Value))
			r.controlKnobValueRegulators[name] = reg
		}
	}

	for name, knob := range currentControlKnob {
		reg, ok := r.controlKnobValueRegulators[name]
		if !ok || reg == nil {
			reg = r.newRegulator(name)
			reg.SetEssentials(r.essentials)
		}

		reg.Regulate(knob.Value)
		r.controlKnobValueRegulators[name] = reg
	}
}

// newRegulator new regulator according to the control knob name
func (r *provisionPolicyResult) newRegulator(name types.ControlKnobName) regulator.Regulator {
	switch name {
	// only non-reclaimed cpu size need regulate now
	case types.ControlKnobNonReclaimedCPUSize:
		return regulator.NewCPURegulator()
	default:
		return regulator.NewDummyRegulator()
	}
}

// getControlKnob is to get final control knob from regulators
func (r *provisionPolicyResult) getControlKnob() types.ControlKnob {
	controlKnob := make(types.ControlKnob)
	for name, r := range r.controlKnobValueRegulators {
		controlKnob[name] = types.ControlKnobValue{
			Value:  float64(r.GetRequirement()),
			Action: types.ControlKnobActionNone,
		}
	}
	return controlKnob
}

type QoSRegionBase struct {
	sync.Mutex
	conf *config.Configuration

	name          string
	ownerPoolName string
	regionType    types.QoSRegionType
	regionStatus  types.RegionStatus

	types.ResourceEssentials
	types.ControlEssentials

	// bindingNumas records numas assigned to this region
	bindingNumas machine.CPUSet
	// podSet records current pod and containers in region keyed by pod uid and container name
	podSet types.PodSet
	// containerTopologyAwareAssignment changes dynamically by adding container
	containerTopologyAwareAssignment types.TopologyAwareAssignment
	// indicatorCurrentGetters stores metrics getters for indicators interested in
	indicatorCurrentGetters map[string]types.IndicatorCurrentGetter

	// provisionPolicies for comparing and merging different provision policy results,
	// the former has higher priority; provisionPolicyNameInUse indicates the provision
	// policy in-use currently
	provisionPolicies        []*internalProvisionPolicy
	provisionPolicyNameInUse types.CPUProvisionPolicyName
	provisionPolicyResults   map[types.CPUProvisionPolicyName]*provisionPolicyResult

	// headroomPolicies for comparing and merging different headroom policy results,
	// the former has higher priority; headroomPolicyNameInUse indicates the headroom
	// policy in-use currently
	headroomPolicies        []*internalHeadroomPolicy
	headroomPolicyNameInUse types.CPUHeadroomPolicyName

	metaReader metacache.MetaReader
	metaServer *metaserver.MetaServer
	emitter    metrics.MetricEmitter

	// enableBorweinModel and borweinController will take effect only when using rama provision policy.
	// If enableBorweinModel is set, borweinController will update target indicators by model inference.
	enableBorweinModel bool
	borweinController  *borweinctrl.BorweinController

	// enableReclaim returns true if the resources of region can be reclaimed to supply for reclaimed_cores
	enableReclaim func() bool

	// throttled: true if unable to reach the ResourceUpperBound due to competition for resources with other regions.
	throttled atomic.Bool

	// idle: true if containers in the region is not running as usual, maybe there is no incoming business traffic
	idle atomic.Bool

	isNumaBinding bool
}

// NewQoSRegionBase returns a base qos region instance with common region methods
func NewQoSRegionBase(name string, ownerPoolName string, regionType types.QoSRegionType, conf *config.Configuration, extraConf interface{}, isNumaBinding bool,
	metaReader metacache.MetaReader, metaServer *metaserver.MetaServer, emitter metrics.MetricEmitter) *QoSRegionBase {
	r := &QoSRegionBase{
		conf:          conf,
		name:          name,
		ownerPoolName: ownerPoolName,
		regionType:    regionType,

		bindingNumas:                     machine.NewCPUSet(),
		podSet:                           make(types.PodSet),
		containerTopologyAwareAssignment: make(types.TopologyAwareAssignment),

		provisionPolicies: make([]*internalProvisionPolicy, 0),
		headroomPolicies:  make([]*internalHeadroomPolicy, 0),

		provisionPolicyNameInUse: types.CPUProvisionPolicyNone,
		headroomPolicyNameInUse:  types.CPUHeadroomPolicyNone,

		provisionPolicyResults: make(map[types.CPUProvisionPolicyName]*provisionPolicyResult),

		metaReader: metaReader,
		metaServer: metaServer,
		emitter:    emitter,

		enableBorweinModel: conf.PolicyRama.EnableBorwein,
		throttled:          *atomic.NewBool(false),
		idle:               *atomic.NewBool(false),

		isNumaBinding: isNumaBinding,
	}

	r.initHeadroomPolicy(conf, extraConf, metaReader, metaServer, emitter)
	r.initProvisionPolicy(conf, extraConf, metaReader, metaServer, emitter)

	// enableBorweinModel is initialized according to rama provision policy config.
	// it only takes effect when updating target indicators,
	// if there are more code positions depending on it,
	// we should consider provide a dummy borwein controller to avoid redundant judgement.
	if r.enableBorweinModel {
		r.borweinController = borweinctrl.NewBorweinController(name, regionType, ownerPoolName, conf, metaReader, emitter)
	}
	r.enableReclaim = r.EnableReclaim

	klog.Infof("[qosaware-cpu] created region [%v/%v/%v]", r.Name(), r.Type(), r.OwnerPoolName())

	return r
}

func (r *QoSRegionBase) Name() string {
	return r.name
}

// TryUpdateProvision is implemented in specific region
func (r *QoSRegionBase) TryUpdateProvision() {}

func (r *QoSRegionBase) Type() types.QoSRegionType {
	return r.regionType
}

func (r *QoSRegionBase) OwnerPoolName() string {
	return r.ownerPoolName
}

func (r *QoSRegionBase) IsEmpty() bool {
	r.Lock()
	defer r.Unlock()

	return len(r.podSet) <= 0
}

func (r *QoSRegionBase) Clear() {
	r.Lock()
	defer r.Unlock()

	r.podSet = make(types.PodSet)
	r.containerTopologyAwareAssignment = make(types.TopologyAwareAssignment)
}

func (r *QoSRegionBase) GetBindingNumas() machine.CPUSet {
	r.Lock()
	defer r.Unlock()

	return r.bindingNumas.Clone()
}

func (r *QoSRegionBase) GetPods() types.PodSet {
	r.Lock()
	defer r.Unlock()

	return r.podSet.Clone()
}

func (r *QoSRegionBase) SetBindingNumas(numas machine.CPUSet) {
	r.Lock()
	defer r.Unlock()

	r.bindingNumas = numas
}

func (r *QoSRegionBase) SetEssentials(essentials types.ResourceEssentials) {
	r.Lock()
	defer r.Unlock()

	r.ResourceEssentials = essentials
}

func (r *QoSRegionBase) SetThrottled(throttled bool) {
	r.throttled.Store(throttled)
}

func (r *QoSRegionBase) IsNumaBinding() bool {
	return r.isNumaBinding
}

func (r *QoSRegionBase) AddContainer(ci *types.ContainerInfo) error {
	r.Lock()
	defer r.Unlock()

	if ci == nil {
		return fmt.Errorf("container info nil")
	}

	r.podSet.Insert(ci.PodUID, ci.ContainerName)

	if len(r.containerTopologyAwareAssignment) <= 0 {
		r.containerTopologyAwareAssignment = ci.TopologyAwareAssignments.Clone()
	} else {
		// Sanity check: all containers in the region share the same cpuset
		// Do not return error when sanity check fails to prevent unnecessary stall
		if !r.containerTopologyAwareAssignment.Equals(ci.TopologyAwareAssignments) {
			klog.Warningf("[qosaware-cpu] sanity check failed")
		}
	}

	return nil
}

func (r *QoSRegionBase) TryUpdateHeadroom() {
	r.Lock()
	defer r.Unlock()

	for _, internal := range r.headroomPolicies {
		internal.updateStatus = types.PolicyUpdateFailed

		// set essentials for policy
		internal.policy.SetPodSet(r.podSet)
		internal.policy.SetBindingNumas(r.bindingNumas)
		internal.policy.SetEssentials(r.ResourceEssentials)

		// run an episode of policy and calculator update
		if err := internal.policy.Update(); err != nil {
			klog.Errorf("[qosaware-cpu] update policy %v failed: %v", internal.name, err)
			continue
		}
		internal.updateStatus = types.PolicyUpdateSucceeded
	}
}

func (r *QoSRegionBase) GetProvision() (types.ControlKnob, error) {
	r.Lock()
	defer r.Unlock()

	oldProvisionPolicyNameInUse := r.provisionPolicyNameInUse
	r.provisionPolicyNameInUse = types.CPUProvisionPolicyNone

	for _, internal := range r.provisionPolicies {
		if internal.updateStatus != types.PolicyUpdateSucceeded {
			_ = r.emitter.StoreInt64(metricCPUGetProvisionFailed, 1, metrics.MetricTypeNameRaw,
				metrics.MetricTag{Key: metricTagKeyPolicyName, Val: string(internal.name)})
			continue
		}

		result, ok := r.provisionPolicyResults[internal.name]
		if !ok {
			klog.Errorf("[qosaware-cpu] get control knob by policy %v failed", internal.name)
			continue
		}
		r.provisionPolicyNameInUse = internal.name

		if r.provisionPolicyNameInUse != oldProvisionPolicyNameInUse {
			klog.Infof("[qosaware-cpu] region: %v provision policy switch from %v to %v",
				r.Name(), oldProvisionPolicyNameInUse, r.provisionPolicyNameInUse)
			if r.enableBorweinModel {
				r.borweinController.ResetIndicatorOffsets()
			}
		}

		return result.getControlKnob(), nil
	}

	return types.ControlKnob{}, fmt.Errorf("failed to get legal provision")
}

func (r *QoSRegionBase) GetHeadroom() (float64, error) {
	r.Lock()
	defer r.Unlock()

	r.headroomPolicyNameInUse = types.CPUHeadroomPolicyNone

	for _, internal := range r.headroomPolicies {
		if internal.updateStatus != types.PolicyUpdateSucceeded {
			_ = r.emitter.StoreInt64(metricCPUGetHeadroomFailed, 1, metrics.MetricTypeNameRaw,
				metrics.MetricTag{Key: metricTagKeyPolicyName, Val: string(internal.name)})
			continue
		}
		headroom, err := internal.policy.GetHeadroom()
		if err != nil {
			klog.Errorf("[qosaware-cpu] get headroom by policy %v failed: %v", internal.name, err)
			continue
		}
		_ = r.emitter.StoreFloat64(metricRegionHeadroom, headroom, metrics.MetricTypeNameRaw,
			metrics.ConvertMapToTags(map[string]string{metricTagKeyRegionType: string(r.regionType),
				metricTagKeyRegionName: r.name, metricTagKeyPolicyName: string(internal.name),
				metricTagKeyRegionNUMAs: r.bindingNumas.String()})...)
		r.headroomPolicyNameInUse = internal.name
		return headroom, nil
	}

	return 0, fmt.Errorf("failed to get valid headroom")
}

func (r *QoSRegionBase) GetProvisionPolicy() (policyTopPriority types.CPUProvisionPolicyName, policyInUse types.CPUProvisionPolicyName) {
	r.Lock()
	defer r.Unlock()

	policyTopPriority = types.CPUProvisionPolicyNone
	if len(r.provisionPolicies) > 0 {
		policyTopPriority = r.provisionPolicies[0].name
	}

	if !r.enableReclaim() {
		policyInUse = types.CPUProvisionPolicyNonReclaim
	} else {
		policyInUse = r.provisionPolicyNameInUse
	}

	return
}

func (r *QoSRegionBase) GetHeadRoomPolicy() (policyTopPriority types.CPUHeadroomPolicyName, policyInUse types.CPUHeadroomPolicyName) {
	r.Lock()
	defer r.Unlock()

	policyTopPriority = types.CPUHeadroomPolicyNone
	if len(r.headroomPolicies) > 0 {
		policyTopPriority = r.headroomPolicies[0].name
	}

	if !r.enableReclaim() {
		policyInUse = types.CPUHeadroomPolicyNonReclaim
	} else {
		policyInUse = r.headroomPolicyNameInUse
	}

	return
}

func (r *QoSRegionBase) GetStatus() types.RegionStatus {
	r.Lock()
	defer r.Unlock()

	return r.regionStatus
}

func (r *QoSRegionBase) GetControlEssentials() types.ControlEssentials {
	r.Lock()
	defer r.Unlock()

	return r.ControlEssentials
}

// getRegionNameFromMetaCache returns region name owned by container from metacache,
// to restore region info after restart. If numaID is specified, binding numas of the
// region will be checked, otherwise only one region should be owned by container.
func getRegionNameFromMetaCache(ci *types.ContainerInfo, numaID int, metaReader metacache.MetaReader) string {
	if ci.QoSLevel == consts.PodAnnotationQoSLevelSharedCores {
		if len(ci.RegionNames) == 1 {
			// get region name from metaCache
			regionName := ci.RegionNames.List()[0]
			regionInfo, ok := metaReader.GetRegionInfo(regionName)
			if ok {
				// the region-name is valid if it suits it follows constrains below
				// - current container is isolated and the region is for isolation-type
				// - current container isn't isolated and the region is for share-type

				if !ci.Isolated && regionInfo.RegionType == types.QoSRegionTypeShare {
					return regionName
				} else if ci.Isolated && regionInfo.RegionType == types.QoSRegionTypeIsolation {
					return regionName
				}
			}
		}
	} else if ci.IsDedicatedNumaBinding() {
		for regionName := range ci.RegionNames {
			regionInfo, ok := metaReader.GetRegionInfo(regionName)
			if ok && regionInfo.RegionType == types.QoSRegionTypeDedicatedNumaExclusive {
				regionNUMAs := regionInfo.BindingNumas.ToSliceInt()
				if len(regionNUMAs) == 1 && regionNUMAs[0] == numaID {
					return regionName
				}
			}
		}
	}
	return ""
}

// initProvisionPolicy initializes provision by adding additional policies into default ones
func (r *QoSRegionBase) initProvisionPolicy(conf *config.Configuration, extraConf interface{},
	metaReader metacache.MetaReader, metaServer *metaserver.MetaServer, emitter metrics.MetricEmitter) {
	configuredProvisionPolicy, ok := conf.CPUAdvisorConfiguration.ProvisionPolicies[r.regionType]
	if !ok {
		klog.Warningf("[qosaware-cpu] failed to find provision policies for region %v", r.regionType)
		return
	}

	// try new policies
	// todo move to separate functions
	initializers := provisionpolicy.GetRegisteredInitializers()
	for _, policyName := range configuredProvisionPolicy {
		if initializer, ok := initializers[policyName]; ok {
			policy := initializer(r.name, r.regionType, r.ownerPoolName, conf, extraConf, metaReader, metaServer, emitter)
			policy.SetBindingNumas(r.bindingNumas)
			r.provisionPolicies = append(r.provisionPolicies, &internalProvisionPolicy{
				name:                policyName,
				policy:              policy,
				internalPolicyState: internalPolicyState{updateStatus: types.PolicyUpdateFailed},
			})
		} else {
			general.ErrorS(fmt.Errorf("failed to find region policy"), "policyName", policyName, "region", r.regionType)
		}
	}
}

// initHeadroomPolicy initializes headroom by adding additional policies into default ones
func (r *QoSRegionBase) initHeadroomPolicy(conf *config.Configuration, extraConf interface{},
	metaReader metacache.MetaReader, metaServer *metaserver.MetaServer, emitter metrics.MetricEmitter) {
	configuredHeadroomPolicy, ok := conf.CPUAdvisorConfiguration.HeadroomPolicies[r.regionType]
	if !ok {
		klog.Warningf("[qosaware-cpu] failed to find headroom policies for region %v", r.regionType)
		return
	}

	// try new policies
	headroomInitializers := headroompolicy.GetRegisteredInitializers()
	for _, policyName := range configuredHeadroomPolicy {
		if initializer, ok := headroomInitializers[policyName]; ok {
			policy := initializer(r.name, r.regionType, r.ownerPoolName, conf, extraConf, metaReader, metaServer, emitter)
			r.headroomPolicies = append(r.headroomPolicies, &internalHeadroomPolicy{
				name:                policyName,
				policy:              policy,
				internalPolicyState: internalPolicyState{updateStatus: types.PolicyUpdateFailed},
			})
		} else {
			general.ErrorS(fmt.Errorf("failed to find headroom policy"), "policyName", policyName, "region", r.regionType)
		}
	}
}

// getProvisionControlKnob get current adjusted control knobs for each policy
func (r *QoSRegionBase) getProvisionControlKnob() map[types.CPUProvisionPolicyName]types.ControlKnob {
	provisionControlKnob := make(map[types.CPUProvisionPolicyName]types.ControlKnob)
	for _, internal := range r.provisionPolicies {
		if internal.updateStatus != types.PolicyUpdateSucceeded {
			continue
		}

		controlKnob, err := internal.policy.GetControlKnobAdjusted()
		if err != nil {
			klog.Errorf("[qosaware-cpu] get control knob by policy %v failed: %v", internal.name, err)
			continue
		}

		provisionControlKnob[internal.name] = controlKnob

		for name, value := range controlKnob {
			_ = r.emitter.StoreFloat64(metricCPUProvisionControlKnobRaw, value.Value, metrics.MetricTypeNameRaw, []metrics.MetricTag{
				{Key: metricTagKeyRegionType, Val: string(r.regionType)},
				{Key: metricTagKeyRegionName, Val: r.name},
				{Key: metricTagKeyPolicyName, Val: string(internal.name)},
				{Key: metricTagKeyControlKnobName, Val: string(name)},
				{Key: metricTagKeyControlKnobAction, Val: string(value.Action)},
			}...)
			klog.Infof("[qosaware-cpu] get raw control knob by policy: %v, knob: %v, action: %v, value: %v",
				internal.name, name, value.Action, value.Value)
		}
	}

	return provisionControlKnob
}

// regulateProvisionControlKnob regulate provision control knob for each provision policy
func (r *QoSRegionBase) regulateProvisionControlKnob(originControlKnob map[types.CPUProvisionPolicyName]types.ControlKnob,
	lastControlKnob *types.ControlKnob) {
	provisionPolicyResults := make(map[types.CPUProvisionPolicyName]*provisionPolicyResult)
	firstValidPolicy := types.CPUProvisionPolicyNone
	for _, internal := range r.provisionPolicies {
		if internal.updateStatus != types.PolicyUpdateSucceeded {
			continue
		}

		controlKnob, ok := originControlKnob[internal.name]
		if !ok {
			continue
		}

		if firstValidPolicy == types.CPUProvisionPolicyNone {
			firstValidPolicy = internal.name
		}

		policyResult, ok := r.provisionPolicyResults[internal.name]
		if !ok || policyResult == nil {
			policyResult = newProvisionPolicyResult(r.ResourceEssentials)
			policyResult.regulateControlKnob(controlKnob, lastControlKnob)
		} else {
			policyResult.setEssentials(r.ResourceEssentials)
			// only set regulator last cpu requirement for first valid policy
			if internal.name == firstValidPolicy {
				policyResult.regulateControlKnob(controlKnob, lastControlKnob)
			} else {
				policyResult.regulateControlKnob(controlKnob, nil)
			}
		}

		provisionPolicyResults[internal.name] = policyResult
	}

	r.provisionPolicyResults = provisionPolicyResults
	for policy, result := range r.provisionPolicyResults {
		for knob, value := range result.getControlKnob() {
			_ = r.emitter.StoreFloat64(metricCPUProvisionControlKnobRegulated, value.Value, metrics.MetricTypeNameRaw, []metrics.MetricTag{
				{Key: metricTagKeyRegionType, Val: string(r.regionType)},
				{Key: metricTagKeyRegionName, Val: r.name},
				{Key: metricTagKeyPolicyName, Val: string(policy)},
				{Key: metricTagKeyControlKnobName, Val: string(knob)},
				{Key: metricTagKeyControlKnobAction, Val: string(value.Action)},
			}...)
			klog.Infof("[qosaware-cpu] get regulated control knob by policy: %v, knob: %v, action: %v, value: %v",
				policy, knob, value.Action, value.Value)
		}
	}
}

// getIndicators returns indicator targets from spd and current by region specific indicator getters
func (r *QoSRegionBase) getIndicators() (types.Indicator, error) {
	ctx := context.Background()
	indicatorTargetConfig, ok := r.conf.RegionIndicatorTargetConfiguration[r.regionType]
	if !ok {
		return nil, fmt.Errorf("get %v indicators failed", r.regionType)
	}

	indicators := make(types.Indicator)
	for _, indicator := range indicatorTargetConfig {
		indicatorName := indicator.Name
		defaultTarget := indicator.Target
		indicatorCurrentGetter, ok := r.indicatorCurrentGetters[indicatorName]
		if !ok {
			continue
		}

		current, err := indicatorCurrentGetter()
		if err != nil {
			return nil, err
		}

		minTarget := defaultTarget
		if len(r.podSet) > 0 {
			minTarget = math.MaxFloat64
			for podUID := range r.podSet {
				indicatorTarget, err := r.getPodIndicatorTarget(ctx, podUID, indicatorName, defaultTarget)
				if err != nil {
					return nil, err
				}

				minTarget = math.Min(minTarget, *indicatorTarget)
			}
		}

		indicatorValue := types.IndicatorValue{
			Current: current,
			Target:  minTarget,
		}

		if indicatorValue.Target <= 0 || indicatorValue.Current <= 0 {
			return nil, fmt.Errorf("%v with invalid indicator value %v", indicatorName, indicatorValue)
		}

		indicators[indicatorName] = indicatorValue
	}

	if r.enableBorweinModel && r.provisionPolicyNameInUse == types.CPUProvisionPolicyRama {
		general.Infof("try to update indicators by borwein model")
		return r.borweinController.GetUpdatedIndicators(indicators, r.podSet), nil
	} else {
		return indicators, nil
	}

}

// getPodIndicatorTarget gets pod indicator target by given pod uid and indicator name,
// if no performance target or indicator is found, it will return default target
func (r *QoSRegionBase) getPodIndicatorTarget(ctx context.Context, podUID string,
	indicatorName string, defaultTarget float64) (*float64, error) {
	pod, err := r.metaServer.GetPod(ctx, podUID)
	if err != nil {
		return nil, err
	}

	indicatorTarget := defaultTarget
	servicePerformanceTarget, err := r.metaServer.ServiceSystemPerformanceTarget(ctx, pod)
	if err != nil && !errors.IsNotFound(err) {
		return nil, err
	} else if err != nil {
		return &indicatorTarget, nil
	}

	target, ok := servicePerformanceTarget[indicatorName]
	if !ok {
		return &indicatorTarget, nil
	}

	// get the indicator target in the following order:
	// 1. service indicator upperBound
	// 2. service indicator lowerBound
	// 3. default indicator target

	if target.LowerBound != nil {
		indicatorTarget = *target.LowerBound
	}

	if target.UpperBound != nil {
		indicatorTarget = *target.UpperBound
	}

	return &indicatorTarget, nil
}

// UpdateStatus updates region status based on resource and control essentials
func (r *QoSRegionBase) UpdateStatus() {
	overshoot := r.updateOvershootStatus()
	r.updateBoundType(overshoot)
}

func (r *QoSRegionBase) updateBoundType(overshoot bool) {
	// todo BoundType logic is kind of mess, need to refine this in the future
	boundType := types.BoundUnknown
	resourceUpperBoundHit := false
	if r.IsThrottled() {
		boundType = types.BoundUpper
	} else if v, ok := r.ControlEssentials.ControlKnobs[types.ControlKnobNonReclaimedCPUSize]; ok {
		if v.Value <= r.ResourceEssentials.ResourceLowerBound {
			boundType = types.BoundLower
		} else {
			boundType = types.BoundNone
			if v.Value >= r.ResourceEssentials.ResourceUpperBound || !r.enableReclaim() {
				resourceUpperBoundHit = true
			}
		}
	}
	if overshoot && resourceUpperBoundHit {
		boundType = types.BoundUpper
	}
	// fill in bound entry
	r.regionStatus.BoundType = boundType
}

func (r *QoSRegionBase) updateOvershootStatus() bool {
	// reset entries
	r.regionStatus.OvershootStatus = make(map[string]types.OvershootType)

	for indicatorName := range r.indicatorCurrentGetters {
		r.regionStatus.OvershootStatus[indicatorName] = types.OvershootUnknown
	}
	overshoot := false

	// fill in overshoot entry
	for indicatorName, indicator := range r.ControlEssentials.Indicators {
		if indicator.Current > indicator.Target && !r.IsIdle() {
			overshoot = true
			r.regionStatus.OvershootStatus[indicatorName] = types.OvershootTrue
		} else {
			r.regionStatus.OvershootStatus[indicatorName] = types.OvershootFalse
		}
	}
	return overshoot
}

func (r *QoSRegionBase) EnableReclaim() bool {
	return r.ResourceEssentials.EnableReclaim
}

func (r *QoSRegionBase) IsThrottled() bool {
	return r.throttled.Load()
}

func (r *QoSRegionBase) IsIdle() bool {
	return r.idle.Load()
}
