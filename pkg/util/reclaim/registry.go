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
	"fmt"
	"sync"

	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

var (
	consumersMu sync.RWMutex
	consumers   = map[string]ReclaimedConsumer{}
)

// registerConsumer stores a ReclaimedConsumer under the given name. It
// returns an error when name is already registered, or when the total of
// GetReclaimedPercentage() across every registered consumer plus c would
// exceed 100. Kept unexported so external callers must go through the
// factory path (RegisterFactory + SetupConsumers) or through the
// RegisterNamedGenericConsumer helper.
func registerConsumer(name string, c ReclaimedConsumer) error {
	consumersMu.Lock()
	defer consumersMu.Unlock()
	if err := validateNewConsumer(name, c); err != nil {
		return err
	}
	consumers[name] = c
	general.Infof("reclaim: consumer %q successfully registered", name)
	return nil
}

// UnregisterConsumer removes name from the registry. Removing a name that
// was never registered is a no-op. Primarily intended for tests that need
// to re-register a name; production code does not use this.
func UnregisterConsumer(name string) {
	consumersMu.Lock()
	defer consumersMu.Unlock()
	delete(consumers, name)
}

// validateNewConsumer checks the invariants enforced at registration time:
//   - name must not already be registered
//   - the sum of GetReclaimedPercentage() across every registered consumer
//     plus c must be <= 100
//
// The caller must hold consumersMu for writing.
func validateNewConsumer(name string, c ReclaimedConsumer) error {
	if _, exists := consumers[name]; exists {
		return fmt.Errorf("reclaim consumer %q is already registered", name)
	}
	total := c.GetReclaimedPercentage()
	for _, existing := range consumers {
		total += existing.GetReclaimedPercentage()
	}
	if total > 100 {
		return fmt.Errorf("registering reclaim consumer %q would push total reclaimed percentage to %.2f > 100", name, total)
	}
	return nil
}

// GetReclaimedPercentage returns the reclaimed percentage (0-100) reported by
// the consumer registered under name, and a boolean indicating whether such a
// consumer exists. If name is not registered, the returned percentage is 0
// and a log line is emitted.
//
// Special case: if exactly one consumer is registered, its percentage is
// forced to 100 regardless of GetReclaimedPercentage() — with a single
// consumer there is nothing to split, so it owns the full budget.
func GetReclaimedPercentage(name string) (float64, bool) {
	consumersMu.RLock()
	defer consumersMu.RUnlock()
	c, ok := consumers[name]
	if !ok {
		general.Infof("reclaim: consumer %q not found in registry", name)
		return 0, false
	}
	if len(consumers) == 1 {
		return 100, true
	}
	return c.GetReclaimedPercentage(), true
}

// RegisterNamedGenericConsumer constructs a GenericConsumer from the given
// values and registers it under name. Intended for tests that need to
// populate the registry with named generic consumers; production code should
// use SetupConsumers.
func RegisterNamedGenericConsumer(name string, cgroupPath string, percentage float64) error {
	return registerConsumer(name, NewGenericConsumer(cgroupPath, percentage))
}

// AggregateCgroupPaths returns every registered consumer's cgroup path.
// Consumers whose GetCgroupPath returns an empty string are skipped.
// Order is undefined because it walks the registry's underlying map.
func AggregateCgroupPaths() []string {
	consumersMu.RLock()
	defer consumersMu.RUnlock()
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

// AggregateNumaBindingCgroupPaths returns, for each NUMA node id in numaNodes,
// the list of per-NUMA reclaim cgroup paths contributed by every registered
// consumer. Consumers whose GetNumaBindingCgroupPaths returns a nil map are
// skipped.
//
// Within each key's slice, entries from different consumers may appear in any
// order (registry iteration is undefined).
func AggregateNumaBindingCgroupPaths(numaNodes []int) map[int][]string {
	consumersMu.RLock()
	defer consumersMu.RUnlock()
	out := make(map[int][]string, len(numaNodes))
	for _, numaID := range numaNodes {
		out[numaID] = nil
	}
	for _, c := range consumers {
		paths := c.GetNumaBindingCgroupPaths(numaNodes)
		if paths == nil {
			continue
		}
		for _, numaID := range numaNodes {
			out[numaID] = append(out[numaID], paths[numaID])
		}
	}
	return out
}

// CgroupPathWithPercentage pairs a reclaim cgroup path with the reclaimed
// percentage (0-100) reported by the consumer that owns it. Used by callers
// that need to scale per-consumer resource advice (e.g. memory.max / memory.high).
type CgroupPathWithPercentage struct {
	Path       string
	Percentage float64
}

// AggregateCgroupPathsWithPercentage returns every registered consumer's
// cgroup path paired with the consumer's reclaimed percentage. Consumers
// whose GetCgroupPath returns an empty string are skipped. Order is
// undefined (registry iteration).
//
// Special case: if exactly one consumer contributes an entry, its
// percentage is forced to 100 regardless of GetReclaimedPercentage() —
// with a single contributor there is nothing to split, so it owns the
// full budget.
func AggregateCgroupPathsWithPercentage() []CgroupPathWithPercentage {
	consumersMu.RLock()
	defer consumersMu.RUnlock()
	out := make([]CgroupPathWithPercentage, 0, len(consumers))
	for _, c := range consumers {
		path := c.GetCgroupPath()
		if path == "" {
			continue
		}
		out = append(out, CgroupPathWithPercentage{
			Path:       path,
			Percentage: c.GetReclaimedPercentage(),
		})
	}
	if len(out) == 1 {
		out[0].Percentage = 100
	}
	return out
}

// AggregateNumaBindingCgroupPathsWithPercentage returns, for each NUMA node
// id in numaNodes, the list of per-NUMA reclaim cgroup paths contributed by
// every registered consumer, each paired with that consumer's reclaimed
// percentage. Consumers whose GetNumaBindingCgroupPaths returns a nil map
// are skipped.
//
// Within each key's slice, entries from different consumers may appear in
// any order (registry iteration is undefined).
//
// Special case: if exactly one consumer contributes entries, its
// percentage is forced to 100 regardless of GetReclaimedPercentage().
func AggregateNumaBindingCgroupPathsWithPercentage(numaNodes []int) map[int][]CgroupPathWithPercentage {
	consumersMu.RLock()
	defer consumersMu.RUnlock()
	out := make(map[int][]CgroupPathWithPercentage, len(numaNodes))
	for _, numaID := range numaNodes {
		out[numaID] = nil
	}
	contributing := 0
	for _, c := range consumers {
		paths := c.GetNumaBindingCgroupPaths(numaNodes)
		if paths == nil {
			continue
		}
		contributing++
		pct := c.GetReclaimedPercentage()
		for _, numaID := range numaNodes {
			out[numaID] = append(out[numaID], CgroupPathWithPercentage{
				Path:       paths[numaID],
				Percentage: pct,
			})
		}
	}
	if contributing == 1 {
		for _, numaID := range numaNodes {
			if len(out[numaID]) > 0 {
				out[numaID][0].Percentage = 100
			}
		}
	}
	return out
}
