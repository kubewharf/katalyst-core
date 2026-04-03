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

package userwatermark

import (
	cliflag "k8s.io/component-base/cli/flag"

	defaultOptions "github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options/dynamic/userwatermark/userwatermarkdefault"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/userwatermark"
)

const (
	DefaultReconcileInterval = 30
)

type UserWatermarkOptions struct {
	EnableReclaimer   bool
	ReconcileInterval int64
	ServiceLabel      string
	*defaultOptions.DefaultOptions
}

func NewUserWatermarkOptions() *UserWatermarkOptions {
	return &UserWatermarkOptions{
		ReconcileInterval: DefaultReconcileInterval,
		DefaultOptions:    defaultOptions.NewDefaultOptions(),
	}
}

func (o *UserWatermarkOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("user-watermark")

	fs.BoolVar(&o.EnableReclaimer, "enable-user-watermark-reclaimer", o.EnableReclaimer,
		"whether to enable memory reclaimer")
	fs.Int64Var(&o.ReconcileInterval, "user-watermark-reconcile-interval", o.ReconcileInterval,
		"the interval to reconcile memory")
	fs.StringVar(&o.ServiceLabel, "user-watermark-pod-service-label", o.ServiceLabel,
		"the service label to filter")

	o.DefaultOptions.AddFlags(fss)
}

func (o *UserWatermarkOptions) ApplyTo(c *userwatermark.UserWatermarkConfiguration) error {
	c.EnableReclaimer = o.EnableReclaimer
	c.ReconcileInterval = o.ReconcileInterval
	c.ServiceLabel = o.ServiceLabel

	return o.DefaultOptions.ApplyTo(c.DefaultConfig)
}
