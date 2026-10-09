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

// Package topology implements the cpuset partition DAG planner and convergence
// coordinator. It is a pure function of (desired view, applied view, CPU
// inventory); it holds no manager/plugin state.
//
// Performance: planning at 10k nodes targets <1s and ~810K allocs/op; hot paths
// reuse precomputed childRel paths and pooled objects.
//
// Boundary: buildPlanOperations must fall back deterministically when childRels
// is uninitialized (see TestBuildPlanOperationsNilChildRelsByRelFallback).
package topology
