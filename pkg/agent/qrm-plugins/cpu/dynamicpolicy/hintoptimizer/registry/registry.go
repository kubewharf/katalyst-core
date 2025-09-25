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

package registry

import (
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/hintoptimizer/policy"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/hintoptimizer/policy/canonical"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/hintoptimizer/policy/memorybandwidth"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/hintoptimizer/policy/metricbased"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/hintoptimizer/policy/resourcepackage"
)

var SharedCoresHintOptimizerRegistry = policy.HintOptimizerRegistry{
	canonical.HintOptimizerNameCanonical:             canonical.NewCanonicalHintOptimizer,
	memorybandwidth.HintOptimizerNameMemoryBandwidth: memorybandwidth.NewMemoryBandwidthHintOptimizer,
	metricbased.HintOptimizerNameMetricBased:         metricbased.NewMetricBasedHintOptimizer,
	resourcepackage.HintOptimizerNameResourcePackage: resourcepackage.NewResourcePackageHintOptimizer,
}

var DedicatedCoresHintOptimizerRegistry = policy.HintOptimizerRegistry{
	memorybandwidth.HintOptimizerNameMemoryBandwidth: memorybandwidth.NewMemoryBandwidthHintOptimizer,
	resourcepackage.HintOptimizerNameResourcePackage: resourcepackage.NewResourcePackageHintOptimizer,
}
