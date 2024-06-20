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
	cliflag "k8s.io/component-base/cli/flag"
	"time"

	"github.com/kubewharf/katalyst-core/pkg/config/controller"
)

type NPDOptions struct {
	NPDIndicatorPlugins   []string
	EnableScopeDuplicated bool
	SyncWorkers           int
	*LoadAwarePluginOptions
}

func NewNPDOptions() *NPDOptions {
	return &NPDOptions{
		NPDIndicatorPlugins:    []string{},
		EnableScopeDuplicated:  false,
		SyncWorkers:            1,
		LoadAwarePluginOptions: NewLoadAwarePluginOptions(),
	}
}

func (o *NPDOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("npd")

	fs.StringSliceVar(&o.NPDIndicatorPlugins, "npd-indicator-plugins", o.NPDIndicatorPlugins,
		"A list  of indicator plugins to be used")
	fs.BoolVar(&o.EnableScopeDuplicated, "npd-enable-scope-duplicated", o.EnableScopeDuplicated,
		"Whether metrics with the same scope can be updated by multiple plugins")
	fs.IntVar(&o.SyncWorkers, "npd-sync-workers", o.SyncWorkers,
		"Number of workers to sync npd status")

	fs.IntVar(&o.Workers, "loadaware-sync-workers", o.Workers,
		"num of workers to sync node metrics")
	fs.DurationVar(&o.SyncMetricInterval, "loadaware-sync-interval", o.SyncMetricInterval,
		"interval of syncing node metrics")
	fs.DurationVar(&o.ListMetricTimeout, "loadaware-list-metric-timeout", o.ListMetricTimeout,
		"timeout duration when list metrics from metrics server")

	fs.StringVar(&o.PodUsageSelectorNamespace, "loadaware-podusage-selector-namespace", o.PodUsageSelectorNamespace,
		"pod namespace used to detect whether podusage should be calculated")
	fs.StringVar(&o.PodUsageSelectorKey, "loadaware-podusage-selector-key", o.PodUsageSelectorKey,
		"pod label key used to detect whether podusage should be calculated")
	fs.StringVar(&o.PodUsageSelectorVal, "loadaware-podusage-selector-val", o.PodUsageSelectorVal,
		"pod label value used to detect whether podusage should be calculated")
	fs.IntVar(&o.MaxPodUsageCount, "loadaware-max-podusage-count", o.MaxPodUsageCount,
		"max podusage count on nodemonitor")
}

func (o *NPDOptions) ApplyTo(c *controller.NPDConfig) error {
	c.NPDIndicatorPlugins = o.NPDIndicatorPlugins
	c.EnableScopeDuplicated = o.EnableScopeDuplicated
	c.SyncWorkers = o.SyncWorkers

	c.Workers = o.Workers
	c.SyncMetricInterval = o.SyncMetricInterval
	c.ListMetricTimeout = o.ListMetricTimeout
	c.PodUsageSelectorNamespace = o.PodUsageSelectorNamespace
	c.PodUsageSelectorKey = o.PodUsageSelectorKey
	c.PodUsageSelectorVal = o.PodUsageSelectorVal
	c.MaxPodUsageCount = o.MaxPodUsageCount
	return nil
}

func (o *NPDOptions) Config() (*controller.NPDConfig, error) {
	c := &controller.NPDConfig{}
	if err := o.ApplyTo(c); err != nil {
		return nil, err
	}

	return c, nil
}

type LoadAwarePluginOptions struct {
	// number of workers to sync node metrics
	Workers int
	// time interval of sync node metrics
	SyncMetricInterval time.Duration
	// timeout of list metrics from apiserver
	ListMetricTimeout time.Duration

	// pod selector for checking if pod usage is required
	PodUsageSelectorNamespace string
	PodUsageSelectorKey       string
	PodUsageSelectorVal       string

	MaxPodUsageCount int
}

func NewLoadAwarePluginOptions() *LoadAwarePluginOptions {
	return &LoadAwarePluginOptions{
		Workers:                   3,
		SyncMetricInterval:        time.Minute * 1,
		ListMetricTimeout:         time.Second * 10,
		PodUsageSelectorNamespace: "",
		PodUsageSelectorKey:       "",
		PodUsageSelectorVal:       "",
		MaxPodUsageCount:          20,
	}
}
