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

package tmo

import (
	"k8s.io/apimachinery/pkg/util/errors"
	cliflag "k8s.io/component-base/cli/flag"

	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options/dynamic/tmo/tmodefault"
	tmodynamicconf "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/tmo"
)

type TransparentMemoryOffloadingOptions struct {
	*tmodefault.DefaultOptions
}

func NewTransparentMemoryOffloadingOptions() *TransparentMemoryOffloadingOptions {
	return &TransparentMemoryOffloadingOptions{
		DefaultOptions: tmodefault.NewDefaultOptions(),
	}
}

func (o *TransparentMemoryOffloadingOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	o.DefaultOptions.AddFlags(fss)
}

func (o *TransparentMemoryOffloadingOptions) ApplyTo(c *tmodynamicconf.TransparentMemoryOffloadingConfiguration) error {
	var errList []error
	errList = append(errList, o.DefaultOptions.ApplyTo(c.DefaultConfigurations))
	return errors.NewAggregate(errList)
}
