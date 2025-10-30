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

package feature_cpu

import (
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/advisorsvc"
	"github.com/kubewharf/katalyst-core/pkg/agent/utilcomponent/featuregatenegotiation/finders"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

const NegotiationFeatureGateQuotaCtrlKnob = "feature_gate_quota_ctrl_knob"

type QuotaCtrlKnob struct{}

func (e *QuotaCtrlKnob) GetFeatureGate(conf *config.Configuration) *advisorsvc.FeatureGate {
	if conf.EnableControlKnobCPUQuota == false {
		general.Infof("feature_gate_quota_ctrl_knob is not supported: %v", conf.EnableControlKnobCPUQuota)
		return nil
	}

	if conf.EnableControlKnobCPUQuotaForV1 == false && !common.CheckCgroup2UnifiedMode() {
		general.Infof("feature_gate_quota_ctrl_knob for v1 is not supported: %v", conf.EnableControlKnobCPUQuotaForV1)
		return nil
	}

	return &advisorsvc.FeatureGate{
		Name:                  NegotiationFeatureGateQuotaCtrlKnob,
		Type:                  finders.FeatureGateTypeCPU,
		MustMutuallySupported: false,
	}
}
