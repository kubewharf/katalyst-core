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

package adminqos

import (
	"k8s.io/apimachinery/pkg/util/errors"
	cliflag "k8s.io/component-base/cli/flag"

	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options/dynamic/adminqos/advisor"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options/dynamic/adminqos/eviction"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options/dynamic/adminqos/qrm"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options/dynamic/adminqos/reclaimedresource"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/adminqos"
)

type AdminQoSOptions struct {
	*reclaimedresource.ReclaimedResourceOptions
	*qrm.QRMPluginOptions
	*eviction.EvictionOptions
	*advisor.AdvisorOptions
}

func NewAdminQoSOptions() *AdminQoSOptions {
	return &AdminQoSOptions{
		ReclaimedResourceOptions: reclaimedresource.NewReclaimedResourceOptions(),
		QRMPluginOptions:         qrm.NewQRMPluginOptions(),
		EvictionOptions:          eviction.NewEvictionOptions(),
		AdvisorOptions:           advisor.NewAdvisorOptions(),
	}
}

func (o *AdminQoSOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	o.ReclaimedResourceOptions.AddFlags(fss)
	o.QRMPluginOptions.AddFlags(fss)
	o.EvictionOptions.AddFlags(fss)
	o.AdvisorOptions.AddFlags(fss)
}

func (o *AdminQoSOptions) ApplyTo(c *adminqos.AdminQoSConfiguration) error {
	var errList []error
	errList = append(errList, o.ReclaimedResourceOptions.ApplyTo(c.ReclaimedResourceConfiguration))
	errList = append(errList, o.QRMPluginOptions.ApplyTo(c.QRMPluginConfiguration))
	errList = append(errList, o.EvictionOptions.ApplyTo(c.EvictionConfiguration))
	errList = append(errList, o.AdvisorOptions.ApplyTo(c.AdvisorConfiguration))
	return errors.NewAggregate(errList)
}
