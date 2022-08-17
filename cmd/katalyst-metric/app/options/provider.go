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
	basecmd "sigs.k8s.io/custom-metrics-apiserver/pkg/cmd"

	"github.com/kubewharf/katalyst-core/pkg/config/metric"
)

// ProviderOptions holds the configurations for katalyst metrics module.
type ProviderOptions struct {
	// since we use the wrapped tools in custom-metrics-api-server,
	// use all the flags in it (to connect with APIServer)
	AdapterBase *basecmd.AdapterBase
}

// NewProviderOptions creates a new ProviderOptions with a default config.
func NewProviderOptions() *ProviderOptions {
	return &ProviderOptions{
		AdapterBase: &basecmd.AdapterBase{},
	}
}

// AddFlags adds flags  to the specified FlagSet.
func (o *ProviderOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("metric-server")

	o.AdapterBase.FlagSet = fs
	o.AdapterBase.InstallFlags()
}

// ApplyTo fills up config with options
func (o *ProviderOptions) ApplyTo(c *metric.ProviderConfiguration) error {
	c.Adapter = o.AdapterBase
	return nil
}

func (o *ProviderOptions) Config() (*metric.ProviderConfiguration, error) {
	c := metric.NewProviderConfiguration()
	if err := o.ApplyTo(c); err != nil {
		return nil, err
	}
	return c, nil
}
