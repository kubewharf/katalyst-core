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

	controllerconfig "github.com/kubewharf/katalyst-core/pkg/config/controller"
)

type ControllersOptions struct {
	*IHPAOptions
	*VPAOptions
	*KCCOptions
	*SPDOptions
	*LifeCycleOptions
	*MonitorOptions
	*OvercommitOptions
	*ResourceRecommenderOptions
}

func NewControllersOptions() *ControllersOptions {
	return &ControllersOptions{
		IHPAOptions:                NewIHPAOptions(),
		VPAOptions:                 NewVPAOptions(),
		KCCOptions:                 NewKCCOptions(),
		SPDOptions:                 NewSPDOptions(),
		LifeCycleOptions:           NewLifeCycleOptions(),
		MonitorOptions:             NewMonitorOptions(),
		OvercommitOptions:          NewOvercommitOptions(),
		ResourceRecommenderOptions: NewResourceRecommenderOptions(),
	}
}

func (o *ControllersOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	o.IHPAOptions.AddFlags(fss)
	o.VPAOptions.AddFlags(fss)
	o.KCCOptions.AddFlags(fss)
	o.SPDOptions.AddFlags(fss)
	o.LifeCycleOptions.AddFlags(fss)
	o.MonitorOptions.AddFlags(fss)
	o.OvercommitOptions.AddFlags(fss)
	o.ResourceRecommenderOptions.AddFlags(fss)
}

// ApplyTo fills up config with options
func (o *ControllersOptions) ApplyTo(c *controllerconfig.ControllersConfiguration) error {
	var errList []error

	errList = append(errList, o.IHPAOptions.ApplyTo(c.IHPAConfig))
	errList = append(errList, o.VPAOptions.ApplyTo(c.VPAConfig))
	errList = append(errList, o.KCCOptions.ApplyTo(c.KCCConfig))
	errList = append(errList, o.SPDOptions.ApplyTo(c.SPDConfig))
	errList = append(errList, o.LifeCycleOptions.ApplyTo(c.LifeCycleConfig))
	errList = append(errList, o.MonitorOptions.ApplyTo(c.MonitorConfig))
	errList = append(errList, o.OvercommitOptions.ApplyTo(c.OvercommitConfig))
	errList = append(errList, o.ResourceRecommenderOptions.ApplyTo(c.ResourceRecommenderConfig))
	return errors.NewAggregate(errList)
}

func (o *ControllersOptions) Config() (*controllerconfig.ControllersConfiguration, error) {
	c := controllerconfig.NewControllersConfiguration()
	if err := o.ApplyTo(c); err != nil {
		return nil, err
	}
	return c, nil
}
