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

package topology

import (
	"path/filepath"

	"golang.org/x/sys/unix"
)

// newCgroupV1Driver is a test-only thin wrapper over newCgroupFSDriver pinned to
// the cgroup v1 policy.
func newCgroupV1Driver(
	rootPath string,
	configuredRoots []string,
	fstat func(int, *unix.Stat_t) error,
	stable bool,
) *cgroupFSDriver {
	return newCgroupFSDriver(rootPath, configuredRoots, cgroupV1Policy, fstat, stable)
}

// parentRelInSnapshot is a test-only helper returning the nearest ancestor rel
// present in the snapshot, or "" when none exists.
func parentRelInSnapshot(rel string, snapshot *CompleteSnapshot) string {
	parent := filepath.Dir(rel)
	for parent != "." && parent != "" {
		if _, ok := snapshot.Entries[parent]; ok {
			return parent
		}
		parent = filepath.Dir(parent)
	}
	return ""
}
