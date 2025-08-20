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
	cliflag "k8s.io/component-base/cli/flag"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/irqtuning/throughputclassswitch"
)

type NormalThroughputThresholdOptions struct {
	RxPPSThreshold  uint64
	SuccessiveCount int
}

func NewNormalThroughputThresholdOptions() *NormalThroughputThresholdOptions {
	return &NormalThroughputThresholdOptions{
		RxPPSThreshold:  6000,
		SuccessiveCount: 10,
	}
}

func (o *NormalThroughputThresholdOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("normal-throughput-thresholds")
	fs.Uint64Var(&o.RxPPSThreshold, "rx-pps-threshold", o.RxPPSThreshold, "rx pps threshold")
	fs.IntVar(&o.SuccessiveCount, "successive-count", o.SuccessiveCount, "successive count")
}

func (o *NormalThroughputThresholdOptions) ApplyTo(c *throughputclassswitch.NormalThroughputThresholdConfig) error {
	c.RxPPSThreshold = o.RxPPSThreshold
	c.SuccessiveCount = o.SuccessiveCount

	return nil
}
