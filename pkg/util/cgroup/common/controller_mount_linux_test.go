//go:build linux

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

package common

import (
	"errors"
	"strings"
	"testing"
)

func TestParseControllerMounts(t *testing.T) {
	t.Parallel()

	mounts, err := parseControllerMounts(strings.NewReader(
		"29 23 0:26 / /sys/fs/cgroup/cpu,cpuacct rw,nosuid,nodev,noexec,relatime - cgroup cgroup rw,cpu,cpuacct\n" +
			"30 23 0:27 / /sys/fs/cgroup/cpuset rw,nosuid,nodev,noexec,relatime - cgroup cgroup rw,cpuset\n",
	))
	if err != nil {
		t.Fatalf("parseControllerMounts: %v", err)
	}
	if got := mounts[CgroupSubsysCPU]; got != "/sys/fs/cgroup/cpu,cpuacct" {
		t.Fatalf("cpu mount = %q", got)
	}
	if got := mounts[CgroupSubsysCPUSet]; got != "/sys/fs/cgroup/cpuset" {
		t.Fatalf("cpuset mount = %q", got)
	}
}

func TestResolveControllerMountV1CombinedCPU(t *testing.T) {
	t.Parallel()

	got, err := resolveControllerMount(CgroupSubsysCPU, false, strings.NewReader(
		"29 23 0:26 / /sys/fs/cgroup/cpu,cpuacct rw - cgroup cgroup rw,cpu,cpuacct\n",
	))
	if err != nil {
		t.Fatalf("ResolveControllerMount: %v", err)
	}
	if got.Root != "/sys/fs/cgroup/cpu,cpuacct" || got.Unified {
		t.Fatalf("mount = %+v", got)
	}
}

func TestResolveControllerMountUnifiedV2(t *testing.T) {
	t.Parallel()

	got, err := resolveControllerMount(CgroupSubsysCPU, true, nil)
	if err != nil {
		t.Fatalf("resolveControllerMount unified: %v", err)
	}
	if !got.Unified {
		t.Fatalf("expected Unified=true, got %+v", got)
	}
	if got.Root != CgroupFSMountPoint {
		t.Fatalf("unified root = %q, want %q", got.Root, CgroupFSMountPoint)
	}
}

func TestResolveControllerMountUnifiedV2IgnoresReader(t *testing.T) {
	t.Parallel()

	got, err := resolveControllerMount(CgroupSubsysCPUSet, true, strings.NewReader("malformed"))
	if err != nil {
		t.Fatalf("unified must ignore reader: %v", err)
	}
	if !got.Unified {
		t.Fatalf("expected Unified=true, got %+v", got)
	}
}

func TestResolveControllerMountV1MissingControllerNoPanic(t *testing.T) {
	t.Parallel()

	_, err := resolveControllerMount(CgroupSubsysCPU, false, strings.NewReader(""))
	if !errors.Is(err, ErrControllerMountUnavailable) {
		t.Fatalf("error = %v, want ErrControllerMountUnavailable", err)
	}
}

func TestResolveControllerMountUnavailable(t *testing.T) {
	t.Parallel()

	_, err := resolveControllerMount(CgroupSubsysCPU, false, strings.NewReader(
		"30 23 0:27 / /sys/fs/cgroup/cpuset rw - cgroup cgroup rw,cpuset\n",
	))
	if !errors.Is(err, ErrControllerMountUnavailable) {
		t.Fatalf("error = %v, want ErrControllerMountUnavailable", err)
	}
}
