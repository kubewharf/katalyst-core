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
)

// ConsumerFactory constructs a concrete ReclaimedConsumer. Implementations
// close over any configuration they need at the point where they register.
type ConsumerFactory func() ReclaimedConsumer

// factories holds the pre-registered ConsumerFactory for each known consumer
// name. Impls register themselves at agent boot from a config-aware call site
// (e.g. cmd/katalyst-agent/app/agent.go) rather than in init(), so the
// factory closure can capture the concrete config values.
var factories sync.Map

// RegisterFactory registers a ConsumerFactory under name. Overwrites any
// prior entry with the same name.
func RegisterFactory(name string, f ConsumerFactory) {
	factories.Store(name, f)
}

// registerGenericFactory registers the default GenericConsumer factory under
// GenericConsumerName. It captures the two scalar config values the consumer
// needs so callers do not have to repeat the closure boilerplate.
func registerGenericFactory(cgroupPath string, percentage float64) {
	RegisterFactory(GenericConsumerName, func() ReclaimedConsumer {
		return NewGenericConsumer(cgroupPath, percentage)
	})
}

// SetupConsumers is the one-call convenience path for booting the reclaim
// registry: it installs the default GenericConsumer factory with the given
// scalar config, then boots names. Out-of-tree consumers that have registered
// their own factory via RegisterFactory (typically from an init()) will still
// be booted if they appear in names.
func SetupConsumers(cgroupPath string, percentage float64, names []string) error {
	registerGenericFactory(cgroupPath, percentage)
	return initConsumers(names)
}

// initConsumers looks up each name's factory, constructs the consumer, and
// stores it in the runtime registry under that same name. If names is empty
// it defaults to [GenericConsumerName]. An unknown name is a hard error.
func initConsumers(names []string) error {
	if len(names) == 0 {
		names = []string{GenericConsumerName}
	}
	for _, name := range names {
		v, ok := factories.Load(name)
		if !ok {
			return fmt.Errorf("unknown reclaim consumer %q", name)
		}
		if err := registerConsumer(name, v.(ConsumerFactory)()); err != nil {
			return fmt.Errorf("registering consumer %q failed: %w", name, err)
		}
	}
	return nil
}
