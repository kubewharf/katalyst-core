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

package advisor

import (
	"fmt"

	cliflag "k8s.io/component-base/cli/flag"

	configv1alpha1 "github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/adminqos/advisor"
)

type MemoryGuardOptions struct {
	Enable                       bool
	CriticalWatermarkScaleFactor float64
	CriticalWatermarkSource      string
}

func NewMemoryGuardOptions() *MemoryGuardOptions {
	return &MemoryGuardOptions{
		Enable:                       true,
		CriticalWatermarkScaleFactor: 1.0,
		CriticalWatermarkSource:      string(configv1alpha1.CriticalWatermarkSourceLow),
	}
}

// AddFlags parses the flags to MemoryGuardOptions
func (o *MemoryGuardOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("memory-guard")

	fs.BoolVar(&o.Enable, "memory-guard-enable", o.Enable,
		"set true to enable memory guard")
	fs.Float64Var(&o.CriticalWatermarkScaleFactor, "memory-guard-critical-watermark-scale-factor", o.CriticalWatermarkScaleFactor,
		"set critical watermark scale factor")
	fs.StringVar(&o.CriticalWatermarkSource, "memory-advisor-critical-watermark-source",
		o.CriticalWatermarkSource,
		`which zoneinfo watermark memoryGuard uses as the per-NUMA critical baseline. One of "low", "high".`)
}

func (o *MemoryGuardOptions) ApplyTo(c *advisor.MemoryGuardConfiguration) error {
	c.Enable = o.Enable
	c.CriticalWatermarkScaleFactor = o.CriticalWatermarkScaleFactor

	source, err := normalizeCriticalWatermarkSource(o.CriticalWatermarkSource)
	if err != nil {
		return err
	}
	c.CriticalWatermarkSource = source
	return nil
}

func normalizeCriticalWatermarkSource(source string) (configv1alpha1.CriticalWatermarkSource, error) {
	switch source {
	case "":
		return configv1alpha1.CriticalWatermarkSourceLow, nil
	case string(configv1alpha1.CriticalWatermarkSourceLow):
		return configv1alpha1.CriticalWatermarkSourceLow, nil
	case string(configv1alpha1.CriticalWatermarkSourceHigh):
		return configv1alpha1.CriticalWatermarkSourceHigh, nil
	default:
		return "", fmt.Errorf(
			"invalid --memory-advisor-critical-watermark-source %q, want \"low\" or \"high\"",
			source,
		)
	}
}
