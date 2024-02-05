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

package qosaware

import (
	"time"

	"k8s.io/apimachinery/pkg/util/errors"
	cliflag "k8s.io/component-base/cli/flag"

	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options/sysadvisor/qosaware/model"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options/sysadvisor/qosaware/reporter"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options/sysadvisor/qosaware/resource"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options/sysadvisor/qosaware/server"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/sysadvisor/qosaware"
)

const (
	defaultQoSAwareSyncPeriod = 5 * time.Second
)

// QoSAwarePluginOptions holds the configurations for qos aware plugin.
type QoSAwarePluginOptions struct {
	SyncPeriod time.Duration

	*resource.ResourceAdvisorOptions
	*server.QRMServerOptions
	*reporter.ReporterOptions
	*model.ModelOptions
}

// NewQoSAwarePluginOptions creates a new Options with a default config.
func NewQoSAwarePluginOptions() *QoSAwarePluginOptions {
	return &QoSAwarePluginOptions{
		SyncPeriod:             defaultQoSAwareSyncPeriod,
		ResourceAdvisorOptions: resource.NewResourceAdvisorOptions(),
		QRMServerOptions:       server.NewQRMServerOptions(),
		ReporterOptions:        reporter.NewReporterOptions(),
		ModelOptions:           model.NewModelOptions(),
	}
}

// AddFlags adds flags to the specified FlagSet.
func (o *QoSAwarePluginOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("qos_aware_plugin")

	fs.DurationVar(&o.SyncPeriod, "qos-aware-sync-period", o.SyncPeriod, "Period for QoS aware plugin to sync")

	o.ResourceAdvisorOptions.AddFlags(fs)
	o.QRMServerOptions.AddFlags(fs)
	o.ReporterOptions.AddFlags(fs)
	o.ModelOptions.AddFlags(fs)
}

// ApplyTo fills up config with options
func (o *QoSAwarePluginOptions) ApplyTo(c *qosaware.QoSAwarePluginConfiguration) error {
	c.SyncPeriod = o.SyncPeriod

	var errList []error
	errList = append(errList, o.ResourceAdvisorOptions.ApplyTo(c.ResourceAdvisorConfiguration))
	errList = append(errList, o.QRMServerOptions.ApplyTo(c.QRMServerConfiguration))
	errList = append(errList, o.ReporterOptions.ApplyTo(c.ReporterConfiguration))
	errList = append(errList, o.ModelOptions.ApplyTo(c.ModelConfiguration))

	return errors.NewAggregate(errList)
}
