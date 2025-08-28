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
	"testing"

	v1 "k8s.io/api/core/v1"

	"github.com/stretchr/testify/require"
)

func TestGetNodeMemoryStats(t *testing.T) {
	t.Parallel()
	ic := NewRodanClient(&FakePodFetcher{}, func(url string, params map[string]string) ([]byte, error) {
		return []byte(`{"data":[{"key":"memory_swaptotal","val":0},{"key":"memory_sreclaimable","val":573520},{"key":"memory_shmem","val":1643536},{"key":"memory_writeback","val":0},{"key":"memory_memavailable","val":19710408},{"key":"memory_pgsteal_kswapd","val":7067178},{"key":"memory_cached","val":16703864},{"key":"memory_memtotal","val":32614152},{"key":"memory_swapfree","val":0},{"key":"memory_buffers","val":360688},{"key":"memory_memfree","val":14053836},{"key":"memory_dirty","val":1124}]}`), nil
	}, 9102)

	res, err := ic.GetNodeMemoryStats()
	require.NoError(t, err)
	require.Equal(t, 12, len(res))
}

func TestGetNUMAMemoryStats(t *testing.T) {
	t.Parallel()
	ic := NewRodanClient(&FakePodFetcher{}, func(url string, params map[string]string) ([]byte, error) {
		return []byte(`{"data":[{"key":"numastat_node0_memfree","val":14549598208},{"key":"numastat_node0_memtotal","val":33396891648}]}`), nil
	}, 9102)

	res, err := ic.GetNUMAMemoryStats()
	require.NoError(t, err)
	require.Equal(t, 1, len(res))
	require.NotNil(t, res[0])
	require.Equal(t, 2, len(res[0]))
}

func TestGetNodeCgroupMemoryStats(t *testing.T) {
	t.Parallel()
	ic := NewRodanClient(&FakePodFetcher{}, func(url string, params map[string]string) ([]byte, error) {
		return []byte(`{"data":[{"key":"qosgroupmem_besteffort_memory_rss","val":112414720},{"key":"qosgroupmem_besteffort_memory_usage","val":1244418048},{"key":"qosgroupmem_burstable_memory_rss","val":183828480},{"key":"qosgroupmem_burstable_memory_usage","val":377950208}]}`), nil
	}, 9102)

	res, err := ic.GetNodeCgroupMemoryStats()
	require.NoError(t, err)
	require.Equal(t, 4, len(res))
}

func TestGetCoreCPUStats(t *testing.T) {
	t.Parallel()
	ic := NewRodanClient(&FakePodFetcher{}, func(url string, params map[string]string) ([]byte, error) {
		return []byte(`{"data":[{"key":"percorecpu_cpu5_sched_wait","val":0},{"key":"percorecpu_cpu0_usage","val":19},{"key":"percorecpu_cpu3_sched_wait","val":0},{"key":"percorecpu_cpu7_usage","val":53},{"key":"percorecpu_cpu0_sched_wait","val":0},{"key":"percorecpu_cpu6_usage","val":31},{"key":"percorecpu_cpu7_sched_wait","val":0},{"key":"percorecpu_cpu2_usage","val":23},{"key":"percorecpu_cpu1_usage","val":28},{"key":"percorecpu_cpu6_sched_wait","val":0},{"key":"percorecpu_cpu3_usage","val":78},{"key":"percorecpu_cpu4_sched_wait","val":0},{"key":"percorecpu_cpu2_sched_wait","val":0},{"key":"percorecpu_cpu1_sched_wait","val":0},{"key":"percorecpu_cpu5_usage","val":64},{"key":"percorecpu_cpu4_usage","val":32}]}`), nil
	}, 9102)

	res, err := ic.GetCoreCPUStats()
	require.NoError(t, err)
	require.Equal(t, 8, len(res))
	require.NotNil(t, res[0])
	require.Equal(t, 2, len(res[0]))
}

func TestGetNodeSysctl(t *testing.T) {
	t.Parallel()
	ic := NewRodanClient(&FakePodFetcher{}, func(url string, params map[string]string) ([]byte, error) {
		return []byte(`{"data":[{"key":"sysctl_vm_watermark_scale_factor","val":1000},{"key":"sysctl_tcp_mem_pressure","val":507145},{"key":"sysctl_tcp_mem_extreme","val":760716},{"key":"sysctl_tcp_mem_original","val":380358}]}`), nil
	}, 9102)

	res, err := ic.GetNodeSysctl()
	require.NoError(t, err)
	require.Equal(t, 4, len(res))
}

type FakePodFetcher struct {
	GetPodFunc func(ctx context.Context, podUID string) (*v1.Pod, error)
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
	return nil, nil
}
