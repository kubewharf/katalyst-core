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

package memorybandwidth

import (
	"fmt"
	"os"
	"reflect"
	"testing"

	"github.com/bytedance/mockey"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/hintoptimizer"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/hintoptimizer/policy"
	hintoptimizerutil "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/hintoptimizer/util"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	cpuutil "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/util"
	"github.com/kubewharf/katalyst-core/pkg/config"
	pkgconsts "github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/spd"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	utilmetric "github.com/kubewharf/katalyst-core/pkg/util/metric"
)

func generateTestMetricsFetcher(val0, val1 float64) *metric.FakeMetricsFetcher {
	m := metric.NewFakeMetricsFetcher(metrics.DummyMetrics{})

	fm, ok := m.(*metric.FakeMetricsFetcher)
	if !ok {
		panic("failed to cast to *FakeMetricsFetcher")
	}

	fm.SetNumaMetric(0, pkgconsts.MetricMemBandwidthTheoryNuma, utilmetric.MetricData{
		Value: val0,
	})
	fm.SetNumaMetric(1, pkgconsts.MetricMemBandwidthTheoryNuma, utilmetric.MetricData{
		Value: val1,
	})
	fm.SetByStringIndex(pkgconsts.MetricCPUCodeName, "test")

	return fm
}

func TestNewMemoryBandwidthHintOptimizer(t *testing.T) {
	t.Parallel()

	cpuTopology, _ := machine.GenerateDummyCPUTopology(8, 1, 2) // 2 NUMA nodes
	tmpDir, err := os.MkdirTemp("", "checkpoint-TestNewMemoryBandwidthHintOptimizer")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	stateImpl, err := state.NewCheckpointState(tmpDir, "test", "test", cpuTopology, false, state.GenerateMachineStateFromPodEntries, metrics.DummyMetrics{})
	require.NoError(t, err)

	type args struct {
		options policy.HintOptimizerFactoryOptions
	}
	tests := []struct {
		name    string
		args    args
		want    hintoptimizer.HintOptimizer
		wantErr bool
	}{
		{
			name: "NewMemoryBandwidthHintOptimizer normal case",
			args: args{
				options: policy.HintOptimizerFactoryOptions{
					MetaServer: &metaserver.MetaServer{},
					Emitter:    metrics.DummyMetrics{},
					State:      stateImpl,
					Conf: func() *config.Configuration {
						conf := config.NewConfiguration()
						conf.MemoryBandwidthIgnoreNegativeHint = true
						conf.MemoryBandwidthPreferSpreading = false
						return conf
					}(),
				},
			},
			want: &memoryBandwidthOptimizer{
				metaServer:         &metaserver.MetaServer{},
				emitter:            metrics.DummyMetrics{},
				state:              stateImpl,
				ignoreNegativeHint: true,
				preferSpreading:    false,
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := NewMemoryBandwidthHintOptimizer(tt.args.options)
			if (err != nil) != tt.wantErr {
				t.Errorf("NewMemoryBandwidthHintOptimizer() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("NewMemoryBandwidthHintOptimizer() = %v, want %v", got, tt.want)
			}
			// Test Run method for coverage
			go got.Run(make(<-chan struct{}))
			assert.NotNil(t, got, "optimizer should not be nil")
		})
	}
}

func TestMemoryBandwidthOptimizer_sortMemBWLeftAllocatableList(t *testing.T) {
	t.Parallel()

	type fields struct {
		spreadingMemoryBandwidthHint bool
	}
	type args struct {
		hintAvailabilityList []*hintMemoryBandwidthAvailability
		hints                *pluginapi.ListOfTopologyHints
	}
	tests := []struct {
		name   string
		fields fields
		args   args
		want   []*hintMemoryBandwidthAvailability // Expected order after sorting
	}{
		{
			name: "spreading: true, sort by RemainingBandwidth desc",
			fields: fields{
				spreadingMemoryBandwidthHint: true,
			},
			args: args{
				hintAvailabilityList: []*hintMemoryBandwidthAvailability{
					{RemainingBandwidth: 10, OriginalHintIndex: 0},
					{RemainingBandwidth: 20, OriginalHintIndex: 1},
					{RemainingBandwidth: 5, OriginalHintIndex: 2},
				},
				hints: &pluginapi.ListOfTopologyHints{
					Hints: []*pluginapi.TopologyHint{
						{Nodes: []uint64{0}, Preferred: false},
						{Nodes: []uint64{1}, Preferred: false},
						{Nodes: []uint64{2}, Preferred: false},
					},
				},
			},
			want: []*hintMemoryBandwidthAvailability{
				{RemainingBandwidth: 20, OriginalHintIndex: 1},
				{RemainingBandwidth: 10, OriginalHintIndex: 0},
				{RemainingBandwidth: 5, OriginalHintIndex: 2},
			},
		},
		{
			name: "spreading: false (packing), sort by RemainingBandwidth asc (non-negative preferred)",
			fields: fields{
				spreadingMemoryBandwidthHint: false,
			},
			args: args{
				hintAvailabilityList: []*hintMemoryBandwidthAvailability{
					{RemainingBandwidth: 10, OriginalHintIndex: 0},
					{RemainingBandwidth: -5, OriginalHintIndex: 1},
					{RemainingBandwidth: 20, OriginalHintIndex: 2},
					{RemainingBandwidth: 0, OriginalHintIndex: 3},
					{RemainingBandwidth: -2, OriginalHintIndex: 4},
				},
				hints: &pluginapi.ListOfTopologyHints{
					Hints: []*pluginapi.TopologyHint{
						{Nodes: []uint64{0}, Preferred: false},
						{Nodes: []uint64{1}, Preferred: false},
						{Nodes: []uint64{2}, Preferred: false},
						{Nodes: []uint64{3}, Preferred: false},
						{Nodes: []uint64{4}, Preferred: false},
					},
				},
			},
			want: []*hintMemoryBandwidthAvailability{
				{RemainingBandwidth: 0, OriginalHintIndex: 3},
				{RemainingBandwidth: 10, OriginalHintIndex: 0},
				{RemainingBandwidth: 20, OriginalHintIndex: 2},
				{RemainingBandwidth: -2, OriginalHintIndex: 4},
				{RemainingBandwidth: -5, OriginalHintIndex: 1},
			},
		},
		{
			name: "sort by Preferred status (true comes first)",
			fields: fields{
				spreadingMemoryBandwidthHint: false, // Spreading doesn't matter for this specific check
			},
			args: args{
				hintAvailabilityList: []*hintMemoryBandwidthAvailability{
					{RemainingBandwidth: 10, OriginalHintIndex: 0}, // Preferred: false
					{RemainingBandwidth: 10, OriginalHintIndex: 1}, // Preferred: true
					{RemainingBandwidth: 10, OriginalHintIndex: 2}, // Preferred: false
				},
				hints: &pluginapi.ListOfTopologyHints{
					Hints: []*pluginapi.TopologyHint{
						{Nodes: []uint64{0}, Preferred: false},
						{Nodes: []uint64{1}, Preferred: true},
						{Nodes: []uint64{2}, Preferred: false},
					},
				},
			},
			want: []*hintMemoryBandwidthAvailability{
				{RemainingBandwidth: 10, OriginalHintIndex: 1},
				{RemainingBandwidth: 10, OriginalHintIndex: 0},
				{RemainingBandwidth: 10, OriginalHintIndex: 2},
			},
		},
		{
			name: "sort by number of NUMA nodes (fewer nodes preferred)",
			fields: fields{
				spreadingMemoryBandwidthHint: false,
			},
			args: args{
				hintAvailabilityList: []*hintMemoryBandwidthAvailability{
					{RemainingBandwidth: 10, OriginalHintIndex: 0}, // Nodes: 2
					{RemainingBandwidth: 10, OriginalHintIndex: 1}, // Nodes: 1
					{RemainingBandwidth: 10, OriginalHintIndex: 2}, // Nodes: 3
				},
				hints: &pluginapi.ListOfTopologyHints{
					Hints: []*pluginapi.TopologyHint{
						{Nodes: []uint64{0, 1}, Preferred: false},
						{Nodes: []uint64{2}, Preferred: false},
						{Nodes: []uint64{3, 4, 5}, Preferred: false},
					},
				},
			},
			want: []*hintMemoryBandwidthAvailability{
				{RemainingBandwidth: 10, OriginalHintIndex: 1},
				{RemainingBandwidth: 10, OriginalHintIndex: 0},
				{RemainingBandwidth: 10, OriginalHintIndex: 2},
			},
		},
		{
			name: "sort by original hint index (stability)",
			fields: fields{
				spreadingMemoryBandwidthHint: false,
			},
			args: args{
				hintAvailabilityList: []*hintMemoryBandwidthAvailability{
					{RemainingBandwidth: 10, OriginalHintIndex: 2},
					{RemainingBandwidth: 10, OriginalHintIndex: 0},
					{RemainingBandwidth: 10, OriginalHintIndex: 1},
				},
				hints: &pluginapi.ListOfTopologyHints{
					Hints: []*pluginapi.TopologyHint{
						{Nodes: []uint64{0}, Preferred: false}, // Index 0
						{Nodes: []uint64{1}, Preferred: false}, // Index 1
						{Nodes: []uint64{2}, Preferred: false}, // Index 2
					},
				},
			},
			want: []*hintMemoryBandwidthAvailability{
				{RemainingBandwidth: 10, OriginalHintIndex: 0},
				{RemainingBandwidth: 10, OriginalHintIndex: 1},
				{RemainingBandwidth: 10, OriginalHintIndex: 2},
			},
		},
		{
			name: "complex case: spreading=false, mixed criteria",
			fields: fields{
				spreadingMemoryBandwidthHint: false,
			},
			args: args{
				hintAvailabilityList: []*hintMemoryBandwidthAvailability{
					{RemainingBandwidth: 10, OriginalHintIndex: 0}, // P: F, Nodes: 2
					{RemainingBandwidth: 20, OriginalHintIndex: 1}, // P: T, Nodes: 1
					{RemainingBandwidth: 10, OriginalHintIndex: 2}, // P: F, Nodes: 1
					{RemainingBandwidth: -5, OriginalHintIndex: 3}, // P: F, Nodes: 1
					{RemainingBandwidth: 20, OriginalHintIndex: 4}, // P: F, Nodes: 1
				},
				hints: &pluginapi.ListOfTopologyHints{
					Hints: []*pluginapi.TopologyHint{
						{Nodes: []uint64{0, 1}, Preferred: false}, // Idx 0
						{Nodes: []uint64{2}, Preferred: true},     // Idx 1
						{Nodes: []uint64{3}, Preferred: false},    // Idx 2
						{Nodes: []uint64{4}, Preferred: false},    // Idx 3
						{Nodes: []uint64{5}, Preferred: false},    // Idx 4
					},
				},
			},
			want: []*hintMemoryBandwidthAvailability{
				{RemainingBandwidth: 10, OriginalHintIndex: 2}, // P:F, Rem:10, Nodes:2
				{RemainingBandwidth: 10, OriginalHintIndex: 0}, // P:F, Rem:10, Nodes:1
				// 1. Preferred: true (OriginalHintIndex: 1, RemainingBandwidth: 20)
				{RemainingBandwidth: 20, OriginalHintIndex: 1},
				// 2. Preferred: false, then by RemainingBandwidth (non-negative first, then ascending)
				//    Then by NUMA nodes (fewer first)
				//    Then by OriginalHintIndex
				{RemainingBandwidth: 20, OriginalHintIndex: 4}, // P:F, Rem:20, Nodes:1
				{RemainingBandwidth: -5, OriginalHintIndex: 3}, // P:F, Rem:-5, Nodes:1
			},
		},
		{
			name: "complex case: spreading=true, mixed criteria",
			fields: fields{
				spreadingMemoryBandwidthHint: true,
			},
			args: args{
				hintAvailabilityList: []*hintMemoryBandwidthAvailability{
					{RemainingBandwidth: 10, OriginalHintIndex: 0}, // P: F, Nodes: 2
					{RemainingBandwidth: 20, OriginalHintIndex: 1}, // P: T, Nodes: 1
					{RemainingBandwidth: 10, OriginalHintIndex: 2}, // P: F, Nodes: 1
					{RemainingBandwidth: -5, OriginalHintIndex: 3}, // P: F, Nodes: 1
					{RemainingBandwidth: 25, OriginalHintIndex: 4}, // P: F, Nodes: 1
				},
				hints: &pluginapi.ListOfTopologyHints{
					Hints: []*pluginapi.TopologyHint{
						{Nodes: []uint64{0, 1}, Preferred: false}, // Idx 0
						{Nodes: []uint64{2}, Preferred: true},     // Idx 1
						{Nodes: []uint64{3}, Preferred: false},    // Idx 2
						{Nodes: []uint64{4}, Preferred: false},    // Idx 3
						{Nodes: []uint64{5}, Preferred: false},    // Idx 4
					},
				},
			},
			want: []*hintMemoryBandwidthAvailability{
				// 1. Preferred: true (OriginalHintIndex: 1, RemainingBandwidth: 20)
				// 2. Preferred: false, then by RemainingBandwidth (descending)
				//    Then by NUMA nodes (fewer first)
				//    Then by OriginalHintIndex
				{RemainingBandwidth: 25, OriginalHintIndex: 4},
				{RemainingBandwidth: 20, OriginalHintIndex: 1},
				{RemainingBandwidth: 10, OriginalHintIndex: 2},
				{RemainingBandwidth: 10, OriginalHintIndex: 0},
				{RemainingBandwidth: -5, OriginalHintIndex: 3},
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			o := &memoryBandwidthOptimizer{
				preferSpreading: tt.fields.spreadingMemoryBandwidthHint,
			}
			o.sortMemBWLeftAllocatableList(tt.args.hintAvailabilityList, tt.args.hints)
			assert.Equal(t, tt.want, tt.args.hintAvailabilityList)
		})
	}
}

func TestMemoryBandwidthOptimizer_calculateHintAvailabilityList(t *testing.T) {
	t.Parallel()

	dummyEmitter := metrics.DummyMetrics{}
	cpuTopology, _ := machine.GenerateDummyCPUTopology(16, 2, 2) // 2 NUMA nodes
	metaServer := &metaserver.MetaServer{
		MetaAgent: &agent.MetaAgent{
			KatalystMachineInfo: &machine.KatalystMachineInfo{
				CPUTopology: cpuTopology,
				ExtraTopologyInfo: &machine.ExtraTopologyInfo{
					SiblingNumaInfo: &machine.SiblingNumaInfo{
						SiblingNumaMap: map[int]sets.Int{
							0: sets.NewInt(1),
							1: sets.NewInt(0),
						},
						SiblingNumaAvgMBWAllocatableRateMap: map[string]float64{
							"test": 1.0,
						},
						SiblingNumaAvgMBWCapacityMap: map[int]int64{
							0: 10,
							1: 10,
						},
						SiblingNumaDefaultMBWAllocatableRate: 1.0,
					},
				},
			},
			MetricsFetcher: generateTestMetricsFetcher(100/pkgconsts.BytesPerGB, 100/pkgconsts.BytesPerGB),
		},
	}

	type fields struct {
		metaServer                        *metaserver.MetaServer
		emitter                           metrics.MetricEmitter
		ignoreNegativeLeftAllocatableHint bool
	}
	type args struct {
		optReq                          hintoptimizer.Request
		hints                           *pluginapi.ListOfTopologyHints
		preferredHintIndexes            []int
		containerMemoryBandwidthRequest int
		numaAllocatedMemBW              map[int]int
		podMeta                         metav1.ObjectMeta
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		want    []*hintMemoryBandwidthAvailability
		wantErr error
	}{
		{
			name: "normal case - two preferred hints, both positive remaining bandwidth",
			fields: fields{
				metaServer:                        metaServer,
				emitter:                           dummyEmitter,
				ignoreNegativeLeftAllocatableHint: false,
			},
			args: args{
				optReq: hintoptimizer.Request{
					ResourceRequest: &pluginapi.ResourceRequest{
						PodUid:       "pod1",
						PodNamespace: "ns1",
						PodName:      "name1",
					},
				},
				hints: &pluginapi.ListOfTopologyHints{
					Hints: []*pluginapi.TopologyHint{
						{Nodes: []uint64{0}, Preferred: true},
						{Nodes: []uint64{1}, Preferred: true},
					},
				},
				preferredHintIndexes:            []int{0, 1},
				containerMemoryBandwidthRequest: 10,
				numaAllocatedMemBW:              map[int]int{0: 5, 1: 5},
				podMeta: metav1.ObjectMeta{
					UID:       "pod1",
					Namespace: "ns1",
					Name:      "name1",
				},
			},
			want: []*hintMemoryBandwidthAvailability{
				{RemainingBandwidth: 180, OriginalHintIndex: 0}, // (100+100) - (5+5+10/1+10/1) = 200 - 20 = 180 (assuming hint 0 uses NUMA 0, hint 1 uses NUMA 1)
				{RemainingBandwidth: 180, OriginalHintIndex: 1},
			},
			wantErr: nil,
		},
		{
			name: "normal case - one hint with negative remaining bandwidth, ignoreNegativeHint is false",
			fields: fields{
				metaServer:                        metaServer,
				emitter:                           dummyEmitter,
				ignoreNegativeLeftAllocatableHint: false,
			},
			args: args{
				optReq: hintoptimizer.Request{
					ResourceRequest: &pluginapi.ResourceRequest{
						PodUid:       "pod2",
						PodNamespace: "ns2",
						PodName:      "name2",
					},
				},
				hints: &pluginapi.ListOfTopologyHints{
					Hints: []*pluginapi.TopologyHint{
						{Nodes: []uint64{0}, Preferred: true},
						{Nodes: []uint64{1}, Preferred: true},
					},
				},
				preferredHintIndexes:            []int{0, 1},
				containerMemoryBandwidthRequest: 150,
				numaAllocatedMemBW:              map[int]int{0: 60, 1: 10},
				podMeta: metav1.ObjectMeta{
					UID:       "pod2",
					Namespace: "ns2",
					Name:      "name2",
				},
			},
			want: []*hintMemoryBandwidthAvailability{
				{RemainingBandwidth: -20, OriginalHintIndex: 0},
				{RemainingBandwidth: -20, OriginalHintIndex: 1},
			},
			wantErr: nil,
		},
		{
			name: "error case - no available hints after filtering/errors",
			fields: fields{
				metaServer:                        metaServer,
				emitter:                           dummyEmitter,
				ignoreNegativeLeftAllocatableHint: true,
			},
			args: args{
				optReq: hintoptimizer.Request{
					ResourceRequest: &pluginapi.ResourceRequest{
						PodUid:       "pod5",
						PodNamespace: "ns5",
						PodName:      "name5",
					},
				},
				hints: &pluginapi.ListOfTopologyHints{
					Hints: []*pluginapi.TopologyHint{
						{Nodes: []uint64{0}, Preferred: true}, // Will be negative and skipped
					},
				},
				preferredHintIndexes:            []int{0},
				containerMemoryBandwidthRequest: 200,
				numaAllocatedMemBW:              map[int]int{0: 10, 1: 10},
				podMeta: metav1.ObjectMeta{
					UID:       "pod5",
					Namespace: "ns5",
					Name:      "name5",
				},
			},
			want:    nil,
			wantErr: cpuutil.ErrNoAvailableMemoryBandwidthHints,
		},
		{
			name: "skip hint with no nodes",
			fields: fields{
				metaServer:                        metaServer,
				emitter:                           dummyEmitter,
				ignoreNegativeLeftAllocatableHint: false,
			},
			args: args{
				optReq: hintoptimizer.Request{
					ResourceRequest: &pluginapi.ResourceRequest{
						PodUid:       "pod6",
						PodNamespace: "ns6",
						PodName:      "name6",
					},
				},
				hints: &pluginapi.ListOfTopologyHints{
					Hints: []*pluginapi.TopologyHint{
						{Nodes: []uint64{}, Preferred: true},  // Empty nodes, should be skipped
						{Nodes: []uint64{1}, Preferred: true}, // Valid hint
					},
				},
				preferredHintIndexes:            []int{0, 1},
				containerMemoryBandwidthRequest: 10,
				numaAllocatedMemBW:              map[int]int{0: 5, 1: 5},
				podMeta: metav1.ObjectMeta{
					UID:       "pod6",
					Namespace: "ns6",
					Name:      "name6",
				},
			},
			want: []*hintMemoryBandwidthAvailability{
				{RemainingBandwidth: 180, OriginalHintIndex: 1},
			},
			wantErr: nil,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			o := &memoryBandwidthOptimizer{
				metaServer:         tt.fields.metaServer,
				emitter:            tt.fields.emitter,
				ignoreNegativeHint: tt.fields.ignoreNegativeLeftAllocatableHint,
			}

			got, err := o.calculateHintAvailabilityList(tt.args.optReq, tt.args.hints, tt.args.preferredHintIndexes, tt.args.containerMemoryBandwidthRequest, tt.args.numaAllocatedMemBW, tt.args.podMeta)
			if tt.wantErr != nil {
				assert.EqualError(t, err, tt.wantErr.Error())
			} else {
				assert.NoError(t, err)
			}
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestMemoryBandwidthOptimizer_OptimizeHints(t *testing.T) {
	t.Parallel()

	dummyEmitter := metrics.DummyMetrics{}
	cpuTopology, _ := machine.GenerateDummyCPUTopology(16, 2, 2) // 2 NUMA nodes, 8 cores per NUMA
	tmpDir, err := os.MkdirTemp("", "checkpoint-TestOptimizeHints")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	stateImpl, err := state.NewCheckpointState(tmpDir, "test", "test", cpuTopology, false, state.GenerateMachineStateFromPodEntries, dummyEmitter)
	require.NoError(t, err)

	type fields struct {
		metaServer                        *metaserver.MetaServer
		emitter                           metrics.MetricEmitter
		state                             state.State
		ignoreNegativeLeftAllocatableHint bool
		spreadingMemoryBandwidthHint      bool
	}
	type args struct {
		request hintoptimizer.Request
		hints   *pluginapi.ListOfTopologyHints
	}
	tests := []struct {
		name      string
		fields    fields
		args      args
		mocks     func(f *fields, a *args)
		wantErr   error
		wantHints *pluginapi.ListOfTopologyHints
	}{
		{
			name: "GenericOptimizeHintsCheck returns error",
			fields: fields{
				emitter: dummyEmitter,
			},
			args: args{
				request: hintoptimizer.Request{
					ResourceRequest: &pluginapi.ResourceRequest{
						PodUid:        "test-pod-uid",
						PodNamespace:  "test-ns",
						PodName:       "test-pod",
						ContainerName: "test-container",
						ContainerType: pluginapi.ContainerType_MAIN,
					},
				},
				hints: &pluginapi.ListOfTopologyHints{Hints: []*pluginapi.TopologyHint{{Nodes: []uint64{0}, Preferred: true}}},
			},
			mocks: func(f *fields, a *args) {
				mockey.Mock(hintoptimizerutil.GenericOptimizeHintsCheck).Return(fmt.Errorf("generic check failed")).Build()
			},
			wantErr:   fmt.Errorf("generic check failed"),
			wantHints: &pluginapi.ListOfTopologyHints{Hints: []*pluginapi.TopologyHint{{Nodes: []uint64{0}, Preferred: true}}},
		},
		{
			name: "getNUMAAllocatedMemBW returns error",
			fields: fields{
				emitter: dummyEmitter,
				state:   stateImpl,
			},
			args: args{
				request: hintoptimizer.Request{ResourceRequest: &pluginapi.ResourceRequest{
					PodUid:        "test-pod-uid",
					PodNamespace:  "test-ns",
					PodName:       "test-pod",
					ContainerName: "test-container",
					ContainerType: pluginapi.ContainerType_MAIN,
				}},
				hints: &pluginapi.ListOfTopologyHints{Hints: []*pluginapi.TopologyHint{{Nodes: []uint64{0}, Preferred: true}}},
			},
			mocks: func(f *fields, a *args) {
				mockey.Mock(hintoptimizerutil.GenericOptimizeHintsCheck).Return(nil).Build()
				// Simulate an error from a dependency of getNUMAAllocatedMemBW.
				mockey.Mock(spd.GetContainerMemoryBandwidthRequest).
					To(func(profilingManager spd.ServiceProfilingManager, podMeta metav1.ObjectMeta, cpuRequest int) (int, error) {
						if podMeta.Name != a.request.PodName {
							return 0, fmt.Errorf("spd failed for current pod")
						}
						return 100, nil
					}).Build()
				// Ensure machineState has some pods for this to be triggered.
				f.state = func() state.State {
					ms := state.NUMANodeMap{
						0: &state.NUMANodeState{
							PodEntries: state.PodEntries{
								"existing-pod": state.ContainerEntries{
									"existing-container": &state.AllocationInfo{
										AllocationMeta: commonstate.AllocationMeta{
											PodUid:        "existing-pod",
											ContainerType: pluginapi.ContainerType_MAIN.String(),
											Labels:        map[string]string{apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable},
											Annotations:   map[string]string{apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable},
										},
									},
								},
							},
						},
					}
					st, _ := state.NewCheckpointState(tmpDir, "test-state-err", "test", cpuTopology, false, func(_ *machine.CPUTopology, _ state.PodEntries) (state.NUMANodeMap, error) {
						return ms, nil
					}, dummyEmitter)
					return st
				}()
			},
			wantErr:   hintoptimizerutil.ErrHintOptimizerSkip, // getNUMAAllocatedMemBW failure leads to skip
			wantHints: &pluginapi.ListOfTopologyHints{Hints: []*pluginapi.TopologyHint{{Nodes: []uint64{0}, Preferred: true}}},
		},
		{
			name: "spd.GetContainerMemoryBandwidthRequest for current request returns error",
			fields: fields{
				metaServer: &metaserver.MetaServer{
					MetaAgent: &agent.MetaAgent{
						KatalystMachineInfo: &machine.KatalystMachineInfo{CPUTopology: cpuTopology},
					},
				},
				emitter: dummyEmitter,
				state:   stateImpl,
			},
			args: args{
				request: hintoptimizer.Request{
					ResourceRequest: &pluginapi.ResourceRequest{
						PodUid:        "test-pod-uid",
						PodNamespace:  "test-ns",
						PodName:       "test-pod",
						ContainerName: "test-container",
						ContainerType: pluginapi.ContainerType_MAIN,
					},
					CPURequest: 2,
				},
				hints: &pluginapi.ListOfTopologyHints{Hints: []*pluginapi.TopologyHint{{Nodes: []uint64{0}, Preferred: true}}},
			},
			mocks: func(f *fields, a *args) {
				mockey.Mock(hintoptimizerutil.GenericOptimizeHintsCheck).Return(nil).Build()
				mockey.Mock(spd.GetContainerMemoryBandwidthRequest).
					To(func(profilingManager spd.ServiceProfilingManager, podMeta metav1.ObjectMeta, cpuRequest int) (int, error) {
						if podMeta.Name == a.request.PodName {
							return 0, fmt.Errorf("spd failed for current pod")
						}
						return 100, nil
					}).Build()
				mockey.Mock(cpuutil.GetContainerRequestedCores).Return(1.0).Build()
			},
			wantErr:   hintoptimizerutil.ErrHintOptimizerSkip,
			wantHints: &pluginapi.ListOfTopologyHints{Hints: []*pluginapi.TopologyHint{{Nodes: []uint64{0}, Preferred: true}}},
		},
		{
			name: "spd.GetContainerMemoryBandwidthRequest for current request returns 0",
			fields: fields{
				metaServer: &metaserver.MetaServer{
					MetaAgent: &agent.MetaAgent{
						KatalystMachineInfo: &machine.KatalystMachineInfo{CPUTopology: cpuTopology},
					},
				},
				emitter: dummyEmitter,
				state:   stateImpl,
			},
			args: args{
				request: hintoptimizer.Request{
					ResourceRequest: &pluginapi.ResourceRequest{
						PodUid:        "test-pod-uid",
						PodNamespace:  "test-ns",
						PodName:       "test-pod",
						ContainerName: "test-container",
						ContainerType: pluginapi.ContainerType_MAIN,
					},
					CPURequest: 2,
				},
				hints: &pluginapi.ListOfTopologyHints{Hints: []*pluginapi.TopologyHint{{Nodes: []uint64{0}, Preferred: true}}},
			},
			mocks: func(f *fields, a *args) {
				mockey.Mock(hintoptimizerutil.GenericOptimizeHintsCheck).Return(nil).Build()
				mockey.Mock(spd.GetContainerMemoryBandwidthRequest).
					To(func(profilingManager spd.ServiceProfilingManager, podMeta metav1.ObjectMeta, cpuRequest int) (int, error) {
						if podMeta.Name == a.request.PodName {
							return 0, nil
						}
						return 100, nil
					}).Build()
				mockey.Mock(cpuutil.GetContainerRequestedCores).Return(1.0).Build()
			},
			wantErr:   hintoptimizerutil.ErrHintOptimizerSkip,
			wantHints: &pluginapi.ListOfTopologyHints{Hints: []*pluginapi.TopologyHint{{Nodes: []uint64{0}, Preferred: true}}},
		},
		{
			name: "no preferred hints initially",
			fields: fields{
				metaServer: &metaserver.MetaServer{
					MetaAgent: &agent.MetaAgent{
						KatalystMachineInfo: &machine.KatalystMachineInfo{CPUTopology: cpuTopology},
					},
				},
				emitter: dummyEmitter,
				state:   stateImpl,
			},
			args: args{
				request: hintoptimizer.Request{
					ResourceRequest: &pluginapi.ResourceRequest{
						PodUid:        "test-pod-uid",
						PodNamespace:  "test-ns",
						PodName:       "test-pod",
						ContainerName: "test-container",
						ContainerType: pluginapi.ContainerType_MAIN,
					},
					CPURequest: 2,
				},
				hints: &pluginapi.ListOfTopologyHints{Hints: []*pluginapi.TopologyHint{{Nodes: []uint64{0}, Preferred: false}}},
			},
			mocks: func(f *fields, a *args) {
				mockey.Mock(hintoptimizerutil.GenericOptimizeHintsCheck).Return(nil).Build()
				mockey.Mock(spd.GetContainerMemoryBandwidthRequest).Return(100, nil).Build()
				mockey.Mock(cpuutil.GetContainerRequestedCores).Return(1.0).Build()
			},
			wantErr:   hintoptimizerutil.ErrHintOptimizerSkip,
			wantHints: &pluginapi.ListOfTopologyHints{Hints: []*pluginapi.TopologyHint{{Nodes: []uint64{0}, Preferred: false}}},
		},
		{
			name: "calculateHintAvailabilityList returns ErrNoAvailableCPUHints",
			fields: fields{
				metaServer: &metaserver.MetaServer{
					MetaAgent: &agent.MetaAgent{
						KatalystMachineInfo: &machine.KatalystMachineInfo{
							CPUTopology: cpuTopology,
							ExtraTopologyInfo: &machine.ExtraTopologyInfo{
								SiblingNumaInfo: &machine.SiblingNumaInfo{
									SiblingNumaMap: map[int]sets.Int{0: sets.NewInt(), 1: sets.NewInt()},
									SiblingNumaAvgMBWAllocatableRateMap: map[string]float64{
										"test": 1.0,
									},
									SiblingNumaAvgMBWCapacityMap: map[int]int64{
										0: 10,
										1: 10,
									},
									SiblingNumaDefaultMBWAllocatableRate: 1.0,
								},
							},
						},
						MetricsFetcher: generateTestMetricsFetcher(1000/pkgconsts.BytesPerGB, 1000/pkgconsts.BytesPerGB),
					},
				},
				emitter:                           dummyEmitter,
				state:                             stateImpl,
				ignoreNegativeLeftAllocatableHint: true,
			},
			args: args{
				request: hintoptimizer.Request{
					ResourceRequest: &pluginapi.ResourceRequest{
						PodUid:        "test-pod-uid",
						PodNamespace:  "test-ns",
						PodName:       "test-pod",
						ContainerName: "test-container",
						ContainerType: pluginapi.ContainerType_MAIN,
					},
					CPURequest: 2,
				},
				hints: &pluginapi.ListOfTopologyHints{
					Hints: []*pluginapi.TopologyHint{
						{Nodes: []uint64{0}, Preferred: true},
					},
				},
			},
			mocks: func(f *fields, a *args) {
				mockey.Mock(hintoptimizerutil.GenericOptimizeHintsCheck).Return(nil).Build()
				mockey.Mock(spd.GetContainerMemoryBandwidthRequest).Return(2000, nil).Build()
				mockey.Mock(cpuutil.GetContainerRequestedCores).Return(1.0).Build()
			},
			wantErr: cpuutil.ErrNoAvailableMemoryBandwidthHints,
			wantHints: &pluginapi.ListOfTopologyHints{
				Hints: []*pluginapi.TopologyHint{
					{Nodes: []uint64{0}, Preferred: true},
				},
			},
		},
		{
			name: "successful optimization - packing, one hint becomes preferred",
			fields: fields{
				metaServer: &metaserver.MetaServer{
					MetaAgent: &agent.MetaAgent{
						KatalystMachineInfo: &machine.KatalystMachineInfo{
							CPUTopology: cpuTopology,
							ExtraTopologyInfo: &machine.ExtraTopologyInfo{
								SiblingNumaInfo: &machine.SiblingNumaInfo{
									SiblingNumaMap: map[int]sets.Int{0: sets.NewInt(), 1: sets.NewInt()},
									SiblingNumaAvgMBWAllocatableRateMap: map[string]float64{
										"test": 1.0,
									},
									SiblingNumaAvgMBWCapacityMap: map[int]int64{
										0: 10,
										1: 10,
									},
									SiblingNumaDefaultMBWAllocatableRate: 1.0,
								},
							},
						},
						MetricsFetcher: generateTestMetricsFetcher(1000/pkgconsts.BytesPerGB, 800/pkgconsts.BytesPerGB),
					},
				},
				emitter:                           dummyEmitter,
				state:                             stateImpl,
				ignoreNegativeLeftAllocatableHint: false,
				spreadingMemoryBandwidthHint:      false, // Packing
			},
			args: args{
				request: hintoptimizer.Request{
					ResourceRequest: &pluginapi.ResourceRequest{
						PodUid:        "test-pod-uid",
						PodNamespace:  "test-ns",
						PodName:       "test-pod",
						ContainerName: "test-container",
						ContainerType: pluginapi.ContainerType_MAIN,
					},
					CPURequest: 1,
				},
				hints: &pluginapi.ListOfTopologyHints{
					Hints: []*pluginapi.TopologyHint{
						{Nodes: []uint64{0}, Preferred: true}, // Initially preferred, will have positive remaining
						{Nodes: []uint64{1}, Preferred: true}, // Initially preferred, will have negative remaining
					},
				},
			},
			mocks: func(f *fields, a *args) {
				mockey.Mock(hintoptimizerutil.GenericOptimizeHintsCheck).Return(nil).Build()
				// Mock getNUMAAllocatedMemBW to return some initial allocation
				// This requires mocking dependencies of getNUMAAllocatedMemBW
				f.state = func() state.State {
					ms := state.NUMANodeMap{
						0: &state.NUMANodeState{PodEntries: state.PodEntries{ // NUMA 0 has 100 allocated
							"existing-pod-0": state.ContainerEntries{
								"c0": &state.AllocationInfo{
									AllocationMeta:  commonstate.AllocationMeta{PodUid: "existing-pod-0", ContainerType: pluginapi.ContainerType_MAIN.String(), Labels: map[string]string{apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable}, Annotations: map[string]string{apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable}},
									RequestQuantity: 1,
								},
							},
						}},
						1: &state.NUMANodeState{PodEntries: state.PodEntries{ // NUMA 1 has 700 allocated
							"existing-pod-1": state.ContainerEntries{
								"c1": &state.AllocationInfo{
									AllocationMeta:  commonstate.AllocationMeta{PodUid: "existing-pod-1", ContainerType: pluginapi.ContainerType_MAIN.String(), Labels: map[string]string{apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable}, Annotations: map[string]string{apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable}},
									RequestQuantity: 1,
								},
							},
						}},
					}
					st, _ := state.NewCheckpointState(tmpDir, "test-state-success", "test", cpuTopology, false, state.GenerateMachineStateFromPodEntries, dummyEmitter)
					st.SetMachineState(ms, false)
					return st
				}()
				mockey.Mock(spd.GetContainerMemoryBandwidthRequest).To(func(profilingManager spd.ServiceProfilingManager, podMeta metav1.ObjectMeta, cpuRequest int) (int, error) {
					switch podMeta.UID {
					case "existing-pod-0": // From getNUMAAllocatedMemBW
						return 100, nil
					case "existing-pod-1": // From getNUMAAllocatedMemBW
						return 700, nil
					case "test-pod-uid": // For current request
						return 200, nil // Requesting 200MBps
					}
					return 0, fmt.Errorf("unmocked spd call for pod %s", podMeta.UID)
				}).Build()
				mockey.Mock(cpuutil.GetContainerRequestedCores).Return(1.0).Build()
			},
			wantErr: nil,
			wantHints: &pluginapi.ListOfTopologyHints{
				Hints: []*pluginapi.TopologyHint{
					// NUMA 0: Allocatable 1000. Allocated 100. Request 200. New Allocated 300. Remaining 1000-300 = 700. Preferred = true.
					{Nodes: []uint64{0}, Preferred: true},
					// NUMA 1: Allocatable 800. Allocated 700. Request 200. New Allocated 900. Remaining 800-900 = -100. Preferred = false.
					{Nodes: []uint64{1}, Preferred: false},
				},
			},
		},
		{
			name: "successful optimization - spreading, one hint becomes preferred",
			fields: fields{
				metaServer: &metaserver.MetaServer{
					MetaAgent: &agent.MetaAgent{
						KatalystMachineInfo: &machine.KatalystMachineInfo{
							CPUTopology: cpuTopology,
							ExtraTopologyInfo: &machine.ExtraTopologyInfo{
								SiblingNumaInfo: &machine.SiblingNumaInfo{
									SiblingNumaMap: map[int]sets.Int{0: sets.NewInt(), 1: sets.NewInt()},
									SiblingNumaAvgMBWAllocatableRateMap: map[string]float64{
										"test": 1.0,
									},
									SiblingNumaAvgMBWCapacityMap: map[int]int64{
										0: 10,
										1: 10,
									},
									SiblingNumaDefaultMBWAllocatableRate: 1.0,
								},
							},
						},
						MetricsFetcher: generateTestMetricsFetcher(1000/pkgconsts.BytesPerGB, 1200/pkgconsts.BytesPerGB),
					},
				},
				emitter:                           dummyEmitter,
				state:                             stateImpl,
				ignoreNegativeLeftAllocatableHint: false,
				spreadingMemoryBandwidthHint:      true, // Spreading
			},
			args: args{
				request: hintoptimizer.Request{
					ResourceRequest: &pluginapi.ResourceRequest{
						PodUid:        "test-pod-uid",
						PodNamespace:  "test-ns",
						PodName:       "test-pod",
						ContainerName: "test-container",
						ContainerType: pluginapi.ContainerType_MAIN,
					},
					CPURequest: 1,
				},
				hints: &pluginapi.ListOfTopologyHints{
					Hints: []*pluginapi.TopologyHint{
						{Nodes: []uint64{0}, Preferred: true}, // Less remaining after allocation
						{Nodes: []uint64{1}, Preferred: true}, // More remaining after allocation
					},
				},
			},
			mocks: func(f *fields, a *args) {
				mockey.Mock(hintoptimizerutil.GenericOptimizeHintsCheck).Return(nil).Build()
				f.state = func() state.State {
					ms := state.NUMANodeMap{
						0: &state.NUMANodeState{PodEntries: state.PodEntries{ // NUMA 0 has 100 allocated
							"existing-pod-s0": state.ContainerEntries{
								"c0": &state.AllocationInfo{
									AllocationMeta:  commonstate.AllocationMeta{PodUid: "existing-pod-s0", ContainerType: pluginapi.ContainerType_MAIN.String(), Labels: map[string]string{apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable}, Annotations: map[string]string{apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable}},
									RequestQuantity: 1,
								},
							},
						}},
						1: &state.NUMANodeState{PodEntries: state.PodEntries{ // NUMA 1 has 50 allocated
							"existing-pod-s1": state.ContainerEntries{
								"c1": &state.AllocationInfo{
									AllocationMeta:  commonstate.AllocationMeta{PodUid: "existing-pod-s1", ContainerType: pluginapi.ContainerType_MAIN.String(), Labels: map[string]string{apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable}, Annotations: map[string]string{apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable}},
									RequestQuantity: 1,
								},
							},
						}},
					}
					st, _ := state.NewCheckpointState(tmpDir, "test-state-spread", "test", cpuTopology, false, state.GenerateMachineStateFromPodEntries, dummyEmitter)
					st.SetMachineState(ms, false)
					return st
				}()
				mockey.Mock(spd.GetContainerMemoryBandwidthRequest).To(func(profilingManager spd.ServiceProfilingManager, podMeta metav1.ObjectMeta, cpuRequest int) (int, error) {
					switch podMeta.UID {
					case "existing-pod-s0":
						return 100, nil
					case "existing-pod-s1":
						return 50, nil
					case "test-pod-uid": // Current request
						return 150, nil // Requesting 150MBps
					}
					return 0, fmt.Errorf("unmocked spd call for pod %s", podMeta.UID)
				}).Build()
				mockey.Mock(cpuutil.GetContainerRequestedCores).Return(1.0).Build()
			},
			wantErr: nil,
			wantHints: &pluginapi.ListOfTopologyHints{
				Hints: []*pluginapi.TopologyHint{
					// NUMA 0: Allocatable 1000. Allocated 100. Request 150. New Allocated 250. Remaining 1000-250 = 750.
					// NUMA 1: Allocatable 1200. Allocated 50. Request 150. New Allocated 200. Remaining 1200-200 = 1000.
					// Spreading prefers more remaining, so NUMA 1 should be preferred if both are positive.
					// However, the reevaluatePreferredHints logic only sets preferred to true if RemainingBandwidth >= 0.
					// The sorting happens first, then reevaluation. Both will be >=0 here.
					// The final `Preferred` status depends on the sorting logic and then the re-evaluation. Since both are positive, both will be true.
					// The key is the order in hintAvailabilityList after sorting, which then drives reevaluatePreferredHints.
					// For spreading, higher remaining bandwidth comes first. So hint for NUMA 1 (index 1) will be processed first by reevaluatePreferredHints.
					// The actual `Preferred` flags are set based on `availability.RemainingBandwidth >= 0`.
					// The sorting influences which hint *might* be chosen if only one could be, but here both are valid.
					{Nodes: []uint64{1}, Preferred: true}, // Remaining 1000
					{Nodes: []uint64{0}, Preferred: true}, // Remaining 750
				},
			},
		},
		{
			name: "ignoreNegativeHint is true, negative hint skipped",
			fields: fields{
				metaServer: &metaserver.MetaServer{
					MetaAgent: &agent.MetaAgent{
						KatalystMachineInfo: &machine.KatalystMachineInfo{
							CPUTopology: cpuTopology,
							ExtraTopologyInfo: &machine.ExtraTopologyInfo{
								SiblingNumaInfo: &machine.SiblingNumaInfo{
									SiblingNumaMap: map[int]sets.Int{0: sets.NewInt(), 1: sets.NewInt()},
									SiblingNumaAvgMBWAllocatableRateMap: map[string]float64{
										"test": 1.0,
									},
									SiblingNumaAvgMBWCapacityMap: map[int]int64{
										0: 10,
										1: 10,
									},
									SiblingNumaDefaultMBWAllocatableRate: 1.0,
								},
							},
						},
						MetricsFetcher: generateTestMetricsFetcher(1000/pkgconsts.BytesPerGB, 500/pkgconsts.BytesPerGB), // NUMA 1 has less, will go negative
					},
				},
				emitter:                           dummyEmitter,
				state:                             stateImpl,
				ignoreNegativeLeftAllocatableHint: true, // Ignore negative
				spreadingMemoryBandwidthHint:      false,
			},
			args: args{
				request: hintoptimizer.Request{
					ResourceRequest: &pluginapi.ResourceRequest{
						PodUid:        "uid-ignore-neg",
						PodNamespace:  "test-ns",
						PodName:       "test-pod",
						ContainerName: "test-container",
						ContainerType: pluginapi.ContainerType_MAIN,
					},
					CPURequest: 1,
				},
				hints: &pluginapi.ListOfTopologyHints{
					Hints: []*pluginapi.TopologyHint{
						{Nodes: []uint64{0}, Preferred: true}, // Positive remaining
						{Nodes: []uint64{1}, Preferred: true}, // Negative remaining, should be skipped by calculateHintAvailabilityList
					},
				},
			},
			mocks: func(f *fields, a *args) {
				mockey.Mock(hintoptimizerutil.GenericOptimizeHintsCheck).Return(nil).Build()
				f.state = func() state.State {
					ms := state.NUMANodeMap{
						0: &state.NUMANodeState{PodEntries: state.PodEntries{"p0": state.ContainerEntries{"c0": &state.AllocationInfo{AllocationMeta: commonstate.AllocationMeta{PodUid: "p0", ContainerType: pluginapi.ContainerType_MAIN.String(), Labels: map[string]string{apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable}, Annotations: map[string]string{apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable}}, RequestQuantity: 1}}}},
						1: &state.NUMANodeState{PodEntries: state.PodEntries{"p1": state.ContainerEntries{"c1": &state.AllocationInfo{AllocationMeta: commonstate.AllocationMeta{PodUid: "p1", ContainerType: pluginapi.ContainerType_MAIN.String(), Labels: map[string]string{apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable}, Annotations: map[string]string{apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable}}, RequestQuantity: 1}}}},
					}
					st, _ := state.NewCheckpointState(tmpDir, "test-state-ign-neg", "test", cpuTopology, false, state.GenerateMachineStateFromPodEntries, dummyEmitter)
					st.SetMachineState(ms, false)
					return st
				}()
				mockey.Mock(spd.GetContainerMemoryBandwidthRequest).To(func(profilingManager spd.ServiceProfilingManager, podMeta metav1.ObjectMeta, cpuRequest int) (int, error) {
					switch podMeta.UID {
					case "p0":
						return 100, nil
					case "p1":
						return 300, nil
					case "uid-ignore-neg":
						return 400, nil // Requesting 400MBps
					}
					return 0, fmt.Errorf("unmocked spd call for pod %s", podMeta.UID)
				}).Build()
				mockey.Mock(cpuutil.GetContainerRequestedCores).Return(1.0).Build()
			},
			wantErr: nil,
			wantHints: &pluginapi.ListOfTopologyHints{
				Hints: []*pluginapi.TopologyHint{
					// NUMA 0: Allocatable 1000. Allocated 100. Request 400. New Allocated 500. Remaining 500. Preferred = true.
					{Nodes: []uint64{0}, Preferred: true},
				},
			},
		},
	}

	for _, tt := range tests {
		mockey.PatchConvey(tt.name, t, func() {
			tt.mocks(&tt.fields, &tt.args)
			o := &memoryBandwidthOptimizer{
				metaServer:         tt.fields.metaServer,
				emitter:            tt.fields.emitter,
				state:              tt.fields.state,
				ignoreNegativeHint: tt.fields.ignoreNegativeLeftAllocatableHint,
				preferSpreading:    tt.fields.spreadingMemoryBandwidthHint,
			}

			err := o.OptimizeHints(tt.args.request, tt.args.hints)

			if tt.wantErr != nil {
				assert.EqualErrorf(t, err, tt.wantErr.Error(), "test: %v", tt.name)
			} else {
				assert.NoErrorf(t, err, "test: %v", tt.name)
			}
			assert.Equalf(t, tt.wantHints, tt.args.hints, "test: %v", tt.name)
		})
	}
}
