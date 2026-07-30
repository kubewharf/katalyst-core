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
// stores it in the runtime registry under that same name. If names is empty
// it defaults to [GenericConsumerName]. An unknown name is a hard error.
// Names already present in the registry are skipped, making SetupConsumers
// idempotent across repeated calls.
func initConsumers(conf *config.Configuration, machineInfo *machine.KatalystMachineInfo) error {
	if len(conf.ReclaimConsumers) == 0 {
		conf.ReclaimConsumers = []string{GenericConsumerName}
	}
	for _, name := range conf.ReclaimConsumers {
		if _, ok := GetConsumerByName(name); ok {
			continue
		}
		v, ok := factories.Load(name)
		if !ok {
			return fmt.Errorf("unknown reclaim consumer %q", name)
		}
		if err := registerConsumer(name, v.(ConsumerFactory)(conf, machineInfo)); err != nil {
			return fmt.Errorf("registering consumer %q failed: %w", name, err)
		}
	}
	return nil
}
