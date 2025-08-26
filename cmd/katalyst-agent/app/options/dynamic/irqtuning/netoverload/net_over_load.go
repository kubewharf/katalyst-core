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

package netoverload

import (
	cliflag "k8s.io/component-base/cli/flag"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/irqtuning/netoverload"
)

type IRQCoreNetOverloadThresholdOptions struct {
	// Ratio of softnet_stat 3rd col time_squeeze packets / softnet_stat 1st col processed packets
	SoftNetTimeSqueezeRatio float64
}

func NewIRQCoreNetOverloadThresholdOptions() *IRQCoreNetOverloadThresholdOptions {
	return &IRQCoreNetOverloadThresholdOptions{
		SoftNetTimeSqueezeRatio: 0.1,
	}
}

func (o *IRQCoreNetOverloadThresholdOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("net-overload-thresholds")
	fs.Float64Var(&o.SoftNetTimeSqueezeRatio, "softnet-time-squeeze-ratio", o.SoftNetTimeSqueezeRatio, "ratio of softnet_stat 3rd col time_squeeze packets / softnet_stat 1st col processed packets")
}

func (o *IRQCoreNetOverloadThresholdOptions) ApplyTo(c *netoverload.IRQCoreNetOverloadThresholds) error {
	c.SoftNetTimeSqueezeRatio = o.SoftNetTimeSqueezeRatio
	return nil
}
