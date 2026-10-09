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

	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

var (
	consumersMu        sync.RWMutex
	consumers          = map[string]ReclaimedConsumer{}
	pathToConsumerName = map[string]string{} // cgroup path -> consumer name
)

// registerConsumer stores a ReclaimedConsumer under the given name and
// pre-populates the reverse index for each path returned by the consumer.
// It returns an error when name is already registered. This is the deliberate
// placement entry point (used by RegisterNamedGenericConsumer and direct
// tests): an existing entry means a real conflict and must surface, so it is
// never silently overwritten.
func registerConsumer(name string, c ReclaimedConsumer) error {
	consumersMu.Lock()
	defer consumersMu.Unlock()
	if _, exists := consumers[name]; exists {
		return fmt.Errorf("reclaim consumer %q is already registered", name)
	}
	storeConsumerLocked(name, c)
	return nil
}

// getOrRegisterConsumer is the atomic get-or-register used by initConsumers.
// It closes the check-then-act window between a "not present" lookup and the
// insert, so concurrent SetupConsumers callers cannot both lose with an
// "already registered" error: exactly one call inserts, the others observe the
// winner. An already-present name is treated as an idempotent boot and its
// existing consumer is returned unchanged (created=false). Deliberate test
// placement that must detect a conflicting entry still goes through
// registerConsumer, which hard-errors instead of skipping.
func getOrRegisterConsumer(name string, c ReclaimedConsumer) (existing ReclaimedConsumer, created bool) {
	consumersMu.Lock()
	defer consumersMu.Unlock()
	if current, ok := consumers[name]; ok {
		return current, false
	}
	storeConsumerLocked(name, c)
	return c, true
}

// storeConsumerLocked inserts name->c and rebuilds the path reverse index.
// Callers must hold consumersMu.
func storeConsumerLocked(name string, c ReclaimedConsumer) {
	consumers[name] = c
	for _, p := range c.GetAllCgroupPaths() {
		if p != "" {
			pathToConsumerName[p] = name
		}
	}
	general.Infof("reclaim: consumer %q successfully registered", name)
}

// UnregisterConsumer removes name from the registry and purges every
// reverse-index entry pointing at it. Removing a name that was never
// registered is a no-op. Primarily intended for tests that need to
// re-register a name; production code does not use this.
func UnregisterConsumer(name string) {
	consumersMu.Lock()
	defer consumersMu.Unlock()
	delete(consumers, name)
	for p, owner := range pathToConsumerName {
		if owner == name {
			delete(pathToConsumerName, p)
		}
	}
}

// RegisterNamedGenericConsumer constructs a GenericConsumer from the given
// values and registers it under name. Intended for tests that need to
// populate the registry with named generic consumers; production code should
// use SetupConsumers.
func RegisterNamedGenericConsumer(name string, conf *config.Configuration, machineInfo *machine.KatalystMachineInfo) error {
	return registerConsumer(name, NewGenericConsumer(conf, machineInfo))
}

// ResetConsumerForTest atomically replaces (or inserts) the consumer under
// name in a single registry write-lock critical section. It purges any prior
// entry and its reverse-index paths, then stores c. This exists so tests that
// must pin "generic" to a fresh consumer do not have to compose
// UnregisterConsumer + RegisterNamedGenericConsumer, whose delete-then-insert
// gap lets a concurrent NewMetaServer -> SetupConsumers bootstrap slip in and
// turn the deliberate register into an "already registered" error.
//
// Test-only: production boot goes through initConsumers -> getOrRegisterConsumer
// and must never blindly overwrite an already-registered consumer.
func ResetConsumerForTest(name string, c ReclaimedConsumer) {
	consumersMu.Lock()
	defer consumersMu.Unlock()
	delete(consumers, name)
	for p, owner := range pathToConsumerName {
		if owner == name {
			delete(pathToConsumerName, p)
		}
	}
	storeConsumerLocked(name, c)
}

// ResetNamedGenericConsumerForTest is the GenericConsumer convenience wrapper
// around ResetConsumerForTest. Test-only.
func ResetNamedGenericConsumerForTest(name string, conf *config.Configuration, machineInfo *machine.KatalystMachineInfo) {
	ResetConsumerForTest(name, NewGenericConsumer(conf, machineInfo))
}
