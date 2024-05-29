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

package model

import (
	"github.com/spf13/pflag"
	"k8s.io/apimachinery/pkg/util/errors"

	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options/sysadvisor/qosaware/model/borwein"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/sysadvisor/qosaware/model"
)

// ModelConfiguration holds options of models used in qos aware plugin
type ModelOptions struct {
	*borwein.BorweinOptions
}

// NewModelOptions creates new Options with default config
func NewModelOptions() *ModelOptions {
	return &ModelOptions{
		BorweinOptions: borwein.NewBorweinOptions(),
	}
}

// AddFlags adds flags to the specified FlagSet.
func (o *ModelOptions) AddFlags(fs *pflag.FlagSet) {
	o.BorweinOptions.AddFlags(fs)
}

// ApplyTo fills up config with options
func (o *ModelOptions) ApplyTo(c *model.ModelConfiguration) error {
	var errList []error
	errList = append(errList, o.BorweinOptions.ApplyTo(c.BorweinConfiguration))

	return errors.NewAggregate(errList)
}
