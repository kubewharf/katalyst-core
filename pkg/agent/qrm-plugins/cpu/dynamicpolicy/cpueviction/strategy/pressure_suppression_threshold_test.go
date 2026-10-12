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

package strategy

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/uuid"
	resourcepluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"
	maputil "k8s.io/kubernetes/pkg/util/maps"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	evictionpluginapi "github.com/kubewharf/katalyst-api/pkg/protocol/evictionplugin/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	qrmstate "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	cpuutil "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/util"
	"github.com/kubewharf/katalyst-core/pkg/config"
	pkgconsts "github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	utilmetric "github.com/kubewharf/katalyst-core/pkg/util/metric"
)

const (
	testSuppressionUsageRingSize   = 3
	testSuppressionUsagePercentage = 0.5
	testSuppressionSoftRate        = 5
	testSuppressionSoftUsage       = 0.8
	testSuppressionHardRate        = 10
	testSuppressionHardUsage       = 0.01
)

// reclaimPoolCPUSet is the cpuset of the reclaim pool in the tests, 10 cpus in total.
var reclaimPoolCPUSet = machine.MustParse("1,3-6,9,11-14")

// makeSuppressionUsageCPUPressureEvictionConf returns a configuration with the suppression
// pressure eviction channel enabled (independent from the GetEvictPods path).
func makeSuppressionUsageCPUPressureEvictionConf() *config.Configuration {
	conf := config.NewConfiguration()
	conf.ReclaimRelativeRootCgroupPath = "test"

	supConf := &conf.GetDynamicConfiguration().CPUPressureEvictionConfiguration.SuppressionUsageCPUPressureEvictionConfiguration
	supConf.EnableSuppressionUsageEviction = true
	supConf.SyncPeriod = 15
	supConf.MetricRingSize = testSuppressionUsageRingSize
	supConf.ThresholdMetPercentage = testSuppressionUsagePercentage
	supConf.SoftSuppressionRateThreshold = testSuppressionSoftRate
	supConf.SoftCPUUsageThreshold = testSuppressionSoftUsage
	supConf.HardSuppressionRateThreshold = testSuppressionHardRate
	supConf.HardCPUUsageThreshold = testSuppressionHardUsage
	supConf.PodCPUUsageEvictionThreshold = 0
	supConf.GracePeriod = 30
	return conf
}

// makeSuppressionReclaimAllocation returns a reclaim pod allocation info with
// the given request cores and suppression tolerance rate annotation.
func makeSuppressionReclaimAllocation(podUID, podName string, requestCores float64, toleranceRate string) *qrmstate.AllocationInfo {
	return &qrmstate.AllocationInfo{
		AllocationMeta: commonstate.AllocationMeta{
			PodUid:         podUID,
			PodNamespace:   podName,
			PodName:        podName,
			ContainerName:  podName,
			ContainerType:  resourcepluginapi.ContainerType_MAIN.String(),
			ContainerIndex: 0,
			OwnerPoolName:  commonstate.PoolNameReclaim,
			Labels: map[string]string{
				apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelReclaimedCores,
			},
			Annotations: map[string]string{
				apiconsts.PodAnnotationQoSLevelKey:       apiconsts.PodAnnotationQoSLevelReclaimedCores,
				apiconsts.PodAnnotationCPUEnhancementKey: `{"suppression_tolerance_rate": "` + toleranceRate + `"}`,
			},
			QoSLevel: apiconsts.PodAnnotationQoSLevelReclaimedCores,
		},
		RampUp:                   false,
		AllocationResult:         reclaimPoolCPUSet,
		OriginalAllocationResult: reclaimPoolCPUSet,
		TopologyAwareAssignments: map[int]machine.CPUSet{
			0: machine.NewCPUSet(1, 9),
			1: machine.NewCPUSet(3, 11),
			2: machine.NewCPUSet(4, 5, 11, 12),
			3: machine.NewCPUSet(6, 14),
		},
		OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
			0: machine.NewCPUSet(1, 9),
			1: machine.NewCPUSet(3, 11),
			2: machine.NewCPUSet(4, 5, 11, 12),
			3: machine.NewCPUSet(6, 14),
		},
		RequestQuantity: requestCores,
	}
}

// makeSuppressionReclaimPod builds a pod object from the allocation info.
func makeSuppressionReclaimPod(entry *qrmstate.AllocationInfo) *v1.Pod {
	return &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			UID:         types.UID(entry.PodUid),
			Name:        entry.PodName,
			Namespace:   entry.PodNamespace,
			Annotations: maputil.CopySS(entry.Annotations),
			Labels:      maputil.CopySS(entry.Labels),
		},
		Spec: v1.PodSpec{
			Containers: []v1.Container{
				{
					Name: entry.ContainerName,
					Resources: v1.ResourceRequirements{
						Requests: v1.ResourceList{
							apiconsts.ReclaimedResourceMilliCPU: *resource.NewQuantity(int64(entry.RequestQuantity*1000), resource.DecimalSI),
						},
						Limits: v1.ResourceList{
							apiconsts.ReclaimedResourceMilliCPU: *resource.NewQuantity(int64(entry.RequestQuantity*1000), resource.DecimalSI),
						},
					},
				},
			},
		},
	}
}

// setupSuppressionUsageCPUPressureEvictionPlugin builds the plugin with the given pods and
// the reclaim pool allocation, and returns the plugin, the metrics store and
// the pod entries.
func setupSuppressionUsageCPUPressureEvictionPlugin(t *testing.T, conf *config.Configuration, podEntries ...*qrmstate.AllocationInfo,
) (*CPUPressureSuppression, *metric.FakeMetricsFetcher, []*qrmstate.AllocationInfo) {
	t.Helper()

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	require.NoError(t, err)

	pods := make([]*v1.Pod, 0, len(podEntries))
	for _, entry := range podEntries {
		pods = append(pods, makeSuppressionReclaimPod(entry))
	}

	metricsFetcher := metric.NewFakeMetricsFetcher(metrics.DummyMetrics{})
	store := metricsFetcher.(*metric.FakeMetricsFetcher)

	metaServer := makeMetaServerWithPodList(metricsFetcher, cpuTopology, pods)
	stateImpl, err := makeState(cpuTopology)
	require.NoError(t, err)

	plugin, err := NewCPUPressureSuppressionEviction(metrics.DummyMetrics{}, metaServer, conf, stateImpl)
	require.NoError(t, err)
	require.NotNil(t, plugin)

	p := plugin.(*CPUPressureSuppression)
	p.state = stateImpl

	poolEntry := &qrmstate.AllocationInfo{
		AllocationMeta:           commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
		AllocationResult:         reclaimPoolCPUSet,
		OriginalAllocationResult: reclaimPoolCPUSet,
		TopologyAwareAssignments: map[int]machine.CPUSet{
			0: machine.NewCPUSet(1, 9),
			1: machine.NewCPUSet(3, 11),
			2: machine.NewCPUSet(4, 5, 11, 12),
			3: machine.NewCPUSet(6, 14),
		},
		OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
			0: machine.NewCPUSet(1, 9),
			1: machine.NewCPUSet(3, 11),
			2: machine.NewCPUSet(4, 5, 11, 12),
			3: machine.NewCPUSet(6, 14),
		},
	}
	stateImpl.SetAllocationInfo(commonstate.PoolNameReclaim, "", poolEntry, true)
	for _, entry := range podEntries {
		stateImpl.SetAllocationInfo(entry.PodUid, entry.ContainerName, entry, true)
	}

	return p, store, podEntries
}

// setReclaimPoolMetrics sets the pool cpu usage ratio and the reclaim cgroup
// metrics used by helper.GetReclaimMetrics.
func setReclaimPoolMetrics(store *metric.FakeMetricsFetcher, now time.Time, cgroupUsage, cfsQuota float64) {
	for _, cpuID := range reclaimPoolCPUSet.ToSliceNoSortInt() {
		store.SetCPUMetric(cpuID, pkgconsts.MetricCPUUsageRatio, utilmetric.MetricData{Value: 0.5, Time: &now})
	}
	store.SetCgroupMetric("test", pkgconsts.MetricCPUUsageCgroup, utilmetric.MetricData{Value: cgroupUsage, Time: &now})
	store.SetCgroupMetric("test", pkgconsts.MetricCPUQuotaCgroup, utilmetric.MetricData{Value: cfsQuota, Time: &now})
	store.SetCgroupMetric("test", pkgconsts.MetricCPUPeriodCgroup, utilmetric.MetricData{Value: 1000, Time: &now})
}

// runSuppressionUsageSyncs runs sync enough times to fill the metric ring.
func runSuppressionUsageSyncs(p *CPUPressureSuppression, times int) {
	for i := 0; i < times; i++ {
		p.sync(context.TODO())
		time.Sleep(time.Millisecond)
	}
}

func TestCPUPressureSuppression_ThresholdMet_Disabled(t *testing.T) {
	t.Parallel()

	conf := makeSuppressionUsageCPUPressureEvictionConf()
	conf.GetDynamicConfiguration().CPUPressureEvictionConfiguration.SuppressionUsageCPUPressureEvictionConfiguration.EnableSuppressionUsageEviction = false

	pod1Entry := makeSuppressionReclaimAllocation(string(uuid.NewUUID()), "pod-1", 15, "1.2")
	p, store, _ := setupSuppressionUsageCPUPressureEvictionPlugin(t, conf, pod1Entry)
	setReclaimPoolMetrics(store, time.Now(), 8, 1000)

	runSuppressionUsageSyncs(p, testSuppressionUsageRingSize)

	resp, err := p.ThresholdMet(context.TODO(), &evictionpluginapi.GetThresholdMetRequest{})
	require.NoError(t, err)
	assert.Equal(t, evictionpluginapi.ThresholdMetType_NOT_MET, resp.MetType)

	topResp, err := p.GetTopEvictionPods(context.TODO(), &evictionpluginapi.GetTopEvictionPodsRequest{
		ActivePods: []*v1.Pod{makeSuppressionReclaimPod(pod1Entry)},
		TopN:       1,
	})
	require.NoError(t, err)
	assert.Empty(t, topResp.TargetPods)
}

func TestCPUPressureSuppression_ThresholdMet_Debounce(t *testing.T) {
	t.Parallel()

	conf := makeSuppressionUsageCPUPressureEvictionConf()
	pod1Entry := makeSuppressionReclaimAllocation(string(uuid.NewUUID()), "pod-1", 15, "1.2")
	p, store, _ := setupSuppressionUsageCPUPressureEvictionPlugin(t, conf, pod1Entry)

	// pool cpu usage ratio 0.5 on 10 cpus, cgroup usage 8 with quota 1000/1000,
	// supply = min(max(10-5,0)+8, 1) = 1, rate = 19/1 = 19, usage ratio = 8/1 = 8.
	setReclaimPoolMetrics(store, time.Now(), 8, 1000)

	// only one sample pushed, 1/3 < 0.5, not sustained yet
	runSuppressionUsageSyncs(p, 1)

	resp, err := p.ThresholdMet(context.TODO(), &evictionpluginapi.GetThresholdMetRequest{})
	require.NoError(t, err)
	assert.Equal(t, evictionpluginapi.ThresholdMetType_NOT_MET, resp.MetType)
}

func TestCPUPressureSuppression_ThresholdMet_SoftAndHard(t *testing.T) {
	t.Parallel()

	pod1Entry := makeSuppressionReclaimAllocation(string(uuid.NewUUID()), "pod-1", 15, "1.2")
	pod2Entry := makeSuppressionReclaimAllocation(string(uuid.NewUUID()), "pod-2", 4, "1.2")

	tests := []struct {
		name          string
		cgroupUsage   float64
		cfsQuota      float64
		wantMetType   evictionpluginapi.ThresholdMetType
		wantThreshold float64
	}{
		{
			name:          "soft met",
			cgroupUsage:   8,
			cfsQuota:      2000,
			wantMetType:   evictionpluginapi.ThresholdMetType_SOFT_MET,
			wantThreshold: testSuppressionSoftRate,
		},
		{
			name:          "hard met",
			cgroupUsage:   8,
			cfsQuota:      1000,
			wantMetType:   evictionpluginapi.ThresholdMetType_HARD_MET,
			wantThreshold: testSuppressionHardRate,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			conf := makeSuppressionUsageCPUPressureEvictionConf()
			p, store, _ := setupSuppressionUsageCPUPressureEvictionPlugin(t, conf, pod1Entry, pod2Entry)

			// supply = min(max(10-5,0)+8, quota/1000)
			//   quota 2000 -> supply 2, rate = 19/2 = 9.5 (soft over, hard not)
			//   quota 1000 -> supply 1, rate = 19/1 = 19 (soft and hard over)
			setReclaimPoolMetrics(store, time.Now(), tt.cgroupUsage, tt.cfsQuota)
			runSuppressionUsageSyncs(p, testSuppressionUsageRingSize)

			resp, err := p.ThresholdMet(context.TODO(), &evictionpluginapi.GetThresholdMetRequest{})
			require.NoError(t, err)
			assert.Equal(t, tt.wantMetType, resp.MetType)
			assert.Equal(t, tt.wantThreshold, resp.ThresholdValue)
			assert.Greater(t, resp.ObservedValue, tt.wantThreshold)
			assert.Equal(t, evictionScopeSuppressionUsage, resp.EvictionScope)
			assert.Nil(t, resp.Condition)
		})
	}
}

func TestCPUPressureSuppression_ThresholdMet_DualGate(t *testing.T) {
	t.Parallel()

	conf := makeSuppressionUsageCPUPressureEvictionConf()
	// both pods request 30 cores, total 60
	pod1Entry := makeSuppressionReclaimAllocation(string(uuid.NewUUID()), "pod-1", 30, "1.2")
	pod2Entry := makeSuppressionReclaimAllocation(string(uuid.NewUUID()), "pod-2", 30, "1.2")
	p, store, _ := setupSuppressionUsageCPUPressureEvictionPlugin(t, conf, pod1Entry, pod2Entry)

	// supply = min(max(10-5,0)+5, 20) = 10, rate = 60/10 = 6 (> soft rate, < hard rate),
	// usage ratio = 5/10 = 0.5 (< soft usage gate): the AND condition is not met
	setReclaimPoolMetrics(store, time.Now(), 5, 20000)
	runSuppressionUsageSyncs(p, testSuppressionUsageRingSize)

	resp, err := p.ThresholdMet(context.TODO(), &evictionpluginapi.GetThresholdMetRequest{})
	require.NoError(t, err)
	assert.Equal(t, evictionpluginapi.ThresholdMetType_NOT_MET, resp.MetType)
}

func TestCPUPressureSuppression_GetTopEvictionPods(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name               string
		setContainerMetric func(store *metric.FakeMetricsFetcher, now time.Time, pod1Entry, pod2Entry *qrmstate.AllocationInfo)
		setTolerance       bool
		topN               uint64
		wantTargetPods     []string
	}{
		{
			name: "top1 by actual cpu usage descending",
			setContainerMetric: func(store *metric.FakeMetricsFetcher, now time.Time, pod1Entry, pod2Entry *qrmstate.AllocationInfo) {
				store.SetContainerMetric(pod1Entry.PodUid, pod1Entry.ContainerName, pkgconsts.MetricCPUUsageContainer, utilmetric.MetricData{Value: 3, Time: &now})
				store.SetContainerMetric(pod2Entry.PodUid, pod2Entry.ContainerName, pkgconsts.MetricCPUUsageContainer, utilmetric.MetricData{Value: 6, Time: &now})
			},
			topN:           1,
			wantTargetPods: []string{"pod-2"},
		},
		{
			name: "topN zero returns top1 as soft candidate",
			setContainerMetric: func(store *metric.FakeMetricsFetcher, now time.Time, pod1Entry, pod2Entry *qrmstate.AllocationInfo) {
				store.SetContainerMetric(pod1Entry.PodUid, pod1Entry.ContainerName, pkgconsts.MetricCPUUsageContainer, utilmetric.MetricData{Value: 3, Time: &now})
				store.SetContainerMetric(pod2Entry.PodUid, pod2Entry.ContainerName, pkgconsts.MetricCPUUsageContainer, utilmetric.MetricData{Value: 6, Time: &now})
			},
			topN:           0,
			wantTargetPods: []string{"pod-2"},
		},
		{
			name: "topN larger than candidates returns all",
			setContainerMetric: func(store *metric.FakeMetricsFetcher, now time.Time, pod1Entry, pod2Entry *qrmstate.AllocationInfo) {
				store.SetContainerMetric(pod1Entry.PodUid, pod1Entry.ContainerName, pkgconsts.MetricCPUUsageContainer, utilmetric.MetricData{Value: 3, Time: &now})
				store.SetContainerMetric(pod2Entry.PodUid, pod2Entry.ContainerName, pkgconsts.MetricCPUUsageContainer, utilmetric.MetricData{Value: 6, Time: &now})
			},
			topN:           5,
			wantTargetPods: []string{"pod-2", "pod-1"},
		},
		{
			name: "idle pod excluded by per-pod usage threshold",
			setContainerMetric: func(store *metric.FakeMetricsFetcher, now time.Time, pod1Entry, pod2Entry *qrmstate.AllocationInfo) {
				store.SetContainerMetric(pod1Entry.PodUid, pod1Entry.ContainerName, pkgconsts.MetricCPUUsageContainer, utilmetric.MetricData{Value: 0, Time: &now})
				store.SetContainerMetric(pod2Entry.PodUid, pod2Entry.ContainerName, pkgconsts.MetricCPUUsageContainer, utilmetric.MetricData{Value: 6, Time: &now})
			},
			topN:           1,
			wantTargetPods: []string{"pod-2"},
		},
		{
			name: "high tolerance pod excluded",
			setContainerMetric: func(store *metric.FakeMetricsFetcher, now time.Time, pod1Entry, pod2Entry *qrmstate.AllocationInfo) {
				store.SetContainerMetric(pod1Entry.PodUid, pod1Entry.ContainerName, pkgconsts.MetricCPUUsageContainer, utilmetric.MetricData{Value: 3, Time: &now})
				store.SetContainerMetric(pod2Entry.PodUid, pod2Entry.ContainerName, pkgconsts.MetricCPUUsageContainer, utilmetric.MetricData{Value: 6, Time: &now})
			},
			setTolerance:   true,
			topN:           1,
			wantTargetPods: []string{"pod-1"},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			conf := makeSuppressionUsageCPUPressureEvictionConf()
			pod1Entry := makeSuppressionReclaimAllocation(string(uuid.NewUUID()), "pod-1", 15, "1.2")
			pod2Entry := makeSuppressionReclaimAllocation(string(uuid.NewUUID()), "pod-2", 4, "1.2")
			if tt.setTolerance {
				// raise pod2's tolerance above the group rate
				pod2Entry.Annotations[apiconsts.PodAnnotationCPUEnhancementKey] = `{"suppression_tolerance_rate": "100"}`
			}

			p, store, _ := setupSuppressionUsageCPUPressureEvictionPlugin(t, conf, pod1Entry, pod2Entry)

			// hard met: supply 1, rate 19
			now := time.Now()
			setReclaimPoolMetrics(store, now, 8, 1000)
			tt.setContainerMetric(store, now, pod1Entry, pod2Entry)
			runSuppressionUsageSyncs(p, testSuppressionUsageRingSize)

			metResp, err := p.ThresholdMet(context.TODO(), &evictionpluginapi.GetThresholdMetRequest{})
			require.NoError(t, err)
			assert.Equal(t, evictionpluginapi.ThresholdMetType_HARD_MET, metResp.MetType)

			resp, err := p.GetTopEvictionPods(context.TODO(), &evictionpluginapi.GetTopEvictionPodsRequest{
				ActivePods: []*v1.Pod{makeSuppressionReclaimPod(pod1Entry), makeSuppressionReclaimPod(pod2Entry)},
				TopN:       tt.topN,
			})
			require.NoError(t, err)

			gotPods := make([]string, 0, len(resp.TargetPods))
			for _, pod := range resp.TargetPods {
				gotPods = append(gotPods, pod.Name)
			}
			assert.Equal(t, tt.wantTargetPods, gotPods)
			require.NotNil(t, resp.DeletionOptions)
			assert.Equal(t, int64(30), resp.DeletionOptions.GracePeriodSeconds)
		})
	}
}

func TestCPUPressureSuppression_GetTopEvictionPods_NoDeletionOptions(t *testing.T) {
	t.Parallel()

	conf := makeSuppressionUsageCPUPressureEvictionConf()
	conf.GetDynamicConfiguration().CPUPressureEvictionConfiguration.SuppressionUsageCPUPressureEvictionConfiguration.GracePeriod = -1

	pod1Entry := makeSuppressionReclaimAllocation(string(uuid.NewUUID()), "pod-1", 15, "1.2")
	p, store, _ := setupSuppressionUsageCPUPressureEvictionPlugin(t, conf, pod1Entry)

	now := time.Now()
	setReclaimPoolMetrics(store, now, 8, 1000)
	store.SetContainerMetric(pod1Entry.PodUid, pod1Entry.ContainerName, pkgconsts.MetricCPUUsageContainer, utilmetric.MetricData{Value: 3, Time: &now})
	runSuppressionUsageSyncs(p, testSuppressionUsageRingSize)

	resp, err := p.GetTopEvictionPods(context.TODO(), &evictionpluginapi.GetTopEvictionPodsRequest{
		ActivePods: []*v1.Pod{makeSuppressionReclaimPod(pod1Entry)},
		TopN:       1,
	})
	require.NoError(t, err)
	assert.Len(t, resp.TargetPods, 1)
	assert.Nil(t, resp.DeletionOptions)
}

func TestCPUPressureSuppression_OverStatsSeverityOrder(t *testing.T) {
	t.Parallel()

	conf := makeSuppressionUsageCPUPressureEvictionConf()
	p, _, _ := setupSuppressionUsageCPUPressureEvictionPlugin(t, conf)

	config := p.suppressionUsageCPUPressureEvictionConfig
	// group -1 (non actual numa binding): rate 6 (> soft 5, < hard 10) -> soft over only
	// group 0 (actual numa binding): rate 15 (> hard 10) with usage 0.5 (> hard 0.01) -> hard over
	for i := 0; i < testSuppressionUsageRingSize; i++ {
		p.metricsHistory.Push(nonNUMABindingGroupID, cpuutil.FakePodUID, metricNameSuppressionRate, 6,
			config.SoftSuppressionRateThreshold, config.HardSuppressionRateThreshold)
		p.metricsHistory.Push(nonNUMABindingGroupID, cpuutil.FakePodUID, metricNameSuppressionUsageRatio, 1,
			config.SoftCPUUsageThreshold, config.HardCPUUsageThreshold)
		p.metricsHistory.Push(0, cpuutil.FakePodUID, metricNameSuppressionRate, 15,
			config.SoftSuppressionRateThreshold, config.HardSuppressionRateThreshold)
		p.metricsHistory.Push(0, cpuutil.FakePodUID, metricNameSuppressionUsageRatio, 0.5,
			config.SoftCPUUsageThreshold, config.HardCPUUsageThreshold)
		// the metric ring dedups pushes with the same timestamp, so separate each
		// iteration to make every sample land in the ring
		time.Sleep(time.Millisecond)
	}

	p.updateSuppressionOverStats()
	require.Len(t, p.suppressionOverStats, 2)
	// the hard over group is first
	assert.Equal(t, 0, p.suppressionOverStats[0].GroupID)
	assert.True(t, p.suppressionOverStats[0].IsHardOver)
	assert.Equal(t, nonNUMABindingGroupID, p.suppressionOverStats[1].GroupID)
	assert.True(t, p.suppressionOverStats[1].IsSoftOver)

	resp, err := p.ThresholdMet(context.TODO(), &evictionpluginapi.GetThresholdMetRequest{})
	require.NoError(t, err)
	assert.Equal(t, evictionpluginapi.ThresholdMetType_HARD_MET, resp.MetType)
	assert.Equal(t, float64(testSuppressionHardRate), resp.ThresholdValue)
	assert.Equal(t, 15.0, resp.ObservedValue)
}
