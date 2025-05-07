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

package throughputclassswitch

import (
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/irqtuning/throughputclassswitch"
	cliflag "k8s.io/component-base/cli/flag"
)

type LowThroughputThresholdOptions struct {
	RxPPSThresh     uint64
	SuccessiveCount int
}

func NewLowThroughputThresholdOptions() *LowThroughputThresholdOptions {
	return &LowThroughputThresholdOptions{
		RxPPSThresh:     3000,
		SuccessiveCount: 30,
	}
}

func (o *LowThroughputThresholdOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("low-throughput-thresholds")
	fs.Uint64Var(&o.RxPPSThresh, "rx-pps-thresh", o.RxPPSThresh, "rx pps threshold")
	fs.IntVar(&o.SuccessiveCount, "successive-count", o.SuccessiveCount, "successive count")
}

func (o *LowThroughputThresholdOptions) ApplyTo(c *throughputclassswitch.LowThroughputThresholdConfig) error {
	c.RxPPSThresh = o.RxPPSThresh
	c.SuccessiveCount = o.SuccessiveCount

	return nil
}
