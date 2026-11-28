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

	coreconfig "github.com/kubewharf/katalyst-core/pkg/config"
)

func newRegistryTestRuleInitializer(*coreconfig.Configuration, KillerFactory) (KillerRule, error) {
	return testKillerRule{name: "registry-test-rule", priority: 1, matched: false}, nil
}

func TestRegisterKillerRuleInitializer(t *testing.T) {
	t.Parallel()

	name := "registry-test-rule"
	t.Cleanup(func() {
		killerRuleInitializerLock.Lock()
		delete(killerRuleInitializers, name)
		killerRuleInitializerLock.Unlock()
	})

	RegisterKillerRuleInitializer(name, newRegistryTestRuleInitializer)
	initializers := GetRegisteredKillerRuleInitializers()
	if initializers[name] == nil {
		t.Fatalf("expected rule initializer to be registered")
	}
}

func TestRegisterKillerRuleInitializerPanicsOnDuplicate(t *testing.T) {
	t.Parallel()

	name := "registry-duplicate-rule"
	t.Cleanup(func() {
		killerRuleInitializerLock.Lock()
		delete(killerRuleInitializers, name)
		killerRuleInitializerLock.Unlock()
	})

	RegisterKillerRuleInitializer(name, newRegistryTestRuleInitializer)
	defer func() {
		if recover() == nil {
			t.Fatalf("expected duplicate registration panic")
		}
	}()
	RegisterKillerRuleInitializer(name, newRegistryTestRuleInitializer)
}

func TestGetRegisteredKillerRuleInitializersReturnsCopy(t *testing.T) {
	t.Parallel()

	initializers := GetRegisteredKillerRuleInitializers()
	initializers["mutated-rule"] = newRegistryTestRuleInitializer
	if GetRegisteredKillerRuleInitializers()["mutated-rule"] != nil {
		t.Fatalf("expected registry snapshot to be immutable")
	}
}
