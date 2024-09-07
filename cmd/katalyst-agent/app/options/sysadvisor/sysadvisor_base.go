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

package sysadvisor

import (
	"fmt"

	"k8s.io/apimachinery/pkg/util/errors"
	cliflag "k8s.io/component-base/cli/flag"

	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options/sysadvisor/inference"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options/sysadvisor/metacache"
	metricemitter "github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options/sysadvisor/metric-emitter"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options/sysadvisor/overcommit"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options/sysadvisor/poweraware"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options/sysadvisor/qosaware"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/sysadvisor"
)

// GenericSysAdvisorOptions holds the configurations for sysadvisor
type GenericSysAdvisorOptions struct {
	SysAdvisorPlugins           []string
	StateFileDirectory          string
	ClearStateFileDirectory     bool
	EnableShareCoresNumaBinding bool
	SkipStateCorruption         bool
}

// NewGenericSysAdvisorOptions creates a new Options with a default config.
func NewGenericSysAdvisorOptions() *GenericSysAdvisorOptions {
	return &GenericSysAdvisorOptions{
		SysAdvisorPlugins: []string{
			types.AdvisorPluginNameQoSAware,
			types.AdvisorPluginNameMetaCache,
			types.AdvisorPluginNameMetricEmitter,
			types.AdvisorPluginNameInference,
			types.AdvisorPluginNamePowerAware,
		},
		StateFileDirectory:          "/var/lib/katalyst/sys_advisor/",
		ClearStateFileDirectory:     false,
		EnableShareCoresNumaBinding: true,
		SkipStateCorruption:         false,
	}
}

// AddFlags adds flags to the specified FlagSet.
func (o *GenericSysAdvisorOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("sysadvisor")

	fs.StringSliceVar(&o.SysAdvisorPlugins, "sysadvisor-plugins", o.SysAdvisorPlugins, fmt.Sprintf(""+
		"A list of sysadvisor plugins to enable. '*' enables all on-by-default sysadvisor plugins, 'foo' enables the sysadvisor plugin "+
		"named 'foo', '-foo' disables the sysadvisor plugin named 'foo'"))
	fs.StringVar(&o.StateFileDirectory, "state-dir", o.StateFileDirectory, "directory for sys advisor to store state file")
	fs.BoolVar(&o.ClearStateFileDirectory, "clear-state-dir", o.ClearStateFileDirectory, "clear state file when starting up (only for rollback)")
	fs.BoolVar(&o.EnableShareCoresNumaBinding, "enable-share-cores-numa-binding", o.EnableShareCoresNumaBinding, "enable share cores with NUMA binding feature")
	fs.BoolVar(&o.SkipStateCorruption, "skip-state-corruption", o.SkipStateCorruption, "skip meta cache state corruption")
}

// ApplyTo fills up config with options
func (o *GenericSysAdvisorOptions) ApplyTo(c *sysadvisor.GenericSysAdvisorConfiguration) error {
	c.SysAdvisorPlugins = o.SysAdvisorPlugins
	c.StateFileDirectory = o.StateFileDirectory
	c.ClearStateFileDirectory = o.ClearStateFileDirectory
	c.EnableShareCoresNumaBinding = o.EnableShareCoresNumaBinding
	c.SkipStateCorruption = o.SkipStateCorruption
	return nil
}

// Config returns a new generic sysadvisor plugin configuration instance.
func (o *GenericSysAdvisorOptions) Config() (*sysadvisor.GenericSysAdvisorConfiguration, error) {
	c := sysadvisor.NewGenericSysAdvisorConfiguration()
	if err := o.ApplyTo(c); err != nil {
		return nil, err
	}
	return c, nil
}

// SysAdvisorPluginsOptions holds the configurations for sysadvisor plugins
type SysAdvisorPluginsOptions struct {
	*qosaware.QoSAwarePluginOptions
	*metacache.MetaCachePluginOptions
	*metricemitter.MetricEmitterPluginOptions
	*inference.InferencePluginOptions
	*overcommit.OvercommitAwarePluginOptions
	*poweraware.PowerAwarePluginOptions
}

// NewSysAdvisorPluginsOptions creates a new Options with a default config.
func NewSysAdvisorPluginsOptions() *SysAdvisorPluginsOptions {
	return &SysAdvisorPluginsOptions{
		QoSAwarePluginOptions:        qosaware.NewQoSAwarePluginOptions(),
		MetaCachePluginOptions:       metacache.NewMetaCachePluginOptions(),
		MetricEmitterPluginOptions:   metricemitter.NewMetricEmitterPluginOptions(),
		InferencePluginOptions:       inference.NewInferencePluginOptions(),
		OvercommitAwarePluginOptions: overcommit.NewOvercommitAwarePluginOptions(),
		PowerAwarePluginOptions:      poweraware.NewPowerAwarePluginOptions(),
	}
}

// AddFlags adds flags to the specified FlagSet.
func (o *SysAdvisorPluginsOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	o.QoSAwarePluginOptions.AddFlags(fss)
	o.MetaCachePluginOptions.AddFlags(fss)
	o.MetricEmitterPluginOptions.AddFlags(fss)
	o.InferencePluginOptions.AddFlags(fss)
	o.OvercommitAwarePluginOptions.AddFlags(fss)
	o.PowerAwarePluginOptions.AddFlags(fss)
}

// ApplyTo fills up config with options
func (o *SysAdvisorPluginsOptions) ApplyTo(c *sysadvisor.SysAdvisorPluginsConfiguration) error {
	var errList []error
	errList = append(errList, o.QoSAwarePluginOptions.ApplyTo(c.QoSAwarePluginConfiguration))
	errList = append(errList, o.MetaCachePluginOptions.ApplyTo(c.MetaCachePluginConfiguration))
	errList = append(errList, o.MetricEmitterPluginOptions.ApplyTo(c.MetricEmitterPluginConfiguration))
	errList = append(errList, o.InferencePluginOptions.ApplyTo(c.InferencePluginConfiguration))
	errList = append(errList, o.OvercommitAwarePluginOptions.ApplyTo(c.OvercommitAwarePluginConfiguration))
	errList = append(errList, o.PowerAwarePluginOptions.ApplyTo(c.PowerAwarePluginOptions))
	return errors.NewAggregate(errList)
}

// Config returns a new sysadvisor plugins configuration instance.
func (o *SysAdvisorPluginsOptions) Config() (*sysadvisor.SysAdvisorPluginsConfiguration, error) {
	c := sysadvisor.NewSysAdvisorPluginsConfiguration()
	if err := o.ApplyTo(c); err != nil {
		return nil, err
	}
	return c, nil
}
