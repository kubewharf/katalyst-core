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
)

const (
	defaultNodeOvercommitSyncWorkers     = 1
	defaultNodeOvercommitReconcilePeriod = 30 * time.Minute
)

// OvercommitOptions holds the configurations for overcommit.
type OvercommitOptions struct {
	NodeOvercommitOptions
}

// NodeOvercommitOptions holds the configurations for nodeOvercommitConfig controller.
type NodeOvercommitOptions struct {
	// numer of workers to sync overcommit config
	SyncWorkers int

	// time interval of reconcile overcommit config
	ConfigReconcilePeriod time.Duration
}

// NewOvercommitOptions creates a new Options with a default config.
func NewOvercommitOptions() *OvercommitOptions {
	return &OvercommitOptions{}
}

// AddFlags adds flags to the specified FlagSet
func (o *OvercommitOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("noc")

	fs.IntVar(&o.SyncWorkers, "nodeovercommit-sync-workers", defaultNodeOvercommitSyncWorkers, "num of goroutines to sync nodeovercommitconfig")
	fs.DurationVar(&o.ConfigReconcilePeriod, "nodeovercommit-reconcile-period", defaultNodeOvercommitReconcilePeriod, "Period for nodeovercommit controller to sync configs")
}

func (o *OvercommitOptions) ApplyTo(c *controller.OvercommitConfig) error {
	c.Node.SyncWorkers = o.SyncWorkers
	c.Node.ConfigReconcilePeriod = o.ConfigReconcilePeriod
	return nil
}

func (o *OvercommitOptions) Config() (*controller.OvercommitConfig, error) {
	c := &controller.OvercommitConfig{}
	if err := o.ApplyTo(c); err != nil {
		return nil, err
	}
	return c, nil
}
