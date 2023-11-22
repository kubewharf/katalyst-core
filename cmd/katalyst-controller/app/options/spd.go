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
	"fmt"
	"time"

	cliflag "k8s.io/component-base/cli/flag"

	"github.com/kubewharf/katalyst-core/pkg/config/controller"
)

// SPDOptions holds the configurations for service profile data.
type SPDOptions struct {
	ResyncPeriod           time.Duration
	SPDWorkloadGVResources []string
	SPDPodLabelIndexerKeys []string
	IndicatorPlugins       []string
	BaselinePercent        map[string]int64
}

// NewSPDOptions creates a new Options with a default config.
func NewSPDOptions() *SPDOptions {
	return &SPDOptions{
		ResyncPeriod:    time.Second * 30,
		BaselinePercent: map[string]int64{},
	}
}

// AddFlags adds flags  to the specified FlagSet.
func (o *SPDOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("spd")

	fs.DurationVar(&o.ResyncPeriod, "spd-resync-period", o.ResyncPeriod, fmt.Sprintf(""+
		"Period for spd controller to resync"))
	fs.StringSliceVar(&o.SPDWorkloadGVResources, "spd-workload-resources", o.SPDWorkloadGVResources, ""+
		"A list of resources to be spd controller watched. "+
		"SPDWorkloadGVResources should be in the format of `resource.version.group.com` like 'deployments.v1.apps'.")
	fs.StringSliceVar(&o.SPDPodLabelIndexerKeys, "spd-pod-label-indexers", o.SPDPodLabelIndexerKeys, ""+
		"A list of pod label keys to be used as indexers for pod informer")
	fs.StringSliceVar(&o.IndicatorPlugins, "spd-indicator-plugins", o.IndicatorPlugins,
		"A list of indicator plugins to be used")
	fs.StringToInt64Var(&o.BaselinePercent, "spd-qos-baseline-percent", o.BaselinePercent, ""+
		"A map of qosLeve to default baseline percent[0,100]")
}

// ApplyTo fills up config with options
func (o *SPDOptions) ApplyTo(c *controller.SPDConfig) error {
	c.ReSyncPeriod = o.ResyncPeriod
	c.SPDWorkloadGVResources = o.SPDWorkloadGVResources
	c.SPDPodLabelIndexerKeys = o.SPDPodLabelIndexerKeys
	c.IndicatorPlugins = o.IndicatorPlugins
	c.BaselinePercent = o.BaselinePercent
	return nil
}

func (o *SPDOptions) Config() (*controller.SPDConfig, error) {
	c := &controller.SPDConfig{}
	if err := o.ApplyTo(c); err != nil {
		return nil, err
	}
	return c, nil
}
