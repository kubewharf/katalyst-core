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

	"github.com/kubewharf/katalyst-core/pkg/consts"
)

var (
	killerInitializerLock sync.RWMutex
	killerInitializers    = make(map[string]InitFunc)
)

func init() {
	RegisterKillerInitializer(consts.KillerNameEvictionKiller, NewEvictionAPIKiller)
	RegisterKillerInitializer(consts.KillerNameDeletionKiller, NewDeletionAPIKiller)
	RegisterKillerInitializer(consts.KillerNameContainerKiller, NewContainerKiller)
}

// RegisterKillerInitializer registers a pod killer initializer by name.
func RegisterKillerInitializer(name string, initFunc InitFunc) {
	if name == "" {
		panic("killer initializer name must not be empty")
	}
	if initFunc == nil {
		panic(fmt.Sprintf("killer initializer %q must not be nil", name))
	}

	killerInitializerLock.Lock()
	defer killerInitializerLock.Unlock()

	if _, ok := killerInitializers[name]; ok {
		panic(fmt.Sprintf("killer initializer %q already registered", name))
	}
	killerInitializers[name] = initFunc
}

// GetRegisteredKillerInitializers returns a snapshot of registered pod killer initializers.
func GetRegisteredKillerInitializers() map[string]InitFunc {
	killerInitializerLock.RLock()
	defer killerInitializerLock.RUnlock()

	initializers := make(map[string]InitFunc, len(killerInitializers))
	for name, initFunc := range killerInitializers {
		initializers[name] = initFunc
	}
	return initializers
}
