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

package metric

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/malachite"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

func Test_noneExistMetricsFetcher(t *testing.T) {
	var err error
	implement := NewMalachiteMetricsFetcher(metrics.DummyMetrics{})

	fakeSystemCompute := &malachite.SystemComputeData{
		CPU: []malachite.CPU{
			{
				Name: "CPU1111",
			},
		},
	}
	fakeSystemMemory := &malachite.SystemMemoryData{
		Numa: []malachite.Numa{
			{},
		},
	}
	fakeSystemIO := &malachite.SystemDiskIoData{
		DiskIo: []malachite.DiskIo{
			{},
		},
	}
	fakeCgroupInfoV1 := &malachite.MalachiteCgroupInfo{
		CgroupType: "V1",
		V1: &malachite.MalachiteCgroupV1Info{
			Memory:    &malachite.MemoryCgDataV1{},
			Blkio:     &malachite.BlkIOCgDataV1{},
			NetCls:    &malachite.NetClsCgData{},
			PerfEvent: &malachite.PerfEventData{},
			CpuSet:    &malachite.CPUSetCgDataV1{},
			Cpu:       &malachite.CPUCgDataV1{},
		},
	}
	fakeCgroupInfoV2 := &malachite.MalachiteCgroupInfo{
		CgroupType: "V2",
		V2: &malachite.MalachiteCgroupV2Info{
			Memory:    &malachite.MemoryCgDataV2{},
			Blkio:     &malachite.BlkIOCgDataV2{},
			NetCls:    &malachite.NetClsCgData{},
			PerfEvent: &malachite.PerfEventData{},
			CpuSet:    &malachite.CPUSetCgDataV2{},
			Cpu:       &malachite.CPUCgDataV2{},
		},
	}

	implement.(*MalachiteMetricsFetcher).processSystemComputeData(fakeSystemCompute)
	implement.(*MalachiteMetricsFetcher).processSystemMemoryData(fakeSystemMemory)
	implement.(*MalachiteMetricsFetcher).processSystemIOData(fakeSystemIO)
	implement.(*MalachiteMetricsFetcher).processSystemNumaData(fakeSystemMemory)
	implement.(*MalachiteMetricsFetcher).processSystemCPUComputeData(fakeSystemCompute)

	implement.(*MalachiteMetricsFetcher).processCgroupCPUData("pod-not-exist", "container-not-exist", fakeCgroupInfoV1)
	implement.(*MalachiteMetricsFetcher).processCgroupMemoryData("pod-not-exist", "container-not-exist", fakeCgroupInfoV1)
	implement.(*MalachiteMetricsFetcher).processCgroupBlkIOData("pod-not-exist", "container-not-exist", fakeCgroupInfoV1)
	implement.(*MalachiteMetricsFetcher).processCgroupNetData("pod-not-exist", "container-not-exist", fakeCgroupInfoV1)
	implement.(*MalachiteMetricsFetcher).processCgroupPerfData("pod-not-exist", "container-not-exist", fakeCgroupInfoV1)
	implement.(*MalachiteMetricsFetcher).processCgroupPerNumaMemoryData("pod-not-exist", "container-not-exist", fakeCgroupInfoV1)

	implement.(*MalachiteMetricsFetcher).processCgroupCPUData("pod-not-exist", "container-not-exist", fakeCgroupInfoV2)
	implement.(*MalachiteMetricsFetcher).processCgroupMemoryData("pod-not-exist", "container-not-exist", fakeCgroupInfoV2)
	implement.(*MalachiteMetricsFetcher).processCgroupBlkIOData("pod-not-exist", "container-not-exist", fakeCgroupInfoV2)
	implement.(*MalachiteMetricsFetcher).processCgroupNetData("pod-not-exist", "container-not-exist", fakeCgroupInfoV2)
	implement.(*MalachiteMetricsFetcher).processCgroupPerfData("pod-not-exist", "container-not-exist", fakeCgroupInfoV2)
	implement.(*MalachiteMetricsFetcher).processCgroupPerNumaMemoryData("pod-not-exist", "container-not-exist", fakeCgroupInfoV2)

	_, err = implement.GetNodeMetric("test-not-exist")
	if err == nil {
		t.Errorf("GetNode() error = %v, wantErr not nil", err)
		return
	}

	_, err = implement.GetNumaMetric(1, "test-not-exist")
	if err == nil {
		t.Errorf("GetNode() error = %v, wantErr not nil", err)
		return
	}

	_, err = implement.GetDeviceMetric("device-not-exist", "test-not-exist")
	if err == nil {
		t.Errorf("GetNode() error = %v, wantErr not nil", err)
		return
	}

	_, err = implement.GetCPUMetric(1, "test-not-exist")
	if err == nil {
		t.Errorf("GetNode() error = %v, wantErr not nil", err)
		return
	}

	_, err = implement.GetContainerMetric("pod-not-exist", "container-not-exist", "test-not-exist")
	if err == nil {
		t.Errorf("GetNode() error = %v, wantErr not nil", err)
		return
	}

	_, err = implement.GetContainerNumaMetric("pod-not-exist", "container-not-exist", "", "test-not-exist")
	if err == nil {
		t.Errorf("GetContainerNuma() error = %v, wantErr not nil", err)
		return
	}
}

func Test_notifySystem(t *testing.T) {
	f := NewMalachiteMetricsFetcher(metrics.DummyMetrics{})

	rChan := make(chan NotifiedResponse, 20)
	f.RegisterNotifier(MetricsScopeNode, NotifiedRequest{
		MetricName: "test-node-metric",
	}, rChan)
	f.RegisterNotifier(MetricsScopeNuma, NotifiedRequest{
		MetricName: "test-numa-metric",
		NumaID:     1,
	}, rChan)
	f.RegisterNotifier(MetricsScopeCPU, NotifiedRequest{
		MetricName: "test-cpu-metric",
		CoreID:     2,
	}, rChan)
	f.RegisterNotifier(MetricsScopeDevice, NotifiedRequest{
		MetricName: "test-device-metric",
		DeviceID:   "test-device",
	}, rChan)
	f.RegisterNotifier(MetricsScopeContainer, NotifiedRequest{
		MetricName:    "test-container-metric",
		PodUID:        "test-pod",
		ContainerName: "test-container",
	}, rChan)
	f.RegisterNotifier(MetricsScopeContainer, NotifiedRequest{
		MetricName:    "test-container-numa-metric",
		PodUID:        "test-pod",
		ContainerName: "test-container",
		NumaNode:      "3",
	}, rChan)

	m := f.(*MalachiteMetricsFetcher)
	m.metricStore.SetNodeMetric("test-node-metric", 34)
	m.metricStore.SetNumaMetric(1, "test-numa-metric", 56)
	m.metricStore.SetCPUMetric(2, "test-cpu-metric", 78)
	m.metricStore.SetDeviceMetric("test-device", "test-device-metric", 91)
	m.metricStore.SetContainerMetric("test-pod", "test-container", "test-container-metric", 91)
	m.metricStore.SetContainerNumaMetric("test-pod", "test-container", "3", "test-container-numa-metric", 75)

	go func() {
		cnt := 0
		for {
			select {
			case response := <-rChan:
				cnt++
				switch response.Req.MetricName {
				case "test-node-metric":
					assert.Equal(t, response.Result, 34)
				case "test-numa-metric":
					assert.Equal(t, response.Result, 56)
				case "test-cpu-metric":
					assert.Equal(t, response.Result, 78)
				case "test-device-metric":
					assert.Equal(t, response.Result, 91)
				case "test-container-metric":
					assert.Equal(t, response.Result, 91)
				case "test-container-numa-metric":
					assert.Equal(t, response.Result, 75)
				}
			}
		}
		assert.Equal(t, cnt, 6)
	}()

	time.Sleep(time.Second * 10)
}
