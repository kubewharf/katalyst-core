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

package adminqos

import (
	cliflag "k8s.io/component-base/cli/flag"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/global/adminqos"
)

type AdminQoSOptions struct {
	*ReclaimedResourceOptions
}

func NewAdminQoSOptions() *AdminQoSOptions {
	return &AdminQoSOptions{
		ReclaimedResourceOptions: NewReclaimedResourceOptions(),
	}
}

func (o *AdminQoSOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	o.ReclaimedResourceOptions.AddFlags(fss)
}

func (o *AdminQoSOptions) ApplyTo(c *adminqos.AdminQoSConfiguration) error {
	if err := o.ReclaimedResourceOptions.ApplyTo(c.ReclaimedResourceConfiguration); err != nil {
		return err
	}
	return nil
}
