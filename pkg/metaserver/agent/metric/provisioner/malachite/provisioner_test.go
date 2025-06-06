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

package malachite

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/bytedance/mockey"
	"github.com/stretchr/testify/assert"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/global"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	malachitetypes "github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/provisioner/malachite/types"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	utilmetric "github.com/kubewharf/katalyst-core/pkg/util/metric"
)

func Test_noneExistMetricsProvisioner(t *testing.T) {
	t.Parallel()

	store := utilmetric.NewMetricStore()

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	assert.Nil(t, err)

	implement := NewMalachiteMetricsProvisioner(&global.BaseConfiguration{
		ReclaimRelativeRootCgroupPath: "test",
		MalachiteConfiguration: &global.MalachiteConfiguration{
			GeneralRelativeCgroupPaths:  []string{"d1", "d2"},
			OptionalRelativeCgroupPaths: []string{"d3", "d4"},
		},
	}, &metaserver.MetricConfiguration{}, metrics.DummyMetrics{}, &pod.PodFetcherStub{}, store,
		&machine.KatalystMachineInfo{
			CPUTopology: cpuTopology,
		})

	fakeSystemCompute := &malachitetypes.SystemComputeData{
		CPU: []malachitetypes.CPU{
			{
				Name: "CPU1111",
			},
		},
	}
	fakeSystemMemory := &malachitetypes.SystemMemoryData{
		Numa: []malachitetypes.Numa{
			{},
		},
	}
	fakeSystemIO := &malachitetypes.SystemDiskIoData{
		DiskIo: []malachitetypes.DiskIo{
			{
				PrimaryDeviceID:   8,
				SecondaryDeviceID: 16,
				DeviceName:        "sdb",
				DiskType:          "HDD",
				WBTValue:          1234,
			},
			{
				PrimaryDeviceID:   8,
				SecondaryDeviceID: 24,
				DeviceName:        "sdc",
				DiskType:          "SSD",
				WBTValue:          2234,
			},
			{
				PrimaryDeviceID:   8,
				SecondaryDeviceID: 32,
				DeviceName:        "nvme01",
				DiskType:          "NVME",
				WBTValue:          3234,
			},
			{
				PrimaryDeviceID:   8,
				SecondaryDeviceID: 48,
				DeviceName:        "vdf",
				DiskType:          "VIRTIO",
				WBTValue:          75234,
			},
		},
	}
	fakeSystemNet := &malachitetypes.SystemNetworkData{
		TCP: malachitetypes.TCP{},
		NetworkCard: []malachitetypes.NetworkCard{
			{
				Name: "eth0",
			},
		},
	}
	fakeCgroupInfoV1 := &malachitetypes.MalachiteCgroupInfo{
		CgroupType: "V1",
		V1: &malachitetypes.MalachiteCgroupV1Info{
			Memory: &malachitetypes.MemoryCgDataV1{},
			Blkio:  &malachitetypes.BlkIOCgDataV1{},
			NetCls: &malachitetypes.NetClsCgData{},
			CpuSet: &malachitetypes.CPUSetCgDataV1{},
			Cpu:    &malachitetypes.CPUCgDataV1{},
		},
	}
	fakeCgroupInfoV2 := &malachitetypes.MalachiteCgroupInfo{
		CgroupType: "V2",
		V2: &malachitetypes.MalachiteCgroupV2Info{
			Memory: &malachitetypes.MemoryCgDataV2{},
			Blkio:  &malachitetypes.BlkIOCgDataV2{},
			NetCls: &malachitetypes.NetClsCgData{},
			CpuSet: &malachitetypes.CPUSetCgDataV2{},
			Cpu:    &malachitetypes.CPUCgDataV2{},
		},
	}

	implement.(*MalachiteMetricsProvisioner).processSystemComputeData(fakeSystemCompute)
	implement.(*MalachiteMetricsProvisioner).processSystemMemoryData(fakeSystemMemory)
	implement.(*MalachiteMetricsProvisioner).processSystemIOData(fakeSystemIO)
	implement.(*MalachiteMetricsProvisioner).processSystemNumaData(fakeSystemMemory, fakeSystemCompute)
	implement.(*MalachiteMetricsProvisioner).processSystemExtFragData(fakeSystemMemory)
	implement.(*MalachiteMetricsProvisioner).processSystemCPUComputeData(fakeSystemCompute)
	implement.(*MalachiteMetricsProvisioner).processSystemNetData(fakeSystemNet)

	implement.(*MalachiteMetricsProvisioner).processContainerCPUData("pod-not-exist", "container-not-exist", fakeCgroupInfoV1)
	implement.(*MalachiteMetricsProvisioner).processContainerMemoryData("pod-not-exist", "container-not-exist", fakeCgroupInfoV1)
	implement.(*MalachiteMetricsProvisioner).processContainerBlkIOData("pod-not-exist", "container-not-exist", fakeCgroupInfoV1)
	implement.(*MalachiteMetricsProvisioner).processContainerNetData("pod-not-exist", "container-not-exist", fakeCgroupInfoV1)
	implement.(*MalachiteMetricsProvisioner).processContainerPerfData("pod-not-exist", "container-not-exist", fakeCgroupInfoV1)
	implement.(*MalachiteMetricsProvisioner).processContainerPerNumaMemoryData("pod-not-exist", "container-not-exist", fakeCgroupInfoV1)

	implement.(*MalachiteMetricsProvisioner).processContainerCPUData("pod-not-exist", "container-not-exist", fakeCgroupInfoV2)
	implement.(*MalachiteMetricsProvisioner).processContainerCPUData("pod-not-exist", "container-not-exist", nil)
	implement.(*MalachiteMetricsProvisioner).processContainerMemoryData("pod-not-exist", "container-not-exist", fakeCgroupInfoV2)
	implement.(*MalachiteMetricsProvisioner).processContainerMemoryData("pod-not-exist", "container-not-exist", nil)
	implement.(*MalachiteMetricsProvisioner).processContainerBlkIOData("pod-not-exist", "container-not-exist", fakeCgroupInfoV2)
	implement.(*MalachiteMetricsProvisioner).processContainerBlkIOData("pod-not-exist", "container-not-exist", nil)
	implement.(*MalachiteMetricsProvisioner).processContainerNetData("pod-not-exist", "container-not-exist", fakeCgroupInfoV2)
	implement.(*MalachiteMetricsProvisioner).processContainerNetData("pod-not-exist", "container-not-exist", nil)
	implement.(*MalachiteMetricsProvisioner).processContainerPerfData("pod-not-exist", "container-not-exist", fakeCgroupInfoV2)
	implement.(*MalachiteMetricsProvisioner).processContainerPerfData("pod-not-exist", "container-not-exist", nil)
	implement.(*MalachiteMetricsProvisioner).processContainerPerNumaMemoryData("pod-not-exist", "container-not-exist", fakeCgroupInfoV2)
	implement.(*MalachiteMetricsProvisioner).processContainerPerNumaMemoryData("pod-not-exist", "container-not-exist", nil)
	implement.(*MalachiteMetricsProvisioner).processContainerPerNumaMemoryData("pod-not-exist", "container-not-exist",
		&malachitetypes.MalachiteCgroupInfo{CgroupType: "V2"})

	_, err = store.GetNodeMetric("test-not-exist")
	if err == nil {
		t.Errorf("GetNode() error = %v, wantErr not nil", err)
		return
	}

	_, err = store.GetNumaMetric(1, "test-not-exist")
	if err == nil {
		t.Errorf("GetNode() error = %v, wantErr not nil", err)
		return
	}

	_, err = store.GetDeviceMetric("device-not-exist", "test-not-exist")
	if err == nil {
		t.Errorf("GetNode() error = %v, wantErr not nil", err)
		return
	}

	_, err = store.GetCPUMetric(1, "test-not-exist")
	if err == nil {
		t.Errorf("GetNode() error = %v, wantErr not nil", err)
		return
	}

	_, err = store.GetContainerMetric("pod-not-exist", "container-not-exist", "test-not-exist")
	if err == nil {
		t.Errorf("GetNode() error = %v, wantErr not nil", err)
		return
	}

	_, err = store.GetContainerNumaMetric("pod-not-exist", "container-not-exist", -1, "test-not-exist")
	if err == nil {
		t.Errorf("GetContainerNuma() error = %v, wantErr not nil", err)
		return
	}

	defer mockey.UnPatchAll()
	mockey.Mock(general.IsPathExists).To(func(path string) bool {
		if path == "/sys/fs/cgroup/d3" {
			return true
		}
		return false
	}).Build()
	mockey.Mock(common.CheckCgroup2UnifiedMode).Return(true).Build()

	paths := implement.(*MalachiteMetricsProvisioner).getCgroupPaths()
	assert.ElementsMatch(t, paths, []string{
		"d1", "d2", "d3", "/kubepods/burstable", "/kubepods/besteffort",
		"test", "test-0", "test-1", "test-2", "test-3",
	})
}

func Test_getCPI(t *testing.T) {
	t.Parallel()

	store := utilmetric.NewMetricStore()

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	assert.Nil(t, err)

	implement := NewMalachiteMetricsProvisioner(&global.BaseConfiguration{
		ReclaimRelativeRootCgroupPath: "test",
		MalachiteConfiguration: &global.MalachiteConfiguration{
			GeneralRelativeCgroupPaths:  []string{"d1", "d2"},
			OptionalRelativeCgroupPaths: []string{"d3", "d4"},
		},
	}, &metaserver.MetricConfiguration{}, metrics.DummyMetrics{}, &pod.PodFetcherStub{}, store,
		&machine.KatalystMachineInfo{
			CPUTopology: cpuTopology,
		})

	fakeCgroupInfoV1t1 := &malachitetypes.MalachiteCgroupInfo{
		CgroupType: "V1",
		V1: &malachitetypes.MalachiteCgroupV1Info{
			Cpu: &malachitetypes.CPUCgDataV1{Instructions: 1, Cycles: 1, UpdateTime: int64(time.Date(2024, 1, 1, 0, 0, 0, 0, time.Local).Second())},
		},
	}

	fakeCgroupInfoV1t2 := &malachitetypes.MalachiteCgroupInfo{
		CgroupType: "V1",
		V1: &malachitetypes.MalachiteCgroupV1Info{
			Cpu: &malachitetypes.CPUCgDataV1{Instructions: 2, Cycles: 2, UpdateTime: int64(time.Date(2024, 1, 1, 0, 0, 1, 0, time.Local).Second())},
		},
	}
	fakeCgroupInfoV1t3 := &malachitetypes.MalachiteCgroupInfo{
		CgroupType: "V1",
		V1: &malachitetypes.MalachiteCgroupV1Info{
			Cpu: &malachitetypes.CPUCgDataV1{Instructions: 3, Cycles: 3, UpdateTime: int64(time.Date(2024, 1, 1, 0, 0, 2, 0, time.Local).Second())},
		},
	}

	implement.(*MalachiteMetricsProvisioner).processContainerCPUData("pod1", "container1", fakeCgroupInfoV1t1)
	implement.(*MalachiteMetricsProvisioner).processContainerCPUData("pod1", "container1", fakeCgroupInfoV1t2)
	implement.(*MalachiteMetricsProvisioner).processContainerCPUData("pod1", "container1", fakeCgroupInfoV1t3)

	data, err := store.GetContainerMetric("pod1", "container1", consts.MetricCPUCPIContainer)
	assert.NoError(t, err)
	assert.Equal(t, float64(1), data.Value)

	fakeCgroupInfoV2t1 := &malachitetypes.MalachiteCgroupInfo{
		CgroupType: "V2",
		V2: &malachitetypes.MalachiteCgroupV2Info{
			Cpu: &malachitetypes.CPUCgDataV2{Instructions: 1, Cycles: 1, UpdateTime: int64(time.Date(2024, 1, 1, 0, 0, 0, 0, time.Local).Second())},
		},
	}
	fakeCgroupInfoV2t2 := &malachitetypes.MalachiteCgroupInfo{
		CgroupType: "V2",
		V2: &malachitetypes.MalachiteCgroupV2Info{
			Cpu: &malachitetypes.CPUCgDataV2{Instructions: 2, Cycles: 2, UpdateTime: int64(time.Date(2024, 1, 1, 0, 0, 1, 0, time.Local).Second())},
		},
	}
	fakeCgroupInfoV2t3 := &malachitetypes.MalachiteCgroupInfo{
		CgroupType: "V2",
		V2: &malachitetypes.MalachiteCgroupV2Info{
			Cpu: &malachitetypes.CPUCgDataV2{Instructions: 3, Cycles: 3, UpdateTime: int64(time.Date(2024, 1, 1, 0, 0, 2, 0, time.Local).Second())},
		},
	}

	implement.(*MalachiteMetricsProvisioner).processContainerCPUData("pod2", "container1", fakeCgroupInfoV2t1)
	implement.(*MalachiteMetricsProvisioner).processContainerCPUData("pod2", "container1", fakeCgroupInfoV2t2)
	implement.(*MalachiteMetricsProvisioner).processContainerCPUData("pod2", "container1", fakeCgroupInfoV2t3)

	data, err = store.GetContainerMetric("pod2", "container1", consts.MetricCPUCPIContainer)
	assert.NoError(t, err)
	assert.Equal(t, float64(1), data.Value)
}

func Test_setContainerMbmTotalMetric(t *testing.T) {
	t.Parallel()

	type args struct {
		podUID        string
		containerName string
		mbmData       malachitetypes.MbmbandData
		updateTime    *time.Time
		cpuCodeName   string
	}
	now := time.Now()
	mb1 := uint64(100)
	mb2 := uint64(200)
	mb3 := uint64(0)
	mbLocal1 := uint64(50)
	mbLocal2 := uint64(70)

	testCases := []struct {
		name      string
		args      args
		wantValue float64
	}{
		{
			name: "sum two values and zero",
			args: args{
				podUID:        "test pod 1",
				containerName: "test container 1",
				mbmData: malachitetypes.MbmbandData{
					Mbm: []malachitetypes.MBMItem{
						{MBMTotalBytes: &mb1},
						{MBMTotalBytes: &mb2},
						{MBMTotalBytes: nil},
						{MBMTotalBytes: &mb3},
					},
				},
				updateTime:  &now,
				cpuCodeName: "",
			},
			wantValue: 300,
		},
		{
			name: "all nil MBMTotalBytes",
			args: args{
				podUID:        "test pod 2",
				containerName: "test container 2",
				mbmData: malachitetypes.MbmbandData{
					Mbm: []malachitetypes.MBMItem{
						{MBMTotalBytes: nil},
						{MBMTotalBytes: nil},
					},
				},
				updateTime:  &now,
				cpuCodeName: "",
			},
			wantValue: 0,
		},
		{
			name: "empty Mbm slice",
			args: args{
				podUID:        "test pod 3",
				containerName: "test container 3",
				mbmData: malachitetypes.MbmbandData{
					Mbm: []malachitetypes.MBMItem{},
				},
				updateTime:  &now,
				cpuCodeName: "",
			},
			wantValue: 0,
		},
		{
			name: "AMD Genoa arch sums MBMTotalBytes and MBMLocalBytes",
			args: args{
				podUID:        "test pod 4",
				containerName: "test container 4",
				mbmData: malachitetypes.MbmbandData{
					Mbm: []malachitetypes.MBMItem{
						{MBMTotalBytes: &mb1, MBMLocalBytes: &mbLocal1},
						{MBMTotalBytes: &mb2, MBMLocalBytes: &mbLocal2},
					},
				},
				updateTime:  &now,
				cpuCodeName: consts.AMDGenoaArch,
			},
			wantValue: 100 + 50 + 200 + 70, // sum of all MBMTotalBytes and MBMLocalBytes
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := utilmetric.NewMetricStore()
			// Set cpu code name if needed
			if tc.args.cpuCodeName != "" {
				store.SetByStringIndex(consts.MetricCPUCodeName, tc.args.cpuCodeName)
			}
			mmp := &MalachiteMetricsProvisioner{
				metricStore: store,
			}
			mmp.setContainerMbmTotalMetric(tc.args.podUID, tc.args.containerName, tc.args.mbmData, tc.args.updateTime)
			data, err := store.GetContainerMetric(tc.args.podUID, tc.args.containerName, consts.MetricMbmTotalContainer)
			assert.NoError(t, err)
			assert.Equal(t, tc.wantValue, data.Value)
			assert.Equal(t, *tc.args.updateTime, *data.Time)
		})
	}
}

func Test_cpuInList(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		cpu      int
		cpuList  string
		expected bool
	}{
		{
			name:     "single cpu match",
			cpu:      2,
			cpuList:  "2",
			expected: true,
		},
		{
			name:     "single cpu no match",
			cpu:      3,
			cpuList:  "2",
			expected: false,
		},
		{
			name:     "range match",
			cpu:      4,
			cpuList:  "2-5",
			expected: true,
		},
		{
			name:     "range no match",
			cpu:      6,
			cpuList:  "2-5",
			expected: false,
		},
		{
			name:     "empty cpuList",
			cpu:      0,
			cpuList:  "",
			expected: false,
		},
		{
			name:     "malformed range",
			cpu:      2,
			cpuList:  "1-b",
			expected: false,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result := cpuInList(tc.cpu, tc.cpuList)
			assert.Equal(t, tc.expected, result)
		})
	}
}

// Helper to create a fake sysfs structure for testing
func setupFakeSysfs(t *testing.T, l3ID, cpuID, numaID int) (cpuDir, nodeDir string, cleanup func()) {
	t.Helper()
	tmpDir := t.TempDir()

	cpuDir = filepath.Join(tmpDir, "cpu")
	nodeDir = filepath.Join(tmpDir, "node")

	// Fake /sys/devices/system/cpu/cpuX/cache/index3/id
	cpuSubDir := filepath.Join(cpuDir, "cpu"+strconv.Itoa(cpuID), "cache", "index3")
	assert.NoError(t, os.MkdirAll(cpuSubDir, 0o755))
	assert.NoError(t, os.WriteFile(filepath.Join(cpuSubDir, "id"), []byte(strconv.Itoa(l3ID)), 0o644))

	// Fake /sys/devices/system/node/nodeX/cpulist
	nodeSubDir := filepath.Join(nodeDir, "node"+strconv.Itoa(numaID))
	assert.NoError(t, os.MkdirAll(nodeSubDir, 0o755))
	assert.NoError(t, os.WriteFile(filepath.Join(nodeSubDir, "cpulist"), []byte(strconv.Itoa(cpuID)), 0o644))

	return cpuDir, nodeDir, func() {}
}

func Test_getNumaIDByL3CacheID(t *testing.T) {
	t.Parallel()

	type sysfsSetup struct {
		l3ID   int
		cpuID  int
		numaID int
	}
	testCases := []struct {
		name       string
		setup      *sysfsSetup
		queryL3ID  int
		wantNumaID int
		wantErr    bool
	}{
		{
			name: "found numa id",
			setup: &sysfsSetup{
				l3ID:   10,
				cpuID:  2,
				numaID: 1,
			},
			queryL3ID:  10,
			wantNumaID: 1,
			wantErr:    false,
		},
		{
			name: "not found",
			setup: &sysfsSetup{
				l3ID:   10,
				cpuID:  2,
				numaID: 1,
			},
			queryL3ID:  999,
			wantNumaID: -1,
			wantErr:    true,
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var cpuDir, nodeDir string
			var cleanup func()
			if tc.setup != nil {
				cpuDir, nodeDir, cleanup = setupFakeSysfs(t, tc.setup.l3ID, tc.setup.cpuID, tc.setup.numaID)
				defer cleanup()
			}
			numaID, err := getNumaIDByL3CacheID(tc.queryL3ID, cpuDir, nodeDir)
			if tc.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
			assert.Equal(t, tc.wantNumaID, numaID)
		})
	}
}
