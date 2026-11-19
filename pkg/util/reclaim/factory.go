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
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// ConsumerFactory constructs a concrete ReclaimedConsumer. Implementations
// close over any configuration they need at the point where they register.
type ConsumerFactory func(conf *config.Configuration, machineInfo *machine.KatalystMachineInfo) ReclaimedConsumer

var factories sync.Map

// RegisterFactory registers a ConsumerFactory under name. Overwrites any
// prior entry with the same name. Intended for out-of-tree consumers.
func RegisterFactory(name string, f ConsumerFactory) {
	factories.Store(name, f)
}

// SetupConsumers boots the consumers named in conf.ReclaimConsumers.
func SetupConsumers(conf *config.Configuration, machineInfo *machine.KatalystMachineInfo) error {
	return initConsumers(conf, machineInfo)
}

// initConsumers looks up each name's factory, constructs the consumer, and
// stores it in the runtime registry under that same name. An empty consumer
// list defaults to [GenericConsumerName] via a local copy, so the shared conf
// is never mutated (concurrent boot callers must not race on it). An unknown
// name is a hard error. Names already present in the registry are skipped,
// making SetupConsumers idempotent across repeated and concurrent calls: the
// final insert uses the atomic getOrRegisterConsumer, so concurrent boot
// callers cannot both fail with "already registered", while registerConsumer
// (deliberate test placement) still hard-errors on a conflicting existing
// entry.
func initConsumers(conf *config.Configuration, machineInfo *machine.KatalystMachineInfo) error {
	names := conf.ReclaimConsumers
	if len(names) == 0 {
		names = []string{GenericConsumerName}
	}
	// track the names this call successfully created so a later failure can
	// roll back the partial registration instead of leaking consumers.
	var created []string
	for _, name := range names {
		// Fast path for the warm, already-booted registry. This read-only skip
		// is an optimization; correctness does not depend on it because the
		// insert below is atomic.
		if _, ok := GetConsumerByName(name); ok {
			continue
		}
		v, ok := factories.Load(name)
		if !ok {
			// roll back every consumer we registered in this loop; consumers that
			// were already present (created=false) are left untouched.
			for _, n := range created {
				UnregisterConsumer(n)
			}
			return fmt.Errorf("unknown reclaim consumer %q", name)
		}
		// Construct outside the registry lock, then atomically claim the name.
		// A racing caller may have inserted first; in that case the existing
		// consumer wins and the just-constructed instance is discarded.
		if _, isNew := getOrRegisterConsumer(name, v.(ConsumerFactory)(conf, machineInfo)); isNew {
			created = append(created, name)
		}
	}
	return nil
}
