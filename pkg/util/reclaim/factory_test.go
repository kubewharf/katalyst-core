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
	"sync/atomic"
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

// TestInitConsumers_RollsBackPartialRegistration ensures that if a later
// consumer name is unknown, the consumers registered earlier in the same call
// are unregistered instead of leaking into the process-global registry.
func TestInitConsumers_RollsBackPartialRegistration(t *testing.T) {
	t.Parallel()
	lockGlobalRegistry(t)
	resetRegistry()

	RegisterFactory("test-good", func(c *config.Configuration, machineInfo *machine.KatalystMachineInfo) ReclaimedConsumer {
		return NewGenericConsumer(c, machineInfo)
	})
	defer factories.Delete("test-good")

	conf := config.NewConfiguration()
	conf.BaseConfiguration.ReclaimConsumers = []string{"test-good", "does-not-exist"}

	err := initConsumers(conf, nil)
	if err == nil {
		t.Fatal("expected error for unknown consumer, got nil")
	}
	if _, ok := GetConsumerByName("test-good"); ok {
		t.Fatal("consumer test-good should have been rolled back after unknown-name error, but is still registered")
	}
}

// TestInitConsumers_ConcurrentBoot is the regression test for the
// "reclaim consumer "generic" is already registered" flake: parallel test
// subtests each call NewMetaServer -> SetupConsumers -> initConsumers on a
// fresh registry. Every concurrent caller must succeed (the winner inserts,
// the losers observe it) and exactly one "generic" entry must exist.
func TestInitConsumers_ConcurrentBoot(t *testing.T) {
	t.Parallel()
	lockGlobalRegistry(t)
	resetRegistry()

	conf := config.NewConfiguration()
	conf.BaseConfiguration.ReclaimRelativeRootCgroupPath = "/kubepods/besteffort"
	// ReclaimConsumers intentionally left empty and shared: this exercises the
	// default-to-[GenericConsumerName] branch under concurrent callers. The
	// fix must derive the default from a local copy and never write back to
	// conf, otherwise the shared config races.

	const workers = 64
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	start := make(chan struct{})
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if err := initConsumers(conf, nil); err != nil {
				errs <- err
			}
		}()
	}
	close(start)
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Fatalf("concurrent initConsumers returned error: %v", err)
	}
	if _, ok := GetConsumerByName(GenericConsumerName); !ok {
		t.Fatalf("expected %q to be registered after concurrent boot", GenericConsumerName)
	}
}

// TestGetOrRegisterConsumer_Concurrent pins the atomic get-or-register
// contract: among N racing callers for one name, exactly one creates the
// entry and N-1 observe the pre-existing one.
func TestGetOrRegisterConsumer_Concurrent(t *testing.T) {
	t.Parallel()
	lockGlobalRegistry(t)
	resetRegistry()

	const name = "race-get-or-register"
	const workers = 64
	var wg sync.WaitGroup
	var created int64
	start := make(chan struct{})
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if _, c := getOrRegisterConsumer(name, NewGenericConsumer(newTestConf("/race"), nil)); c {
				atomic.AddInt64(&created, 1)
			}
		}()
	}
	close(start)
	wg.Wait()

	if got := atomic.LoadInt64(&created); got != 1 {
		t.Fatalf("expected exactly 1 creator, got %d", got)
	}
	if _, ok := GetConsumerByName(name); !ok {
		t.Fatalf("expected %q to be registered", name)
	}
}

// TestGetOrRegisterConsumer_DoesNotClobberExisting verifies idempotent boot
// semantics while keeping the deliberate-placement conflict hard error intact:
// getOrRegister on an occupied name returns the existing consumer and leaves
// it in place, whereas registerConsumer on the same occupied name still errors
// (so a genuinely different consumer claiming the name is never silently
// swallowed).
func TestGetOrRegisterConsumer_DoesNotClobberExisting(t *testing.T) {
	t.Parallel()
	lockGlobalRegistry(t)
	resetRegistry()

	const name = "occupied"
	first := NewGenericConsumer(newTestConf("/first"), nil)
	if err := registerConsumer(name, first); err != nil {
		t.Fatalf("initial registerConsumer: unexpected error: %v", err)
	}

	existing, created := getOrRegisterConsumer(name, NewGenericConsumer(newTestConf("/second"), nil))
	if created {
		t.Fatal("getOrRegisterConsumer: expected created=false for an occupied name")
	}
	if existing != first {
		t.Fatal("getOrRegisterConsumer: returned consumer differs from the existing one")
	}
	if got := existing.GetCgroupPath(); got != "/first" {
		t.Fatalf("existing consumer was clobbered: path=%q, want /first", got)
	}

	if err := registerConsumer(name, NewGenericConsumer(newTestConf("/third"), nil)); err == nil ||
		!strings.Contains(err.Error(), "already registered") {
		t.Fatalf("registerConsumer on occupied name: expected 'already registered' error, got %v", err)
	}
}
