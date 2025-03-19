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
