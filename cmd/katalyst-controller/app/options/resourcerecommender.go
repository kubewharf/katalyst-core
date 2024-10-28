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

	cliflag "k8s.io/component-base/cli/flag"

	"github.com/kubewharf/katalyst-core/pkg/config/controller"
	"github.com/kubewharf/katalyst-core/pkg/util/datasource/prometheus"
)

const (
	defaultRecSyncWorkers                = 1
	defaultResourceRecommendReSyncPeriod = 24 * time.Hour
)

type ResourceRecommenderOptions struct {
	OOMRecordMaxNumber int `desc:"max number for oom record"`

	HealthProbeBindPort string `desc:"The port the health probe binds to."`
	MetricsBindPort     string `desc:"The port the metric endpoint binds to."`

	// available datasource: prom
	DataSource []string
	// DataSourcePromConfig is the prometheus datasource config
	DataSourcePromConfig prometheus.PromConfig

	// LogVerbosityLevel to specify log verbosity level. (The default level is 4)
	// Set it to something larger than 4 if more detailed logs are needed.
	LogVerbosityLevel string

	RecSyncWorkers int
	RecSyncPeriod  time.Duration
}

// NewResourceRecommenderOptions creates a new Options with a default config.
func NewResourceRecommenderOptions() *ResourceRecommenderOptions {
	return &ResourceRecommenderOptions{
		OOMRecordMaxNumber:  5000,
		HealthProbeBindPort: "8080",
		MetricsBindPort:     "8081",
		DataSource:          []string{"prom"},
		DataSourcePromConfig: prometheus.PromConfig{
			KeepAlive:                   60 * time.Second,
			Timeout:                     3 * time.Minute,
			BRateLimit:                  false,
			MaxPointsLimitPerTimeSeries: 11000,
		},
		LogVerbosityLevel: "4",
	}
}

// AddFlags adds flags to the specified FlagSet
func (o *ResourceRecommenderOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("resource-recommend")
	fs.IntVar(&o.OOMRecordMaxNumber, "oom-record-max-number", 5000, "Max number for oom records to store in configmap")

	fs.StringVar(&o.HealthProbeBindPort, "resourcerecommend-health-probe-bind-port", "8080", "The port the health probe binds to.")
	fs.StringVar(&o.MetricsBindPort, "resourcerecommend-metrics-bind-port", "8081", "The port the metric endpoint binds to.")

	fs.StringSliceVar(&o.DataSource, "resourcerecommend-datasource", []string{"prom"}, "available datasource: prom")
	fs.StringVar(&o.DataSourcePromConfig.Address, "resourcerecommend-prometheus-address", "", "prometheus address")
	fs.StringVar(&o.DataSourcePromConfig.Auth.Type, "resourcerecommend-prometheus-auth-type", "", "prometheus auth type")
	fs.StringVar(&o.DataSourcePromConfig.Auth.Username, "resourcerecommend-prometheus-auth-username", "", "prometheus auth username")
	fs.StringVar(&o.DataSourcePromConfig.Auth.Password, "resourcerecommend-prometheus-auth-password", "", "prometheus auth password")
	fs.StringVar(&o.DataSourcePromConfig.Auth.BearerToken, "resourcerecommend-prometheus-auth-bearertoken", "", "prometheus auth bearertoken")
	fs.DurationVar(&o.DataSourcePromConfig.KeepAlive, "resourcerecommend-prometheus-keepalive", 60*time.Second, "prometheus keep alive")
	fs.DurationVar(&o.DataSourcePromConfig.Timeout, "resourcerecommend-prometheus-timeout", 3*time.Minute, "prometheus timeout")
	fs.BoolVar(&o.DataSourcePromConfig.BRateLimit, "resourcerecommend-prometheus-bratelimit", false, "prometheus bratelimit")
	fs.IntVar(&o.DataSourcePromConfig.MaxPointsLimitPerTimeSeries, "resourcerecommend-prometheus-maxpoints", 11000, "prometheus max points limit per time series")
	fs.StringVar(&o.DataSourcePromConfig.BaseFilter, "resourcerecommend-prometheus-promql-base-filter", "", ""+
		"Get basic filters in promql for historical usage data. This filter is added to all promql statements. "+
		"Supports filters format of promql, e.g: group=\\\"Katalyst\\\",cluster=\\\"cfeaf782fasdfe\\\"")
	fs.IntVar(&o.RecSyncWorkers, "res-sync-workers", defaultRecSyncWorkers, "num of goroutine to sync recs")
	fs.DurationVar(&o.RecSyncPeriod, "resource-recommend-resync-period", defaultResourceRecommendReSyncPeriod, "period for recommend controller to sync resource recommend")
}

func (o *ResourceRecommenderOptions) ApplyTo(c *controller.ResourceRecommenderConfig) error {
	c.OOMRecordMaxNumber = o.OOMRecordMaxNumber
	c.HealthProbeBindPort = o.HealthProbeBindPort
	c.MetricsBindPort = o.MetricsBindPort
	c.DataSource = o.DataSource
	c.DataSourcePromConfig = o.DataSourcePromConfig
	c.LogVerbosityLevel = o.LogVerbosityLevel
	c.RecSyncWorkers = o.RecSyncWorkers
	c.RecSyncPeriod = o.RecSyncPeriod
	return nil
}

func (o *ResourceRecommenderOptions) Config() (*controller.ResourceRecommenderConfig, error) {
	c := &controller.ResourceRecommenderConfig{}
	if err := o.ApplyTo(c); err != nil {
		return nil, err
	}
	return c, nil
}
