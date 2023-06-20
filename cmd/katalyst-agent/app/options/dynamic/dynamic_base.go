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

package dynamic

import (
	"k8s.io/apimachinery/pkg/util/errors"
	cliflag "k8s.io/component-base/cli/flag"

	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options/dynamic/adminqos"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
)

type DynamicOptions struct {
	*adminqos.AdminQoSOptions
}

func NewDynamicOptions() *DynamicOptions {
	return &DynamicOptions{
		AdminQoSOptions: adminqos.NewAdminQoSOptions(),
	}
}

func (o *DynamicOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	o.AdminQoSOptions.AddFlags(fss)
}

func (o *DynamicOptions) ApplyTo(c *dynamic.Configuration) error {
	var errList []error
	errList = append(errList, o.AdminQoSOptions.ApplyTo(c.AdminQoSConfiguration))
	return errors.NewAggregate(errList)
}
