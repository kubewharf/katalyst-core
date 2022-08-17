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

// Package target is the package that is responsible to maintain the kcc-target logic
//
// handler.go: store kcc-target mapping info (mata) in-memory, common use cases include
// - maintain accessor lifecycle for a given kcc-target CRD
// - find kcc-target CR for a given kcc CR (considering history data rather than current CR)
// - find kcc CR for a given kcc-target CR (considering history data rather than current CR)
// ...
//
// accessor.go: construct a handler for each given kcc-target (mapping with 1:1), common use cases include
// - maintain the informer lifecycle for each kcc-target CRD
// - handle events for each kcc-target CR (using registered handler function)
// ...
package target // import "github.com/kubewharf/katalyst-core/pkg/controller/kcc/target"
