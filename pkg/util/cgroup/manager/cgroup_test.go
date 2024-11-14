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
	"fmt"
	"io/ioutil"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"bou.ke/monkey"
	"github.com/opencontainers/runc/libcontainer/cgroups"
	"github.com/stretchr/testify/assert"

	"github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	v1 "github.com/kubewharf/katalyst-core/pkg/util/cgroup/manager/v1"
	v2 "github.com/kubewharf/katalyst-core/pkg/util/cgroup/manager/v2"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

var mu = sync.Mutex{}

func TestV1Manager(t *testing.T) {
	cgroups.TestMode = true
	t.Parallel()
	mu.Lock()
	defer mu.Unlock()

	defer monkey.UnpatchAll()
	monkey.Patch(common.CheckCgroup2UnifiedMode, func() bool { return false })
	monkey.Patch(GetManager, func() Manager { return v1.NewManager() })

	testManager(t, "v1")
	testNetCls(t, "v1")
	testMemPressureV1(t)
}

func TestV2Manager(t *testing.T) {
	cgroups.TestMode = true
	t.Parallel()
	mu.Lock()
	defer mu.Unlock()

	defer monkey.UnpatchAll()
	monkey.Patch(common.CheckCgroup2UnifiedMode, func() bool { return true })
	monkey.Patch(GetManager, func() Manager { return v2.NewManager() })

	testManager(t, "v2")
	testSwapMax(t)
	testMemPressure(t)
	testMemoryOffloadingWithAbsolutePath(t)
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
	_, _ = GetMemoryPressureWithAbsolutePath("/", common.SOME)
	_, _ = GetMemoryPressureWithAbsolutePath("/", common.FULL)
	_, _ = GetCPUWithRelativePath("/")
	_, _ = GetMetricsWithRelativePath("/", map[string]struct{}{"cpu": {}})
	_, _ = GetPidsWithRelativePath("/")
	_, _ = GetPidsWithAbsolutePath("/")
	_, _ = GetTasksWithRelativePath("/", "cpu")
	_, _ = GetTasksWithAbsolutePath("/")

	_ = DropCacheWithTimeoutForContainer(context.Background(), "fake-pod", "fake-container", 1, 0)
	_ = DropCacheWithTimeoutAndAbsCGPath(1, "/test", 0)
}

func testNetCls(t *testing.T, version string) {
	t.Logf("test net_cls with version %v", version)
	var err error

	err = ApplyNetClsWithRelativePath("/test", &common.NetClsData{})
	assert.NoError(t, err)

	err = ApplyNetClsForContainer("fake-pod", "fake-container", &common.NetClsData{})
	assert.Error(t, err)
}

func testSwapMax(t *testing.T) {
	rootDir := os.TempDir()
	dir := filepath.Join(rootDir, "tmp")
	err := os.Mkdir(dir, 0o700)
	assert.NoError(t, err)
	defer os.RemoveAll(dir)

	tmpDir, err := ioutil.TempDir(dir, "fake-cgroup")
	assert.NoError(t, err)

	monkey.Patch(common.GetCgroupRootPath, func(s string) string {
		t.Logf("rootDir=%v", rootDir)
		return rootDir
	})

	err = cgroups.WriteFile(tmpDir, "memory.swap.max", "")
	assert.NoError(t, err)

	err = cgroups.WriteFile(dir, "memory.swap.max", "")
	assert.NoError(t, err)

	err = cgroups.WriteFile(tmpDir, "memory.max", "12800")
	assert.NoError(t, err)

	err = cgroups.WriteFile(tmpDir, "memory.current", "12600")
	assert.NoError(t, err)

	err = SetSwapMaxWithAbsolutePathRecursive(tmpDir)
	assert.NoError(t, err)

	s, err := cgroups.ReadFile(tmpDir, "memory.swap.max")
	assert.NoError(t, err)
	assert.Equal(t, fmt.Sprintf("%v", 200), s)

	s, err = cgroups.ReadFile(dir, "memory.swap.max")
	assert.NoError(t, err)
	assert.Equal(t, fmt.Sprintf("%v", math.MaxInt64), s)

	err = DisableSwapMaxWithAbsolutePathRecursive(tmpDir)
	assert.NoError(t, err)

	s, err = cgroups.ReadFile(tmpDir, "memory.swap.max")
	assert.NoError(t, err)
	assert.Equal(t, fmt.Sprintf("%v", 0), s)
}

func testMemPressure(t *testing.T) {
	rootDir := os.TempDir()
	dir := filepath.Join(rootDir, "tmp")
	err := os.Mkdir(dir, 0o700)
	assert.NoError(t, err)
	defer os.RemoveAll(dir)

	tmpDir, err := ioutil.TempDir(dir, "fake-cgroup")
	assert.NoError(t, err)

	monkey.Patch(common.GetCgroupRootPath, func(s string) string {
		t.Logf("rootDir=%v", rootDir)
		return rootDir
	})

	content := "some avg10=1.00 avg60=2.00 avg300=3.00 total=67455876\nfull avg10=4.00 avg60=5.00 avg300=6.00 total=65331646\n"
	statFile := filepath.Join(tmpDir, "memory.pressure")
	err = ioutil.WriteFile(statFile, []byte(content), 0o700)
	assert.NoError(t, err)

	some, err := GetManager().GetMemoryPressure(tmpDir, common.SOME)
	assert.NoError(t, err)
	assert.Equal(t, "1", fmt.Sprint(some.Avg10))
	assert.Equal(t, "2", fmt.Sprint(some.Avg60))
	assert.Equal(t, "3", fmt.Sprint(some.Avg300))

	full, err := GetManager().GetMemoryPressure(tmpDir, common.FULL)
	assert.NoError(t, err)
	assert.Equal(t, "4", fmt.Sprint(full.Avg10))
	assert.Equal(t, "5", fmt.Sprint(full.Avg60))
	assert.Equal(t, "6", fmt.Sprint(full.Avg300))

	_, err = GetManager().GetMemoryPressure(tmpDir, 123)
	assert.Error(t, err)
}

func testMemPressureV1(t *testing.T) {
	some, err := GetManager().GetMemoryPressure("test", common.SOME)
	assert.NoError(t, err)
	assert.Equal(t, "0", fmt.Sprint(some.Avg10))
	assert.Equal(t, "0", fmt.Sprint(some.Avg60))
	assert.Equal(t, "0", fmt.Sprint(some.Avg300))
}

func testMemoryOffloadingWithAbsolutePath(t *testing.T) {
	rootDir := os.TempDir()
	dir := filepath.Join(rootDir, "tmp")
	err := os.Mkdir(dir, 0o700)
	assert.NoError(t, err)
	defer os.RemoveAll(dir)

	tmpDir, err := ioutil.TempDir(dir, "fake-cgroup")
	assert.NoError(t, err)

	monkey.Patch(common.GetCgroupRootPath, func(s string) string {
		t.Logf("rootDir=%v", rootDir)
		return rootDir
	})

	err = MemoryOffloadingWithAbsolutePath(context.TODO(), tmpDir, 100, machine.NewCPUSet(0))
	assert.NoError(t, err)

	reclaimFile := filepath.Join(tmpDir, "memory.reclaim")

	s, err := ioutil.ReadFile(reclaimFile)
	assert.NoError(t, err)
	assert.Equal(t, fmt.Sprintf("%v\n", 100), string(s))
}

func TestGetEffectiveCPUSetWithAbsolutePathV1(t *testing.T) {
	cgroups.TestMode = true
	t.Parallel()
	mu.Lock()
	defer mu.Unlock()

	defer monkey.UnpatchAll()
	monkey.Patch(common.CheckCgroup2UnifiedMode, func() bool { return false })
	monkey.Patch(GetManager, func() Manager { return v1.NewManager() })

	rootDir := os.TempDir()
	dir := filepath.Join(rootDir, "tmp")
	err := os.Mkdir(dir, 0o700)
	assert.NoError(t, err)
	defer os.RemoveAll(dir)

	tmpDir, err := ioutil.TempDir(dir, "fake-cgroup")
	assert.NoError(t, err)

	monkey.Patch(IsCgroupPath, func(path string) bool {
		return strings.Contains(path, tmpDir)
	})

	monkey.Patch(common.GetCgroupRootPath, func(s string) string {
		t.Logf("rootDir=%v", rootDir)
		return rootDir
	})

	// tmpDir is root cgroup
	err = cgroups.WriteFile(tmpDir, "cpuset.cpus", "0-1")
	assert.NoError(t, err)

	err = cgroups.WriteFile(tmpDir, "cpuset.effective_cpus", "0-1")
	assert.NoError(t, err)

	err = cgroups.WriteFile(tmpDir, "cpuset.mems", "0-1")
	assert.NoError(t, err)

	err = cgroups.WriteFile(tmpDir, "cpuset.effective_mems", "0-1")
	assert.NoError(t, err)

	cpus, mems, err := GetEffectiveCPUSetWithAbsolutePath(tmpDir)
	assert.NoError(t, err)
	assert.Equal(t, "0-1", cpus.String())
	assert.Equal(t, "0-1", mems.String())

	// cg1 is sub cgroup
	cg1 := filepath.Join(tmpDir, "cg1")
	err = os.Mkdir(cg1, 0o700)
	assert.NoError(t, err)

	err = cgroups.WriteFile(cg1, "cpuset.cpus", "")
	assert.NoError(t, err)

	err = cgroups.WriteFile(cg1, "cpuset.effective_cpus", "0")
	assert.NoError(t, err)

	err = cgroups.WriteFile(cg1, "cpuset.mems", "")
	assert.NoError(t, err)

	err = cgroups.WriteFile(cg1, "cpuset.effective_mems", "")
	assert.NoError(t, err)

	cpus, mems, err = GetEffectiveCPUSetWithAbsolutePath(cg1)
	assert.NoError(t, err)
	assert.Equal(t, "0", cpus.String())
	assert.Equal(t, "0-1", mems.String())

	// cg2 is sub cgroup
	cg2 := filepath.Join(tmpDir, "cg2")
	err = os.Mkdir(cg2, 0o700)
	assert.NoError(t, err)

	err = cgroups.WriteFile(cg2, "cpuset.cpus", "")
	assert.NoError(t, err)

	err = cgroups.WriteFile(cg2, "cpuset.effective_cpus", "")
	assert.NoError(t, err)

	err = cgroups.WriteFile(cg2, "cpuset.mems", "")
	assert.NoError(t, err)

	err = cgroups.WriteFile(cg2, "cpuset.effective_mems", "0")
	assert.NoError(t, err)

	cpus, mems, err = GetEffectiveCPUSetWithAbsolutePath(cg2)
	assert.NoError(t, err)
	assert.Equal(t, "0-1", cpus.String())
	assert.Equal(t, "0", mems.String())

	// not existed dir
	_, _, err = GetEffectiveCPUSetWithAbsolutePath("xxx")
	assert.Error(t, err)
}

func TestGetEffectiveCPUSetWithAbsolutePathV2(t *testing.T) {
	cgroups.TestMode = true
	t.Parallel()
	mu.Lock()
	defer mu.Unlock()

	defer monkey.UnpatchAll()
	monkey.Patch(common.CheckCgroup2UnifiedMode, func() bool { return true })
	monkey.Patch(GetManager, func() Manager { return v2.NewManager() })

	rootDir := os.TempDir()
	dir := filepath.Join(rootDir, "tmp")
	err := os.Mkdir(dir, 0o700)
	assert.NoError(t, err)
	defer os.RemoveAll(dir)

	tmpDir, err := ioutil.TempDir(dir, "fake-cgroup")
	assert.NoError(t, err)

	monkey.Patch(IsCgroupPath, func(path string) bool {
		return strings.Contains(path, tmpDir)
	})

	monkey.Patch(common.GetCgroupRootPath, func(s string) string {
		t.Logf("rootDir=%v", rootDir)
		return rootDir
	})

	// tmpDir is root cgroup
	err = cgroups.WriteFile(tmpDir, "cpuset.cpus", "0-1")
	assert.NoError(t, err)

	err = cgroups.WriteFile(tmpDir, "cpuset.cpus.effective", "0-1")
	assert.NoError(t, err)

	err = cgroups.WriteFile(tmpDir, "cpuset.mems", "0-1")
	assert.NoError(t, err)

	err = cgroups.WriteFile(tmpDir, "cpuset.mems.effective", "0-1")
	assert.NoError(t, err)

	cpus, mems, err := GetEffectiveCPUSetWithAbsolutePath(tmpDir)
	assert.NoError(t, err)
	assert.Equal(t, "0-1", cpus.String())
	assert.Equal(t, "0-1", mems.String())

	// cg1 is sub cgroup
	cg1 := filepath.Join(tmpDir, "cg1")
	err = os.Mkdir(cg1, 0o700)
	assert.NoError(t, err)

	err = cgroups.WriteFile(cg1, "cpuset.cpus", "")
	assert.NoError(t, err)

	err = cgroups.WriteFile(cg1, "cpuset.cpus.effective", "0")
	assert.NoError(t, err)

	err = cgroups.WriteFile(cg1, "cpuset.mems", "")
	assert.NoError(t, err)

	err = cgroups.WriteFile(cg1, "cpuset.mems.effective", "")
	assert.NoError(t, err)

	cpus, mems, err = GetEffectiveCPUSetWithAbsolutePath(cg1)
	assert.NoError(t, err)
	assert.Equal(t, "0", cpus.String())
	assert.Equal(t, "0-1", mems.String())

	// cg2 is sub cgroup
	cg2 := filepath.Join(tmpDir, "cg2")
	err = os.Mkdir(cg2, 0o700)
	assert.NoError(t, err)

	err = cgroups.WriteFile(cg2, "cpuset.cpus", "")
	assert.NoError(t, err)

	err = cgroups.WriteFile(cg2, "cpuset.cpus.effective", "")
	assert.NoError(t, err)

	err = cgroups.WriteFile(cg2, "cpuset.mems", "")
	assert.NoError(t, err)

	err = cgroups.WriteFile(cg2, "cpuset.mems.effective", "0")
	assert.NoError(t, err)

	cpus, mems, err = GetEffectiveCPUSetWithAbsolutePath(cg2)
	assert.NoError(t, err)
	assert.Equal(t, "0-1", cpus.String())
	assert.Equal(t, "0", mems.String())

	// not existed dir
	_, _, err = GetEffectiveCPUSetWithAbsolutePath("xxx")
	assert.Error(t, err)

	// cg3's cpuset controller is disabled
	cg3 := filepath.Join(tmpDir, "cg3")
	err = os.Mkdir(cg3, 0o700)
	assert.NoError(t, err)
	cpus, mems, err = GetEffectiveCPUSetWithAbsolutePath(cg3)
	assert.NoError(t, err)
	assert.Equal(t, "0-1", cpus.String())
	assert.Equal(t, "0-1", mems.String())
}

func TestIsCgroup(t *testing.T) {
	t.Parallel()
	mu.Lock()
	defer mu.Unlock()

	mounts, err := cgroups.GetCgroupMounts(true)
	assert.NoError(t, err)
	for _, mount := range mounts {
		assert.True(t, IsCgroupPath(mount.Mountpoint))
	}
}
