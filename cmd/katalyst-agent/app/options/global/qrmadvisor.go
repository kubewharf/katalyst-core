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

// QRMAdvisorOptions holds the configurations for both qrm plugins and sys advisor qrm servers
type QRMAdvisorOptions struct {
	CPUAdvisorSocketAbsPath string
	CPUPluginSocketAbsPath  string

	MemoryAdvisorSocketAbsPath string
}

// NewQRMAdvisorOptions creates a new options with a default config
func NewQRMAdvisorOptions() *QRMAdvisorOptions {
	return &QRMAdvisorOptions{
		CPUAdvisorSocketAbsPath:    "/var/lib/katalyst/qrm_advisor/cpu_advisor.sock",
		CPUPluginSocketAbsPath:     "/var/lib/katalyst/qrm_advisor/cpu_plugin.sock",
		MemoryAdvisorSocketAbsPath: "/var/lib/katalyst/qrm_advisor/memory_advisor.sock",
	}
}

// AddFlags adds flags to the specified FlagSet.
func (o *QRMAdvisorOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("qrm-advisor")

	fs.StringVar(&o.CPUAdvisorSocketAbsPath, "cpu-advisor-sock-abs-path", o.CPUAdvisorSocketAbsPath, "absolute path of socket file for cpu advisor served in sys-advisor")
	fs.StringVar(&o.CPUPluginSocketAbsPath, "cpu-plugin-sock-abs-path", o.CPUPluginSocketAbsPath, "absolute path of socket file for cpu plugin to communicate with cpu advisor")
	fs.StringVar(&o.MemoryAdvisorSocketAbsPath, "memory-advisor-sock-abs-path", o.MemoryAdvisorSocketAbsPath, "absolute path of socket file for memory advisor served in sys-advisor")
}

// ApplyTo fills up config with options
func (o *QRMAdvisorOptions) ApplyTo(c *global.QRMAdvisorConfiguration) error {
	c.CPUAdvisorSocketAbsPath = o.CPUAdvisorSocketAbsPath
	c.CPUPluginSocketAbsPath = o.CPUPluginSocketAbsPath
	c.MemoryAdvisorSocketAbsPath = o.MemoryAdvisorSocketAbsPath
	return nil
}
