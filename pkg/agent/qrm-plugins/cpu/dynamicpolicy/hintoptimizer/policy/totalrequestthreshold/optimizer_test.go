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

package totalrequestthreshold

import (
	"testing"

	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/hintoptimizer"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/hintoptimizer/policy"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	cpuutil "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/util"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

type fakeState struct {
	machineState state.NUMANodeMap
	podEntries   state.PodEntries
	allocations  map[string]map[string]*state.AllocationInfo
	allowOverlap bool
}

func (f *fakeState) GetMachineState() state.NUMANodeMap {
	return f.machineState
}

func (f *fakeState) GetNUMAHeadroom() map[int]float64 {
	return nil
}

func (f *fakeState) GetPodEntries() state.PodEntries {
	return f.podEntries
}

func (f *fakeState) GetAllocationInfo(podUID string, containerName string) *state.AllocationInfo {
	if f.allocations == nil {
		return nil
	}
	return f.allocations[podUID][containerName]
}

func (f *fakeState) GetAllowSharedCoresOverlapReclaimedCores() bool {
	return f.allowOverlap
}

func (f *fakeState) SetMachineState(numaNodeMap state.NUMANodeMap, _ bool) {
	f.machineState = numaNodeMap
}

func (f *fakeState) SetNUMAHeadroom(_ map[int]float64, _ bool) {}

func (f *fakeState) SetPodEntries(podEntries state.PodEntries, _ bool) {
	f.podEntries = podEntries
}

func (f *fakeState) SetAllocationInfo(podUID string, containerName string, allocationInfo *state.AllocationInfo, _ bool) {
	if f.allocations == nil {
		f.allocations = map[string]map[string]*state.AllocationInfo{}
	}
	if f.allocations[podUID] == nil {
		f.allocations[podUID] = map[string]*state.AllocationInfo{}
	}
	f.allocations[podUID][containerName] = allocationInfo
}

func (f *fakeState) SetAllowSharedCoresOverlapReclaimedCores(allowSharedCoresOverlapReclaimedCores, _ bool) {
	f.allowOverlap = allowSharedCoresOverlapReclaimedCores
}

func (f *fakeState) Delete(podUID string, containerName string, _ bool) {
	if f.allocations != nil {
		delete(f.allocations[podUID], containerName)
	}
}

func (f *fakeState) ClearState() {
	f.machineState = nil
	f.podEntries = nil
	f.allocations = nil
}

func (f *fakeState) StoreState() error {
	return nil
}

func newTestOptimizer(ratio float64, machineState state.NUMANodeMap) *cpuTotalRequestThresholdHintOptimizer {
	conf := config.NewConfiguration()
	conf.CPUQRMPluginConfig.TotalRequestThresholdHintOptimizerConfig.CPUTotalRequestThresholdRatio = ratio
	cpuTopology, _ := machine.GenerateDummyCPUTopology(8, 1, 2)

	return &cpuTotalRequestThresholdHintOptimizer{
		conf:  conf,
		state: &fakeState{machineState: machineState},
		metaServer: &metaserver.MetaServer{
			MetaAgent: &agent.MetaAgent{
				KatalystMachineInfo: &machine.KatalystMachineInfo{
					CPUTopology: cpuTopology,
				},
				PodFetcher: &pod.PodFetcherStub{},
			},
		},
	}
}

func newTestResourceRequest(reqCPU float64) *pluginapi.ResourceRequest {
	return &pluginapi.ResourceRequest{
		PodUid:        "pod-uid",
		PodNamespace:  "default",
		PodName:       "pod",
		ContainerName: "main",
		ResourceName:  string(v1.ResourceCPU),
		ResourceRequests: map[string]float64{
			string(v1.ResourceCPU): reqCPU,
		},
		Annotations: map[string]string{
			consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelSharedCores,
			consts.PodAnnotationMemoryEnhancementKey: `{"numa_binding": "true"}`,
		},
	}
}

func newTestAllocationInfo(podUID, containerName, containerType, ownerPoolName, qosLevel string,
	numaBinding bool, request float64,
) *state.AllocationInfo {
	annotations := map[string]string{
		consts.PodAnnotationQoSLevelKey: qosLevel,
	}
	if numaBinding {
		annotations[consts.PodAnnotationMemoryEnhancementNumaBinding] = consts.PodAnnotationMemoryEnhancementNumaBindingEnable
	}

	return &state.AllocationInfo{
		AllocationMeta: commonstate.AllocationMeta{
			PodUid:        podUID,
			PodNamespace:  "default",
			PodName:       podUID,
			ContainerName: containerName,
			ContainerType: containerType,
			OwnerPoolName: ownerPoolName,
			Annotations:   annotations,
			QoSLevel:      qosLevel,
		},
		RequestQuantity: request,
	}
}

func TestGetTotalAllocatableUsesTopologyCPUDetails(t *testing.T) {
	t.Parallel()

	cpuTopology, err := machine.GenerateDummyCPUTopology(8, 1, 2)
	require.NoError(t, err)

	optimizer := &cpuTotalRequestThresholdHintOptimizer{
		metaServer: &metaserver.MetaServer{
			MetaAgent: &agent.MetaAgent{
				KatalystMachineInfo: &machine.KatalystMachineInfo{
					CPUTopology: cpuTopology,
				},
				PodFetcher: &pod.PodFetcherStub{},
			},
		},
		reservedCPUs: machine.NewCPUSet(0),
	}

	require.Equal(t, float64(cpuTopology.CPUDetails.CPUsInNUMANodes(0).Difference(optimizer.reservedCPUs).Size()),
		optimizer.getTotalAllocatable(machine.NewCPUSet(0)))
}

func TestOptimizeHintsDefensiveBranches(t *testing.T) {
	t.Parallel()

	req := newTestResourceRequest(1)
	request := hintoptimizer.Request{
		ResourceRequest: req,
		CPURequest:      1,
	}

	for _, tt := range []struct {
		name        string
		ratio       float64
		request     hintoptimizer.Request
		hints       *pluginapi.ListOfTopologyHints
		wantErr     error
		wantErrText string
		wantHintLen int
	}{
		{
			name:        "nil request",
			ratio:       0.5,
			request:     hintoptimizer.Request{},
			hints:       &pluginapi.ListOfTopologyHints{},
			wantErrText: "got nil req",
		},
		{
			name:    "disabled ratio",
			ratio:   0,
			request: request,
			hints: &pluginapi.ListOfTopologyHints{
				Hints: []*pluginapi.TopologyHint{{Nodes: []uint64{0}, Preferred: true}},
			},
			wantHintLen: 1,
		},
		{
			name:    "invalid ratio",
			ratio:   2,
			request: request,
			hints: &pluginapi.ListOfTopologyHints{
				Hints: []*pluginapi.TopologyHint{{Nodes: []uint64{0}, Preferred: true}},
			},
			wantErrText: "hint optimizer skip",
		},
		{
			name:    "empty hints",
			ratio:   0.5,
			request: request,
			hints:   &pluginapi.ListOfTopologyHints{},
			wantErr: cpuutil.ErrNoAvailableCPUHints,
		},
		{
			name:    "non-positive allocatable filters hint",
			ratio:   0.5,
			request: request,
			hints: &pluginapi.ListOfTopologyHints{
				Hints: []*pluginapi.TopologyHint{{Nodes: []uint64{9}, Preferred: true}},
			},
			wantErr: cpuutil.ErrNoAvailableCPUHints,
		},
		{
			name:    "nil topology hint skipped",
			ratio:   0.5,
			request: request,
			hints: &pluginapi.ListOfTopologyHints{
				Hints: []*pluginapi.TopologyHint{nil, {Nodes: []uint64{0}, Preferred: true}},
			},
			wantHintLen: 1,
		},
		{
			name:    "invalid topology hint node",
			ratio:   0.5,
			request: request,
			hints: &pluginapi.ListOfTopologyHints{
				Hints: []*pluginapi.TopologyHint{{Nodes: []uint64{^uint64(0)}, Preferred: true}},
			},
			wantErrText: "parse elem",
		},
	} {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			machineState := state.NUMANodeMap{
				0: {DefaultCPUSet: machine.NewCPUSet(0, 1, 2, 3)},
			}
			err := newTestOptimizer(tt.ratio, machineState).OptimizeHints(tt.request, tt.hints)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			if tt.wantErrText != "" {
				require.ErrorContains(t, err, tt.wantErrText)
				return
			}

			require.NoError(t, err)
			require.Len(t, tt.hints.Hints, tt.wantHintLen)
		})
	}
}

func TestOptimizeHintsDisabledWithoutConfig(t *testing.T) {
	t.Parallel()

	req := newTestResourceRequest(1)
	hints := &pluginapi.ListOfTopologyHints{
		Hints: []*pluginapi.TopologyHint{{Nodes: []uint64{0}, Preferred: true}},
	}

	optimizer := &cpuTotalRequestThresholdHintOptimizer{
		state: &fakeState{
			machineState: state.NUMANodeMap{
				0: {DefaultCPUSet: machine.NewCPUSet(0, 1, 2, 3)},
			},
		},
	}
	err := optimizer.OptimizeHints(hintoptimizer.Request{
		ResourceRequest: req,
		CPURequest:      1,
	}, hints)
	require.NoError(t, err)
	require.Len(t, hints.Hints, 1)
}

func TestOptimizeHintsFiltersByNUMATotalRequest(t *testing.T) {
	t.Parallel()

	req := newTestResourceRequest(1)
	machineState := state.NUMANodeMap{
		0: {
			DefaultCPUSet: machine.NewCPUSet(0, 1, 2, 3),
			PodEntries: state.PodEntries{
				"snb-pod": {
					"main": newTestAllocationInfo("snb-pod", "main", pluginapi.ContainerType_MAIN.String(),
						commonstate.PoolNameShare, consts.PodAnnotationQoSLevelSharedCores, true, 1.5),
				},
			},
		},
		1: {
			DefaultCPUSet: machine.NewCPUSet(4, 5, 6, 7),
		},
	}

	hints := &pluginapi.ListOfTopologyHints{
		Hints: []*pluginapi.TopologyHint{
			{Nodes: []uint64{0}, Preferred: true},
			{Nodes: []uint64{1}, Preferred: true},
		},
	}

	err := newTestOptimizer(0.5, machineState).OptimizeHints(hintoptimizer.Request{
		ResourceRequest: req,
		CPURequest:      1,
	}, hints)
	require.NoError(t, err)
	require.Len(t, hints.Hints, 1)
	require.Equal(t, []uint64{1}, hints.Hints[0].Nodes)
}

func TestOptimizeHintsRejectsAllHintsByNUMATotalRequest(t *testing.T) {
	t.Parallel()

	req := newTestResourceRequest(3)
	machineState := state.NUMANodeMap{
		0: {
			DefaultCPUSet: machine.NewCPUSet(0, 1, 2, 3),
		},
	}
	hints := &pluginapi.ListOfTopologyHints{
		Hints: []*pluginapi.TopologyHint{{Nodes: []uint64{0}, Preferred: true}},
	}

	err := newTestOptimizer(0.5, machineState).OptimizeHints(hintoptimizer.Request{
		ResourceRequest: req,
		CPURequest:      3,
	}, hints)
	require.ErrorContains(t, err, "exceeds threshold")
	require.ErrorIs(t, err, cpuutil.ErrNoAvailableCPUHints)
}

func TestGetTotalRequest(t *testing.T) {
	t.Parallel()

	optimizer := newTestOptimizer(0.5, nil)
	machineState := state.NUMANodeMap{
		0: {
			DefaultCPUSet: machine.NewCPUSet(0, 1, 2, 3),
			PodEntries: state.PodEntries{
				"snb-pod": {
					"main": newTestAllocationInfo("snb-pod", "main", pluginapi.ContainerType_MAIN.String(),
						commonstate.PoolNameShare, consts.PodAnnotationQoSLevelSharedCores, true, 1.25),
					"sidecar": newTestAllocationInfo("snb-pod", "sidecar", pluginapi.ContainerType_SIDECAR.String(),
						commonstate.PoolNameShare, consts.PodAnnotationQoSLevelSharedCores, true, 0.5),
					"nil": nil,
				},
				"legacy-snb-pod": {
					"main": newTestAllocationInfo("legacy-snb-pod", "main", pluginapi.ContainerType_MAIN.String(),
						commonstate.PoolNameShare, consts.PodAnnotationQoSLevelSharedCores, true, 1.5),
				},
				"non-binding-pod": {
					"main": newTestAllocationInfo("non-binding-pod", "main", pluginapi.ContainerType_MAIN.String(),
						commonstate.PoolNameShare, consts.PodAnnotationQoSLevelSharedCores, false, 10),
				},
				"dedicated-pod": {
					"main": newTestAllocationInfo("dedicated-pod", "main", pluginapi.ContainerType_MAIN.String(),
						commonstate.PoolNameDedicated, consts.PodAnnotationQoSLevelDedicatedCores, true, 4),
				},
				"reclaimed-pod": {
					"main": newTestAllocationInfo("reclaimed-pod", "main", pluginapi.ContainerType_MAIN.String(),
						commonstate.PoolNameReclaim, consts.PodAnnotationQoSLevelReclaimedCores, true, 2),
				},
				"pod-uid": {
					"main": newTestAllocationInfo("pod-uid", "main", pluginapi.ContainerType_MAIN.String(),
						commonstate.PoolNameShare, consts.PodAnnotationQoSLevelSharedCores, true, 0.4),
				},
				commonstate.PoolNameShare: {
					commonstate.FakedContainerName: newTestAllocationInfo(commonstate.PoolNameShare, commonstate.FakedContainerName,
						pluginapi.ContainerType_MAIN.String(), commonstate.PoolNameShare, consts.PodAnnotationQoSLevelSharedCores, true, 100),
				},
			},
		},
		1: {
			DefaultCPUSet: machine.NewCPUSet(4, 5, 6, 7),
			PodEntries: state.PodEntries{
				"snb-on-numa-1": {
					"main": newTestAllocationInfo("snb-on-numa-1", "main", pluginapi.ContainerType_MAIN.String(),
						commonstate.PoolNameShare, consts.PodAnnotationQoSLevelSharedCores, true, 2),
				},
			},
		},
	}

	req := newTestResourceRequest(1)
	require.InDelta(t, 8.25, optimizer.getTotalRequest(req, 1.0, machineState, machine.NewCPUSet(0)), 1e-9)
	require.InDelta(t, 10.25, optimizer.getTotalRequest(req, 1.0, machineState, machine.NewCPUSet(0, 1)), 1e-9)

	reqNew := newTestResourceRequest(1)
	reqNew.PodUid = "another-pod"
	require.InDelta(t, 8.65, optimizer.getTotalRequest(reqNew, 1.0, machineState, machine.NewCPUSet(0)), 1e-9)
}

func TestNewCPUTotalRequestThresholdHintOptimizer(t *testing.T) {
	t.Parallel()

	conf := config.NewConfiguration()
	cpuTopology, _ := machine.GenerateDummyCPUTopology(8, 1, 2)

	opt, err := NewTotalRequestThresholdHintOptimizer(policy.HintOptimizerFactoryOptions{
		Conf:  conf,
		State: &fakeState{},
		MetaServer: &metaserver.MetaServer{
			MetaAgent: &agent.MetaAgent{
				KatalystMachineInfo: &machine.KatalystMachineInfo{CPUTopology: cpuTopology},
				PodFetcher:          &pod.PodFetcherStub{},
			},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, opt)
}

func TestRunReturnsNil(t *testing.T) {
	t.Parallel()

	optimizer := newTestOptimizer(0.5, nil)
	stopCh := make(chan struct{})
	close(stopCh)
	require.NoError(t, optimizer.Run(stopCh))
}
