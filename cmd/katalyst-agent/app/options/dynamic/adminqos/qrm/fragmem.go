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

package qrm

import (
	cliflag "k8s.io/component-base/cli/flag"

	dynamicqrm "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/adminqos/qrm"
)

type FragMemOptions struct {
	EnableFragMem              bool
	MemFragScoreAsync          int
	THPDefaultConfig           string
	THPConfigPolicy            string
	THPHighOrderScoreThreshold int
}

func NewFragMemOptions() *FragMemOptions {
	return &FragMemOptions{
		EnableFragMem:              false,
		MemFragScoreAsync:          80,
		THPDefaultConfig:           "madvise",
		THPConfigPolicy:            "dynamic",
		THPHighOrderScoreThreshold: 85,
	}
}

func (o *FragMemOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("memory_resource_plugin")
	fs.BoolVar(&o.EnableFragMem, "enable-setting-mem-compaction",
		o.EnableFragMem, "if set true, we will enable memory compaction related features")
	fs.IntVar(&o.MemFragScoreAsync, "qrm-memory-frag-score-async",
		o.MemFragScoreAsync, "set the threshold of frag score for async memory compaction")
	fs.StringVar(&o.THPDefaultConfig, "qrm-memory-thp-default-config",
		o.THPDefaultConfig, "default host THP config to recover to (madvise/always/never)")
	fs.StringVar(&o.THPConfigPolicy, "qrm-memory-thp-config-policy",
		o.THPConfigPolicy, "host THP config policy (dynamic/static)")
	fs.IntVar(&o.THPHighOrderScoreThreshold, "qrm-memory-thp-high-order-score-threshold",
		o.THPHighOrderScoreThreshold, "disable THP when max highOrderScore > threshold")
}

func (o *FragMemOptions) ApplyTo(c *dynamicqrm.FragMemConfiguration) error {
	c.EnableFragMem = o.EnableFragMem
	c.MemFragScoreAsync = o.MemFragScoreAsync
	c.THPDefaultConfig = o.THPDefaultConfig
	c.THPConfigPolicy = o.THPConfigPolicy
	c.THPHighOrderScoreThreshold = o.THPHighOrderScoreThreshold
	return nil
}
