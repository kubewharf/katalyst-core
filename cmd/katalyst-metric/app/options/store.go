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

	"k8s.io/apimachinery/pkg/labels"
	cliflag "k8s.io/component-base/cli/flag"

	"github.com/kubewharf/katalyst-core/pkg/config/metric"
	"github.com/kubewharf/katalyst-core/pkg/custom-metric/store/local"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

// StoreOptions holds the configurations for katalyst metrics stores.
type StoreOptions struct {
	StoreName      string
	GCInterval     time.Duration
	PurgeInterval  time.Duration
	IndexLabelKeys []string

	StoreServerShardCount   int
	StoreServerReplicaTotal int

	ServiceDiscoveryName string
	SDPodSelector        string
	SDServiceNamespace   string
	SDServiceName        string
}

// NewStoreOptions creates a new StoreOptions with a default config.
func NewStoreOptions() *StoreOptions {
	return &StoreOptions{
		StoreName:      local.MetricStoreNameLocalMemory,
		GCInterval:     10 * time.Second,
		PurgeInterval:  600 * time.Second,
		IndexLabelKeys: []string{"app"},

		StoreServerShardCount:   1,
		StoreServerReplicaTotal: 3,

		SDPodSelector: "katalyst-custom-metric=store-server",
	}
}

// AddFlags adds flags  to the specified FlagSet.
func (o *StoreOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("metric-store")

	fs.StringVar(&o.StoreName, "store-name", o.StoreName,
		"which store implementation will be started")
	fs.DurationVar(&o.GCInterval, "store-gc-interval", o.GCInterval,
		"the interval between the store collect out of date metric")
	fs.DurationVar(&o.PurgeInterval, "store-purge-interval", o.PurgeInterval,
		"the interval between the store purge the useless meta info")
	fs.StringSliceVar(&o.IndexLabelKeys, "store-object-index-label-key", o.IndexLabelKeys,
		"the label names which will be used as the index key")

	fs.IntVar(&o.StoreServerShardCount, "store-server-shard", o.StoreServerShardCount,
		"the amount of sharding this store implementation splits, only valid in store-server mode")
	fs.IntVar(&o.StoreServerReplicaTotal, "store-server-replica-total", o.StoreServerReplicaTotal,
		"the amount of duplicated replicas this store will use, only valid in store-server mode")

	fs.StringVar(&o.ServiceDiscoveryName, "store-server-sd-name", o.ServiceDiscoveryName,
		"defines which service-discovery manager will be used")
	fs.StringVar(&o.SDServiceNamespace, "store-server-service-ns", o.SDServiceNamespace,
		"for service sd-manager, this defines the namespace for service object")
	fs.StringVar(&o.SDServiceName, "store-server-service-name", o.SDServiceName,
		"for service sd-manager, this defines the name for service object")
	fs.StringVar(&o.SDPodSelector, "store-server-pod-selector", o.SDPodSelector,
		"for pod sd-manager, this defines the label-selector to match up with pods")
}

// ApplyTo fills up config with options
func (o *StoreOptions) ApplyTo(c *metric.StoreConfiguration) error {
	c.StoreName = o.StoreName
	c.GCPeriod = o.GCInterval
	c.PurgePeriod = o.PurgeInterval
	c.IndexLabelKeys = o.IndexLabelKeys

	c.StoreServerShardCount = o.StoreServerShardCount
	c.StoreServerReplicaTotal = o.StoreServerReplicaTotal

	c.ServiceDiscoveryConf.Name = o.ServiceDiscoveryName

	c.ServiceDiscoveryConf.PodSinglePortSDConf.PortName = native.ContainerMetricStorePortName
	selector, err := labels.Parse(o.SDPodSelector)
	if err != nil {
		return err
	}
	c.ServiceDiscoveryConf.PodSinglePortSDConf.PodLister = selector

	c.ServiceDiscoveryConf.ServiceSinglePortSDConf.PortName = native.ContainerMetricStorePortName
	c.ServiceDiscoveryConf.ServiceSinglePortSDConf.Namespace = o.SDServiceNamespace
	c.ServiceDiscoveryConf.ServiceSinglePortSDConf.Name = o.SDServiceName

	return nil
}

func (o *StoreOptions) Config() (*metric.StoreConfiguration, error) {
	c := metric.NewStoreConfiguration()
	if err := o.ApplyTo(c); err != nil {
		return nil, err
	}
	return c, nil
}
