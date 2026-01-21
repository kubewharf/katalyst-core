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
	fakeSystemIO := &malachitetypes.SystemIoData{
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
		ZramStat: []malachitetypes.ZramStat{
			{
				Name:          "zram0",
				ComprDataSize: 1073741824,
				OrigDataSize:  2147483648,
				MemUsedTotal:  1077936128,
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
	now := time.Unix(1749596247, 0)
	mb1 := uint64(100)
	mb2 := uint64(200)
	mb3 := uint64(4001 * consts.BytesPerGB)
	mbLocal1 := uint64(50)
	mbLocal2 := uint64(100)

	testCases := []struct {
		name            string
		args            args
		prevResctrlData *malachitetypes.MbmbandData
		prevMetric      *utilmetric.MetricData
		wantData        utilmetric.MetricData
		wantLocalData   *utilmetric.MetricData // optional: expected local MBM metric
		expectSet       bool
	}{
		{
			name: "no previous resctrl data, should not set metric",
			args: args{
				podUID:        "pod1",
				containerName: "container1",
				mbmData: malachitetypes.MbmbandData{
					Mbm: []malachitetypes.MBMItem{
						{ID: 0, MBMTotalBytes: mb1, MBMLocalBytes: mbLocal1},
					},
				},
				updateTime:  &now,
				cpuCodeName: "",
			},
			prevResctrlData: nil,
			prevMetric:      nil,
			wantData:        utilmetric.MetricData{Value: 0, Time: &now},
			expectSet:       false,
		},
		{
			name: "has previous metric and resctrl data, valid time interval",
			args: args{
				podUID:        "pod3",
				containerName: "container3",
				mbmData: malachitetypes.MbmbandData{
					Mbm: []malachitetypes.MBMItem{
						{ID: 0, MBMTotalBytes: mb2},
					},
				},
				updateTime:  &now,
				cpuCodeName: "",
			},
			prevResctrlData: &malachitetypes.MbmbandData{
				Mbm: []malachitetypes.MBMItem{
					{ID: 0, MBMTotalBytes: mb1},
				},
			},
			prevMetric: &utilmetric.MetricData{
				Value: 100,
				Time:  ptrTime(now.Add(-10 * time.Second)),
			},
			wantData:  utilmetric.MetricData{Value: float64(mb2-mb1) / 10, Time: &now},
			expectSet: true,
		},
		{
			name: "has previous metric and resctrl data, time interval <= 0",
			args: args{
				podUID:        "pod4",
				containerName: "container4",
				mbmData: malachitetypes.MbmbandData{
					Mbm: []malachitetypes.MBMItem{
						{ID: 0, MBMTotalBytes: mb2},
					},
				},
				updateTime:  &now,
				cpuCodeName: "",
			},
			prevResctrlData: &malachitetypes.MbmbandData{
				Mbm: []malachitetypes.MBMItem{
					{ID: 0, MBMTotalBytes: mb1},
				},
			},
			prevMetric: &utilmetric.MetricData{
				Value: 100,
				Time:  ptrTime(now.Add(100 * time.Second)), // future time
			},
			wantData:  utilmetric.MetricData{Value: 100, Time: ptrTime(now.Add(100 * time.Second))},
			expectSet: true,
		},
		{
			name: "handles MBM overflow, clamps to MaxMBMStep",
			args: args{
				podUID:        "pod5",
				containerName: "container5",
				mbmData: malachitetypes.MbmbandData{
					Mbm: []malachitetypes.MBMItem{
						{ID: 0, MBMTotalBytes: mb3},
					},
				},
				updateTime:  &now,
				cpuCodeName: "",
			},
			prevResctrlData: &malachitetypes.MbmbandData{
				Mbm: []malachitetypes.MBMItem{
					{ID: 0, MBMTotalBytes: mb1},
				},
			},
			prevMetric: &utilmetric.MetricData{
				Value: float64(mb1),
				Time:  ptrTime(now.Add(-10 * time.Second)),
			},
			wantData:  utilmetric.MetricData{Value: 0, Time: &now},
			expectSet: true,
		},
		{name: "handles Zen4, data adjustments for local MBM",
			args: args{
				podUID:        "pod6",
				containerName: "container6",
				mbmData: malachitetypes.MbmbandData{
					Mbm: []malachitetypes.MBMItem{
						{ID: 0, MBMLocalBytes: mbLocal2, MBMTotalBytes: mb2},
					},
				},
				updateTime:  &now,
				cpuCodeName: consts.AMDGenoaArch,
			},
			prevResctrlData: &malachitetypes.MbmbandData{
				Mbm: []malachitetypes.MBMItem{
					{ID: 0, MBMLocalBytes: mbLocal1, MBMTotalBytes: mb1},
				},
			},
			prevMetric: &utilmetric.MetricData{
				Value: float64(mb1),
				Time:  ptrTime(now.Add(-10 * time.Second)),
			},
			wantData: utilmetric.MetricData{Value: float64(mb2-mb1) / 10, Time: &now},
			// Local MBM with Genoa adjustment: localRate + localRate*2/3 = localRate*5/3
			wantLocalData: &utilmetric.MetricData{Value: float64(mbLocal2-mbLocal1) / 10 * 5 / 3, Time: &now},
			expectSet:     true,
		},
		{name: "no data adjustments for local MBM",
			args: args{
				podUID:        "pod7",
				containerName: "container7",
				mbmData: malachitetypes.MbmbandData{
					Mbm: []malachitetypes.MBMItem{
						{ID: 0, MBMLocalBytes: mbLocal2, MBMTotalBytes: mb2},
					},
				},
				updateTime:  &now,
				cpuCodeName: "",
			},
			prevResctrlData: &malachitetypes.MbmbandData{
				Mbm: []malachitetypes.MBMItem{
					{ID: 0, MBMLocalBytes: mbLocal1, MBMTotalBytes: mb1},
				},
			},
			prevMetric: &utilmetric.MetricData{
				Value: float64(mb1),
				Time:  ptrTime(now.Add(-10 * time.Second)),
			},
			wantData:      utilmetric.MetricData{Value: float64(mb2-mb1) / 10, Time: &now},
			wantLocalData: &utilmetric.MetricData{Value: float64(mbLocal2-mbLocal1) / 10, Time: &now},
			expectSet:     true,
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
			// Set previous metric if needed
			if tc.prevMetric != nil {
				store.SetContainerMetric(tc.args.podUID, tc.args.containerName, consts.MetricMbmTotalPsContainer, *tc.prevMetric)
			}
			// Set previous resctrl data if needed
			if tc.prevResctrlData != nil {
				store.SetByStringIndex(consts.MetricResctrlDataContainer, *tc.prevResctrlData)
			}
			mmp := &MalachiteMetricsProvisioner{
				metricStore: store,
			}
			mmp.setContainerMbmTotalMetric(tc.args.podUID, tc.args.containerName, tc.args.mbmData, tc.args.updateTime)
			// Check per second MBM
			dataPerSec, errPerSec := store.GetContainerMetric(tc.args.podUID, tc.args.containerName, consts.MetricMbmTotalPsContainer)
			if tc.expectSet {
				assert.NoError(t, errPerSec)
				assert.InDelta(t, tc.wantData.Value, dataPerSec.Value, 1e-6)
				assert.Equal(t, *tc.wantData.Time, *dataPerSec.Time)
			} else {
				assert.Error(t, errPerSec)
			}
			// Check local MBM if expected
			if tc.wantLocalData != nil {
				localDataPerSec, errLocalPerSec := store.GetContainerMetric(tc.args.podUID, tc.args.containerName, consts.MetricMbmlocalPsContainer)
				assert.NoError(t, errLocalPerSec)
				assert.InDelta(t, tc.wantLocalData.Value, localDataPerSec.Value, 1e-6)
				assert.Equal(t, *tc.wantLocalData.Time, *localDataPerSec.Time)
			}
		})
	}
}

func ptrTime(t time.Time) *time.Time {
	return &t
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

func Test_findOldL3Cache(t *testing.T) {
	t.Parallel()
	type args struct {
		oldL3Mon *malachitetypes.L3Monitor
		id       int
	}
	l3Mon := &malachitetypes.L3Monitor{
		L3Mon: []malachitetypes.L3Mon{
			{ID: 1}, {ID: 2}, {ID: 3},
		},
	}
	tests := []struct {
		name string
		args args
		want *malachitetypes.L3Mon
	}{
		{"found", args{l3Mon, 2}, &l3Mon.L3Mon[1]},
		{"not found", args{l3Mon, 99}, nil},
		{"nil input", args{nil, 1}, nil},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := findOldL3Cache(tc.args.oldL3Mon, tc.args.id)
			assert.Equal(t, tc.want, got)
		})
	}
}

func Test_calcBytesPerSec(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		current  uint64
		previous uint64
		interval uint64
		expected uint64
	}{
		{"normal", 200, 100, 10, 10},
		{"zero interval", 200, 100, 0, 0},
		{"zero previous", 200, 0, 10, 0},
		{"current < previous", 50, 100, 10, 0},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result := calcBytesPerSec(tc.current, tc.previous, tc.interval)
			assert.Equal(t, tc.expected, result)
		})
	}
}

func Test_clampMBMDelta(t *testing.T) {
	t.Parallel()
	const maxDiff = 100
	const maxStep = 50
	tests := []struct {
		name     string
		current  uint64
		previous uint64
		oldValue uint64
		expected uint64
	}{
		{"no clamp", 150, 100, 100, 150},
		{"clamp", 250, 100, 100, 150},
		{"current < previous", 90, 100, 100, 90},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result := clampMBMDelta(tc.current, tc.previous, tc.oldValue, maxDiff, maxStep)
			assert.Equal(t, tc.expected, result)
		})
	}
}

func Test_getNumaAndMaxBandwidth(t *testing.T) {
	t.Parallel()
	ccdCountMap := map[string]int{
		consts.PlatformGeona:   12,
		consts.PlatformMilan:   8,
		consts.PlatformRome:    8,
		consts.PlatformRapids:  1,
		consts.PlatformLake:    1,
		consts.PlatformUnknown: 1,
	}
	socketBandwidthMap := map[string]uint64{
		consts.PlatformGeona:  322 * 1e9, // logical.max = 460, real.max = 460 * 70%
		consts.PlatformMilan:  142 * 1e9, // logical.max = 204, real.max = 204 * 70%
		consts.PlatformRome:   142 * 1e9, // logical.max = 204, real.max = 204 * 70%
		consts.PlatformRapids: 215 * 1e9, // logical.max = 307, real.max = 307 * 70%, Intel:SapphireRapids
		consts.PlatformLake:   98 * 1e9,  // logical.max = 140, real.max = 140 * 70%, intel:SkyLake/CascadeLake/IceLake
	}

	type testCase struct {
		name        string
		cpuCodeName string
		expectBW    uint64
	}
	cases := []testCase{
		{
			name:        "empty cpu code name (fallback to default)",
			cpuCodeName: "",
			expectBW:    consts.MaxMBGBps,
		},
		{
			name:        "Zen4 cpu code (Genoa)",
			cpuCodeName: "Zen4",
			expectBW:    socketBandwidthMap[consts.PlatformGeona] / uint64(ccdCountMap[consts.PlatformGeona]),
		},
		{
			name:        "Zen3 cpu code (Milan)",
			cpuCodeName: "Zen3",
			expectBW:    socketBandwidthMap[consts.PlatformMilan] / uint64(ccdCountMap[consts.PlatformMilan]),
		},
		{
			name:        "Zen2 cpu code (Rome)",
			cpuCodeName: "Zen2",
			expectBW:    socketBandwidthMap[consts.PlatformRome] / uint64(ccdCountMap[consts.PlatformRome]),
		},
		{
			name:        "Rapids cpu code",
			cpuCodeName: "Rapids",
			expectBW:    socketBandwidthMap[consts.PlatformRapids] / uint64(ccdCountMap[consts.PlatformRapids]),
		},
		{
			name:        "Lake cpu code",
			cpuCodeName: "Lake",
			expectBW:    socketBandwidthMap[consts.PlatformLake] / uint64(ccdCountMap[consts.PlatformLake]),
		},
		{
			name:        "unknown cpu code (fallback to default)",
			cpuCodeName: "UnknownCPU",
			expectBW:    consts.MaxMBGBps,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, maxBW := getNumaAndMaxBandwidth(0, tc.cpuCodeName)
			assert.Equal(t, tc.expectBW, maxBW)
		})
	}
}

func Test_aggregateNUMABytesPS(t *testing.T) {
	t.Parallel()
	l3BytesPS := map[int]malachitetypes.L3CacheBytesPS{
		1: {NumaID: 0, MbmTotalBytesPS: 10, MbmLocalBytesPS: 5, MbmVictimBytesPS: 2, MBMMaxBytesPS: 100},
		2: {NumaID: 0, MbmTotalBytesPS: 20, MbmLocalBytesPS: 10, MbmVictimBytesPS: 4, MBMMaxBytesPS: 100},
		3: {NumaID: 1, MbmTotalBytesPS: 30, MbmLocalBytesPS: 15, MbmVictimBytesPS: 6, MBMMaxBytesPS: 100},
	}
	tests := []struct {
		name      string
		input     map[int]malachitetypes.L3CacheBytesPS
		cpuCode   string
		wantTotal map[int]float64
	}{
		{
			"normal aggregation",
			l3BytesPS,
			"",
			map[int]float64{0: 30, 1: 30},
		},
		{
			"AMD Milan aggregation",
			l3BytesPS,
			consts.AMDMilanArch,
			map[int]float64{0: 30 + 2 + 4, 1: 30 + 6},
		},
		{
			"AMD Genoa aggregation",
			l3BytesPS,
			consts.AMDGenoaArch,
			map[int]float64{0: 30 + 5/3*2 + 10/3*2, 1: 30 + 15/3*2},
		},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result := aggregateNUMABytesPS(tc.input, tc.cpuCode)
			for numaID, want := range tc.wantTotal {
				assert.Equal(t, want, float64(result[numaID].MbmTotalBytesPS))
			}
		})
	}
}

func TestMalachiteMetricsProvisioner_processContainerMemRelevantRate(t *testing.T) {
	t.Parallel()

	podUID := "pod1"
	containerName := "container1"
	now := time.Now()
	lastUpdateTime := now.Add(-10 * time.Second)

	// Common initial metrics
	initialMetrics := map[string]float64{
		consts.MetricMemPgfaultContainer:               1000,
		consts.MetricMemPgmajfaultContainer:            100,
		consts.MetricMemOomContainer:                   1,
		consts.MetricMemKswapdstealContainer:           200,
		consts.MetricMemKswapdScanContainer:            2000,
		consts.MetricMemDirectStealContainer:           300,
		consts.MetricMemDirectScanContainer:            3000,
		consts.MetricMemPgstealContainer:               500,
		consts.MetricMemPgscanContainer:                5000,
		consts.MetricMemWorkingsetRefaultAnonContainer: 50,
		consts.MetricMemWorkingsetRefaultFileContainer: 60,
		consts.MetricMemWorkingsetRefaultContainer:     110,
		consts.MetricMemProactiveReclaimContainer:      400,
	}

	// Test case for Cgroup V1
	t.Run("cgroup v1", func(t *testing.T) {
		t.Parallel()
		store := utilmetric.NewMetricStore()
		for name, val := range initialMetrics {
			store.SetContainerMetric(podUID, containerName, name, utilmetric.MetricData{Value: val, Time: &lastUpdateTime})
		}

		mmp := &MalachiteMetricsProvisioner{
			metricStore: store,
		}

		cgStats := &malachitetypes.MalachiteCgroupInfo{
			CgroupType: "V1",
			V1: &malachitetypes.MalachiteCgroupV1Info{
				Memory: &malachitetypes.MemoryCgDataV1{
					Pgfault:     1500, // delta 500
					Pgmajfault:  150,  // delta 50
					KswapdSteal: 300,  // delta 100
					BpfMemStat: malachitetypes.BpfMemData{
						OomCnt: 2, // delta 1
					},
					UpdateTime: now.Unix(),
				},
			},
		}

		mmp.processContainerMemRelevantRate(podUID, containerName, cgStats, float64(lastUpdateTime.Unix()))

		// Assertions for V1 metrics
		pgFaultRate, err := store.GetContainerMetric(podUID, containerName, consts.MetricMemPgfaultRateContainer)
		assert.NoError(t, err)
		assert.InDelta(t, 50, pgFaultRate.Value, 1e-6) // 500 / 10s

		pgMajFaultRate, err := store.GetContainerMetric(podUID, containerName, consts.MetricMemPgmajfaultRateContainer)
		assert.NoError(t, err)
		assert.InDelta(t, 5, pgMajFaultRate.Value, 1e-6) // 50 / 10s

		oomRate, err := store.GetContainerMetric(podUID, containerName, consts.MetricMemOomRateContainer)
		assert.NoError(t, err)
		assert.InDelta(t, 0.1, oomRate.Value, 1e-6) // 1 / 10s

		kswapdStealRate, err := store.GetContainerMetric(podUID, containerName, consts.MetricMemKswapdstealRateContainer)
		assert.NoError(t, err)
		assert.InDelta(t, 10, kswapdStealRate.Value, 1e-6) // 100 / 10s
	})

	// Test case for Cgroup V2
	t.Run("cgroup v2", func(t *testing.T) {
		t.Parallel()
		store := utilmetric.NewMetricStore()
		for name, val := range initialMetrics {
			store.SetContainerMetric(podUID, containerName, name, utilmetric.MetricData{Value: val, Time: &lastUpdateTime})
		}

		mmp := &MalachiteMetricsProvisioner{
			metricStore: store,
		}

		cgStats := &malachitetypes.MalachiteCgroupInfo{
			CgroupType: "V2",
			V2: &malachitetypes.MalachiteCgroupV2Info{
				Memory: &malachitetypes.MemoryCgDataV2{
					MemStats: malachitetypes.MemStatsV2{
						Pgfault:               1500, // delta 500
						Pgmajfault:            150,  // delta 50
						PgstealKswapd:         300,  // delta 100
						PgscanKswapd:          2500, // delta 500
						PgstealDirect:         400,  // delta 100
						PgscanDirect:          3500, // delta 500
						Pgsteal:               600,  // delta 100
						Pgscan:                5500, // delta 500
						WorkingsetRefaultAnon: 70,   // delta 20
						WorkingsetRefaultFile: 80,   // delta 20
						WorkingsetRefault:     150,  // delta 40
					},
					BpfMemStat: malachitetypes.BpfMemData{
						OomCnt:               2,   // delta 1
						MemReclaimSettingSum: 500, // delta 100
					},
					UpdateTime: now.Unix(),
				},
			},
		}

		mmp.processContainerMemRelevantRate(podUID, containerName, cgStats, float64(lastUpdateTime.Unix()))

		// Assertions for V2 metrics
		pgFaultRate, err := store.GetContainerMetric(podUID, containerName, consts.MetricMemPgfaultRateContainer)
		assert.NoError(t, err)
		assert.InDelta(t, 50, pgFaultRate.Value, 1e-6)

		pgMajFaultRate, err := store.GetContainerMetric(podUID, containerName, consts.MetricMemPgmajfaultRateContainer)
		assert.NoError(t, err)
		assert.InDelta(t, 5, pgMajFaultRate.Value, 1e-6)

		oomRate, err := store.GetContainerMetric(podUID, containerName, consts.MetricMemOomRateContainer)
		assert.NoError(t, err)
		assert.InDelta(t, 0.1, oomRate.Value, 1e-6)

		kswapdStealRate, err := store.GetContainerMetric(podUID, containerName, consts.MetricMemKswapdstealRateContainer)
		assert.NoError(t, err)
		assert.InDelta(t, 10, kswapdStealRate.Value, 1e-6)

		kswapdScanRate, err := store.GetContainerMetric(podUID, containerName, consts.MetricMemKswapdScanRateContainer)
		assert.NoError(t, err)
		assert.InDelta(t, 50, kswapdScanRate.Value, 1e-6)

		directStealRate, err := store.GetContainerMetric(podUID, containerName, consts.MetricMemDirectStealRateContainer)
		assert.NoError(t, err)
		assert.InDelta(t, 10, directStealRate.Value, 1e-6)

		directScanRate, err := store.GetContainerMetric(podUID, containerName, consts.MetricMemDirectScanRateContainer)
		assert.NoError(t, err)
		assert.InDelta(t, 50, directScanRate.Value, 1e-6)

		pgStealRate, err := store.GetContainerMetric(podUID, containerName, consts.MetricMemPgstealRateContainer)
		assert.NoError(t, err)
		assert.InDelta(t, 10, pgStealRate.Value, 1e-6)

		pgScanRate, err := store.GetContainerMetric(podUID, containerName, consts.MetricMemPgscanRateContainer)
		assert.NoError(t, err)
		assert.InDelta(t, 50, pgScanRate.Value, 1e-6)

		refaultAnonRate, err := store.GetContainerMetric(podUID, containerName, consts.MetricMemWorkingsetRefaultAnonRateContainer)
		assert.NoError(t, err)
		assert.InDelta(t, 2, refaultAnonRate.Value, 1e-6)

		refaultFileRate, err := store.GetContainerMetric(podUID, containerName, consts.MetricMemWorkingsetRefaultFileRateContainer)
		assert.NoError(t, err)
		assert.InDelta(t, 2, refaultFileRate.Value, 1e-6)

		refaultRate, err := store.GetContainerMetric(podUID, containerName, consts.MetricMemWorkingsetRefaultRateContainer)
		assert.NoError(t, err)
		assert.InDelta(t, 4, refaultRate.Value, 1e-6)

		proactiveReclaimRate, err := store.GetContainerMetric(podUID, containerName, consts.MetricMemProactiveReclaimRateContainer)
		assert.NoError(t, err)
		assert.InDelta(t, 10, proactiveReclaimRate.Value, 1e-6)
	})
}
