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

	"k8s.io/apimachinery/pkg/labels"
	cliflag "k8s.io/component-base/cli/flag"

	"github.com/kubewharf/katalyst-core/pkg/config/metric"
	"github.com/kubewharf/katalyst-core/pkg/custom-metric/store/local"
)

// StoreOptions holds the configurations for katalyst metrics stores.
type StoreOptions struct {
	StoreName string

	StoreServerShardCount   int
	StoreServerReplicaTotal int
	StoreServerSelector     string
}

// NewStoreOptions creates a new StoreOptions with a default config.
func NewStoreOptions() *StoreOptions {
	return &StoreOptions{
		StoreName:               local.MetricStoreNameLocalMemory,
		StoreServerShardCount:   1,
		StoreServerReplicaTotal: 3,
		StoreServerSelector:     "katalyst-custom-metric=store-server",
	}
}

// AddFlags adds flags  to the specified FlagSet.
func (o *StoreOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("metric-store")

	fs.StringVar(&o.StoreName, "store-name", o.StoreName, fmt.Sprintf(
		"which store implementation will be started"))

	fs.IntVar(&o.StoreServerShardCount, "store-server-shard", o.StoreServerShardCount, fmt.Sprintf(
		"the amount of shardings this store implementation splits, only valid in store-server mode"))
	fs.IntVar(&o.StoreServerReplicaTotal, "store-server-replica-total", o.StoreServerReplicaTotal, fmt.Sprintf(
		"the amount of duplicated replicas this store will use, only valid in store-server mode"))
	fs.StringVar(&o.StoreServerSelector, "store-server-selector", o.StoreServerSelector, fmt.Sprintf(
		"the selector to match up with store server pods, only valid in store-server mode"))
}

// ApplyTo fills up config with options
func (o *StoreOptions) ApplyTo(c *metric.StoreConfiguration) error {
	c.StoreName = o.StoreName

	c.StoreServerShardCount = o.StoreServerShardCount
	c.StoreServerReplicaTotal = o.StoreServerReplicaTotal
	selector, err := labels.Parse(o.StoreServerSelector)
	if err != nil {
		return err
	}
	c.StoreServerSelector = selector

	return nil
}

func (o *StoreOptions) Config() (*metric.StoreConfiguration, error) {
	c := metric.NewStoreConfiguration()
	if err := o.ApplyTo(c); err != nil {
		return nil, err
	}
	return c, nil
}
