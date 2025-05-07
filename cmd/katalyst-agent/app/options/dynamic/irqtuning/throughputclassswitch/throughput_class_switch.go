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
	"k8s.io/apimachinery/pkg/util/errors"
	cliflag "k8s.io/component-base/cli/flag"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/irqtuning/throughputclassswitch"
)

type ThroughputClassSwitchOptions struct {
	LowThresholdOptions    *LowThroughputThresholdOptions
	NormalThresholdOptions *NormalThroughputThresholdOptions
}

func NewThroughputClassSwitchOptions() *ThroughputClassSwitchOptions {
	return &ThroughputClassSwitchOptions{
		LowThresholdOptions:    NewLowThroughputThresholdOptions(),
		NormalThresholdOptions: NewNormalThroughputThresholdOptions(),
	}
}

func (o *ThroughputClassSwitchOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	o.LowThresholdOptions.AddFlags(fss)
	o.NormalThresholdOptions.AddFlags(fss)
}

func (o *ThroughputClassSwitchOptions) ApplyTo(c *throughputclassswitch.ThroughputClassSwitchConfig) error {
	var errList []error
	errList = append(errList, o.LowThresholdOptions.ApplyTo(c.LowThresholdConfig))
	errList = append(errList, o.NormalThresholdOptions.ApplyTo(c.NormalThresholdConfig))

	return errors.NewAggregate(errList)
}
