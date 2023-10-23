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

	"github.com/kubewharf/katalyst-core/pkg/config/controller"
)

// MonitorOptions holds the configurations for Monitor.
type MonitorOptions struct {
	// EnableCNRMonitor is a flag to enable CNR monitor controller
	EnableCNRMonitor bool
}

func NewMonitorOptions() *MonitorOptions {
	return &MonitorOptions{
		EnableCNRMonitor: true,
	}
}

// AddFlags adds flags  to the specified FlagSet.
func (o *MonitorOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("monitor")

	fs.BoolVar(&o.EnableCNRMonitor, "cnr-monitor-enable", o.EnableCNRMonitor,
		"whether to enable the cnr controller")
}

// ApplyTo fills up config with options
func (o *MonitorOptions) ApplyTo(c *controller.MonitorConfig) error {
	c.EnableCNRMonitor = o.EnableCNRMonitor
	return nil
}

func (o *MonitorOptions) Config() (*controller.MonitorConfig, error) {
	c := &controller.MonitorConfig{}
	if err := o.ApplyTo(c); err != nil {
		return nil, err
	}
	return c, nil
}
