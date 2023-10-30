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
	"fmt"
	"github.com/kubewharf/katalyst-core/pkg/config/metric"
	cliflag "k8s.io/component-base/cli/flag"
)

// MockOptions holds the configurations for katalyst metrics mock data.It's only for pressure test.
type MockOptions struct {
	NamespaceCount int
	WorkloadCount  int
	PodCount       int
}

// NewMockOptions creates a new MockOptions with a default config.
func NewMockOptions() *MockOptions {
	return &MockOptions{
		NamespaceCount: 1,
		WorkloadCount:  1000,
		PodCount:       100,
	}
}

// AddFlags adds flags  to the specified FlagSet.
func (o *MockOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("metric-mock")
	fs.IntVar(&o.NamespaceCount, "mock-namespace-count", o.NamespaceCount, fmt.Sprintf(
		"the number of mock namespaces"))

	fs.IntVar(&o.WorkloadCount, "mock-workload-count-per-ns", o.WorkloadCount, fmt.Sprintf(
		"the number of mock workloads per namespace"))
	fs.IntVar(&o.PodCount, "mock-pod-count-per-workload", o.PodCount, fmt.Sprintf(
		"the number of mock pods per workload"))
}

// ApplyTo fills up config with options
func (o *MockOptions) ApplyTo(c *metric.MockConfiguration) error {
	c.NamespaceCount = o.NamespaceCount
	c.WorkloadCount = o.WorkloadCount
	c.PodCount = o.PodCount
	return nil
}
