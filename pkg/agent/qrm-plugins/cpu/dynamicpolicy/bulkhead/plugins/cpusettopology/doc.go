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

// Package cpusettopology is the topology owner plugin: it builds and converges
// the cpuset partition DAG, and owns the disabled-reset transition state.
//
// Why it owns reset state: the manager no longer tracks a per-name reset state
// machine; the plugin reports NeedsDisabledReset() and is marked complete only
// inside a generation-fence commit, so a stale fence leaves the reset pending
// for the next round.
package cpusettopology
