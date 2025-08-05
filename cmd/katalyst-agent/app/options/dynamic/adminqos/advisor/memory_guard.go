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
	cliflag "k8s.io/component-base/cli/flag"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/adminqos/advisor"
)

type MemoryGuardOptions struct {
	Enable                       bool
	CriticalWatermarkScaleFactor float64
}

func NewMemoryGuardOptions() *MemoryGuardOptions {
	return &MemoryGuardOptions{
		Enable:                       true,
		CriticalWatermarkScaleFactor: 1.0,
	}
}

// AddFlags parses the flags to MemoryGuardOptions
func (o *MemoryGuardOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("memory-guard")

	fs.BoolVar(&o.Enable, "memory-guard-enable", o.Enable,
		"set true to enable memory guard")
	fs.Float64Var(&o.CriticalWatermarkScaleFactor, "memory-guard-critical-watermark-scale-factor", o.CriticalWatermarkScaleFactor,
		"set critical watermark scale factor")
}

func (o *MemoryGuardOptions) ApplyTo(c *advisor.MemoryGuardConfiguration) error {
	c.Enable = o.Enable
	c.CriticalWatermarkScaleFactor = o.CriticalWatermarkScaleFactor
	return nil
}
