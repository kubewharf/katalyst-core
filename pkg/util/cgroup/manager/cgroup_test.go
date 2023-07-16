//go:build linux
// +build linux

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

package manager

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	v1 "github.com/kubewharf/katalyst-core/pkg/util/cgroup/manager/v1"
	v2 "github.com/kubewharf/katalyst-core/pkg/util/cgroup/manager/v2"
)

func TestManager(t *testing.T) {
	t.Parallel()

	_ = GetManager()
}

func TestV1Manager(t *testing.T) {
	t.Parallel()

	_ = v1.NewManager()

	testManager(t, "v1")
	testNetCls(t, "v1")
}

func TestV2Manager(t *testing.T) {
	t.Parallel()

	_ = v2.NewManager()

	testManager(t, "v2")
}

func testManager(t *testing.T, version string) {
	t.Logf("test with version %v", version)

	var err error

	err = ApplyMemoryWithRelativePath("/test", &common.MemoryData{})
	assert.NoError(t, err)
	err = ApplyCPUWithRelativePath("/test", &common.CPUData{})
	assert.NoError(t, err)
	err = ApplyCPUSetWithRelativePath("/test", &common.CPUSetData{})
	assert.NoError(t, err)
	err = ApplyCPUSetWithAbsolutePath("/test", &common.CPUSetData{})
	assert.NoError(t, err)
	err = ApplyCPUSetForContainer("fake-pod", "fake-container", &common.CPUSetData{})
	assert.NotNil(t, err)
	err = ApplyUnifiedDataForContainer("fake-pod", "fake-container", common.CgroupSubsysMemory, "memory.high", "max")
	assert.NotNil(t, err)

	_, _ = GetMemoryWithRelativePath("/")
	_, _ = GetMemoryWithAbsolutePath("/")
	_, _ = GetCPUWithRelativePath("/")
	_, _ = GetMetricsWithRelativePath("/", map[string]struct{}{"cpu": {}})
	_, _ = GetPidsWithRelativePath("/")
	_, _ = GetPidsWithAbsolutePath("/")
	_, _ = GetTasksWithRelativePath("/", "cpu")
	_, _ = GetTasksWithAbsolutePath("/")

	_ = DropCacheWithTimeoutForContainer(context.Background(), "fake-pod", "fake-container", 1)
	_ = DropCacheWithTimeoutWithRelativePath(1, "/test")
}

func testNetCls(t *testing.T, version string) {
	t.Logf("test net_cls with version %v", version)
	var err error

	err = ApplyNetClsWithRelativePath("/test", &common.NetClsData{})
	assert.NoError(t, err)

	err = ApplyNetClsForContainer("fake-pod", "fake-container", &common.NetClsData{})
	assert.Error(t, err)
}
