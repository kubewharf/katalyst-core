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

	"github.com/kubewharf/katalyst-core/pkg/consts"
)

func TestStore_SetAndGetNodeMetric(t *testing.T) {
	t.Parallel()

	now := time.Now()

	store := NewMetricStore()
	store.SetNodeMetric("test-metric-name", MetricData{Value: 1.0, Time: &now})
	value, _ := store.GetNodeMetric("test-metric-name")
	assert.Equal(t, MetricData{Value: 1.0, Time: &now}, value)
	_, err := store.GetNodeMetric("test-not-exist")
	assert.Error(t, err)
}

func TestStore_SetAndGetNumaMetric(t *testing.T) {
	t.Parallel()

	now := time.Now()

	store := NewMetricStore()
	store.SetNumaMetric(0, "test-metric-name", MetricData{Value: 1.0, Time: &now})
	value, _ := store.GetNumaMetric(0, "test-metric-name")
	assert.Equal(t, MetricData{Value: 1.0, Time: &now}, value)
	_, err := store.GetNumaMetric(1, "test-not-exist")
	assert.Error(t, err)
}

func TestStore_SetAndGeDeviceMetric(t *testing.T) {
	t.Parallel()

	now := time.Now()

	store := NewMetricStore()
	store.SetDeviceMetric("test-device", "test-metric-name", MetricData{Value: 1.0, Time: &now})
	value, _ := store.GetDeviceMetric("test-device", "test-metric-name")
	assert.Equal(t, MetricData{Value: 1.0, Time: &now}, value)
	_, err := store.GetDeviceMetric("test-device", "test-not-exist")
	assert.Error(t, err)
}

func TestStore_SetAndGetCPUMetric(t *testing.T) {
	t.Parallel()

	now := time.Now()

	store := NewMetricStore()
	store.SetCPUMetric(0, "test-metric-name", MetricData{Value: 1.0, Time: &now})
	value, _ := store.GetCPUMetric(0, "test-metric-name")
	assert.Equal(t, MetricData{Value: 1.0, Time: &now}, value)
	_, err := store.GetCPUMetric(1, "test-not-exist")
	assert.Error(t, err)
}

func TestStore_DeleteCPUMetric(t *testing.T) {
	t.Parallel()

	now := time.Now()

	store := NewMetricStore()
	store.SetCPUMetric(0, "cpi", MetricData{Value: 1.5, Time: &now})
	store.SetCPUMetric(0, "cycles", MetricData{Value: 100, Time: &now})

	// sanity: both present
	_, err := store.GetCPUMetric(0, "cpi")
	assert.NoError(t, err)

	store.DeleteCPUMetric(0, "cpi")
	_, err = store.GetCPUMetric(0, "cpi")
	assert.Error(t, err)

	// other metric on the same cpu is untouched
	v, err := store.GetCPUMetric(0, "cycles")
	assert.NoError(t, err)
	assert.Equal(t, 100.0, v.Value)

	// deleting a missing metric / missing cpu is a no-op
	store.DeleteCPUMetric(0, "cpi")
	store.DeleteCPUMetric(99, "cpi")
}

// TestStore_DeleteCPUMetricRemovesEmptyEntry ensures that deleting the last
// per-CPU metric removes the empty map entry from cpuMetricMap, preventing
// unbounded growth of stale empty entries.
func TestStore_DeleteCPUMetricRemovesEmptyEntry(t *testing.T) {
	t.Parallel()

	now := time.Now()

	store := NewMetricStore()
	store.SetCPUMetric(0, "cpi", MetricData{Value: 1.5, Time: &now})

	// sanity: entry exists
	store.mutex.RLock()
	_, ok := store.cpuMetricMap[0]
	store.mutex.RUnlock()
	assert.True(t, ok, "cpu entry should exist before delete")

	store.DeleteCPUMetric(0, "cpi")

	// after deleting the last metric, the empty entry must be removed
	store.mutex.RLock()
	_, ok = store.cpuMetricMap[0]
	store.mutex.RUnlock()
	assert.False(t, ok, "empty cpu entry should be removed after deleting last metric")
}

func TestStore_ContainerMetric(t *testing.T) {
	t.Parallel()

	now := time.Now()

	store := NewMetricStore()
	store.SetContainerMetric("pod1", "container1", "test-metric-name", MetricData{Value: 1.0, Time: &now})
	store.SetContainerMetric("pod2", "container1", "test-metric-name", MetricData{Value: 1.0, Time: &now})
	value, _ := store.GetContainerMetric("pod1", "container1", "test-metric-name")
	assert.Equal(t, MetricData{Value: 1.0, Time: &now}, value)
	_, err := store.GetContainerMetric("pod1", "container2", "test-not-exist")
	assert.Error(t, err)
	store.GCPodsMetric(map[string]bool{"pod2": true})
	_, err = store.GetContainerMetric("pod1", "container1", "test-metric-name")
	assert.Error(t, err)
	value, _ = store.GetContainerMetric("pod2", "container1", "test-metric-name")
	assert.Equal(t, MetricData{Value: 1.0, Time: &now}, value)
}

func TestStore_SetAndGetPodVolumeMetric(t *testing.T) {
	t.Parallel()

	now := time.Now()
	store := NewMetricStore()
	store.SetPodVolumeMetric("podUID", "volumeName", consts.MetricsPodVolumeAvailable, MetricData{Value: 1.0, Time: &now})
	value, err := store.GetPodVolumeMetric("podUID", "volumeName", consts.MetricsPodVolumeAvailable)
	assert.NoError(t, err)
	assert.Equal(t, MetricData{Value: 1.0, Time: &now}, value)
	_, err = store.GetPodVolumeMetric("podUID", "volumeName", consts.MetricsPodVolumeInodesUsed)
	assert.Error(t, err)
}

func TestStore_SetAndGetByStringIndex(t *testing.T) {
	t.Parallel()

	store := NewMetricStore()
	store.SetByStringIndex("podUID", "volumeName")
	value := store.GetByStringIndex("podUID")
	assert.Equal(t, "volumeName", value)
}

// TestStore_GCCPUMetricDropsStaleCPUSubtree is the RED regression for stale
// per-CPU metrics: a successful snapshot only writes the CPUs it observed this
// round, so a CPU that dropped out of the snapshot must have its whole subtree
// removed. Without GCCPUMetric the old value keeps being readable.
func TestStore_GCCPUMetricDropsStaleCPUSubtree(t *testing.T) {
	t.Parallel()

	now := time.Now()
	mk := func(v float64) MetricData { return MetricData{Value: v, Time: &now} }

	store := NewMetricStore()

	// round snapshot0: CPUs 0,1,2 all alive
	for _, cpu := range []int{0, 1, 2} {
		store.SetCPUMetric(cpu, consts.MetricCPUUsageRatio, mk(0.1))
		store.SetCPUMetric(cpu, consts.MetricCPUSchedwait, mk(1.0))
	}

	// round snapshot1: only CPUs 0,2 are alive. The successful snapshot builds
	// livingCPUSet={0,2} and GC's the whole subtree of every absent CPU.
	for _, cpu := range []int{0, 2} {
		store.SetCPUMetric(cpu, consts.MetricCPUUsageRatio, mk(0.2))
	}
	store.GCCPUMetric(map[int]bool{0: true, 2: true})

	// living CPUs remain readable
	for _, cpu := range []int{0, 2} {
		_, err := store.GetCPUMetric(cpu, consts.MetricCPUUsageRatio)
		assert.NoError(t, err)
	}

	// dropped CPU1: old usage AND schedwait are no longer readable
	_, err := store.GetCPUMetric(1, consts.MetricCPUUsageRatio)
	assert.Error(t, err)
	_, err = store.GetCPUMetric(1, consts.MetricCPUSchedwait)
	assert.Error(t, err)

	// the empty per-CPU entry is pruned from the map entirely
	store.mutex.RLock()
	_, ok := store.cpuMetricMap[1]
	store.mutex.RUnlock()
	assert.False(t, ok, "cpu1 entry should be removed by whole-subtree GC")
}

// TestStore_GCCPUMetricEmptyLivingIsValidSnapshot pins the contract that an
// empty living set is a valid snapshot-valid empty result (not a "did not
// run" signal): it must drop every per-CPU entry. An early return on empty
// would mask this. The protection against a *failed* snapshot wiping data lives
// at the caller (provisioner only invokes this after a successful fetch).
func TestStore_GCCPUMetricEmptyLivingIsValidSnapshot(t *testing.T) {
	t.Parallel()

	now := time.Now()
	store := NewMetricStore()
	store.SetCPUMetric(0, consts.MetricCPUUsageRatio, MetricData{Value: 0.1, Time: &now})
	store.SetCPUMetric(1, consts.MetricCPUUsageRatio, MetricData{Value: 0.2, Time: &now})

	store.GCCPUMetric(map[int]bool{})

	_, err := store.GetCPUMetric(0, consts.MetricCPUUsageRatio)
	assert.Error(t, err)
	_, err = store.GetCPUMetric(1, consts.MetricCPUUsageRatio)
	assert.Error(t, err)
}
