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
	"k8s.io/apimachinery/pkg/util/sets"
	cliflag "k8s.io/component-base/cli/flag"

	"github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/config/controller"
)

// KCCOptions holds the configurations for katalyst config.
type KCCOptions struct {
	ValidAPIGroupSet []string
}

// NewKCCOptions creates a new Options with a default config.
func NewKCCOptions() *KCCOptions {
	return &KCCOptions{
		ValidAPIGroupSet: []string{v1alpha1.SchemeGroupVersion.Group},
	}
}

// AddFlags adds flags  to the specified FlagSet.
func (o *KCCOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("kcc")

	fs.StringSliceVar(&o.ValidAPIGroupSet, "kcc-valid-api-group-set", o.ValidAPIGroupSet, "which Groups is allowed")
}

// ApplyTo fills up config with options
func (o *KCCOptions) ApplyTo(c *controller.KCCConfig) error {
	c.ValidAPIGroupSet = sets.NewString(o.ValidAPIGroupSet...)
	return nil
}

func (o *KCCOptions) Config() (*controller.KCCConfig, error) {
	c := &controller.KCCConfig{}
	if err := o.ApplyTo(c); err != nil {
		return nil, err
	}
	return c, nil
}
