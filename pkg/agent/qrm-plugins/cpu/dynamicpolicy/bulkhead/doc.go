/*
Copyright 2026 The Katalyst Authors.

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

// Package bulkhead implements the bulkhead CPU isolation manager: it owns the
// per-round cpuset adjustment loop, the applied-view publication boundary, and
// the plugin registry lifecycle.
//
// Invariant: the manager is the single writer of the shared applied view;
// plugins return typed outcomes (api.TopologyOutcome) but never mutate shared
// state directly.
//
// Constraint: this package must not import the topology algorithm
// (utils/topology) or any concrete plugin; it depends only on api + model and
// dispatches plugins by interface assertion (see api/api_boundary_test.go).
package bulkhead
