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

package rodan

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/provisioner/rodan/client"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/provisioner/rodan/types"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	utilmetric "github.com/kubewharf/katalyst-core/pkg/util/metric"
)

func TestSample(t *testing.T) {
	t.Parallel()
	testPod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			UID:  "2126079c-8e0a-4cfe-9a0b-583199c14027",
			Name: "testPod",
		},
		Status: v1.PodStatus{
			ContainerStatuses: []v1.ContainerStatus{
				{
					Name:        "testContainer",
					ContainerID: "containerd://1df178c3a7bddf1269d18b556c1d22a025bdf194c4617d9b411d0ef4b9229ec6",
				},
			},
		},
	}

	podFetcher := &FakePodFetcher{
		GetPodFunc: func(ctx context.Context, podUID string) (*v1.Pod, error) {
			return testPod, nil
		},
		GetPodListFunc: func(ctx context.Context, podFilter func(*v1.Pod) bool) ([]*v1.Pod, error) {
			return []*v1.Pod{
				testPod,
			}, nil
		},
	}

	f := &RodanMetricsProvisioner{
		metricStore: utilmetric.NewMetricStore(),
		podFetcher:  podFetcher,
		client:      client.NewRodanClient(podFetcher, makeTestMetricClient(), 9102),
		emitter:     metrics.DummyMetrics{},
		synced:      false,
	}

	f.Run(context.Background())

	data, err := f.metricStore.GetNodeMetric(consts.MetricMemTotalSystem)
	require.NoError(t, err)
	require.Equal(t, float64(32614152<<10), data.Value)

	data, err = f.metricStore.GetNodeMetric(consts.MetricMemFreeSystem)
	require.NoError(t, err)
	require.Equal(t, float64(12891380<<10), data.Value)

	data, err = f.metricStore.GetNodeMetric(consts.MetricMemUsedSystem)
	require.NoError(t, err)
	require.Equal(t, float64(968376<<10), data.Value)

	data, err = f.metricStore.GetNodeMetric(consts.MetricMemScaleFactorSystem)
	require.NoError(t, err)
	require.Equal(t, 1000.0, data.Value)

	data, err = f.metricStore.GetNodeMetric(consts.MetricMemPageCacheSystem)
	require.NoError(t, err)
	require.Equal(t, float64(17783636<<10), data.Value)

	data, err = f.metricStore.GetNodeMetric(consts.MetricMemBufferSystem)
	require.NoError(t, err)
	require.Equal(t, float64(365640<<10), data.Value)

	data, err = f.metricStore.GetNodeMetric(consts.MetricMemKswapdstealSystem)
	require.NoError(t, err)
	require.Equal(t, 7067178.0, data.Value)

	data, err = f.metricStore.GetNumaMetric(0, consts.MetricMemTotalNuma)
	require.NoError(t, err)
	require.Equal(t, 33396891648.0, data.Value)

	data, err = f.metricStore.GetNumaMetric(0, consts.MetricMemFreeNuma)
	require.NoError(t, err)
	require.Equal(t, 13186412544.0, data.Value)

	data, err = f.metricStore.GetCPUMetric(1, consts.MetricCPUUsageRatio)
	require.NoError(t, err)
	require.Equal(t, 0.04, data.Value)

	data, err = f.metricStore.GetCPUMetric(6, consts.MetricCPUSchedwait)
	require.NoError(t, err)
	require.Equal(t, 0.0, data.Value)

	data, err = f.metricStore.GetContainerMetric("2126079c-8e0a-4cfe-9a0b-583199c14027", "testContainer", consts.MetricLoad1MinContainer)
	require.NoError(t, err)
	require.Equal(t, 0.54, data.Value)

	data, err = f.metricStore.GetContainerMetric("2126079c-8e0a-4cfe-9a0b-583199c14027", "testContainer", consts.MetricLoad5MinContainer)
	require.NoError(t, err)
	require.Equal(t, 0.54, data.Value)

	data, err = f.metricStore.GetContainerMetric("2126079c-8e0a-4cfe-9a0b-583199c14027", "testContainer", consts.MetricCPUNrRunnableContainer)
	require.NoError(t, err)
	require.Equal(t, 54.0, data.Value)

	data, err = f.metricStore.GetContainerMetric("2126079c-8e0a-4cfe-9a0b-583199c14027", "testContainer", consts.MetricCPUUsageContainer)
	require.NoError(t, err)
	require.Equal(t, 0.99, data.Value)

	data, err = f.metricStore.GetContainerMetric("2126079c-8e0a-4cfe-9a0b-583199c14027", "testContainer", consts.MetricMemRssContainer)
	require.NoError(t, err)
	require.Equal(t, 16220160.0, data.Value)

	data, err = f.metricStore.GetContainerMetric("2126079c-8e0a-4cfe-9a0b-583199c14027", "testContainer", consts.MetricMemCacheContainer)
	require.NoError(t, err)
	require.Equal(t, 0.0, data.Value)

	data, err = f.metricStore.GetContainerMetric("2126079c-8e0a-4cfe-9a0b-583199c14027", "testContainer", consts.MetricMemShmemContainer)
	require.NoError(t, err)
	require.Equal(t, 0.0, data.Value)

	data, err = f.metricStore.GetContainerNumaMetric("2126079c-8e0a-4cfe-9a0b-583199c14027", "testContainer", 0, consts.MetricsMemFilePerNumaContainer)
	require.NoError(t, err)
	require.Equal(t, float64(3352<<12), data.Value)
}

func makeTestMetricClient() client.MetricFunc {
	return func(url string, params map[string]string) ([]byte, error) {
		switch url {
		case fmt.Sprintf("%s%s", "http://localhost:9102", types.NodeMemoryPath):
			return []byte(`{"data":[{"key":"memory_memavailable","val":19583568},{"key":"memory_swapfree","val":0},{"key":"memory_pgsteal_kswapd","val":7067178},{"key":"memory_memused","val":968376},{"key":"memory_writeback","val":0},{"key":"memory_shmem","val":1708908},{"key":"memory_sreclaimable","val":605120},{"key":"memory_cached","val":17783636},{"key":"memory_swaptotal","val":0},{"key":"memory_dirty","val":956},{"key":"memory_memtotal","val":32614152},{"key":"memory_memfree","val":12891380},{"key":"memory_buffers","val":365640}]}`), nil
		case fmt.Sprintf("%s%s", "http://localhost:9102", types.NodeSysctlPath):
			return []byte(`{"data":[{"key":"sysctl_tcp_mem_pressure","val":507145},{"key":"sysctl_tcp_mem_original","val":380358},{"key":"sysctl_tcp_mem_extreme","val":760716},{"key":"sysctl_vm_watermark_scale_factor","val":1000}]}`), nil
		case fmt.Sprintf("%s%s", "http://localhost:9102", types.NumaMemoryPath):
			return []byte(`{"data":[{"key":"numastat_node0_memtotal","val":33396891648},{"key":"numastat_node0_memfree","val":13186412544}]}`), nil
		case fmt.Sprintf("%s%s", "http://localhost:9102", types.NodeCgroupMemoryPath):
			return []byte(`{"data":[{"key":"qosgroupmem_besteffort_memory_usage","val":1266978816},{"key":"qosgroupmem_burstable_memory_rss","val":185856000},{"key":"qosgroupmem_besteffort_memory_rss","val":112910336},{"key":"qosgroupmem_burstable_memory_usage","val":380174336}]}`), nil
		case fmt.Sprintf("%s%s", "http://localhost:9102", types.NodeCPUPath):
			return []byte(`{"data":[{"key":"percorecpu_cpu6_usage","val":21},{"key":"percorecpu_cpu0_sched_wait","val":0},{"key":"percorecpu_cpu2_sched_wait","val":0},{"key":"percorecpu_cpu1_usage","val":4},{"key":"percorecpu_cpu7_usage","val":22},{"key":"percorecpu_cpu6_sched_wait","val":0},{"key":"percorecpu_cpu7_sched_wait","val":0},{"key":"percorecpu_cpu5_usage","val":14},{"key":"percorecpu_cpu0_usage","val":5},{"key":"percorecpu_cpu3_usage","val":4},{"key":"percorecpu_cpu3_sched_wait","val":0},{"key":"percorecpu_cpu2_usage","val":5},{"key":"percorecpu_cpu5_sched_wait","val":0},{"key":"percorecpu_cpu4_usage","val":29},{"key":"percorecpu_cpu4_sched_wait","val":0},{"key":"percorecpu_cpu1_sched_wait","val":0}]}`), nil
		case fmt.Sprintf("%s%s", "http://localhost:9102", types.ContainerCPUPath):
			return []byte(`{"data":{"1df178c3a7bddf1269d18b556c1d22a025bdf194c4617d9b411d0ef4b9229ec6":[{"key":"cgcpu_nsecs","val":2015872049910},{"key":"cgcpu_sysusage","val":0},{"key":"cgcpu_userusage","val":99},{"key":"cgcpu_sys_nsecs","val":179907376},{"key":"cgcpu_user_nsecs","val":2015692142534},{"key":"cgcpu_usage","val":99}]}}`), nil
		case fmt.Sprintf("%s%s", "http://localhost:9102", types.ContainerLoadPath):
			return []byte(`{"data":{"1df178c3a7bddf1269d18b556c1d22a025bdf194c4617d9b411d0ef4b9229ec6":[{"key":"loadavg_nruninterruptible","val":54},{"key":"loadavg_loadavg15","val":54},{"key":"loadavg_loadavg5","val":54},{"key":"loadavg_nrrunning","val":54},{"key":"loadavg_nriowait","val":54},{"key":"loadavg_loadavg1","val":54},{"key":"loadavg_nrsleeping","val":54}]}}`), nil
		case fmt.Sprintf("%s%s", "http://localhost:9102", types.ContainerCghardwarePath):
			return []byte(`{"data":{"1df178c3a7bddf1269d18b556c1d22a025bdf194c4617d9b411d0ef4b9229ec6":[{"key":"cghardware_cycles","val":0},{"key":"cghardware_instructions","val":0}]}}`), nil
		case fmt.Sprintf("%s%s", "http://localhost:9102", types.ContainerCgroupMemoryPath):
			return []byte(`{"data":{"1df178c3a7bddf1269d18b556c1d22a025bdf194c4617d9b411d0ef4b9229ec6":[{"key":"cgmem_total_dirty","val":0},{"key":"cgmem_total_shmem","val":0},{"key":"cgmem_total_rss","val":16220160},{"key":"cgmem_total_cache","val":0}]}}`), nil
		case fmt.Sprintf("%s%s", "http://localhost:9102", types.ContainerNumaStatPath):
			return []byte(`{"data":{"1df178c3a7bddf1269d18b556c1d22a025bdf194c4617d9b411d0ef4b9229ec6":[{"key":"cgnumastat_filepage","val":3352},{"key":"cgnumastat_node0_filepage","val":3352}]}}`), nil
		default:
			return nil, fmt.Errorf("unknow url")
		}
	}
}

type FakePodFetcher struct {
	GetPodFunc     func(ctx context.Context, podUID string) (*v1.Pod, error)
	GetPodListFunc func(ctx context.Context, podFilter func(*v1.Pod) bool) ([]*v1.Pod, error)
}

func (f *FakePodFetcher) Run(ctx context.Context) {
	return
}

func (f *FakePodFetcher) GetContainerID(podUID, containerName string) (string, error) {
	return "", nil
}

func (f *FakePodFetcher) GetContainerSpec(podUID, containerName string) (*v1.Container, error) {
	return nil, nil
}

func (f *FakePodFetcher) GetPod(ctx context.Context, podUID string) (*v1.Pod, error) {
	return f.GetPodFunc(ctx, podUID)
}

func (f *FakePodFetcher) GetPodList(ctx context.Context, podFilter func(*v1.Pod) bool) ([]*v1.Pod, error) {
	return f.GetPodListFunc(ctx, podFilter)
}
