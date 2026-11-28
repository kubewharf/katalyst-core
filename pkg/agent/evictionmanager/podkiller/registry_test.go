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

package podkiller

import (
	"testing"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/events"

	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

func TestRegisterKillerInitializer(t *testing.T) {
	t.Parallel()

	name := "test-killer-register"
	RegisterKillerInitializer(name, newDummyKillerForRegistryTest)

	initializers := GetRegisteredKillerInitializers()
	if initializers[name] == nil {
		t.Fatalf("expected %q to be registered", name)
	}
}

func TestRegisterKillerInitializerPanicsOnDuplicate(t *testing.T) {
	t.Parallel()

	name := "test-killer-duplicate"
	RegisterKillerInitializer(name, newDummyKillerForRegistryTest)

	assertPanic(t, func() {
		RegisterKillerInitializer(name, newDummyKillerForRegistryTest)
	})
}

func TestRegisterKillerInitializerPanicsOnInvalidInput(t *testing.T) {
	t.Parallel()

	assertPanic(t, func() {
		RegisterKillerInitializer("", newDummyKillerForRegistryTest)
	})
	assertPanic(t, func() {
		RegisterKillerInitializer("test-killer-nil", nil)
	})
}

func TestGetRegisteredKillerInitializersReturnsCopy(t *testing.T) {
	t.Parallel()

	initializers := GetRegisteredKillerInitializers()
	initializers["test-killer-mutated-copy"] = newDummyKillerForRegistryTest

	if _, ok := GetRegisteredKillerInitializers()["test-killer-mutated-copy"]; ok {
		t.Fatalf("expected registry snapshot mutation not to affect registered initializers")
	}
}

func TestDefaultKillerInitializersRegistered(t *testing.T) {
	t.Parallel()

	initializers := GetRegisteredKillerInitializers()

	for _, name := range []string{
		consts.KillerNameEvictionKiller,
		consts.KillerNameDeletionKiller,
		consts.KillerNameContainerKiller,
	} {
		if initializers[name] == nil {
			t.Fatalf("expected default killer %q to be registered", name)
		}
	}
}

func assertPanic(t *testing.T, fn func()) {
	t.Helper()

	defer func() {
		if recover() == nil {
			t.Fatalf("expected panic")
		}
	}()
	fn()
}

func newDummyKillerForRegistryTest(_ *config.Configuration, _ kubernetes.Interface, _ events.EventRecorder, _ metrics.MetricEmitter) (Killer, error) {
	return DummyKiller{}, nil
}
