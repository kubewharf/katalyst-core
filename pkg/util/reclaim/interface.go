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

// ReclaimedConsumer is a small cross-cutting view of the reclaimed-cores
// configuration used by both qrm-plugins and sysadvisor. Implementations
// typically wrap the agent configuration.
type ReclaimedConsumer interface {
	// GetCgroupPath returns the reclaimed-cores parent (relative) cgroup path,
	// e.g. "/kubepods/besteffort".
	GetCgroupPath() string

	// GetNumaBindingCgroupPaths returns per-NUMA reclaim cgroup paths keyed by
	// NUMA node id. Semantics match
	// cgroup/common.GetNUMABindingReclaimRelativeRootCgroupPaths.
	GetNumaBindingCgroupPaths() map[int]string

	// GetAllCgroupPaths returns all cgroup paths owned by this consumer.
	GetAllCgroupPaths() []string
}
