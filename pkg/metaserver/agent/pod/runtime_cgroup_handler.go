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

package pod

import "sync"

type RuntimeCgroupPathHandlerInitializer func(RuntimePodFetcher)

type runtimeCgroupPathHandlerInitializerRegistry struct {
	sync.RWMutex
	initializers []RuntimeCgroupPathHandlerInitializer
}

var runtimeCgroupPathHandlerInitializers runtimeCgroupPathHandlerInitializerRegistry

func RegisterRuntimeCgroupPathHandlerInitializer(initializer RuntimeCgroupPathHandlerInitializer) {
	runtimeCgroupPathHandlerInitializers.register(initializer)
}

func (r *runtimeCgroupPathHandlerInitializerRegistry) snapshot() []RuntimeCgroupPathHandlerInitializer {
	r.RLock()
	defer r.RUnlock()
	return append([]RuntimeCgroupPathHandlerInitializer(nil), r.initializers...)
}

func (r *runtimeCgroupPathHandlerInitializerRegistry) register(initializer RuntimeCgroupPathHandlerInitializer) {
	if initializer == nil {
		return
	}
	r.Lock()
	defer r.Unlock()
	r.initializers = append(r.initializers, initializer)
}

func (r *runtimeCgroupPathHandlerInitializerRegistry) initialize(fetcher RuntimePodFetcher) {
	for _, init := range r.snapshot() {
		init(fetcher)
	}
}

// initializeRuntimeCgroupPathHandlersOn fans out the fetcher to every
// initializer in the provided registry. It takes the registry as an explicit
// parameter so tests exercise the fan-out on a local instance without touching
// the production global registry.
func initializeRuntimeCgroupPathHandlersOn(r *runtimeCgroupPathHandlerInitializerRegistry, runtimePodFetcher RuntimePodFetcher) {
	if r == nil {
		return
	}
	r.initialize(runtimePodFetcher)
}

func initializeRuntimeCgroupPathHandlers(runtimePodFetcher RuntimePodFetcher) {
	initializeRuntimeCgroupPathHandlersOn(&runtimeCgroupPathHandlerInitializers, runtimePodFetcher)
}
