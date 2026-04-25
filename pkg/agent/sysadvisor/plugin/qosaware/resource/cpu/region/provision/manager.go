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

package provision

import (
	"fmt"
	"sync"

	"k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"

	configapi "github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/region/regulator"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

const (
	metricCPUProvisionControlKnobRaw       = "cpu_provision_control_knob_raw"
	metricCPUProvisionControlKnobRegulated = "cpu_provision_control_knob_regulated"

	metricTagKeyPolicyName        = "policy_name"
	metricTagKeyRegionName        = "region_name"
	metricTagKeyControlKnobName   = "control_knob_name"
	metricTagKeyControlKnobAction = "control_knob_action"
)

type provisionPolicyResult struct {
	msg                        string
	essentials                 types.ResourceEssentials
	regulatorOptions           regulator.RegulatorOptions
	controlKnobValueRegulators map[configapi.ControlKnobName]regulator.Regulator
}

func newProvisionPolicyResult(essentials types.ResourceEssentials, regulatorOptions regulator.RegulatorOptions, msg string) *provisionPolicyResult {
	return &provisionPolicyResult{
		msg:                        msg,
		essentials:                 essentials,
		regulatorOptions:           regulatorOptions,
		controlKnobValueRegulators: make(map[configapi.ControlKnobName]regulator.Regulator),
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
func (r *provisionPolicyResult) regulateControlKnob(currentControlKnob, effectiveControlKnob types.ControlKnob) {
	klog.InfoS("[provisionPolicyResult]", "region", r.msg,
		"currentControlKnob", currentControlKnob, "effectiveControlKnob", effectiveControlKnob)
	for name, knob := range currentControlKnob {
		reg, ok := r.controlKnobValueRegulators[name]
		if !ok || reg == nil {
			reg = r.newRegulator(name)
		}
		effectiveKnobItem, ok := effectiveControlKnob[name]
		if ok {
			reg.Regulate(knob, &effectiveKnobItem)
		} else {
			reg.Regulate(knob, nil)
		}
		r.controlKnobValueRegulators[name] = reg
	}
	// cleanup control knob regulators
	for name := range r.controlKnobValueRegulators {
		if _, ok := currentControlKnob[name]; !ok {
			delete(r.controlKnobValueRegulators, name)
		}
	}
}

// newRegulator new regulator according to the control knob name
func (r *provisionPolicyResult) newRegulator(name configapi.ControlKnobName) regulator.Regulator {
	switch name {
	// only non-reclaimed cpu size need regulate now
	case configapi.ControlKnobNonReclaimedCPURequirement:
		return regulator.NewCPURegulator(r.essentials, r.regulatorOptions)
	default:
		return regulator.NewDummyRegulator()
	}
}

// getControlKnob is to get final control knob from regulators
func (r *provisionPolicyResult) getControlKnob() types.ControlKnob {
	controlKnob := make(types.ControlKnob)
	for name, rg := range r.controlKnobValueRegulators {
		controlKnob[name] = types.ControlKnobItem{
			Value:  float64(rg.GetValue()),
			Action: types.ControlKnobActionNone,
		}
	}
	return controlKnob
}

type Manager struct {
	sync.Mutex
	conf                     *config.Configuration
	regionName               string
	emitter                  metrics.MetricEmitter
	policies                 []Policy
	provisionPolicyNameInUse types.CPUProvisionPolicyName
	provisionPolicyResults   map[types.CPUProvisionPolicyName]*provisionPolicyResult
	restrictedCtrlKnobs      sets.String
}

func NewManager(regionName string, conf *config.Configuration, emitter metrics.MetricEmitter) *Manager {
	return &Manager{
		conf:                     conf,
		regionName:               regionName,
		emitter:                  emitter,
		policies:                 []Policy{},
		provisionPolicyNameInUse: types.CPUProvisionPolicyNone,
		provisionPolicyResults:   make(map[types.CPUProvisionPolicyName]*provisionPolicyResult),
		restrictedCtrlKnobs:      sets.NewString(string(configapi.ControlKnobReclaimedCoresCPUQuota)),
	}
}

func (m *Manager) Add(p Policy) {
	m.Lock()
	defer m.Unlock()
	m.policies = append(m.policies, p)
}

func (m *Manager) Update(ctx PolicyContext) error {
	m.Lock()
	defer m.Unlock()

	var errList []error
	for _, p := range m.policies {
		if err := p.Update(ctx); err != nil {
			general.ErrorS(err, " update provision policy", "policy", p.Name())
			errList = append(errList, err)
		}
	}

	// policy -> controlKnob
	validControlKnobs := m.getValidControlKnobs()
	validControlKnobs = m.restrictProvisionControlKnob(validControlKnobs)
	m.regulateProvisionControlKnob(validControlKnobs, ctx)
	return errors.NewAggregate(errList)
}

func (m *Manager) getValidControlKnobs() map[types.CPUProvisionPolicyName]types.ControlKnob {
	provisionControlKnob := make(map[types.CPUProvisionPolicyName]types.ControlKnob)
	for _, p := range m.policies {
		controlKnob, err := p.GetControlKnobAdjusted()
		if err != nil || controlKnob == nil {
			general.Errorf("[qosaware-cpu] get control knob by policy %v failed: %v", p.Name(), err)
			continue
		}

		provisionControlKnob[p.Name()] = controlKnob

		for name, value := range controlKnob {
			_ = m.emitter.StoreFloat64(metricCPUProvisionControlKnobRaw, value.Value, metrics.MetricTypeNameRaw, []metrics.MetricTag{
				{Key: metricTagKeyRegionName, Val: m.regionName},
				{Key: metricTagKeyPolicyName, Val: string(p.Name())},
				{Key: metricTagKeyControlKnobName, Val: string(name)},
				{Key: metricTagKeyControlKnobAction, Val: string(value.Action)},
			}...)

			klog.InfoS("[qosaware-cpu] get raw control knob", "meta", p.GetMetaInfo(), "policy", p.Name(),
				"knob", name, "action", value.Action, "value", value.Value, "regionName", m.regionName)
		}
	}

	return provisionControlKnob
}

func (m *Manager) restrictProvisionControlKnob(originControlKnob map[types.CPUProvisionPolicyName]types.ControlKnob) map[types.CPUProvisionPolicyName]types.ControlKnob {
	restrictedControlKnob := make(map[types.CPUProvisionPolicyName]types.ControlKnob)
	for policyName, controlKnob := range originControlKnob {
		restrictedControlKnob[policyName] = controlKnob.Clone()
		refPolicyName, ok := m.conf.RestrictRefPolicy[policyName]
		if !ok {
			continue
		}
		refControlKnob, ok := originControlKnob[refPolicyName]
		if !ok {
			klog.Errorf("get control knob from reference policy %v for policy %v failed", refPolicyName, policyName)
			continue
		}

		for controlKnobName, rawKnobValue := range controlKnob {
			if !m.restrictedCtrlKnobs.Has(string(controlKnobName)) {
				continue
			}

			refKnobValue, ok := refControlKnob[controlKnobName]
			if !ok {
				continue
			}
			restrictedKnobValue := rawKnobValue
			if rawKnobValue.Value > refKnobValue.Value {
				restrictedKnobValue = refKnobValue

				klog.Infof("[qosaware-cpu] restrict control knob %v for policy %v by policy %v from %.2f to %.2f, refKnobValue: %v",
					controlKnobName, policyName, refPolicyName, rawKnobValue.Value, restrictedKnobValue.Value, refKnobValue.Value)
			}

			restrictedControlKnob[policyName][controlKnobName] = restrictedKnobValue
		}
	}
	return restrictedControlKnob
}

// regulateProvisionControlKnob regulate provision control knob for each provision policy
func (m *Manager) regulateProvisionControlKnob(originControlKnob map[types.CPUProvisionPolicyName]types.ControlKnob, ctx PolicyContext) {
	general.InfoS("[qosaware-cpu] regulate control knobs", "ctx", ctx, "originControlKnob", originControlKnob)
	if originControlKnob == nil {
		return
	}
	provisionPolicyResults := make(map[types.CPUProvisionPolicyName]*provisionPolicyResult)
	firstValidPolicy := types.CPUProvisionPolicyNone
	for _, p := range m.policies {
		general.InfoS("regulate", "policy", p.Name())
		controlKnob, ok := originControlKnob[p.Name()]
		if !ok {
			continue
		}
		if firstValidPolicy == types.CPUProvisionPolicyNone {
			firstValidPolicy = p.Name()
		}
		policyResult, ok := m.provisionPolicyResults[p.Name()]
		if !ok || policyResult == nil {
			policyResult = newProvisionPolicyResult(ctx.ResourceEssentials, ctx.RegulatorOptions, "")
		}
		policyResult.setEssentials(ctx.ResourceEssentials)
		effective := ctx.ControlKnobs
		// only set regulator last cpu requirement for first valid policy
		if p.Name() != firstValidPolicy {
			effective = nil
		}
		policyResult.regulateControlKnob(controlKnob, effective)
		provisionPolicyResults[p.Name()] = policyResult
	}

	m.provisionPolicyNameInUse = firstValidPolicy
	m.provisionPolicyResults = provisionPolicyResults
	for policy, result := range m.provisionPolicyResults {
		for knob, value := range result.getControlKnob() {
			_ = m.emitter.StoreFloat64(metricCPUProvisionControlKnobRegulated, value.Value, metrics.MetricTypeNameRaw, []metrics.MetricTag{
				{Key: metricTagKeyRegionName, Val: m.regionName},
				{Key: metricTagKeyPolicyName, Val: string(policy)},
				{Key: metricTagKeyControlKnobName, Val: string(knob)},
				{Key: metricTagKeyControlKnobAction, Val: string(value.Action)},
			}...)
			klog.InfoS("[qosaware-cpu] get regulated control knob", "region", m.regionName, "bindingNumas", ctx.CpusetMems.String(),
				"policy", policy, "knob", knob, "action", value.Action, "value", value.Value)
		}
	}
}

func (m *Manager) GetCtrlKnob() (types.CPUProvisionPolicyName, types.ControlKnob, error) {
	m.Lock()
	defer m.Unlock()

	result, ok := m.provisionPolicyResults[m.provisionPolicyNameInUse]
	if !ok || result == nil {
		return "", types.ControlKnob{}, fmt.Errorf("no provision policy found in use")
	}

	return m.provisionPolicyNameInUse, result.getControlKnob(), nil
}

func (m *Manager) GetPolicies() []types.CPUProvisionPolicyName {
	m.Lock()
	defer m.Unlock()
	var names []types.CPUProvisionPolicyName
	for _, p := range m.policies {
		names = append(names, p.Name())
	}
	return names
}
