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

package global

import (
	cliflag "k8s.io/component-base/cli/flag"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/global"
)

const (
	defaultPluginsRegistrationDir = "/var/lib/katalyst/plugin-socks"
)

type PluginManagerOptions struct {
	PluginRegistrationDir string
}

func NewPluginManagerOptions() *PluginManagerOptions {
	return &PluginManagerOptions{
		PluginRegistrationDir: defaultPluginsRegistrationDir,
	}
}

// AddFlags adds flags to the specified FlagSet.
func (o *PluginManagerOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("plugin-manager")

	fs.StringVar(&o.PluginRegistrationDir, "plugin-registration-dir", o.PluginRegistrationDir, "The path where the plugin manager finds the listening plugins for types of managers")
}

// ApplyTo fills up config with options
func (o *PluginManagerOptions) ApplyTo(c *global.PluginManagerConfiguration) error {
	c.PluginRegistrationDir = o.PluginRegistrationDir
	return nil
}
