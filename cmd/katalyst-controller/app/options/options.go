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
	"k8s.io/apimachinery/pkg/util/errors"
	cliflag "k8s.io/component-base/cli/flag"

	"github.com/kubewharf/katalyst-core/cmd/base/options"
	"github.com/kubewharf/katalyst-core/pkg/config"
)

// Options holds the configurations for controllers.
type Options struct {
	*options.GenericOptions

	genericControllerOptions *GenericControllerOptions
	controllersOptions       *ControllersOptions
}

// NewOptions creates a new Options with a default config.
func NewOptions() *Options {
	return &Options{
		GenericOptions:           options.NewGenericOptions(),
		genericControllerOptions: NewGenericControllerOptions(),
		controllersOptions:       NewControllersOptions(),
	}
}

// AddFlags adds flags  to the specified FlagSet.
func (o *Options) AddFlags(fss *cliflag.NamedFlagSets) {
	o.GenericOptions.AddFlags(fss)
	o.genericControllerOptions.AddFlags(fss)
	o.controllersOptions.AddFlags(fss)
}

// ApplyTo fills up config with options
func (o *Options) ApplyTo(c *config.Configuration) error {
	errList := make([]error, 0, 3)

	errList = append(errList, o.GenericOptions.ApplyTo(c.GenericConfiguration))
	errList = append(errList, o.genericControllerOptions.ApplyTo(c.GenericControllerConfiguration))
	errList = append(errList, o.controllersOptions.ApplyTo(c.ControllersConfiguration))

	return errors.NewAggregate(errList)
}

func (o *Options) Config() (*config.Configuration, error) {
	c := config.NewConfiguration()
	if err := o.ApplyTo(c); err != nil {
		return nil, err
	}
	return c, nil
}
