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
	"k8s.io/apimachinery/pkg/util/errors"
	cliflag "k8s.io/component-base/cli/flag"

	dynamicqrm "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/adminqos/qrm"
)

type MemoryPluginOptions struct {
	*FragMemOptions
	*HostWatermarkOptions
}

func NewMemoryPluginOptions() *MemoryPluginOptions {
	return &MemoryPluginOptions{
		FragMemOptions:       NewFragMemOptions(),
		HostWatermarkOptions: NewHostWatermarkOptions(),
	}
}

func (o *MemoryPluginOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	o.FragMemOptions.AddFlags(fss)
	o.HostWatermarkOptions.AddFlags(fss)
}

func (o *MemoryPluginOptions) ApplyTo(c *dynamicqrm.MemoryPluginConfiguration) error {
	var errList []error

	errList = append(errList, o.FragMemOptions.ApplyTo(c.FragMemConfiguration))
	errList = append(errList, o.HostWatermarkOptions.ApplyTo(c.HostWatermarkConfiguration))
	return errors.NewAggregate(errList)
}
