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
	"fmt"
	"sync"

	coreconfig "github.com/kubewharf/katalyst-core/pkg/config"
)

type KillerFactory func(killerName string) (Killer, error)

type RuleInitFunc func(conf *coreconfig.Configuration, factory KillerFactory) (KillerRule, error)

var (
	killerRuleInitializerLock sync.RWMutex
	killerRuleInitializers    = make(map[string]RuleInitFunc)
)

func RegisterKillerRuleInitializer(name string, initFunc RuleInitFunc) {
	if name == "" {
		panic("killer rule initializer name must not be empty")
	}
	if initFunc == nil {
		panic(fmt.Sprintf("killer rule initializer %q must not be nil", name))
	}

	killerRuleInitializerLock.Lock()
	defer killerRuleInitializerLock.Unlock()
	if _, ok := killerRuleInitializers[name]; ok {
		panic(fmt.Sprintf("killer rule initializer %q already registered", name))
	}
	killerRuleInitializers[name] = initFunc
}

func GetRegisteredKillerRuleInitializers() map[string]RuleInitFunc {
	killerRuleInitializerLock.RLock()
	defer killerRuleInitializerLock.RUnlock()

	initializers := make(map[string]RuleInitFunc, len(killerRuleInitializers))
	for name, initFunc := range killerRuleInitializers {
		initializers[name] = initFunc
	}
	return initializers
}
