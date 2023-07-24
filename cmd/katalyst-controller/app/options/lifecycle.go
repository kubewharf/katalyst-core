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

	"k8s.io/apimachinery/pkg/labels"
	cliflag "k8s.io/component-base/cli/flag"

	"github.com/kubewharf/katalyst-core/pkg/config/controller"
)

type HealthzOptions struct {
	DryRun        bool
	NodeSelector  string
	AgentSelector map[string]string

	CheckWindow        time.Duration
	UnhealthyPeriods   time.Duration
	AgentUnhealthySecs map[string]int

	HandlePeriod  time.Duration
	AgentHandlers map[string]string

	TaintQPS                 float32
	EvictQPS                 float32
	DisruptionTaintThreshold float32
	DisruptionEvictThreshold float32
}

// LifeCycleOptions holds the configurations for life cycle.
type LifeCycleOptions struct {
	EnableHealthz      bool
	EnableCNCLifecycle bool

	*HealthzOptions
}

// NewLifeCycleOptions creates a new Options with a default config.
func NewLifeCycleOptions() *LifeCycleOptions {
	return &LifeCycleOptions{
		EnableHealthz:      false,
		EnableCNCLifecycle: true,
		HealthzOptions: &HealthzOptions{
			DryRun:        false,
			NodeSelector:  "",
			AgentSelector: map[string]string{"katalyst-agent": "app=katalyst-agent"},

			CheckWindow:      5 * time.Minute,
			UnhealthyPeriods: 10 * time.Minute,

			HandlePeriod:  5 * time.Minute,
			AgentHandlers: map[string]string{},

			TaintQPS:                 0.2,
			EvictQPS:                 0.1,
			DisruptionTaintThreshold: 0.2,
			DisruptionEvictThreshold: 0.2,
		},
	}
}

// AddFlags adds flags  to the specified FlagSet.
func (o *LifeCycleOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("lifecycle")

	fs.BoolVar(&o.EnableHealthz, "healthz-enabled", o.EnableHealthz,
		"whether to enable the healthz controller")
	fs.BoolVar(&o.EnableCNCLifecycle, "cnc-lifecycle-enabled", o.EnableCNCLifecycle,
		"whether to enable the cnc lifecycle controller")

	fs.BoolVar(&o.DryRun, "healthz-dry-run", o.DryRun,
		"a bool to enable and disable dry-run logic of healthz controller.")
	fs.StringVar(&o.NodeSelector, "healthz-node-selector", o.NodeSelector,
		"the selector to match up with to-be handled nodes")
	fs.StringToStringVar(&o.AgentSelector, "healthz-agent-selector", o.AgentSelector,
		"the selector to match up with to-be handled agents, each agent many have a different selector")

	fs.DurationVar(&o.CheckWindow, "healthz-period-check", o.CheckWindow,
		"the interval to check agent healthz states")
	fs.DurationVar(&o.UnhealthyPeriods, "healthz-unhealthy-period", o.UnhealthyPeriods,
		"the default last intervals to put agent as unhealthy")
	fs.StringToIntVar(&o.AgentUnhealthySecs, "healthz-unhealthy-agent-periods", o.AgentUnhealthySecs,
		"the last intervals to put agent as unhealthy, each agent many have a different period")

	fs.DurationVar(&o.HandlePeriod, "healthz-handle-period", o.HandlePeriod,
		"the interval to trigger performs")
	fs.StringToStringVar(&o.AgentHandlers, "healthz-agent-handles", o.AgentHandlers,
		"the handler-name to handle each agent, each agent many have a corresponding handler")

	fs.Float32Var(&o.TaintQPS, "healthz-taint-qps", o.TaintQPS,
		"the qps to perform tainting")
	fs.Float32Var(&o.EvictQPS, "healthz-evict-qps", o.EvictQPS,
		"the qps to perform evicting")
	fs.Float32Var(&o.DisruptionTaintThreshold, "healthz-taint-threshold", o.DisruptionTaintThreshold,
		"the threshold to judge whether nodes should be disrupted to perform tainting")
	fs.Float32Var(&o.DisruptionEvictThreshold, "healthz-evict-threshold", o.DisruptionEvictThreshold,
		"the threshold to judge whether nodes should be disrupted to perform evicting")
}

// ApplyTo fills up config with options
func (o *LifeCycleOptions) ApplyTo(c *controller.LifeCycleConfig) error {
	c.EnableHealthz = o.EnableHealthz
	c.EnableCNCLifecycle = o.EnableCNCLifecycle

	c.DryRun = o.DryRun

	if selector, err := labels.Parse(o.NodeSelector); err != nil {
		return err
	} else {
		c.NodeSelector = selector
	}

	c.AgentSelector = make(map[string]labels.Selector)
	for agent, s := range o.AgentSelector {
		if selector, err := labels.Parse(s); err != nil {
			return err
		} else {
			c.AgentSelector[agent] = selector
		}
	}

	c.CheckWindow = o.CheckWindow
	c.UnhealthyPeriods = o.UnhealthyPeriods
	c.AgentUnhealthyPeriods = make(map[string]time.Duration)
	for agent, secs := range o.AgentUnhealthySecs {
		c.AgentUnhealthyPeriods[agent] = time.Duration(secs) * time.Second
	}

	c.HandlePeriod = o.HandlePeriod
	c.AgentHandlers = o.AgentHandlers

	c.TaintQPS = o.TaintQPS
	c.EvictQPS = o.EvictQPS
	c.DisruptionTaintThreshold = o.DisruptionTaintThreshold
	c.DisruptionEvictThreshold = o.DisruptionEvictThreshold

	return nil
}

func (o *LifeCycleOptions) Config() (*controller.LifeCycleConfig, error) {
	c := &controller.LifeCycleConfig{}
	if err := o.ApplyTo(c); err != nil {
		return nil, err
	}
	return c, nil
}
