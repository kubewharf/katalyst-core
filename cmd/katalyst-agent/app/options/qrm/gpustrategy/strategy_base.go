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

package gpustrategy

import (
	cliflag "k8s.io/component-base/cli/flag"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/qrm/gpustrategy"
)

type GPUStrategyOptions struct {
	*AllocateStrategyOptions
}

func NewGPUStrategyOptions() *GPUStrategyOptions {
	return &GPUStrategyOptions{
		AllocateStrategyOptions: NewGPUAllocateStrategyOptions(),
	}
}

func (o *GPUStrategyOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	o.AllocateStrategyOptions.AddFlags(fss)
}

func (o *GPUStrategyOptions) ApplyTo(conf *gpustrategy.GPUStrategyConfig) error {
	if err := o.AllocateStrategyOptions.ApplyTo(conf.AllocateStrategyConfig); err != nil {
		return err
	}
	return nil
}
