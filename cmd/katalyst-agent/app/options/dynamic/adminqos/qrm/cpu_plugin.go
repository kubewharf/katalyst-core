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

package qrm

import (
	cliflag "k8s.io/component-base/cli/flag"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/adminqos/qrm"
)

type CPUPluginOptions struct {
	PreferUseExistNUMAHintResult bool
}

func NewCPUPluginOptions() *CPUPluginOptions {
	return &CPUPluginOptions{}
}

func (o *CPUPluginOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("qrm-cpu-plugin")
	fs.BoolVar(&o.PreferUseExistNUMAHintResult, "prefer-use-exist-numa-hint-result", o.PreferUseExistNUMAHintResult,
		"prefer to use existing numa hint results")
}

func (o *CPUPluginOptions) ApplyTo(c *qrm.CPUPluginConfiguration) error {
	c.PreferUseExistNUMAHintResult = o.PreferUseExistNUMAHintResult

	return nil
}
