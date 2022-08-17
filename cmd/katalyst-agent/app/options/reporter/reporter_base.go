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

package reporter

import (
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/util/errors"
	cliflag "k8s.io/component-base/cli/flag"

	reporterconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/reporter"
)

const (
	defaultCollectInterval        = 5 * time.Second
	defaultRefreshLatestCNRPeriod = 5 * time.Minute
)

// GenericReporterOptions holds the configurations for reporter
type GenericReporterOptions struct {
	CollectInterval        time.Duration
	InnerPlugins           []string
	RefreshLatestCNRPeriod time.Duration
}

// NewGenericReporterOptions creates a new Options with a default config.
func NewGenericReporterOptions() *GenericReporterOptions {
	return &GenericReporterOptions{
		InnerPlugins:           []string{"*"},
		CollectInterval:        defaultCollectInterval,
		RefreshLatestCNRPeriod: defaultRefreshLatestCNRPeriod,
	}
}

// AddFlags adds flags to the specified FlagSet.
func (o *GenericReporterOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("reporter")

	fs.DurationVar(&o.CollectInterval, "reporter-collect-interval", o.CollectInterval,
		"Collection interval of agent")
	fs.DurationVar(&o.RefreshLatestCNRPeriod, "reporter-refresh-latest-cnr-period", o.RefreshLatestCNRPeriod,
		"CNR reporter update cache period")

	fs.StringSliceVar(&o.InnerPlugins, "reporter-plugins", o.InnerPlugins, fmt.Sprintf(""+
		"A list of reporter plugins to enable. '*' enables all on-by-default reporter plugins, 'foo' enables the reporter plugin "+
		"named 'foo', '-foo' disables the reporter plugin named 'foo'"))
}

// ApplyTo fills up config with options
func (o *GenericReporterOptions) ApplyTo(c *reporterconfig.GenericReporterConfiguration) error {
	c.CollectInterval = o.CollectInterval
	c.InnerPlugins = o.InnerPlugins
	c.RefreshLatestCNRPeriod = o.RefreshLatestCNRPeriod
	return nil
}

// Config returns a new generic reporter plugin configuration instance.
func (o *GenericReporterOptions) Config() (*reporterconfig.GenericReporterConfiguration, error) {
	c := reporterconfig.NewGenericReporterConfiguration()
	if err := o.ApplyTo(c); err != nil {
		return nil, err
	}
	return c, nil
}

// ReporterPluginsOptions holds the configurations for reporter plugin
type ReporterPluginsOptions struct {
	*KubeletPluginOptions
}

// NewReporterPluginsOptions creates a new reporter plugin Options with a default config.
func NewReporterPluginsOptions() *ReporterPluginsOptions {
	return &ReporterPluginsOptions{
		KubeletPluginOptions: NewKubeletPluginOptions(),
	}
}

// AddFlags adds flags to the specified FlagSet.
func (o *ReporterPluginsOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	o.KubeletPluginOptions.AddFlags(fss)
}

// ApplyTo fills up config with options
func (o *ReporterPluginsOptions) ApplyTo(c *reporterconfig.ReporterPluginsConfiguration) error {
	var errList []error

	errList = append(errList, o.KubeletPluginOptions.ApplyTo(c.KubeletPluginConfiguration))

	return errors.NewAggregate(errList)
}

// Config returns a new reporter plugin configuration instance.
func (o *ReporterPluginsOptions) Config() (*reporterconfig.ReporterPluginsConfiguration, error) {
	c := reporterconfig.NewReporterPluginsConfiguration()
	if err := o.ApplyTo(c); err != nil {
		return nil, err
	}
	return c, nil
}
