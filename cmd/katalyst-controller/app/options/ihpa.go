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
	"time"

	cliflag "k8s.io/component-base/cli/flag"

	"github.com/kubewharf/katalyst-core/pkg/config/controller"
)

const (
	defaultIhpaSyncWorkers  = 1
	defaultIhpaResyncPeriod = 30 * time.Second
)

type IHPAOptions struct {
	SyncWorkers  int
	ResyncPeriod time.Duration
}

func NewIHPAOptions() *IHPAOptions {
	return &IHPAOptions{}
}

func (o *IHPAOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("ihpa")

	fs.IntVar(&o.SyncWorkers, "ihpa-sync-workers", defaultIhpaSyncWorkers, fmt.Sprintf(""+
		"num of goroutines to sync ihpas"))
	fs.DurationVar(&o.ResyncPeriod, "ihpa-resync-period", defaultIhpaResyncPeriod, fmt.Sprintf(""+
		"Period for ihpa controller to resync"))
}

func (o *IHPAOptions) ApplyTo(c *controller.IHPAConfig) error {
	c.SyncWorkers = o.SyncWorkers
	c.ResyncPeriod = o.ResyncPeriod
	return nil
}
