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
