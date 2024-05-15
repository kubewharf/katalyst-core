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

package options

import (
	cliflag "k8s.io/component-base/cli/flag"

	"github.com/kubewharf/katalyst-core/pkg/config/webhook"
)

// VPAOptions holds the configurations for VPA webhook.
type VPAOptions struct{}

// NewVPAOptions creates a new Options with a default config.
func NewVPAOptions() *VPAOptions {
	return &VPAOptions{}
}

// AddFlags adds flags  to the specified FlagSet.
func (o *VPAOptions) AddFlags(fss *cliflag.NamedFlagSets) {
}

// ApplyTo fills up config with options
func (o *VPAOptions) ApplyTo(c *webhook.VPAConfig) error {
	return nil
}

func (o *VPAOptions) Config() (*webhook.VPAConfig, error) {
	c := &webhook.VPAConfig{}
	if err := o.ApplyTo(c); err != nil {
		return nil, err
	}
	return c, nil
}
