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

	"github.com/kubewharf/katalyst-core/pkg/config/controller"
)

type NPDOptions struct {
	NPDIndicatorPlugins   []string
	EnableScopeDuplicated bool
	SyncWorkers           int
}

func NewNPDOptions() *NPDOptions {
	return &NPDOptions{
		NPDIndicatorPlugins:   []string{},
		EnableScopeDuplicated: false,
		SyncWorkers:           1,
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
}

func (o *NPDOptions) ApplyTo(c *controller.NPDConfig) error {
	c.NPDIndicatorPlugins = o.NPDIndicatorPlugins
	c.EnableScopeDuplicated = o.EnableScopeDuplicated
	c.SyncWorkers = o.SyncWorkers
	return nil
}

func (o *NPDOptions) Config() (*controller.NPDConfig, error) {
	c := &controller.NPDConfig{}
	if err := o.ApplyTo(c); err != nil {
		return nil, err
	}

	return c, nil
}
