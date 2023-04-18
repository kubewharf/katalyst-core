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

package resource

import (
	"github.com/spf13/pflag"
	"k8s.io/apimachinery/pkg/util/errors"

	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options/sysadvisor/qosaware/resource/cpu"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options/sysadvisor/qosaware/resource/memory"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/sysadvisor/qosaware/resource"
)

// ResourceAdvisorOptions holds the configurations for resource advisors in qos aware plugin
type ResourceAdvisorOptions struct {
	ResourceAdvisors []string

	*cpu.CPUAdvisorOptions
	*memory.MemoryAdvisorOptions
}

// NewResourceAdvisorOptions creates a new Options with a default config
func NewResourceAdvisorOptions() *ResourceAdvisorOptions {
	return &ResourceAdvisorOptions{
		ResourceAdvisors:     []string{"cpu", "memory"},
		CPUAdvisorOptions:    cpu.NewCPUAdvisorOptions(),
		MemoryAdvisorOptions: memory.NewMemoryAdvisorOptions(),
	}
}

// AddFlags adds flags to the specified FlagSet.
func (o *ResourceAdvisorOptions) AddFlags(fs *pflag.FlagSet) {
	fs.StringSliceVar(&o.ResourceAdvisors, "resource-advisors", o.ResourceAdvisors, "active dimensions for resource advisors")

	o.CPUAdvisorOptions.AddFlags(fs)
}

// ApplyTo fills up config with options
func (o *ResourceAdvisorOptions) ApplyTo(c *resource.ResourceAdvisorConfiguration) error {
	c.ResourceAdvisors = o.ResourceAdvisors

	var errList []error
	errList = append(errList, o.CPUAdvisorOptions.ApplyTo(c.CPUAdvisorConfiguration))
	errList = append(errList, o.MemoryAdvisorOptions.ApplyTo(c.MemoryAdvisorConfiguration))

	return errors.NewAggregate(errList)
}
