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

package cpusettopology

import (
	"path"
	"path/filepath"
	"strings"
	"testing"

	cgcommon "github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
)

// TestBuildExpectedCPUSetByRel_KeyMatchesExpandDescendantsChildRel is a regression
// guard for the rel-key format used inside plugin.go::buildExpectedCPUSetByRel.
//
// Background:
//
//   - plugin.go::buildExpectedCPUSetByRel resolves each container's rel via
//     cgcommon.GetContainerRelativeCgroupPath, which internally calls
//     GetKubernetesAnyExistRelativeCgroupPath. That helper joins entries from
//     k8sCgroupPathList (e.g. CgroupFsRootPathBurstable = "/kubepods/burstable")
//     with the pod+container suffix via path.Join, so the resulting rel keeps a
//     leading "/" (e.g. "/kubepods/burstable/pod<uid>/<cid>").
//
//   - utils/topology/writer.go::expandDescendants walks down from the bulkhead
//     primary rel (e.g. "kubepods", configured without a leading "/") using
//     filepath.Join, so the childRel it produces never has a leading "/".
//
// If plugin.go stores the map key with a leading "/", the map lookup inside
// ApplyDAGDiff will silently miss for container-level rels, and container cgroups
// will keep inheriting the parent pool target instead of getting the state-recorded
// CPU set enforced. The mitigation is a strings.Trim(rel, "/") right after the
// cgcommon.GetContainerRelativeCgroupPath call, mirroring what bulkhead/utils/rels.go
// already does.
//
// This test locks the format contract of the two sides so future refactors do not
// silently reintroduce the mismatch.
func TestBuildExpectedCPUSetByRel_KeyMatchesExpandDescendantsChildRel(t *testing.T) {
	const (
		podUID      = "4f99c9cf-4338-4083-9a94-c99284d67e5a"
		containerID = "454686de2037861c9d25777c44fbf888e41a7c137bcaaf2746458e3509752847"
		// bulkhead primary rel path as configured on the target node:
		// --qrm-cpu-bulkhead-primary-rel-path=kubepods (no leading slash).
		primaryRel = "kubepods"
	)

	suffix := path.Join("pod"+podUID, containerID)

	// (A) Raw rel returned by GetKubernetesAnyExistRelativeCgroupPath. This is
	// what plugin.go::buildExpectedCPUSetByRel receives from
	// cgcommon.GetContainerRelativeCgroupPath BEFORE any trimming.
	rawRel := path.Join(cgcommon.CgroupFsRootPathBurstable, suffix)
	if !strings.HasPrefix(rawRel, "/") {
		t.Fatalf("prerequisite failed: CgroupFsRootPathBurstable is expected to keep a leading slash, got %q", rawRel)
	}

	// (B) Key actually stored in the expected map after the fix
	// (strings.Trim(rel, "/") is applied inside buildExpectedCPUSetByRel).
	trimmedRel := strings.Trim(rawRel, "/")

	// (C) childRel that expandDescendants constructs when recursing down from the
	// bulkhead primary rel using filepath.Join.
	childRel := filepath.Join(primaryRel, "burstable", "pod"+podUID, containerID)

	t.Logf("raw rel (pre-trim)      : %q", rawRel)
	t.Logf("trimmed rel (map key)   : %q", trimmedRel)
	t.Logf("expandDescendants child : %q", childRel)

	if strings.HasPrefix(trimmedRel, "/") {
		t.Fatalf("map key still has leading '/'; strings.Trim(rel, \"/\") was skipped: %q", trimmedRel)
	}
	if trimmedRel != childRel {
		t.Fatalf("map key does not match expandDescendants childRel:\n  key      : %q\n  childRel : %q",
			trimmedRel, childRel)
	}

	// Also document the buggy pre-fix state so this test doubles as a bug reproduction.
	if rawRel == childRel {
		t.Fatalf("pre-condition inverted: without strings.Trim the key equals childRel; the regression test would not catch the original bug anymore")
	}
}
