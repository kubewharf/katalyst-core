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

package reclaim

import (
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

// percentageFromDynamic extracts the AQC-configured percentage for the
// given consumer from the dynamic config, safely handling nil values.
// Missing keys or nil config return 0.
func percentageFromDynamic(d *dynamic.Configuration, name string) float64 {
	if d == nil || d.ReclaimedResourceConfiguration == nil {
		return 0
	}
	return float64(d.ReclaimedPercentageByConsumer[name])
}

// GetConsumers returns a snapshot slice of every registered ReclaimedConsumer.
// Order is undefined (map iteration). The returned slice is safe to walk
// without holding consumersMu.
func GetConsumers() []ReclaimedConsumer {
	consumersMu.RLock()
	defer consumersMu.RUnlock()
	out := make([]ReclaimedConsumer, 0, len(consumers))
	for _, c := range consumers {
		out = append(out, c)
	}
	return out
}

// GetConsumerByName returns the registered ReclaimedConsumer for name.
// The bool is false when name is not registered.
func GetConsumerByName(name string) (ReclaimedConsumer, bool) {
	consumersMu.RLock()
	defer consumersMu.RUnlock()
	c, ok := consumers[name]
	return c, ok
}

// GetConsumerNameByPath returns the name of the consumer that owns the
// given cgroup path (parent or per-NUMA). The bool is false when path is
// not registered.
func GetConsumerNameByPath(path string) (string, bool) {
	consumersMu.RLock()
	defer consumersMu.RUnlock()
	name, ok := pathToConsumerName[path]
	return name, ok
}

// AggregateCgroupPaths returns every registered consumer's cgroup path.
// Consumers whose GetCgroupPath returns an empty string are skipped.
// Order is undefined because it walks the registry's underlying map.
func AggregateCgroupPaths() []string {
	consumers := GetConsumers()
	out := make([]string, 0, len(consumers))
	for _, c := range consumers {
		path := c.GetCgroupPath()
		if path == "" {
			continue
		}
		out = append(out, path)
	}
	return out
}

// AggregateAllCgroupPaths returns all cgroup paths owned by registered consumers.
// Order is undefined.
func AggregateAllCgroupPaths() []string {
	consumers := GetConsumers()
	out := make([]string, 0, len(consumers))
	for _, c := range consumers {
		out = append(out, c.GetAllCgroupPaths()...)
	}
	return out
}

// AggregateNumaBindingCgroupPaths groups registered consumers' NUMA-binding
// cgroup paths by NUMA node id. Map and slice order are undefined.
func AggregateNumaBindingCgroupPaths() map[int][]string {
	out := make(map[int][]string)
	for _, c := range GetConsumers() {
		for numaID, path := range c.GetNumaBindingCgroupPaths() {
			if path == "" {
				continue
			}
			out[numaID] = append(out[numaID], path)
		}
	}
	return out
}

// GetReclaimedPercentageByPath returns the reclaimed percentage for the
// consumer that owns path (parent or NUMA-binding). Unknown paths → 0.
func GetReclaimedPercentageByPath(d *dynamic.Configuration, path string) float64 {
	owner, ok := GetConsumerNameByPath(path)
	if !ok {
		return 0
	}
	return percentageFromDynamic(d, owner)
}

// GetReclaimedPercentage returns the AQC-configured percentage for name.
// Unknown names return 0.
func GetReclaimedPercentage(d *dynamic.Configuration, name string) float64 {
	if _, ok := GetConsumerByName(name); !ok {
		general.Infof("reclaim: consumer %q not found in registry", name)
		return 0
	}
	return percentageFromDynamic(d, name)
}
