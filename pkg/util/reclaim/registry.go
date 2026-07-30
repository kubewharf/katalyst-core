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
// It returns an error when name is already registered.
func registerConsumer(name string, c ReclaimedConsumer) error {
	consumersMu.Lock()
	defer consumersMu.Unlock()
	if _, exists := consumers[name]; exists {
		return fmt.Errorf("reclaim consumer %q is already registered", name)
	}
	consumers[name] = c
	for _, p := range c.GetAllCgroupPaths() {
		if p != "" {
			pathToConsumerName[p] = name
		}
	}
	general.Infof("reclaim: consumer %q successfully registered", name)
	return nil
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
