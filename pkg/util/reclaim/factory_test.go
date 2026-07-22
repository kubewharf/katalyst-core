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
	"strings"
	"sync"
	"testing"

	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// testRegistryMu serializes access to the process-global reclaim state
// (consumers, factories) across tests in this package. Each test that
// mutates or reads that state calls t.Parallel() to opt into the parallel
// scheduler but takes testRegistryMu for the duration of its body, so
// sibling tests cannot observe partially-modified globals.
var testRegistryMu sync.Mutex

func lockGlobalRegistry(t *testing.T) {
	t.Helper()
	testRegistryMu.Lock()
	t.Cleanup(testRegistryMu.Unlock)
}

func resetRegistry() {
	consumersMu.Lock()
	defer consumersMu.Unlock()
	consumers = map[string]ReclaimedConsumer{}
	pathToConsumerName = map[string]string{}
}

func TestInitConsumers_Default(t *testing.T) {
	t.Parallel()
	lockGlobalRegistry(t)
	resetRegistry()

	conf := config.NewConfiguration()
	conf.BaseConfiguration.ReclaimRelativeRootCgroupPath = "/kubepods/besteffort"

	if err := initConsumers(conf, nil); err != nil {
		t.Fatalf("initConsumers(nil): unexpected error: %v", err)
	}
	if _, ok := GetConsumerByName(GenericConsumerName); !ok {
		t.Fatalf("expected %q to be registered after default initConsumers", GenericConsumerName)
	}
}

func TestInitConsumers_Unknown(t *testing.T) {
	t.Parallel()
	lockGlobalRegistry(t)
	resetRegistry()

	conf := config.NewConfiguration()
	conf.BaseConfiguration.ReclaimConsumers = []string{"does-not-exist"}

	err := initConsumers(conf, nil)
	if err == nil {
		t.Fatal("initConsumers with unknown name: expected error, got nil")
	}
	if !strings.Contains(err.Error(), "does-not-exist") {
		t.Fatalf("initConsumers error should mention the unknown name, got: %v", err)
	}
}

func TestInitConsumers_Multiple(t *testing.T) {
	t.Parallel()
	lockGlobalRegistry(t)
	resetRegistry()

	RegisterFactory("test-second", func(c *config.Configuration, machineInfo *machine.KatalystMachineInfo) ReclaimedConsumer {
		return NewGenericConsumer(c, machineInfo)
	})
	defer factories.Delete("test-second")

	conf := config.NewConfiguration()
	conf.BaseConfiguration.ReclaimRelativeRootCgroupPath = "/kubepods/besteffort"
	conf.BaseConfiguration.ReclaimConsumers = []string{GenericConsumerName, "test-second"}

	if err := initConsumers(conf, nil); err != nil {
		t.Fatalf("initConsumers: unexpected error: %v", err)
	}

	if _, ok := GetConsumerByName(GenericConsumerName); !ok {
		t.Fatalf("expected %q in registry", GenericConsumerName)
	}
	if _, ok := GetConsumerByName("test-second"); !ok {
		t.Fatalf("expected %q in registry", "test-second")
	}
}
