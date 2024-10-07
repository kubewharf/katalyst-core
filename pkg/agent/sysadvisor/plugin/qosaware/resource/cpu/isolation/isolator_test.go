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

package isolation

import (
	"fmt"
	"io/ioutil"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	katalyst_base "github.com/kubewharf/katalyst-core/cmd/base"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/sysadvisor/qosaware/resource/cpu"
	metric_consts "github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	metricspool "github.com/kubewharf/katalyst-core/pkg/metrics/metrics-pool"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	utilmetric "github.com/kubewharf/katalyst-core/pkg/util/metric"
)

func makeContainerInfo(podUID, namespace, podName, containerName, qoSLevel, ownerPoolName string,
	annotations map[string]string, topologyAwareAssignments types.TopologyAwareAssignment, req, limit float64,
) *types.ContainerInfo {
	return &types.ContainerInfo{
		PodUID:                           podUID,
		PodNamespace:                     namespace,
		PodName:                          podName,
		ContainerName:                    containerName,
		ContainerIndex:                   0,
		Labels:                           nil,
		Annotations:                      annotations,
		QoSLevel:                         qoSLevel,
		CPURequest:                       req,
		CPULimit:                         limit,
		MemoryRequest:                    0,
		MemoryLimit:                      0,
		RampUp:                           false,
		OriginOwnerPoolName:              ownerPoolName,
		TopologyAwareAssignments:         topologyAwareAssignments,
		OriginalTopologyAwareAssignments: topologyAwareAssignments,
	}
}

func TestLoadIsolator(t *testing.T) {
	t.Parallel()

	ckDir, err := ioutil.TempDir("", "checkpoint-TestLoadIsolator")
	require.NoError(t, err)
	defer func() { _ = os.RemoveAll(ckDir) }()

	sfDir, err := ioutil.TempDir("", "state")
	require.NoError(t, err)
	defer func() { _ = os.RemoveAll(sfDir) }()

	conf, err := options.NewOptions().Config()
	require.NoError(t, err)
	require.NotNil(t, conf)
	conf.GenericSysAdvisorConfiguration.StateFileDirectory = sfDir
	conf.MetaServerConfiguration.CheckpointManagerDir = ckDir

	metaCache, err := metacache.NewMetaCacheImp(conf, metricspool.DummyMetricsEmitterPool{}, metric.NewFakeMetricsFetcher(metrics.DummyMetrics{}))
	require.NoError(t, err)
	require.NotNil(t, metaCache)

	genericCtx, err := katalyst_base.GenerateFakeGenericContext([]runtime.Object{})
	require.NoError(t, err)

	metaServer, err := metaserver.NewMetaServer(genericCtx.Client, metrics.DummyMetrics{}, conf)
	require.NoError(t, err)

	// construct pods
	pods := []*v1.Pod{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "pod1",
				Namespace: "default",
				UID:       "uid1",
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "pod2",
				Namespace: "default",
				UID:       "uid2",
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "pod3",
				Namespace: "default",
				UID:       "uid3",
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "pod4",
				Namespace: "default",
				UID:       "uid4",
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "pod5",
				Namespace: "default",
				UID:       "uid5",
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "pod6",
				Namespace: "default",
				UID:       "uid6",
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "pod7",
				Namespace: "default",
				UID:       "uid7",
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "pod8",
				Namespace: "default",
				UID:       "uid8",
			},
		},
	}

	// construct metrics fetcher
	metricFetcher := metric.NewFakeMetricsFetcher(metrics.DummyMetrics{}).(*metric.FakeMetricsFetcher)
	metaServer.MetaAgent = &agent.MetaAgent{
		PodFetcher: &pod.PodFetcherStub{
			PodList: pods,
		},
		MetricsFetcher: metricFetcher,
	}

	// construct containers
	containers := []*types.ContainerInfo{
		makeContainerInfo("uid1", "default", "pod1", "c1-1",
			consts.PodAnnotationQoSLevelSharedCores, commonstate.PoolNameShare, nil, map[int]machine.CPUSet{}, 0, 4),
		makeContainerInfo("uid1", "default", "pod1", "c1-2",
			consts.PodAnnotationQoSLevelSharedCores, commonstate.PoolNameShare, nil, map[int]machine.CPUSet{}, 4, 0),

		makeContainerInfo("uid2", "default", "pod2", "c2-1",
			consts.PodAnnotationQoSLevelSharedCores, "batch", nil, map[int]machine.CPUSet{}, 0, 4),
		makeContainerInfo("uid2", "default", "pod2", "c2-2",
			consts.PodAnnotationQoSLevelSharedCores, "batch", nil, map[int]machine.CPUSet{}, 4, 0),
		makeContainerInfo("uid2", "default", "pod2", "c2-3",
			consts.PodAnnotationQoSLevelSharedCores, "batch", nil, map[int]machine.CPUSet{}, 0, 4),

		makeContainerInfo("uid3", "default", "pod3", "c3-1",
			consts.PodAnnotationQoSLevelSharedCores, "flink", nil, map[int]machine.CPUSet{}, 4, 0),
		makeContainerInfo("uid3", "default", "pod3", "c3-2",
			consts.PodAnnotationQoSLevelSharedCores, "flink", nil, map[int]machine.CPUSet{}, 0, 4),

		makeContainerInfo("uid4", "default", "pod4", "c4-1",
			consts.PodAnnotationQoSLevelDedicatedCores, "", nil, map[int]machine.CPUSet{}, 4, 4),

		makeContainerInfo("uid5", "default", "pod5", "c5-1",
			consts.PodAnnotationQoSLevelSharedCores, commonstate.PoolNameShare, nil, map[int]machine.CPUSet{}, 0, 4),
		makeContainerInfo("uid5", "default", "pod5", "c5-2",
			consts.PodAnnotationQoSLevelSharedCores, commonstate.PoolNameShare, nil, map[int]machine.CPUSet{}, 0, 4),

		makeContainerInfo("uid6", "default", "pod6", "c6-1",
			consts.PodAnnotationQoSLevelSharedCores, commonstate.PoolNameShare, nil, map[int]machine.CPUSet{}, 0, 4),
		makeContainerInfo("uid6", "default", "pod6", "c6-2",
			consts.PodAnnotationQoSLevelSharedCores, commonstate.PoolNameShare, nil, map[int]machine.CPUSet{}, 0, 4),
		makeContainerInfo("uid6", "default", "pod6", "c6-3",
			consts.PodAnnotationQoSLevelSharedCores, commonstate.PoolNameShare, nil, map[int]machine.CPUSet{}, 4, 0),

		makeContainerInfo("uid7", "default", "pod7", "c7-1",
			consts.PodAnnotationQoSLevelSharedCores, "batch", nil, map[int]machine.CPUSet{}, 0, 4),

		makeContainerInfo("uid8", "default", "pod8", "c8-1",
			consts.PodAnnotationQoSLevelSharedCores, "batch", nil, map[int]machine.CPUSet{}, 0, 4),
	}
	for _, c := range containers {
		err := metaCache.SetContainerInfo(c.PodUID, c.ContainerName, c)
		assert.NoError(t, err)
	}

	// construct pools
	pools := map[string]*types.PoolInfo{
		commonstate.PoolNameReserve: {},
		commonstate.PoolNameShare:   {},
		"batch":                     {},
		"flink":                     {},
	}
	for poolName, poolInfo := range pools {
		err := metaCache.SetPoolInfo(poolName, poolInfo)
		assert.NoError(t, err)
	}

	now := time.Now()

	metricFetcher.SetContainerMetric("uid1", "c1-1", metric_consts.MetricCPUNrRunnableContainer, utilmetric.MetricData{Value: 3, Time: &now})
	metricFetcher.SetContainerMetric("uid1", "c1-2", metric_consts.MetricCPUNrRunnableContainer, utilmetric.MetricData{Value: 2.0, Time: &now})

	metricFetcher.SetContainerMetric("uid2", "c2-1", metric_consts.MetricCPUNrRunnableContainer, utilmetric.MetricData{Value: 6, Time: &now})
	metricFetcher.SetContainerMetric("uid2", "c2-2", metric_consts.MetricCPUNrRunnableContainer, utilmetric.MetricData{Value: 2, Time: &now})
	metricFetcher.SetContainerMetric("uid2", "c2-3", metric_consts.MetricCPUNrRunnableContainer, utilmetric.MetricData{Value: 7, Time: &now})

	metricFetcher.SetContainerMetric("uid3", "c3-1", metric_consts.MetricCPUNrRunnableContainer, utilmetric.MetricData{Value: 2, Time: &now})
	metricFetcher.SetContainerMetric("uid3", "c3-2", metric_consts.MetricCPUNrRunnableContainer, utilmetric.MetricData{Value: 4.1, Time: &now})

	metricFetcher.SetContainerMetric("uid4", "c4-1", metric_consts.MetricCPUNrRunnableContainer, utilmetric.MetricData{Value: 18, Time: &now})

	metricFetcher.SetContainerMetric("uid5", "c5-1", metric_consts.MetricCPUNrRunnableContainer, utilmetric.MetricData{Value: 5.1, Time: &now})
	metricFetcher.SetContainerMetric("uid5", "c5-2", metric_consts.MetricCPUNrRunnableContainer, utilmetric.MetricData{Value: 8, Time: &now})

	metricFetcher.SetContainerMetric("uid6", "c6-1", metric_consts.MetricCPUNrRunnableContainer, utilmetric.MetricData{Value: 9, Time: &now})
	metricFetcher.SetContainerMetric("uid6", "c6-2", metric_consts.MetricCPUNrRunnableContainer, utilmetric.MetricData{Value: 7, Time: &now})
	metricFetcher.SetContainerMetric("uid6", "c6-3", metric_consts.MetricCPUNrRunnableContainer, utilmetric.MetricData{Value: 4, Time: &now})

	metricFetcher.SetContainerMetric("uid7", "c7-1", metric_consts.MetricCPUNrRunnableContainer, utilmetric.MetricData{Value: 2.1, Time: &now})

	metricFetcher.SetContainerMetric("uid8", "c8-1", metric_consts.MetricCPUNrRunnableContainer, utilmetric.MetricData{Value: 1.1, Time: &now})

	for _, tc := range []struct {
		comment string
		conf    *cpu.CPUIsolationConfiguration
		expects []string
	}{
		{
			comment: "lock-in: disable all pools",
			conf: &cpu.CPUIsolationConfiguration{
				IsolatedMaxPoolResourceRatios: map[string]float32{},
				IsolationDisabled:             true,
			},
			expects: []string{},
		},
		{
			comment: "lock-in: disable shared pool",
			conf: &cpu.CPUIsolationConfiguration{
				IsolationCPURatio:             1,
				IsolationCPUSize:              0,
				IsolationLockInThreshold:      1,
				IsolationLockOutPeriodSecs:    1,
				IsolatedMaxResourceRatio:      1,
				IsolatedMaxPoolResourceRatios: map[string]float32{},
				IsolatedMaxPodRatio:           1,
				IsolationDisabled:             false,
				IsolationDisabledPools:        sets.NewString(commonstate.PoolNameShare),
			},
			expects: []string{"uid2"},
		},
		{
			comment: "lock-in: overload for 4",
			conf: &cpu.CPUIsolationConfiguration{
				IsolationCPURatio:             1,
				IsolationCPUSize:              0,
				IsolationLockInThreshold:      1,
				IsolationLockOutPeriodSecs:    1,
				IsolatedMaxResourceRatio:      1,
				IsolatedMaxPoolResourceRatios: map[string]float32{},
				IsolatedMaxPodRatio:           1,
				IsolationDisabled:             false,
				IsolationDisabledPools:        sets.NewString(),
			},
			expects: []string{"uid2", "uid5", "uid6"},
		},
		{
			comment: "lock-in: overload for 5",
			conf: &cpu.CPUIsolationConfiguration{
				IsolationCPURatio:             1.3,
				IsolationCPUSize:              1,
				IsolationLockInThreshold:      1,
				IsolationLockOutPeriodSecs:    1,
				IsolatedMaxResourceRatio:      1,
				IsolatedMaxPoolResourceRatios: map[string]float32{},
				IsolatedMaxPodRatio:           1,
				IsolationDisabled:             false,
				IsolationDisabledPools:        sets.NewString(),
			},
			expects: []string{"uid2", "uid5", "uid6"},
		},
		{
			comment: "lock-in: overload for 8",
			conf: &cpu.CPUIsolationConfiguration{
				IsolationCPURatio:             5,
				IsolationCPUSize:              4,
				IsolationLockInThreshold:      1,
				IsolationLockOutPeriodSecs:    1,
				IsolatedMaxResourceRatio:      1,
				IsolatedMaxPoolResourceRatios: map[string]float32{},
				IsolatedMaxPodRatio:           1,
				IsolationDisabled:             false,
				IsolationDisabledPools:        sets.NewString(),
			},
			expects: []string{"uid6"},
		},
		{
			comment: "lock-in: not-overload because of threshold",
			conf: &cpu.CPUIsolationConfiguration{
				IsolationCPURatio:             1,
				IsolationCPUSize:              0,
				IsolationLockInThreshold:      2,
				IsolationLockOutPeriodSecs:    1,
				IsolatedMaxResourceRatio:      1,
				IsolatedMaxPoolResourceRatios: map[string]float32{},
				IsolatedMaxPodRatio:           1,
				IsolationDisabled:             false,
				IsolationDisabledPools:        sets.NewString(),
			},
			expects: []string{},
		},
		{
			comment: "lock-in: only support to use 0.4",
			conf: &cpu.CPUIsolationConfiguration{
				IsolationCPURatio:             1,
				IsolationCPUSize:              0,
				IsolationLockInThreshold:      1,
				IsolationLockOutPeriodSecs:    1,
				IsolatedMaxResourceRatio:      0.4,
				IsolatedMaxPoolResourceRatios: map[string]float32{},
				IsolatedMaxPodRatio:           1,
				IsolationDisabled:             false,
				IsolationDisabledPools:        sets.NewString(),
			},
			expects: []string{"uid5"},
		},
		{
			comment: "lock-in: only support to use 0.6",
			conf: &cpu.CPUIsolationConfiguration{
				IsolationCPURatio:             1,
				IsolationCPUSize:              0,
				IsolationLockInThreshold:      1,
				IsolationLockOutPeriodSecs:    1,
				IsolatedMaxResourceRatio:      0.6,
				IsolatedMaxPoolResourceRatios: map[string]float32{},
				IsolatedMaxPodRatio:           1,
				IsolationDisabled:             false,
				IsolationDisabledPools:        sets.NewString(),
			},
			expects: []string{"uid2", "uid6"},
		},
		{
			comment: "lock-in: only support to use 0.8",
			conf: &cpu.CPUIsolationConfiguration{
				IsolationCPURatio:             1,
				IsolationCPUSize:              0,
				IsolationLockInThreshold:      1,
				IsolationLockOutPeriodSecs:    1,
				IsolatedMaxResourceRatio:      0.8,
				IsolatedMaxPoolResourceRatios: map[string]float32{},
				IsolatedMaxPodRatio:           1,
				IsolationDisabled:             false,
				IsolationDisabledPools:        sets.NewString(),
			},
			expects: []string{"uid2", "uid5", "uid6"},
		},
	} {
		t.Logf("test cases: %v", tc.comment)

		conf.CPUIsolationConfiguration = tc.conf
		loader := NewLoadIsolator(conf, struct{}{}, metrics.DummyMetrics{}, metaCache, metaServer)

		res := loader.GetIsolatedPods()
		assert.EqualValues(t, tc.expects, res)
	}

	for i := range containers {
		containers[i].Isolated = true

		err := metaCache.SetContainerInfo(containers[i].PodUID, containers[i].ContainerName, containers[i])
		assert.NoError(t, err)
	}

	for _, tc := range []struct {
		comment string
		conf    *cpu.CPUIsolationConfiguration
		expects []string
	}{
		{
			comment: "lock-out: all isolations lock out",
			conf: &cpu.CPUIsolationConfiguration{
				IsolationCPURatio:             30,
				IsolationCPUSize:              30,
				IsolationLockInThreshold:      1,
				IsolatedMaxResourceRatio:      1,
				IsolatedMaxPoolResourceRatios: map[string]float32{},
				IsolatedMaxPodRatio:           1,
				IsolationDisabled:             false,
				IsolationDisabledPools:        sets.NewString(),
				IsolationLockOutPeriodSecs:    0,
			},
			expects: []string{},
		},
		{
			comment: "lock-out: lock out for isolation",
			conf: &cpu.CPUIsolationConfiguration{
				IsolationCPURatio:             1,
				IsolationCPUSize:              1,
				IsolationLockInThreshold:      1,
				IsolatedMaxResourceRatio:      1,
				IsolatedMaxPoolResourceRatios: map[string]float32{},
				IsolatedMaxPodRatio:           1,
				IsolationDisabled:             false,
				IsolationDisabledPools:        sets.NewString(),
				IsolationLockOutPeriodSecs:    0,
			},
			expects: []string{"uid2", "uid5", "uid6"},
		},
		{
			comment: "lock-out: keep all isolations",
			conf: &cpu.CPUIsolationConfiguration{
				IsolationCPURatio:             30,
				IsolationCPUSize:              30,
				IsolationLockInThreshold:      1,
				IsolatedMaxResourceRatio:      1,
				IsolatedMaxPoolResourceRatios: map[string]float32{},
				IsolatedMaxPodRatio:           1,
				IsolationDisabled:             false,
				IsolationDisabledPools:        sets.NewString(),
				IsolationLockOutPeriodSecs:    20,
			},
			expects: []string{"uid5", "uid6", "uid7", "uid8"},
		},
	} {
		t.Logf("test cases: %v", tc.comment)

		conf.CPUIsolationConfiguration = tc.conf
		loader := NewLoadIsolator(conf, struct{}{}, metrics.DummyMetrics{}, metaCache, metaServer)

		now := time.Now()
		for i := 0; i < 8; i++ {
			loader.(*LoadIsolator).states.Store(fmt.Sprintf("uid%v", i), containerIsolationState{
				lockedOutFirstObserved: &now,
			})
		}
		time.Sleep(time.Millisecond * 10)

		res := loader.GetIsolatedPods()
		assert.EqualValues(t, tc.expects, res)
	}
}
