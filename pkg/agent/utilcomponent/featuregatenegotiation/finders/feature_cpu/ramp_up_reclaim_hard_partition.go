/*
Copyright 2026 The Katalyst Authors.

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
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

const NegotiationFeatureGateCPURampUpReclaimHardPartition = "feature_gate_cpu_ramp_up_reclaim_hard_partition"

type CPURampUpReclaimHardPartition struct{}

func (e *CPURampUpReclaimHardPartition) GetFeatureGate(conf *config.Configuration) *advisorsvc.FeatureGate {
	if conf == nil || conf.GetDynamicConfiguration() == nil || !conf.GetDynamicConfiguration().EnableRampUpReclaimHardPartition {
		general.Infof("feature_gate_cpu_ramp_up_reclaim_hard_partition is not supported")
		return nil
	}

	return &advisorsvc.FeatureGate{
		Name:                  NegotiationFeatureGateCPURampUpReclaimHardPartition,
		Type:                  finders.FeatureGateTypeCPU,
		MustMutuallySupported: true,
	}
}
