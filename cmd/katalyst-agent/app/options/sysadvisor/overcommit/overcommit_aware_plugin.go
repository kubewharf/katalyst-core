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

package overcommit

import (
	"k8s.io/apimachinery/pkg/util/errors"
	cliflag "k8s.io/component-base/cli/flag"

	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options/sysadvisor/overcommit/realtime"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/sysadvisor/overcommit"
)

type OvercommitAwarePluginOptions struct {
	*realtime.RealtimeOvercommitOptions
}

// NewOvercommitAwarePluginOptions creates a new Options with a default config.
func NewOvercommitAwarePluginOptions() *OvercommitAwarePluginOptions {
	return &OvercommitAwarePluginOptions{
		RealtimeOvercommitOptions: realtime.NewRealtimeOvercommitOptions(),
	}
}

func (o *OvercommitAwarePluginOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("overcommit_aware_plugin")

	o.RealtimeOvercommitOptions.AddFlags(fs)
}

func (o *OvercommitAwarePluginOptions) ApplyTo(c *overcommit.OvercommitAwarePluginConfiguration) error {
	var errList []error

	errList = append(errList, o.RealtimeOvercommitOptions.ApplyTo(c.RealtimeOvercommitConfiguration))

	return errors.NewAggregate(errList)
}
