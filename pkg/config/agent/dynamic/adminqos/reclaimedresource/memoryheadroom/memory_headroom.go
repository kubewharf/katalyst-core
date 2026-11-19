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

package memoryheadroom

import (
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/crd"
)

type MemoryHeadroomConfiguration struct {
	*MemoryUtilBasedConfiguration
	// ReclaimedMemoryMaxRatio is the ratio (in [0, 1]) of the maximum amount of memory
	// that can be reclaimed per numa at any time. 0 means no limit.
	ReclaimedMemoryMaxRatio float64
}

func NewMemoryHeadroomConfiguration() *MemoryHeadroomConfiguration {
	return &MemoryHeadroomConfiguration{
		MemoryUtilBasedConfiguration: NewMemoryUtilBasedConfiguration(),
	}
}

func (c *MemoryHeadroomConfiguration) ApplyConfiguration(conf *crd.DynamicConfigCRD) {
	c.MemoryUtilBasedConfiguration.ApplyConfiguration(conf)

	if aqc := conf.AdminQoSConfiguration; aqc != nil &&
		aqc.Spec.Config.ReclaimedResourceConfig != nil &&
		aqc.Spec.Config.ReclaimedResourceConfig.MemoryHeadroomConfig != nil {
		if r := aqc.Spec.Config.ReclaimedResourceConfig.MemoryHeadroomConfig.ReclaimedMemoryMaxRatio; r != nil {
			c.ReclaimedMemoryMaxRatio = clampReclaimedMemoryMaxRatio(*r)
		}
	}
}

// clampReclaimedMemoryMaxRatio bounds ReclaimedMemoryMaxRatio into [0, 1]. A
// value >1 would amplify rather than limit reclaim (contradicting the MaxRatio
// semantics), and a negative value is meaningless; both are corrected with a
// warning so a misconfigured CRD cannot silently take effect.
func clampReclaimedMemoryMaxRatio(ratio float64) float64 {
	if ratio < 0 {
		klog.Warningf("ReclaimedMemoryMaxRatio=%.4f is out of range [0,1], clamping to 0", ratio)
		return 0
	}
	if ratio > 1 {
		klog.Warningf("ReclaimedMemoryMaxRatio=%.4f is out of range [0,1], clamping to 1", ratio)
		return 1
	}
	return ratio
}
