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
	"github.com/kubewharf/katalyst-core/pkg/util/datasource/prometheus"
)

// SPDOptions holds the configurations for service profile data.
type SPDOptions struct {
	ResyncPeriod           time.Duration
	SPDWorkloadGVResources []string
	SPDPodLabelIndexerKeys []string
	EnableCNCCache         bool
	IndicatorPlugins       []string
	BaselinePercent        map[string]int64

	*ResourcePortraitIndicatorPluginOptions
}

// NewSPDOptions creates a new Options with a default config.
func NewSPDOptions() *SPDOptions {
	return &SPDOptions{
		ResyncPeriod:    time.Second * 30,
		EnableCNCCache:  true,
		BaselinePercent: map[string]int64{},

		ResourcePortraitIndicatorPluginOptions: NewResourcePortraitIndicatorPluginOptions(),
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
	fs.BoolVar(&o.EnableCNCCache, "spd-enable-cnc-cache", o.EnableCNCCache, ""+
		"Whether enable cnc cache to reduce agent api-server remote request")
	fs.StringSliceVar(&o.IndicatorPlugins, "spd-indicator-plugins", o.IndicatorPlugins,
		"A list of indicator plugins to be used")
	fs.StringToInt64Var(&o.BaselinePercent, "spd-qos-baseline-percent", o.BaselinePercent, ""+
		"A map of qosLeve to default baseline percent[0,100]")

	o.ResourcePortraitIndicatorPluginOptions.AddFlags(fss)
}

// ApplyTo fills up config with options
func (o *SPDOptions) ApplyTo(c *controller.SPDConfig) error {
	c.ReSyncPeriod = o.ResyncPeriod
	c.SPDWorkloadGVResources = o.SPDWorkloadGVResources
	c.SPDPodLabelIndexerKeys = o.SPDPodLabelIndexerKeys
	c.EnableCNCCache = o.EnableCNCCache
	c.IndicatorPlugins = o.IndicatorPlugins
	c.BaselinePercent = o.BaselinePercent

	if err := o.ResourcePortraitIndicatorPluginOptions.ApplyTo(c.ResourcePortraitIndicatorPluginConfig); err != nil {
		return err
	}

	return nil
}

func (o *SPDOptions) Config() (*controller.SPDConfig, error) {
	c := &controller.SPDConfig{}
	if err := o.ApplyTo(c); err != nil {
		return nil, err
	}
	return c, nil
}

// ResourcePortraitIndicatorPluginOptions holds the configurations for resource portrait indicator plugin data.
type ResourcePortraitIndicatorPluginOptions struct {
	// available datasource: prom
	DataSource string
	// DataSourcePromConfig is the prometheus datasource config
	DataSourcePromConfig prometheus.PromConfig
	// AlgorithmServingAddress is the algorithm serving address
	AlgorithmServingAddress string
	// AlgorithmConfigMapName is the configmap name used by resource portrait plugin
	AlgorithmConfigMapName string
	// AlgorithmConfigMapNamespace is the configmap namespace used by resource portrait plugin
	AlgorithmConfigMapNamespace string
	// EnableAutomaticResyncGlobalConfiguration is used to enable automatic synchronization of global configuration.
	// If this switch is enabled, the plug-in will refresh itself configuration in SPD with the configuration in the
	// global ConfigMap. If this switch is disable, users can customize the configuration in SPD.
	EnableAutomaticResyncGlobalConfiguration bool
}

func NewResourcePortraitIndicatorPluginOptions() *ResourcePortraitIndicatorPluginOptions {
	return &ResourcePortraitIndicatorPluginOptions{
		AlgorithmServingAddress:                  "http://localhost:8080",
		AlgorithmConfigMapName:                   "resource-portrait-auto-created-config",
		AlgorithmConfigMapNamespace:              "kube-system",
		EnableAutomaticResyncGlobalConfiguration: false,

		DataSource: "prom",
		DataSourcePromConfig: prometheus.PromConfig{
			KeepAlive:                   60 * time.Second,
			Timeout:                     3 * time.Minute,
			BRateLimit:                  false,
			MaxPointsLimitPerTimeSeries: 11000,
		},
	}
}

// AddFlags adds flags  to the specified FlagSet.
func (o *ResourcePortraitIndicatorPluginOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("spd-resource-portrait")

	fs.StringVar(&o.AlgorithmServingAddress, "spd-resource-portrait-indicator-plugin-algorithm-serving-address", o.AlgorithmServingAddress, "algorithm serving address")
	fs.StringVar(&o.AlgorithmConfigMapName, "spd-resource-portrait-indicator-plugin-algorithm-configmap-name", o.AlgorithmConfigMapName, "algorithm configmap name")
	fs.StringVar(&o.AlgorithmConfigMapNamespace, "spd-resource-portrait-indicator-plugin-algorithm-configmap-namespace", o.AlgorithmConfigMapNamespace, "algorithm configmap namespace")
	fs.BoolVar(&o.EnableAutomaticResyncGlobalConfiguration, "spd-resource-portrait-indicator-plugin-enable-automatic-resync-global-configuration", o.EnableAutomaticResyncGlobalConfiguration, "enable automatic resync global configuration")
	fs.StringVar(&o.DataSource, "spd-resource-portrait-indicator-plugin-datasource", "prom", "available datasource: prom")
	fs.StringVar(&o.DataSourcePromConfig.Address, "spd-resource-portrait-indicator-plugin-prometheus-address", "", "prometheus address")
	fs.StringVar(&o.DataSourcePromConfig.Auth.Type, "spd-resource-portrait-indicator-plugin-prometheus-auth-type", "", "prometheus auth type")
	fs.StringVar(&o.DataSourcePromConfig.Auth.Username, "spd-resource-portrait-indicator-plugin-prometheus-auth-username", "", "prometheus auth username")
	fs.StringVar(&o.DataSourcePromConfig.Auth.Password, "spd-resource-portrait-indicator-plugin-prometheus-auth-password", "", "prometheus auth password")
	fs.StringVar(&o.DataSourcePromConfig.Auth.BearerToken, "spd-resource-portrait-indicator-plugin-prometheus-auth-bearertoken", "", "prometheus auth bearertoken")
	fs.DurationVar(&o.DataSourcePromConfig.KeepAlive, "spd-resource-portrait-indicator-plugin-prometheus-keepalive", 60*time.Second, "prometheus keep alive")
	fs.DurationVar(&o.DataSourcePromConfig.Timeout, "spd-resource-portrait-indicator-plugin-prometheus-timeout", 3*time.Minute, "prometheus timeout")
	fs.BoolVar(&o.DataSourcePromConfig.BRateLimit, "spd-resource-portrait-indicator-plugin-prometheus-bratelimit", false, "prometheus bratelimit")
	fs.IntVar(&o.DataSourcePromConfig.MaxPointsLimitPerTimeSeries, "spd-resource-portrait-indicator-plugin-prometheus-maxpoints", 11000, "prometheus max points limit per time series")
	fs.StringVar(&o.DataSourcePromConfig.BaseFilter, "spd-resource-portrait-indicator-plugin-prometheus-promql-base-filter", "", ""+
		"Get basic filters in promql for historical usage data. This filter is added to all promql statements. "+
		"Supports filters format of promql, e.g: group=\\\"Katalyst\\\",cluster=\\\"cfeaf782fasdfe\\\"")
}

// ApplyTo fills up config with options
func (o *ResourcePortraitIndicatorPluginOptions) ApplyTo(c *controller.ResourcePortraitIndicatorPluginConfig) error {
	c.AlgorithmServingAddress = o.AlgorithmServingAddress
	c.AlgorithmConfigMapName = o.AlgorithmConfigMapName
	c.AlgorithmConfigMapNamespace = o.AlgorithmConfigMapNamespace
	c.EnableAutomaticResyncGlobalConfiguration = o.EnableAutomaticResyncGlobalConfiguration

	c.DataSource = o.DataSource
	c.DataSourcePromConfig = o.DataSourcePromConfig
	return nil
}
