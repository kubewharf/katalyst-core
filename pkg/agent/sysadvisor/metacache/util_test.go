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

package metacache

import (
	"testing"

	"github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"

	"github.com/kubewharf/katalyst-core/pkg/agent/utilcomponent/featuregatenegotiation/finders"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/advisorsvc"
	"github.com/kubewharf/katalyst-core/pkg/agent/utilcomponent/featuregatenegotiation/finders/feature_cpu"
)

func TestUtil_IsQuotaCtrlKnobEnabled(t *testing.T) {
	t.Parallel()

	mc := NewDummyMetaCacheImp()
	enabled, err := IsQuotaCtrlKnobEnabled(mc)
	if err != nil {
		t.Errorf("TestUtil_IsQuotaCtrlKnobEnabled failed: %v", err)
		return
	}

	if enabled {
		t.Errorf("TestUtil_IsQuotaCtrlKnobEnabled unexpected enabled")
		return
	}

	featureGates := map[string]*advisorsvc.FeatureGate{
		feature_cpu.NegotiationFeatureGateQuotaCtrlKnob: {
			Name:                  feature_cpu.NegotiationFeatureGateQuotaCtrlKnob,
			Type:                  finders.FeatureGateTypeCPU,
			MustMutuallySupported: false,
		},
	}
	err = mc.SetSupportedWantedFeatureGates(featureGates)
	if err != nil {
		t.Errorf("TestUtil_IsQuotaCtrlKnobEnabled set featutegates failed: %v", err)
		return
	}

	enabled, err = IsQuotaCtrlKnobEnabled(mc)
	if err != nil {
		t.Errorf("TestUtil_IsQuotaCtrlKnobEnabled failed: %v", err)
		return
	}

	if common.CheckCgroup2UnifiedMode() && !enabled {
		t.Errorf("TestUtil_IsQuotaCtrlKnobEnabled unexpected disabled")
		return
	}
}
