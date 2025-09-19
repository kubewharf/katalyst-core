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
	"fmt"

	"github.com/kubewharf/katalyst-core/pkg/agent/utilcomponent/featuregatenegotiation/finders/feature_cpu"
)

func IsQuotaCtrlKnobEnabled(mr MetaReader) (bool, error) {
	featureGates, err := mr.GetSupportedWantedFeatureGates()
	if err != nil {
		return false, fmt.Errorf("get feature gates failed: %v", err)
	}

	quotaCtrlKnobEnabled := false
	if feature, ok := featureGates[feature_cpu.NegotiationFeatureGateQuotaCtrlKnob]; ok && feature != nil {
		quotaCtrlKnobEnabled = true
	}

	return quotaCtrlKnobEnabled, nil
}
