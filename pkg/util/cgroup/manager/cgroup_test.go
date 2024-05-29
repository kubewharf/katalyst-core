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
	"testing"

	"bou.ke/monkey"
	"github.com/opencontainers/runc/libcontainer/cgroups"
	"github.com/stretchr/testify/assert"

	"github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	v1 "github.com/kubewharf/katalyst-core/pkg/util/cgroup/manager/v1"
	v2 "github.com/kubewharf/katalyst-core/pkg/util/cgroup/manager/v2"
)

func TestManager(t *testing.T) {
	t.Parallel()

	_ = GetManager()

	testV1Manager(t)
	testV2Manager(t)
}

func testV1Manager(t *testing.T) {
	_ = v1.NewManager()

	testManager(t, "v1")
	testNetCls(t, "v1")
}

func testV2Manager(t *testing.T) {
	_ = v2.NewManager()

	testManager(t, "v2")
	testSwapMax(t)
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
	defer monkey.UnpatchAll()
	monkey.Patch(common.CheckCgroup2UnifiedMode, func() bool { return true })
	monkey.Patch(GetManager, func() Manager { return v2.NewManager() })
	monkey.Patch(cgroups.ReadFile, func(dir, file string) (string, error) {
		f := filepath.Join(dir, file)
		tmp, err := ioutil.ReadFile(f)
		if err != nil {
			return "", err
		}
		return string(tmp), nil
	})
	monkey.Patch(cgroups.WriteFile, func(dir, file, data string) error {
		f := filepath.Join(dir, file)
		return ioutil.WriteFile(f, []byte(data), 0o700)
	})

	rootDir := os.TempDir()
	dir := filepath.Join(rootDir, "tmp")
	err := os.Mkdir(dir, 0o700)
	assert.NoError(t, err)

	tmpDir, err := ioutil.TempDir(dir, "fake-cgroup")
	assert.NoError(t, err)
	defer os.RemoveAll(dir)

	monkey.Patch(common.GetCgroupRootPath, func(s string) string {
		t.Logf("rootDir=%v", rootDir)
		return rootDir
	})

	sawpFile := filepath.Join(tmpDir, "memory.swap.max")
	err = ioutil.WriteFile(sawpFile, []byte{}, 0o700)
	assert.NoError(t, err)

	sawpFile2 := filepath.Join(dir, "memory.swap.max")
	err = ioutil.WriteFile(sawpFile2, []byte{}, 0o700)
	assert.NoError(t, err)

	maxFile := filepath.Join(tmpDir, "memory.max")
	err = ioutil.WriteFile(maxFile, []byte("12800"), 0o700)
	assert.NoError(t, err)

	curFile := filepath.Join(tmpDir, "memory.current")
	err = ioutil.WriteFile(curFile, []byte("12600"), 0o700)
	assert.NoError(t, err)

	err = SetSwapMaxWithAbsolutePathRecursive(tmpDir)
	assert.NoError(t, err)

	s, err := ioutil.ReadFile(sawpFile)
	assert.NoError(t, err)
	assert.Equal(t, fmt.Sprintf("%v", 200), string(s))

	s, err = ioutil.ReadFile(sawpFile2)
	assert.NoError(t, err)
	assert.Equal(t, fmt.Sprintf("%v", math.MaxInt64), string(s))

	err = DisableSwapMaxWithAbsolutePathRecursive(tmpDir)
	assert.NoError(t, err)

	s, err = ioutil.ReadFile(sawpFile)
	assert.NoError(t, err)
	assert.Equal(t, fmt.Sprintf("%v", 0), string(s))
}
