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

package metaserver

import (
	"time"

	cliflag "k8s.io/component-base/cli/flag"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/metaserver"
)

const defaultMetricInsurancePeriod = 0 * time.Second

const defaultRodanServerPort = 9102

type MetricFetcherOptions struct {
	MetricInsurancePeriod time.Duration
	MetricProvisions      []string

	DefaultInterval         time.Duration
	ProvisionerIntervalSecs map[string]int

	*MalachiteOptions
	*CgroupOptions
	*KubeletOptions
	*RodanOptions
}

type (
	MalachiteOptions struct{}
	CgroupOptions    struct{}
	KubeletOptions   struct{}
	RodanOptions     struct {
		ServerPort int
	}
)

func NewMetricFetcherOptions() *MetricFetcherOptions {
	return &MetricFetcherOptions{
		MetricInsurancePeriod: defaultMetricInsurancePeriod,
		MetricProvisions:      []string{metaserver.MetricProvisionerMalachite, metaserver.MetricProvisionerKubelet},

		DefaultInterval:         time.Second * 5,
		ProvisionerIntervalSecs: map[string]int{metaserver.MetricProvisionerMalachiteRealtime: 1},

		MalachiteOptions: &MalachiteOptions{},
		CgroupOptions:    &CgroupOptions{},
		KubeletOptions:   &KubeletOptions{},
		RodanOptions: &RodanOptions{
			ServerPort: defaultRodanServerPort,
		},
	}
}

// AddFlags adds flags to the specified FlagSet.
func (o *MetricFetcherOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("metric-server")

	fs.DurationVar(&o.MetricInsurancePeriod, "metric-insurance-period", o.MetricInsurancePeriod,
		"The meta server return metric data and MetricDataExpired if the update time of metric data is earlier than this period.")
	fs.StringSliceVar(&o.MetricProvisions, "metric-provisioners", o.MetricProvisions,
		"The provisioners that should be enabled by default")

	fs.DurationVar(&o.DefaultInterval, "metric-interval", o.DefaultInterval,
		"The default metric provisioner collecting interval")
	fs.StringToIntVar(&o.ProvisionerIntervalSecs, "metric-provisioner-intervals", o.ProvisionerIntervalSecs,
		"The metric provisioner collecting intervals for each individual provisioner")

	fs.IntVar(&o.RodanOptions.ServerPort, "rodan-server-port", o.RodanOptions.ServerPort,
		"The rodan metric provisioner server port")
}

// ApplyTo fills up config with options
func (o *MetricFetcherOptions) ApplyTo(c *metaserver.MetricConfiguration) error {
	c.MetricInsurancePeriod = o.MetricInsurancePeriod
	c.MetricProvisions = o.MetricProvisions

	c.DefaultInterval = o.DefaultInterval
	c.ProvisionerIntervals = make(map[string]time.Duration)
	for name, secs := range o.ProvisionerIntervalSecs {
		c.ProvisionerIntervals[name] = time.Second * time.Duration(secs)
	}

	c.RodanServerPort = o.RodanOptions.ServerPort

	return nil
}
