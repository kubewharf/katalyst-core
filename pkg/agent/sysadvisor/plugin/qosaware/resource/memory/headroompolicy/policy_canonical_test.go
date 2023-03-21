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

package headroompolicy

import (
	"io/ioutil"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

const (
	containerEstimationMemoryFallback int64 = 8 << 30
)

func generateTestConfiguration(t *testing.T) *config.Configuration {
	conf, err := options.NewOptions().Config()
	require.NoError(t, err)
	require.NotNil(t, conf)

	tmpStateDir, err := ioutil.TempDir("", "sys-advisor-test")
	require.NoError(t, err)

	conf.GenericSysAdvisorConfiguration.StateFileDirectory = tmpStateDir

	return conf
}

func TestNewPolicyCanonical(t *testing.T) {
	metaCache, err := metacache.NewMetaCache(generateTestConfiguration(t), metric.NewFakeMetricsFetcher(metrics.DummyMetrics{}))
	require.NoError(t, err)
	require.NotNil(t, metaCache)

	policy := NewPolicyCanonical(metaCache, nil)
	require.NotNil(t, policy)
}

const (
	defaultMemoryTotal    = 512 << 30
	defaultMemoryReserved = 2 << 30
)

func TestGetHeadroom(t *testing.T) {
	tests := []struct {
		name           string
		containers     map[string]map[string]*types.ContainerInfo
		metrics        map[string]map[string]map[string]float64
		totalMemory    int64
		reservedMemory int64
		want           int64
	}{
		{
			name: "reference rss",
			containers: map[string]map[string]*types.ContainerInfo{
				"pod-0": {
					"ctn-0": {
						PodUID:        "pod-0",
						PodName:       "pod-0",
						ContainerName: "ctn-0",
						QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
						MemoryRequest: 10 << 30,
					},
				},
				"pod-1": {
					"ctn-1": {
						PodUID:        "pod-1",
						PodName:       "pod-1",
						ContainerName: "ctn-1",
						QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
						MemoryRequest: 20 << 30,
					},
				},
				"pod-2": {
					"ctn-2": {
						PodUID:        "pod-2",
						PodName:       "pod-2",
						ContainerName: "ctn-2",
						QoSLevel:      apiconsts.PodAnnotationQoSLevelReclaimedCores,
						MemoryRequest: 40 << 30,
					},
				},
			},
			metrics: map[string]map[string]map[string]float64{
				"pod-0": {
					"ctn-0": {
						consts.MetricMemRssContainer: 1 << 30,
					},
				},
				"pod-1": {
					"ctn-1": {
						consts.MetricMemRssContainer: 2 << 30,
					},
				},
				"pod-2": {
					"ctn-2": {
						consts.MetricMemRssContainer: 4 << 30,
					},
				},
			},
			totalMemory:    defaultMemoryTotal,
			reservedMemory: defaultMemoryReserved,
			want:           defaultMemoryTotal - defaultMemoryReserved - 3<<30,
		},
		{
			name: "reference request",
			containers: map[string]map[string]*types.ContainerInfo{
				"pod-0": {
					"ctn-0": {
						PodUID:        "pod-0",
						PodName:       "pod-0",
						ContainerName: "ctn-0",
						QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
						MemoryRequest: 1 << 30,
					},
				},
			},
			metrics: map[string]map[string]map[string]float64{
				"pod-0": {
					"ctn-0": {
						consts.MetricMemRssContainer: 0,
					},
				},
			},
			totalMemory:    defaultMemoryTotal,
			reservedMemory: defaultMemoryReserved,
			want:           defaultMemoryTotal - defaultMemoryReserved - 1<<30,
		},
		{
			name: "reference fallback",
			containers: map[string]map[string]*types.ContainerInfo{
				"pod-0": {
					"ctn-0": {
						PodUID:        "pod-0",
						PodName:       "pod-0",
						ContainerName: "ctn-0",
						QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
						MemoryRequest: 0,
					},
				},
			},
			metrics: map[string]map[string]map[string]float64{
				"pod-0": {
					"ctn-0": {
						consts.MetricMemRssContainer: 0,
					},
				},
			},
			totalMemory:    defaultMemoryTotal,
			reservedMemory: defaultMemoryReserved,
			want:           defaultMemoryTotal - defaultMemoryReserved - containerEstimationMemoryFallback,
		},
	}

	metricsToGather := []string{
		consts.MetricMemRssContainer,
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fakeMetricsFetcher := metric.NewFakeMetricsFetcher(metrics.DummyMetrics{}).(*metric.FakeMetricsFetcher)
			assert.NotNil(t, fakeMetricsFetcher)

			metaCache, err := metacache.NewMetaCache(generateTestConfiguration(t), metric.NewFakeMetricsFetcher(metrics.DummyMetrics{}))
			assert.NoError(t, err)

			for podUID, podInfo := range tt.containers {
				for containerName, containerInfo := range podInfo {
					for _, metricName := range metricsToGather {
						metricValue, ok := tt.metrics[podUID][containerName][metricName]
						assert.True(t, ok)
						metaCache.SetContainerInfo(podUID, containerName, containerInfo)
						fakeMetricsFetcher.SetContainerMetric(podUID, containerName, metricName, metricValue)
					}
				}
			}

			policy := NewPolicyCanonical(metaCache, nil)
			assert.NotNil(t, policy)

			policy.SetMemory(tt.totalMemory, tt.reservedMemory)
			policy.Update()
			provision, err := policy.GetHeadroom()
			assert.NoError(t, err)

			assert.Equal(t, tt.want, provision.Value())
		})
	}
}
