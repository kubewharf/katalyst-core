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
	"time"

	"github.com/kubewharf/katalyst-core/pkg/config/controller"
	cliflag "k8s.io/component-base/cli/flag"
)

// LifeCycleOptions holds the configurations for life cycle.
type LifeCycleOptions struct {
	EnableEviction bool
	DryRun         bool

	CNRUpdateTimeWindow   time.Duration
	CNRMonitorPeriod      time.Duration
	CNRMonitorTaintPeriod time.Duration
	CNRMonitorGracePeriod time.Duration

	CNRAgentSelector []string
	NodeSelector     string
}

// NewLifeCycleOptions creates a new Options with a default config.
func NewLifeCycleOptions() *LifeCycleOptions {
	return &LifeCycleOptions{
		EnableEviction: true,
		DryRun:         true,

		CNRUpdateTimeWindow:   time.Minute,
		CNRMonitorPeriod:      5 * time.Minute,
		CNRMonitorTaintPeriod: 10 * time.Minute,
		CNRMonitorGracePeriod: 20 * time.Minute,

		CNRAgentSelector: []string{"app=katalyst-agent"},
		NodeSelector:     "",
	}
}

// AddFlags adds flags  to the specified FlagSet.
func (o *LifeCycleOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("lifecycle")

	fs.BoolVar(&o.EnableEviction, "lifecycle-enable-eviction", o.EnableEviction, "whether lifecycle controller start eviction controller")
	fs.BoolVar(&o.DryRun, "lifecycle-dry-run", o.DryRun, "A bool to enable and disable lifecycle dry-run.")

	fs.DurationVar(&o.CNRUpdateTimeWindow, "lifecycle-update-agent-health-period", o.CNRUpdateTimeWindow, "how often controller update agent health")
	fs.DurationVar(&o.CNRMonitorPeriod, "lifecycle-monitor-health-period", o.CNRMonitorPeriod, "how often lifecycle controller monitor current agent health")
	fs.DurationVar(&o.CNRMonitorTaintPeriod, "lifecycle-taint-period", o.CNRMonitorTaintPeriod, "if lifecycle controller lost agent for this period,taint node")
	fs.DurationVar(&o.CNRMonitorGracePeriod, "lifecycle-evict-pod-grace-period", o.CNRMonitorGracePeriod, "if lifecycle controller lost agent for this period, evict pod")

	fs.StringArrayVar(&o.CNRAgentSelector, "lifecycle-label-selector", o.CNRAgentSelector, "which agent lifecycle need to monitor")
	fs.StringVar(&o.NodeSelector, "lifecycle-node-selector", o.NodeSelector, "which node should we detect")
}

// ApplyTo fills up config with options
func (o *LifeCycleOptions) ApplyTo(c *controller.LifeCycleConfig) error {
	c.CNRLifecycleConfig.EnableEviction = o.EnableEviction
	c.CNRLifecycleConfig.DryRun = o.DryRun

	c.CNRLifecycleConfig.CNRUpdateTimeWindow = o.CNRUpdateTimeWindow
	c.CNRLifecycleConfig.CNRMonitorPeriod = o.CNRMonitorPeriod
	c.CNRLifecycleConfig.CNRMonitorTaintPeriod = o.CNRMonitorTaintPeriod
	c.CNRLifecycleConfig.CNRMonitorGracePeriod = o.CNRMonitorGracePeriod

	c.CNRLifecycleConfig.CNRAgentSelector = o.CNRAgentSelector
	c.CNRLifecycleConfig.NodeSelector = o.NodeSelector

	return nil
}

func (o *LifeCycleOptions) Config() (*controller.LifeCycleConfig, error) {
	c := &controller.LifeCycleConfig{}
	if err := o.ApplyTo(c); err != nil {
		return nil, err
	}
	return c, nil
}
