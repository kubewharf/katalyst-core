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

package client

import (
	"context"
	"errors"
	"strings"
	"testing"

	cgcommon "github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
)

func TestNewCgroupClient_ReturnsCachedClient(t *testing.T) {
	t.Parallel()

	c := NewCgroupClient()
	if c == nil {
		t.Fatalf("NewCgroupClient() = nil")
	}
	if _, ok := c.(*cachedCgroupClient); !ok {
		t.Fatalf("NewCgroupClient() type = %T, want *cachedCgroupClient", c)
	}
}

func TestNewCachedCgroupClient_PanicsOnNilInner(t *testing.T) {
	t.Parallel()

	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("NewCachedCgroupClient(nil) must panic")
		}
	}()
	_ = NewCachedCgroupClient(nil)
}

func TestCoreCgroupClient_ApplyCPUSet_NilDataRejected(t *testing.T) {
	t.Parallel()

	err := NewCgroupClient().ApplyCPUSet(context.Background(), "kubepods", nil)
	if err == nil {
		t.Fatalf("ApplyCPUSet(nil) must fail")
	}
	if !strings.Contains(err.Error(), "nil data") {
		t.Fatalf("error must mention nil data, got: %v", err)
	}
}

func TestCoreCgroupClient_ApplyCPU_NilDataRejected(t *testing.T) {
	t.Parallel()

	err := NewCgroupClient().ApplyCPU(context.Background(), "kubepods", nil)
	if err == nil {
		t.Fatalf("ApplyCPU(nil) must fail")
	}
	if !strings.Contains(err.Error(), "nil data") {
		t.Fatalf("error must mention nil data, got: %v", err)
	}
}

func TestCoreCgroupClient_AttachPID_InvalidPIDRejected(t *testing.T) {
	t.Parallel()

	c := NewCgroupClient()
	for _, pid := range []int{0, -1, -1000} {
		if err := c.AttachPID(context.Background(), "kubepods", pid); err == nil {
			t.Fatalf("AttachPID(%d) must fail", pid)
		}
	}
}

func TestCoreCgroupClient_Version_ReturnsKnownEnum(t *testing.T) {
	t.Parallel()

	got := NewCgroupClient().Version(context.Background())
	if got != CgroupVersionV1 && got != CgroupVersionV2 {
		t.Fatalf("Version() = %q, want %q or %q", got, CgroupVersionV1, CgroupVersionV2)
	}
}

func TestCoreCgroupClient_StatCPUSet_MissingPathSurfacesError(t *testing.T) {
	t.Parallel()

	_, _, err := NewCgroupClient().StatCPUSet(context.Background(), "/definitely/not/a/real/cgroup/path")
	if err == nil {
		t.Fatalf("StatCPUSet on missing path must error")
	}
}

func TestCoreCgroupClient_StatDir_MissingPathSurfacesError(t *testing.T) {
	t.Parallel()

	_, err := NewCgroupClient().StatDir(context.Background(), "/definitely/not/a/real/cgroup/path")
	if err == nil {
		t.Fatalf("StatDir on missing path must error")
	}
}

func TestCoreCgroupClient_ReadCPUSetPartition_ErrNotSupportedOnMissing(t *testing.T) {
	t.Parallel()

	_, err := NewCgroupClient().ReadCPUSetPartition(context.Background(), "/definitely/not/a/real/cgroup/path")
	if err == nil {
		t.Fatalf("ReadCPUSetPartition on missing path must error")
	}
	if !errors.Is(err, cgcommon.ErrNotSupported) {
		t.Fatalf("ReadCPUSetPartition error = %v, want ErrNotSupported-compatible", err)
	}
}
