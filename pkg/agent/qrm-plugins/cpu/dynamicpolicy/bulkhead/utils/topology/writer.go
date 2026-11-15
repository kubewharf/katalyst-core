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
	"errors"
	"os"
	"strings"
	"syscall"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

const (
	// These invocation-scoped ceilings are deliberately conservative safety
	// bounds. Task 8 will calibrate them from production telemetry; they are not
	// a compatibility path and must never be disabled to accept a larger tree.
	defaultApplyMaxSnapshotNodes = 4096
	defaultApplyMaxSnapshotDepth = 16
)

func memsForNode(n *TopoNode, defaultMems string) string {
	if n != nil && n.Mems != "" {
		return n.Mems
	}
	return defaultMems
}

func observedCPUsForTargetProof(
	entry EntryState,
	target machine.CPUSet,
	capabilities HierarchyCapabilities,
) machine.CPUSet {
	if capabilities.EmptyConfiguredCPUSet && target.IsEmpty() {
		// In v2, empty configured means inheritance, so only configured state can prove an empty target;
		// effective CPUs still prove other targets, avoiding false completion before configuration takes effect.
		return entry.ConfiguredCPUs
	}
	return entry.CPUs
}

func isConfiguredInheritanceClear(
	operation PlanOperation,
	capabilities HierarchyCapabilities,
) bool {
	return capabilities.EmptyConfiguredCPUSet && operation.Target.CPUs.IsEmpty()
}

func isCgroupNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) ||
		errors.Is(err, syscall.ENODEV) {
		return true
	}
	errText := strings.ToLower(err.Error())
	return strings.Contains(errText, "no such file or directory") ||
		strings.Contains(errText, "not a directory") ||
		strings.Contains(errText, "no such device")
}
