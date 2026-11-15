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

package dynamicpolicy

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util"
	dynamicconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

type metricRecord struct {
	key      string
	val      int64
	emitType metrics.MetricTypeName
	tags     []metrics.MetricTag
}

type recordingMetricEmitter struct {
	records []metricRecord
}

func requirePoolSizeMetric(t *testing.T, records []metricRecord, poolName, formattedPoolName string, numaID int, value int64) {
	t.Helper()

	for _, record := range records {
		tags := make(map[string]string, len(record.tags))
		for _, tag := range record.tags {
			tags[tag.Key] = tag.Val
		}
		if record.key == util.MetricNamePoolSize &&
			tags["poolName"] == poolName &&
			tags["pool_name"] == formattedPoolName &&
			tags["numa_id"] == strconv.Itoa(numaID) {
			require.Equal(t, value, record.val)
			return
		}
	}
	t.Fatalf("missing pool size metric for pool %q formatted %q numa %d", poolName, formattedPoolName, numaID)
}

func (e *recordingMetricEmitter) StoreInt64(key string, val int64, emitType metrics.MetricTypeName, tags ...metrics.MetricTag) error {
	e.records = append(e.records, metricRecord{
		key:      key,
		val:      val,
		emitType: emitType,
		tags:     append([]metrics.MetricTag(nil), tags...),
	})
	return nil
}

func (e *recordingMetricEmitter) StoreFloat64(_ string, _ float64, _ metrics.MetricTypeName, _ ...metrics.MetricTag) error {
	return nil
}

func (e *recordingMetricEmitter) WithTags(_ string, _ ...metrics.MetricTag) metrics.MetricEmitter {
	return e
}

func (e *recordingMetricEmitter) Run(_ context.Context) {}

func TestDynamicPolicyEmitRuntimeConfigMetrics(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		dynamicConfig *dynamicconfig.DynamicAgentConfiguration
		wantValue     int64
	}{
		{
			name:      "nil dynamic config emits disabled",
			wantValue: 0,
		},
		{
			name:          "enable reclaim false emits disabled",
			dynamicConfig: newDynamicConfigWithEnableReclaim(false),
			wantValue:     0,
		},
		{
			name:          "enable reclaim true emits enabled",
			dynamicConfig: newDynamicConfigWithEnableReclaim(true),
			wantValue:     1,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			emitter := &recordingMetricEmitter{}
			policy := &DynamicPolicy{emitter: emitterComponent{emitter: emitter}, config: configComponent{dynamicConfig: tt.dynamicConfig}}

			policy.emitRuntimeConfigMetrics()

			require.Len(t, emitter.records, 1)
			require.Equal(t, util.MetricNameReclaimEnabled, emitter.records[0].key)
			require.Equal(t, tt.wantValue, emitter.records[0].val)
			require.Equal(t, metrics.MetricTypeNameRaw, emitter.records[0].emitType)
			require.Empty(t, emitter.records[0].tags)
		})
	}
}

func TestEmitFinalPoolSizeMetrics(t *testing.T) {
	t.Parallel()

	emitter := &recordingMetricEmitter{}
	policy := &DynamicPolicy{emitter: emitterComponent{emitter: emitter}}
	entries := state.PodEntries{
		commonstate.PoolNameShare: {
			commonstate.FakedContainerName: {
				AllocationMeta: commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameShare),
				TopologyAwareAssignments: map[int]machine.CPUSet{
					0: machine.NewCPUSet(0, 1),
					1: machine.NewCPUSet(2, 3),
				},
			},
		},
		commonstate.PoolNameReclaim: {
			commonstate.FakedContainerName: {
				AllocationMeta: commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				TopologyAwareAssignments: map[int]machine.CPUSet{
					0: machine.NewCPUSet(4),
					1: machine.NewCPUSet(5),
				},
			},
		},
		commonstate.PoolNamePrefixIsolation + "-isolation-uid": {
			commonstate.FakedContainerName: {
				AllocationMeta: commonstate.GenerateGenericPoolAllocationMeta(
					commonstate.PoolNamePrefixIsolation + "-isolation-uid"),
				TopologyAwareAssignments: map[int]machine.CPUSet{
					0: machine.NewCPUSet(6),
				},
			},
		},
		"isolation-uid": {
			"main": {
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "isolation-uid",
					PodNamespace:  "default",
					PodName:       "isolation-pod",
					ContainerType: pluginapi.ContainerType_MAIN.String(),
					QoSLevel:      consts.PodAnnotationQoSLevelDedicatedCores,
					OwnerPoolName: commonstate.PoolNamePrefixIsolation + "-isolation-uid",
				},
			},
		},
		"pod": {
			"container": {
				AllocationMeta: commonstate.AllocationMeta{PodUid: "pod"},
			},
		},
		"malformed-pool": {
			commonstate.FakedContainerName: nil,
		},
	}

	policy.emitFinalPoolSizeMetrics(entries)

	actual := make(map[string]int64)
	formattedPoolNames := make(map[string]string)
	for _, record := range emitter.records {
		require.Equal(t, util.MetricNamePoolSize, record.key)
		require.Equal(t, metrics.MetricTypeNameRaw, record.emitType)

		tags := make(map[string]string, len(record.tags))
		for _, tag := range record.tags {
			tags[tag.Key] = tag.Val
		}
		actual[tags["poolName"]+"/"+tags["numa_id"]] = record.val
		require.NotEmpty(t, tags["pool_name"])
		formattedPoolNames[tags["poolName"]+"/"+tags["numa_id"]] = tags["pool_name"]
	}
	require.Equal(t, map[string]int64{
		commonstate.PoolNameShare + "/0":                              2,
		commonstate.PoolNameShare + "/1":                              2,
		commonstate.PoolNameReclaim + "/0":                            1,
		commonstate.PoolNameReclaim + "/1":                            1,
		commonstate.PoolNamePrefixIsolation + "-isolation-uid" + "/0": 1,
	}, actual)
	require.Equal(t, "isolation-default/isolation-pod",
		formattedPoolNames[commonstate.PoolNamePrefixIsolation+"-isolation-uid"+"/0"])
}

func newDynamicConfigWithEnableReclaim(enabled bool) *dynamicconfig.DynamicAgentConfiguration {
	conf := dynamicconfig.NewDynamicAgentConfiguration()
	dyn := conf.GetDynamicConfiguration()
	dyn.EnableReclaim = enabled
	conf.SetDynamicConfiguration(dyn)
	return conf
}

func dedicatedMainContainerEntry(podUID string, assignments map[int]machine.CPUSet) *state.AllocationInfo {
	return dedicatedMainContainerEntryWithPodName(podUID, podUID, assignments)
}

func dedicatedMainContainerEntryWithPodName(
	podUID, podName string,
	assignments map[int]machine.CPUSet,
) *state.AllocationInfo {
	return &state.AllocationInfo{
		AllocationMeta: commonstate.AllocationMeta{
			PodUid:        podUID,
			PodNamespace:  "default",
			PodName:       podName,
			ContainerType: pluginapi.ContainerType_MAIN.String(),
			QoSLevel:      consts.PodAnnotationQoSLevelDedicatedCores,
		},
		TopologyAwareAssignments: assignments,
	}
}

// collectPoolSizeMetrics folds the recorded pool size series into a map keyed by
// "poolName/pool_type/pool_name/numa_id" so that legacy and formatted pool labels
// can be asserted deterministically.
func collectPoolSizeMetrics(t *testing.T, records []metricRecord) map[string]int64 {
	t.Helper()

	actual := make(map[string]int64)
	for _, record := range records {
		require.Equal(t, util.MetricNamePoolSize, record.key)
		require.Equal(t, metrics.MetricTypeNameRaw, record.emitType)

		tags := make(map[string]string, len(record.tags))
		for _, tag := range record.tags {
			tags[tag.Key] = tag.Val
		}
		require.NotEmpty(t, tags["pool_name"])
		actual[tags["poolName"]+"/"+tags["pool_type"]+"/"+tags["pool_name"]+"/"+tags["numa_id"]] = record.val
	}
	return actual
}

func TestEmitFinalPoolSizeMetricsWithDedicated(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		entries    state.PodEntries
		want       map[string]int64
		wantSeries int
	}{
		{
			name: "single dedicated pod single numa",
			entries: state.PodEntries{
				commonstate.PoolNameShare: {
					commonstate.FakedContainerName: {
						AllocationMeta:           commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameShare),
						TopologyAwareAssignments: map[int]machine.CPUSet{0: machine.NewCPUSet(0, 1)},
					},
				},
				"dedicated-pod": {
					"container": dedicatedMainContainerEntry("dedicated-pod",
						map[int]machine.CPUSet{0: machine.NewCPUSet(2, 3, 4, 5)}),
				},
			},
			want: map[string]int64{
				poolSizeMetricKey(commonstate.PoolNameShare, commonstate.PoolNameShare, commonstate.PoolNameShare, 0):                 2,
				poolSizeMetricKey(commonstate.PoolNameDedicated, commonstate.PoolNameDedicated, "dedicated-default/dedicated-pod", 0): 4,
			},
		},
		{
			name: "dedicated pod cross numa siblings aggregated per numa",
			entries: state.PodEntries{
				"dedicated-pod": {
					"container": dedicatedMainContainerEntry("dedicated-pod", map[int]machine.CPUSet{
						2: machine.NewCPUSet(8, 9, 10),
						3: machine.NewCPUSet(11, 12),
					}),
				},
			},
			want: map[string]int64{
				poolSizeMetricKey(commonstate.PoolNameDedicated, commonstate.PoolNameDedicated, "dedicated-default/dedicated-pod", 2): 3,
				poolSizeMetricKey(commonstate.PoolNameDedicated, commonstate.PoolNameDedicated, "dedicated-default/dedicated-pod", 3): 2,
			},
		},
		{
			name: "dedicated pods on same numa keep separate pool names",
			entries: state.PodEntries{
				"dedicated-a": {
					"container": dedicatedMainContainerEntry("dedicated-a",
						map[int]machine.CPUSet{0: machine.NewCPUSet(2, 3)}),
				},
				"dedicated-b": {
					"container": dedicatedMainContainerEntry("dedicated-b",
						map[int]machine.CPUSet{0: machine.NewCPUSet(4, 5, 6)}),
				},
			},
			want: map[string]int64{
				poolSizeMetricKey(commonstate.PoolNameDedicated, commonstate.PoolNameDedicated, "dedicated-default/dedicated-a", 0): 2,
				poolSizeMetricKey(commonstate.PoolNameDedicated, commonstate.PoolNameDedicated, "dedicated-default/dedicated-b", 0): 3,
			},
		},
		{
			name: "dedicated main containers in one pod are unioned per numa",
			entries: state.PodEntries{
				"dedicated-pod": {
					"container-a": dedicatedMainContainerEntry("dedicated-pod",
						map[int]machine.CPUSet{0: machine.NewCPUSet(2, 3)}),
					"container-b": dedicatedMainContainerEntry("dedicated-pod",
						map[int]machine.CPUSet{0: machine.NewCPUSet(3, 4, 5)}),
				},
			},
			want: map[string]int64{
				poolSizeMetricKey(commonstate.PoolNameDedicated, commonstate.PoolNameDedicated, "dedicated-default/dedicated-pod", 0): 4,
			},
		},
		{
			name: "long dedicated pod names keep distinct pool names",
			entries: state.PodEntries{
				"dedicated-a": {
					"container": dedicatedMainContainerEntryWithPodName(
						"dedicated-a", strings.Repeat("a", 245)+"x",
						map[int]machine.CPUSet{0: machine.NewCPUSet(2)}),
				},
				"dedicated-b": {
					"container": dedicatedMainContainerEntryWithPodName(
						"dedicated-b", strings.Repeat("a", 245)+"y",
						map[int]machine.CPUSet{0: machine.NewCPUSet(3)}),
				},
			},
			wantSeries: 2,
		},
		{
			name: "dedicated coexists with share and reclaim sidecar not double counted",
			entries: state.PodEntries{
				commonstate.PoolNameShare: {
					commonstate.FakedContainerName: {
						AllocationMeta:           commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameShare),
						TopologyAwareAssignments: map[int]machine.CPUSet{0: machine.NewCPUSet(0, 1)},
					},
				},
				commonstate.PoolNameReclaim: {
					commonstate.FakedContainerName: {
						AllocationMeta:           commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
						TopologyAwareAssignments: map[int]machine.CPUSet{0: machine.NewCPUSet(2)},
					},
				},
				"dedicated-pod": {
					"main-container": dedicatedMainContainerEntry("dedicated-pod",
						map[int]machine.CPUSet{0: machine.NewCPUSet(3, 4, 5)}),
					"sidecar": {
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:        "dedicated-pod",
							ContainerType: pluginapi.ContainerType_SIDECAR.String(),
							QoSLevel:      consts.PodAnnotationQoSLevelDedicatedCores,
						},
						TopologyAwareAssignments: map[int]machine.CPUSet{0: machine.NewCPUSet(3, 4, 5)},
					},
				},
			},
			want: map[string]int64{
				poolSizeMetricKey(commonstate.PoolNameShare, commonstate.PoolNameShare, commonstate.PoolNameShare, 0):                 2,
				poolSizeMetricKey(commonstate.PoolNameReclaim, commonstate.PoolNameReclaim, commonstate.PoolNameReclaim, 0):           1,
				poolSizeMetricKey(commonstate.PoolNameDedicated, commonstate.PoolNameDedicated, "dedicated-default/dedicated-pod", 0): 3,
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			emitter := &recordingMetricEmitter{}
			policy := &DynamicPolicy{emitter: emitterComponent{emitter: emitter}}

			policy.emitFinalPoolSizeMetrics(tt.entries)

			got := collectPoolSizeMetrics(t, emitter.records)
			if tt.want != nil {
				require.Equal(t, tt.want, got)
			}
			if tt.wantSeries > 0 {
				require.Len(t, emitter.records, tt.wantSeries)
				require.Len(t, got, tt.wantSeries)
			}
		})
	}
}

func poolSizeMetricKey(poolName, poolType, formattedPoolName string, numaID int) string {
	return poolName + "/" + poolType + "/" + formattedPoolName + "/" + strconv.Itoa(numaID)
}
