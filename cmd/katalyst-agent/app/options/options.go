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
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options/eviction"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options/global"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options/global/adminqos"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options/qrm"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options/reporter"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options/sysadvisor"
	"github.com/kubewharf/katalyst-core/pkg/config"
)

// Options holds the configurations for collector agent.
type Options struct {
	// those are options used by all the katalyst components
	*options.GenericOptions

	// those are options used by all the katalyst agents
	*global.BaseOptions
	*global.PluginManagerOptions
	*global.MetaServerOptions
	*global.QRMAdvisorOptions
	*adminqos.AdminQoSOptions

	// the below are options used by all each individual katalyst module/plugin
	genericEvictionOptions *eviction.GenericEvictionOptions
	evictionPluginsOptions *eviction.EvictionPluginsOptions

	genericReporterOptions *reporter.GenericReporterOptions
	reporterPluginsOptions *reporter.ReporterPluginsOptions

	genericSysAdvisorOptions *sysadvisor.GenericSysAdvisorOptions
	sysadvisorPluginsOptions *sysadvisor.SysAdvisorPluginsOptions

	genericQRMPluginOptions *qrm.GenericQRMPluginOptions
	qrmPluginsOptions       *qrm.QRMPluginsOptions
}

// NewOptions creates a new Options with a default config.
func NewOptions() *Options {
	return &Options{
		GenericOptions: options.NewGenericOptions(),

		BaseOptions:          global.NewBaseOptions(),
		MetaServerOptions:    global.NewMetaServerOptions(),
		PluginManagerOptions: global.NewPluginManagerOptions(),
		QRMAdvisorOptions:    global.NewQRMAdvisorOptions(),
		AdminQoSOptions:      adminqos.NewAdminQoSOptions(),

		genericEvictionOptions:   eviction.NewGenericEvictionOptions(),
		evictionPluginsOptions:   eviction.NewEvictionPluginsOptions(),
		genericReporterOptions:   reporter.NewGenericReporterOptions(),
		reporterPluginsOptions:   reporter.NewReporterPluginsOptions(),
		genericSysAdvisorOptions: sysadvisor.NewGenericSysAdvisorOptions(),
		sysadvisorPluginsOptions: sysadvisor.NewSysAdvisorPluginsOptions(),
		genericQRMPluginOptions:  qrm.NewGenericQRMPluginOptions(),
		qrmPluginsOptions:        qrm.NewQRMPluginsOptions(),
	}
}

// AddFlags adds flags  to the specified FlagSet.
func (o *Options) AddFlags(fss *cliflag.NamedFlagSets) {
	o.GenericOptions.AddFlags(fss)
	o.MetaServerOptions.AddFlags(fss)
	o.PluginManagerOptions.AddFlags(fss)
	o.BaseOptions.AddFlags(fss)
	o.ReclaimedResourceOptions.AddFlags(fss)
	o.QRMAdvisorOptions.AddFlags(fss)
	o.genericEvictionOptions.AddFlags(fss)
	o.evictionPluginsOptions.AddFlags(fss)
	o.genericReporterOptions.AddFlags(fss)
	o.reporterPluginsOptions.AddFlags(fss)
	o.genericSysAdvisorOptions.AddFlags(fss)
	o.sysadvisorPluginsOptions.AddFlags(fss)
	o.genericQRMPluginOptions.AddFlags(fss)
	o.qrmPluginsOptions.AddFlags(fss)
}

// ApplyTo fills up config with options
func (o *Options) ApplyTo(c *config.Configuration) error {
	var errList []error

	errList = append(errList, o.GenericOptions.ApplyTo(c.GenericConfiguration))
	errList = append(errList, o.BaseOptions.ApplyTo(c.BaseConfiguration))
	errList = append(errList, o.PluginManagerOptions.ApplyTo(c.PluginManagerConfiguration))
	errList = append(errList, o.MetaServerOptions.ApplyTo(c.MetaServerConfiguration))
	errList = append(errList, o.AdminQoSOptions.ApplyTo(c.AdminQoSConfiguration))
	errList = append(errList, o.QRMAdvisorOptions.ApplyTo(c.QRMAdvisorConfiguration))
	errList = append(errList, o.genericEvictionOptions.ApplyTo(c.GenericEvictionConfiguration))
	errList = append(errList, o.evictionPluginsOptions.ApplyTo(c.EvictionPluginsConfiguration))
	errList = append(errList, o.genericReporterOptions.ApplyTo(c.GenericReporterConfiguration))
	errList = append(errList, o.reporterPluginsOptions.ApplyTo(c.ReporterPluginsConfiguration))
	errList = append(errList, o.genericSysAdvisorOptions.ApplyTo(c.GenericSysAdvisorConfiguration))
	errList = append(errList, o.sysadvisorPluginsOptions.ApplyTo(c.SysAdvisorPluginsConfiguration))
	errList = append(errList, o.genericQRMPluginOptions.ApplyTo(c.GenericQRMPluginConfiguration))
	errList = append(errList, o.qrmPluginsOptions.ApplyTo(c.QRMPluginsConfiguration))

	return errors.NewAggregate(errList)
}

// Config returns a new configuration instance.
func (o *Options) Config() (*config.Configuration, error) {
	c := config.NewConfiguration()
	if err := o.ApplyTo(c); err != nil {
		return nil, err
	}
	return c, nil
}
