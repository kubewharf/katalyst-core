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
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/gogo/protobuf/proto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	resource2 "k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/sets"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/advisorsvc"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	cpuconsts "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead"
	bulkheadtopology "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/utils/topology"
	advisorapi "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/cpuadvisor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	cpusetutil "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/util"
	"github.com/kubewharf/katalyst-core/pkg/agent/utilcomponent/featuregatenegotiation/finders"
	"github.com/kubewharf/katalyst-core/pkg/agent/utilcomponent/featuregatenegotiation/finders/feature_cpu"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/qrm/statedirectory"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	cgroupclient "github.com/kubewharf/katalyst-core/pkg/util/cgroup/client"
	"github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	resourcepackage "github.com/kubewharf/katalyst-core/pkg/util/resource-package"
)

func TestConvertGetAdviceResponsePropagatesDedicatedReclaimDisjoint(t *testing.T) {
	t.Parallel()

	unified := convertGetAdviceResponse(&advisorapi.GetAdviceResponse{
		DisableDedicatedCoresOverlapReclaimedCores: true,
	})

	require.True(t, unified.DisableDedicatedCoresOverlapReclaimedCores)
}

func TestValidateDedicatedReclaimDisjointTransport(t *testing.T) {
	t.Parallel()

	resp := &advisorapi.ListAndWatchResponse{
		DisableDedicatedCoresOverlapReclaimedCores: true,
	}
	req := &advisorapi.GetAdviceRequest{}

	err := validateDedicatedReclaimDisjointTransport(req, resp, nil)
	require.ErrorContains(t, err, feature_cpu.NegotiationFeatureGateDedicatedReclaimDisjointPartition)

	err = validateDedicatedReclaimDisjointTransport(nil, resp, nil)
	require.ErrorContains(t, err, "requires negotiated GetAdvice")

	featureGates := map[string]*advisorsvc.FeatureGate{
		feature_cpu.NegotiationFeatureGateDedicatedReclaimDisjointPartition: {
			Name:                  feature_cpu.NegotiationFeatureGateDedicatedReclaimDisjointPartition,
			Type:                  finders.FeatureGateTypeCPU,
			MustMutuallySupported: true,
		},
	}
	require.NoError(t, validateDedicatedReclaimDisjointTransport(req, resp, featureGates))
}

func TestAdvisorRequestHasActiveRampUp(t *testing.T) {
	t.Parallel()

	req := &advisorapi.GetAdviceRequest{
		Entries: map[string]*advisorapi.ContainerAllocationInfoEntries{
			"pod": {
				Entries: map[string]*advisorapi.ContainerAllocationInfo{
					"main": {
						AllocationInfo: &advisorapi.AllocationInfo{RampUp: true},
					},
				},
			},
		},
	}
	require.True(t, advisorRequestHasActiveRampUp(req))
	require.False(t, advisorRequestHasActiveRampUp(nil))
	require.False(t, advisorRequestHasActiveRampUp(&advisorapi.GetAdviceRequest{}))

	req.Entries["pod"].Entries["main"].AllocationInfo.RampUp = false
	require.False(t, advisorRequestHasActiveRampUp(req))

	req.Entries = map[string]*advisorapi.ContainerAllocationInfoEntries{
		commonstate.PoolNameShare: {
			Entries: map[string]*advisorapi.ContainerAllocationInfo{
				commonstate.FakedContainerName: {
					AllocationInfo: &advisorapi.AllocationInfo{RampUp: true},
				},
			},
		},
	}
	require.False(t, advisorRequestHasActiveRampUp(req))
}

func TestCreateGetAdviceRequestCopiesMainAnnotationsToNilSidecarAnnotations(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 2)
	require.NoError(t, err)
	policy, err := getTestDynamicPolicyWithoutInitialization(topology, t.TempDir())
	require.NoError(t, err)

	const (
		podUID      = "legacy-sidecar"
		mainName    = "main"
		sidecarName = "sidecar"
		annoKey     = "legacy.annotation"
		annoValue   = "enabled"
	)
	req, err := policy.createGetAdviceRequestFromSnapshot(state.PodEntries{
		podUID: {
			mainName: {
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        podUID,
					ContainerName: mainName,
					ContainerType: pluginapi.ContainerType_MAIN.String(),
					QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
					Annotations: map[string]string{
						annoKey: annoValue,
					},
				},
			},
			sidecarName: {
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        podUID,
					ContainerName: sidecarName,
					ContainerType: pluginapi.ContainerType_SIDECAR.String(),
					QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
				},
			},
		},
	}, policy.state.GetMachineState())
	require.NoError(t, err)
	require.Equal(t, annoValue,
		req.Entries[podUID].Entries[sidecarName].Metadata.Annotations[annoKey])
}

func TestAllocateByCPUAdvisorRejectsStaleRampUpGeneration(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 2)
	require.NoError(t, err)
	policy, err := getTestDynamicPolicyWithoutInitialization(topology, t.TempDir())
	require.NoError(t, err)

	req := &advisorapi.GetAdviceRequest{
		Entries: map[string]*advisorapi.ContainerAllocationInfoEntries{
			"pod": {
				Entries: map[string]*advisorapi.ContainerAllocationInfo{
					"main": {
						Metadata: &advisorsvc.ContainerMetadata{
							PodUid:        "pod",
							ContainerName: "main",
							QosLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
						},
						AllocationInfo: &advisorapi.AllocationInfo{RampUp: true},
					},
				},
			},
		},
	}
	revision := policy.state.GetRevision()
	allowOverlap := policy.state.GetAllowSharedCoresOverlapReclaimedCores()

	err = policy.allocateByCPUAdvisor(req, &advisorapi.ListAndWatchResponse{
		AllowSharedCoresOverlapReclaimedCores: !allowOverlap,
	}, nil)
	require.ErrorContains(t, err, "advisor request ramp-up state is stale")
	require.Equal(t, revision, policy.state.GetRevision())
	require.Equal(t, allowOverlap, policy.state.GetAllowSharedCoresOverlapReclaimedCores())
}

func TestAllocateByCPUAdvisorPreservesUnadvisedSharedNUMABindingRampUpAllocation(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 2)
	require.NoError(t, err)
	policy, err := getTestDynamicPolicyWithoutInitialization(topology, t.TempDir())
	require.NoError(t, err)
	policy.config.reservedCPUs = topology.CPUDetails.CPUsInNUMANodes(1)
	policy.adjustment.cpuSetAdjustmentHandlers = map[string]cpusetutil.CPUSetAdjustmentHandler{
		"noop": func(context.Context, cpusetutil.CPUSetAdjustmentHandlerCtx) error {
			return nil
		},
	}

	const (
		podUID        = "shared-numa-ramp-up"
		containerName = "main"
	)
	numa0CPUs := topology.CPUDetails.CPUsInNUMANodes(0).ToSliceInt()
	require.Len(t, numa0CPUs, 4)
	allocated := machine.NewCPUSet(numa0CPUs[:2]...)
	assignments, err := machine.GetNumaAwareAssignments(topology, allocated)
	require.NoError(t, err)
	original := &state.AllocationInfo{
		AllocationMeta: commonstate.AllocationMeta{
			PodUid:        podUID,
			ContainerName: containerName,
			ContainerType: pluginapi.ContainerType_MAIN.String(),
			QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
			Annotations: map[string]string{
				apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
			},
		},
		RampUp:                           true,
		AllocationResult:                 allocated.Clone(),
		OriginalAllocationResult:         allocated.Clone(),
		TopologyAwareAssignments:         assignments,
		OriginalTopologyAwareAssignments: machine.DeepcopyCPUAssignment(assignments),
		RequestQuantity:                  1,
	}
	policy.state.SetPodEntries(state.PodEntries{
		podUID: {
			containerName: original,
		},
	}, false)
	machineState, err := state.GenerateMachineStateFromPodEntries(
		topology, policy.state.GetPodEntries(), policy.state.GetMachineState())
	require.NoError(t, err)
	policy.state.SetMachineState(machineState, false)

	req, requestRevision, err := policy.createGetAdviceRequestAtRevision()
	require.NoError(t, err)

	beforeEntries := policy.state.GetPodEntries()
	beforeMachineState := policy.state.GetMachineState()
	err = policy.allocateByCPUAdvisorAtRevision(
		req, &advisorapi.ListAndWatchResponse{}, nil, requestRevision+1)
	require.ErrorContains(t, err, "advisor request state revision is stale")
	require.Equal(t, requestRevision, policy.state.GetRevision())
	require.Equal(t, beforeEntries, policy.state.GetPodEntries())
	require.Equal(t, beforeMachineState, policy.state.GetMachineState())

	require.NoError(t, policy.allocateByCPUAdvisorAtRevision(
		req, &advisorapi.ListAndWatchResponse{}, nil, requestRevision))

	got := policy.state.GetAllocationInfo(podUID, containerName)
	require.NotNil(t, got)
	require.True(t, got.RampUp)
	require.Empty(t, got.OwnerPoolName)
	require.True(t, got.CheckSharedNUMABinding())
	require.True(t, got.AllocationResult.Equals(allocated))
	require.True(t, got.OriginalAllocationResult.Equals(allocated))
	require.Equal(t, assignments, got.TopologyAwareAssignments)
	require.Equal(t, assignments, got.OriginalTopologyAwareAssignments)
}

func TestAllocateByCPUAdvisorRejectsStaleRequestRevision(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 2)
	require.NoError(t, err)
	policy, err := getTestDynamicPolicyWithoutInitialization(topology, t.TempDir())
	require.NoError(t, err)

	requestRevision := policy.state.GetRevision()
	allowOverlap := policy.state.GetAllowSharedCoresOverlapReclaimedCores()
	policy.state.SetAllowSharedCoresOverlapReclaimedCores(!allowOverlap, false)
	currentRevision := policy.state.GetRevision()
	require.Greater(t, currentRevision, requestRevision)

	err = policy.allocateByCPUAdvisorAtRevision(
		&advisorapi.GetAdviceRequest{},
		&advisorapi.ListAndWatchResponse{
			AllowSharedCoresOverlapReclaimedCores: allowOverlap,
		},
		nil,
		requestRevision,
	)
	require.ErrorContains(t, err, "advisor request state revision is stale")
	require.Equal(t, currentRevision, policy.state.GetRevision())
	require.Equal(t, !allowOverlap, policy.state.GetAllowSharedCoresOverlapReclaimedCores())
}

func TestAllocateByCPUAdvisorLegacyHardReclaimAliases(t *testing.T) {
	t.Parallel()

	newPolicyAndResponse := func(t *testing.T, includeMandatory bool) (*DynamicPolicy, *advisorapi.ListAndWatchResponse) {
		t.Helper()

		topology, err := machine.GenerateDummyCPUTopology(8, 1, 2)
		require.NoError(t, err)
		policy, err := getTestDynamicPolicyWithoutInitialization(topology, t.TempDir())
		require.NoError(t, err)
		policy.config.dynamicConfig.GetDynamicConfiguration().EnableReclaim = true
		policy.config.dynamicConfig.GetDynamicConfiguration().EnableRampUpReclaimHardPartition = true

		numa0CPU := topology.CPUDetails.CPUsInNUMANodes(0).ToSliceInt()[0]
		numa1CPU := topology.CPUDetails.CPUsInNUMANodes(1).ToSliceInt()[0]
		policy.state.SetPodEntries(state.PodEntries{
			"dedicated-0": {
				"main": &state.AllocationInfo{
					AllocationMeta: commonstate.AllocationMeta{
						PodUid:        "dedicated-0",
						ContainerName: "main",
						OwnerPoolName: commonstate.PoolNameDedicated,
						QoSLevel:      apiconsts.PodAnnotationQoSLevelDedicatedCores,
					},
					AllocationResult: machine.NewCPUSet(numa0CPU),
					RampUp:           true,
					TopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.NewCPUSet(numa0CPU),
					},
				},
			},
			"dedicated-1": {
				"main": &state.AllocationInfo{
					AllocationMeta: commonstate.AllocationMeta{
						PodUid:        "dedicated-1",
						ContainerName: "main",
						OwnerPoolName: commonstate.PoolNameDedicated,
						QoSLevel:      apiconsts.PodAnnotationQoSLevelDedicatedCores,
					},
					AllocationResult: machine.NewCPUSet(numa1CPU),
					TopologyAwareAssignments: map[int]machine.CPUSet{
						1: machine.NewCPUSet(numa1CPU),
					},
				},
			},
		}, false)
		machineState, err := state.GenerateMachineStateFromPodEntries(
			topology, policy.state.GetPodEntries(), policy.state.GetMachineState())
		require.NoError(t, err)
		policy.state.SetMachineState(machineState, false)

		overlapTarget := []*advisorapi.OverlapTarget{{
			OverlapTargetPoolName: commonstate.PoolNameDedicated,
			OverlapType:           advisorapi.OverlapType_OverlapWithPool,
		}}
		reclaimResults := map[int64]*advisorapi.NumaCalculationResult{
			0: {Blocks: []*advisorapi.Block{{
				BlockId: "legacy-alias-0", Result: 1, OverlapTargets: overlapTarget,
			}}},
			1: {Blocks: []*advisorapi.Block{{
				BlockId: "legacy-alias-1", Result: 1, OverlapTargets: overlapTarget,
			}}},
		}
		if includeMandatory {
			reclaimResults[commonstate.FakedNUMAID] = &advisorapi.NumaCalculationResult{
				Blocks: []*advisorapi.Block{{BlockId: "mandatory-reclaim", Result: 4}},
			}
		}

		return policy, &advisorapi.ListAndWatchResponse{
			AllowSharedCoresOverlapReclaimedCores:      true,
			DisableDedicatedCoresOverlapReclaimedCores: false,
			Entries: map[string]*advisorapi.CalculationEntries{
				"dedicated-0": {
					Entries: map[string]*advisorapi.CalculationInfo{
						"main": {
							OwnerPoolName: commonstate.PoolNameDedicated,
							CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
								0: {Blocks: []*advisorapi.Block{{BlockId: "legacy-alias-0", Result: 1}}},
							},
						},
					},
				},
				"dedicated-1": {
					Entries: map[string]*advisorapi.CalculationInfo{
						"main": {
							OwnerPoolName: commonstate.PoolNameDedicated,
							CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
								1: {Blocks: []*advisorapi.Block{{BlockId: "legacy-alias-1", Result: 1}}},
							},
						},
					},
				},
				commonstate.PoolNameReclaim: {
					Entries: map[string]*advisorapi.CalculationInfo{
						commonstate.FakedContainerName: {
							OwnerPoolName:             commonstate.PoolNameReclaim,
							CalculationResultsByNumas: reclaimResults,
						},
					},
				},
			},
		}
	}

	t.Run("mandatory reclaim rejects aliases that fragment physical cores", func(t *testing.T) {
		t.Parallel()
		policy, resp := newPolicyAndResponse(t, true)
		revision := policy.state.GetRevision()

		err := policy.allocateByCPUAdvisor(nil, resp, nil)

		require.ErrorContains(t, err, "not core-aligned")
		require.Equal(t, revision, policy.state.GetRevision())
	})

	t.Run("overlap only fails closed without mandatory reclaim", func(t *testing.T) {
		t.Parallel()
		policy, resp := newPolicyAndResponse(t, false)
		revision := policy.state.GetRevision()

		err := policy.allocateByCPUAdvisor(nil, resp, nil)
		require.ErrorContains(t, err, "hard-partition reclaim NUMA 0 has 0 CPUs, minimum is 2")
		require.Equal(t, revision, policy.state.GetRevision())
	})
}

func TestGenerateBlockCPUSetDisjointPlannerRequiresCapability(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 2)
	require.NoError(t, err)
	tmpDir := t.TempDir()
	policy, err := getTestDynamicPolicyWithoutInitialization(topology, tmpDir)
	require.NoError(t, err)

	resp := &advisorapi.ListAndWatchResponse{
		DisableDedicatedCoresOverlapReclaimedCores: true,
		Entries: map[string]*advisorapi.CalculationEntries{},
	}
	_, err = policy.generateBlockCPUSet(resp, nil, false)
	require.ErrorContains(t, err, "dedicated reclaim disjoint partition capability is not negotiated")

	featureGates := map[string]*advisorsvc.FeatureGate{
		feature_cpu.NegotiationFeatureGateDedicatedReclaimDisjointPartition: {
			Name: feature_cpu.NegotiationFeatureGateDedicatedReclaimDisjointPartition,
		},
	}
	got, err := policy.generateBlockCPUSet(resp, featureGates, false)
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestAllocateByCPUAdvisorConvergedFrameIsStrictNoOp(t *testing.T) {
	t.Parallel()
	policy, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()

	applied := 0
	policy.adjustment.cpuSetAdjustmentHandlers = map[string]cpusetutil.CPUSetAdjustmentHandler{
		"count": func(context.Context, cpusetutil.CPUSetAdjustmentHandlerCtx) error {
			applied++
			return nil
		},
	}
	resp := &advisorapi.ListAndWatchResponse{Entries: map[string]*advisorapi.CalculationEntries{}}

	require.NoError(t, policy.allocateByCPUAdvisor(nil, resp, nil))
	require.Equal(t, 1, applied)
	convergedRevision := policy.state.GetRevision()
	convergedEntries := policy.state.GetPodEntries()

	require.NoError(t, policy.allocateByCPUAdvisor(nil, resp, nil))
	require.Equal(t, convergedRevision, policy.state.GetRevision(),
		"a converged advisor frame must not create another state revision")
	require.Equal(t, convergedEntries, policy.state.GetPodEntries())
	require.Equal(t, 1, applied,
		"a converged advisor frame must not invoke the cpuset production chain again")
	require.False(t, policy.hasAnyPendingAdvisorPostCommitTarget())
}

func TestAllocateByCPUAdvisorPropagatesTopologyStaleWithoutPluginRetry(t *testing.T) {
	t.Parallel()
	policy, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()
	policy.adjustment.cpuSetAdjustmentRetryMu.Lock()
	policy.adjustment.cpuSetAdjustmentRetryStopping = true
	policy.adjustment.cpuSetAdjustmentRetryMu.Unlock()

	calls := 0
	policy.adjustment.cpuSetAdjustmentHandlers = map[string]cpusetutil.CPUSetAdjustmentHandler{
		"always-stale": func(context.Context, cpusetutil.CPUSetAdjustmentHandlerCtx) error {
			calls++
			return &frozenInitialSnapshotDriftTestError{}
		},
	}

	err := policy.allocateByCPUAdvisor(
		nil,
		&advisorapi.ListAndWatchResponse{Entries: map[string]*advisorapi.CalculationEntries{}},
		nil,
	)

	require.ErrorIs(t, err, bulkheadtopology.ErrCoordinatorPlanStale)
	require.Equal(t, 1, calls)
}

func TestAllocateByCPUAdvisorControlOnlyFrameIsNotStrictNoOp(t *testing.T) {
	t.Parallel()
	policy, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()

	applied := 0
	policy.adjustment.cpuSetAdjustmentHandlers = map[string]cpusetutil.CPUSetAdjustmentHandler{
		"count": func(context.Context, cpusetutil.CPUSetAdjustmentHandlerCtx) error {
			applied++
			return nil
		},
	}
	base := &advisorapi.ListAndWatchResponse{Entries: map[string]*advisorapi.CalculationEntries{}}
	require.NoError(t, policy.allocateByCPUAdvisor(nil, base, nil))
	convergedRevision := policy.state.GetRevision()

	controlOnly := &advisorapi.ListAndWatchResponse{
		Entries: map[string]*advisorapi.CalculationEntries{},
		ExtraEntries: []*advisorsvc.CalculationInfo{{
			CalculationResult: &advisorsvc.CalculationResult{
				Values: map[string]string{
					string(advisorapi.ControlKnobKeyCPUNUMAHeadroom): `{"0":2.5}`,
				},
			},
		}},
	}
	require.NoError(t, policy.allocateByCPUAdvisor(nil, controlOnly, nil))

	require.Equal(t, map[int]float64{0: 2.5}, policy.state.GetNUMAHeadroom())
	require.Equal(t, convergedRevision+1, policy.state.GetRevision(),
		"a response with changed control items must be committed and applied")
	require.Equal(t, 2, applied,
		"a response with changed control items must run the production chain")
}

func TestAllocateByCPUAdvisorPendingPostCommitApplyBlocksStageAdvanceAndRetriesSameStage(t *testing.T) {
	t.Parallel()
	policy, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()

	policy.adjustment.cpuSetAdjustmentRetryMu.Lock()
	policy.adjustment.cpuSetAdjustmentRetryStopping = true
	policy.adjustment.cpuSetAdjustmentRetryMu.Unlock()

	failApply := true
	applied := 0
	policy.adjustment.cpuSetAdjustmentHandlers = map[string]cpusetutil.CPUSetAdjustmentHandler{
		"controlled": func(context.Context, cpusetutil.CPUSetAdjustmentHandlerCtx) error {
			applied++
			if failApply {
				return fmt.Errorf("injected post-commit apply failure")
			}
			return nil
		},
	}
	resp := &advisorapi.ListAndWatchResponse{Entries: map[string]*advisorapi.CalculationEntries{}}

	err := policy.allocateByCPUAdvisor(nil, resp, nil)
	require.ErrorContains(t, err, "injected post-commit apply failure")
	stagedRevision := policy.state.GetRevision()
	stagedEntries := policy.state.GetPodEntries()
	require.True(t, policy.hasAnyPendingAdvisorPostCommitTarget())

	err = policy.allocateByCPUAdvisor(nil, resp, nil)
	require.ErrorContains(t, err, "injected post-commit apply failure")
	require.Equal(t, stagedRevision, policy.state.GetRevision(),
		"a failed retry must not commit the next advisor frame")
	require.Equal(t, stagedEntries, policy.state.GetPodEntries())
	require.True(t, policy.hasAnyPendingAdvisorPostCommitTarget())

	failApply = false
	require.NoError(t, policy.allocateByCPUAdvisor(nil, resp, nil))
	require.Equal(t, stagedRevision, policy.state.GetRevision(),
		"retrying the same stage must only finish its post-commit apply")
	require.Equal(t, stagedEntries, policy.state.GetPodEntries())
	require.False(t, policy.hasAnyPendingAdvisorPostCommitTarget())
	require.Equal(t, 3, applied)
}

func TestAdvisorReplacementTransactionRetainsExactTargetAcrossRetry(t *testing.T) {
	t.Parallel()
	cgroupClient := &replacementRecordingCgroupClient{}

	policy, resp, topology, reclaimBefore, _ := productionReplacementSourcePoolFixture(t)
	entries := policy.state.GetPodEntries()
	entries["dedicated-pod"]["main"].RampUp = false
	policy.state.SetPodEntries(entries, false)
	checkpointDir := t.TempDir()
	policy.advisorPostCommitCheckpointDir = checkpointDir
	policy.adjustment.cpuSetAdjustmentRetryMu.Lock()
	policy.adjustment.cpuSetAdjustmentRetryStopping = true
	policy.adjustment.cpuSetAdjustmentRetryMu.Unlock()

	expectedDedicated := machine.NewCPUSet(
		33, 34, 35, 36, 37, 38, 39, 40, 41, 42, 43, 45,
		97, 98, 99, 100, 101, 102, 103, 104, 105, 106, 107, 109,
	)
	expectedReclaim := machine.NewCPUSet(32, 44, 46, 47, 96, 108, 110, 111)
	expectedPrimary := topology.CPUDetails.CPUs().Difference(expectedReclaim)
	hierarchy := newReplacementRecordingHierarchyDriver(
		topology, reclaimBefore, "test-primary", "test-reclaim", "test-reclaim/numa-")
	cgroupClient.driver = hierarchy
	hierarchy.failFirstGrow = true
	bulkheadConfig := policy.config.conf.CPUQRMPluginConfig.BulkheadConfiguration
	bulkheadConfig.BulkheadPrimaryRelPath = "test-primary"
	bulkheadConfig.BulkheadReclaimRelPaths = []string{"test-reclaim"}
	bulkheadConfig.BulkheadReclaimNumaPrefixes = []string{"test-reclaim/numa-"}
	bulkheadConfig.BulkheadPartitionRelPaths = nil
	bulkheadConfig.BulkheadReclaimSiblingRelPaths = nil
	bulkheadConfig.EnableBulkheadReclaimSiblings = false
	dynamicConfig := policy.config.dynamicConfig.GetDynamicConfiguration()
	dynamicConfig.AdminQoSConfiguration.CPUPluginConfiguration.BulkheadConfig.Enable = true
	dynamicConfig.AdminQoSConfiguration.CPUPluginConfiguration.BulkheadConfig.EnableBulkheadCpusetTopology = true
	dynamicConfig.AdminQoSConfiguration.CPUPluginConfiguration.BulkheadConfig.EnableBulkheadCpusetMems = false
	dynamicConfig.AdminQoSConfiguration.CPUPluginConfiguration.BulkheadConfig.EnableBulkheadWorkqueue = false
	dynamicConfig.AdminQoSConfiguration.CPUPluginConfiguration.BulkheadConfig.EnableBulkheadSystemService = false
	manager, err := bulkhead.NewManagerWithCgroupClientFactory(policy.config.conf,
		func() cgroupclient.CgroupClient { return cgroupClient })
	require.NoError(t, err)
	policy.bulkhead.bulkheadManager = manager
	require.Empty(t, policy.adjustment.cpuSetAdjustmentHandlers)
	require.NoError(t, policy.RegisterCPUSetAdjustmentHandler(
		"bulkhead", policy.bulkhead.bulkheadManager.RunCPUSetAdjustmentHandlers))
	request := &advisorapi.GetAdviceRequest{
		Entries: map[string]*advisorapi.ContainerAllocationInfoEntries{
			"dedicated-pod": {
				Entries: map[string]*advisorapi.ContainerAllocationInfo{
					"main": {
						Metadata: &advisorsvc.ContainerMetadata{
							PodUid:        "dedicated-pod",
							ContainerName: "main",
							QosLevel:      apiconsts.PodAnnotationQoSLevelDedicatedCores,
						},
						AllocationInfo: &advisorapi.AllocationInfo{RampUp: false},
					},
				},
			},
		},
	}
	featureGates := map[string]*advisorsvc.FeatureGate{
		feature_cpu.NegotiationFeatureGateDedicatedReclaimDisjointPartition: {
			Name: feature_cpu.NegotiationFeatureGateDedicatedReclaimDisjointPartition,
		},
	}

	preCommitRevision := policy.state.GetRevision()
	stagingPath := policy.advisorPostCommitStagingPath()
	activePath := policy.advisorPostCommitCheckpointPath()
	var preparedTarget *advisorPostCommitTarget
	preCASObservations := 0
	policy.allocation.allocationHooks = append(policy.allocation.allocationHooks, func(_, _ *state.AllocationInfo) error {
		current := policy.currentAdvisorPostCommitTarget()
		if current == nil || !current.prepared {
			return nil
		}
		preCASObservations++
		require.Equal(t, preCommitRevision, policy.state.GetRevision(),
			"canonical revision must not advance before the prepared target is durable")
		require.FileExists(t, stagingPath,
			"the staging WAL must be durable before the canonical CAS")
		require.NoFileExists(t, activePath,
			"the active WAL must not be published before the canonical CAS")
		require.Equal(t, preCommitRevision, current.preCommitRevision)
		require.Equal(t, preCommitRevision+1, current.revision)
		require.True(t, proto.Equal(resp, current.response))
		staged, loadErr := loadAdvisorPostCommitTarget(
			stagingPath, policy.machine.machineInfo.CPUTopology)
		require.NoError(t, loadErr)
		require.True(t, advisorPostCommitTargetsEqual(current, staged),
			"the in-memory prepared target and durable staging WAL must be identical")
		if preparedTarget == nil {
			preparedTarget = current
		} else {
			require.Same(t, preparedTarget, current,
				"all pre-CAS hooks must observe one immutable prepared target")
		}
		return nil
	})

	err = policy.allocateByCPUAdvisor(request, resp, featureGates)
	require.ErrorContains(t, err, "injected hierarchy grow failure")
	require.Positive(t, preCASObservations)
	target := policy.currentAdvisorPostCommitTarget()
	require.NotNil(t, target)
	require.Same(t, preparedTarget, target)
	require.Same(t, target, policy.currentAdvisorPostCommitTarget())
	committedRevision := preCommitRevision + 1
	require.Equal(t, committedRevision, policy.state.GetRevision(),
		"the first attempt must CAS-commit exactly once")
	require.NoFileExists(t, stagingPath)
	require.FileExists(t, activePath)
	require.Equal(t, expectedDedicated,
		policy.state.GetAllocationInfo("dedicated-pod", "main").AllocationResult)
	require.Equal(t, expectedReclaim,
		policy.state.GetAllocationInfo(
			commonstate.PoolNameReclaim, commonstate.FakedContainerName).AllocationResult)

	active, err := loadAdvisorPostCommitTarget(activePath, policy.machine.machineInfo.CPUTopology)
	require.NoError(t, err)
	require.Equal(t, target.revision, active.revision)
	require.True(t, proto.Equal(resp, active.response))
	requireMigrationCheckpointTransitionEqual(
		t, target.migrationCheckpointTransition, active.migrationCheckpointTransition)
	firstAttempt := append([]replacementHierarchyWrite(nil), hierarchy.writes...)
	require.Equal(t, []replacementHierarchyWrite{
		{kind: replacementHierarchyWriteDrain, rel: "test-reclaim/numa-2", target: "32,46-47,96,108,110-111"},
		{kind: replacementHierarchyWriteDrain, rel: "test-reclaim", target: "32,46-47,96,108,110-111"},
		{kind: replacementHierarchyWriteDrain, rel: "test-primary", target: "0-31,33-43,45,48-95,97-107,112-127"},
		{kind: replacementHierarchyWriteGrow, rel: "test-reclaim", target: expectedReclaim.String()},
	}, firstAttempt)

	require.NoError(t, os.Mkdir(stagingPath, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(stagingPath, "block-cleanup"), []byte("x"), 0o600))
	err = policy.allocateByCPUAdvisor(request, resp, featureGates)
	require.ErrorContains(t, err, "previous advisor post-commit apply")
	require.ErrorContains(t, err, "remove reconciled advisor post-commit checkpoints")
	reconciledRevision := committedRevision + 1
	require.Equal(t, reconciledRevision, policy.state.GetRevision(),
		"the successful physical retry must commit its adjustment override exactly once")
	require.Equal(t, target.revision, policy.currentAdvisorPostCommitTarget().revision)
	require.True(t, proto.Equal(resp, policy.currentAdvisorPostCommitTarget().response))
	requireMigrationCheckpointTransitionEqual(t, target.migrationCheckpointTransition,
		policy.currentAdvisorPostCommitTarget().migrationCheckpointTransition)
	retryWrites := hierarchy.writes[len(firstAttempt):]
	require.NotEmpty(t, retryWrites)
	require.Equal(t, []replacementHierarchyWrite{
		{kind: replacementHierarchyWriteGrow, rel: "test-reclaim", target: expectedReclaim.String()},
		{kind: replacementHierarchyWriteGrow, rel: "test-primary", target: expectedPrimary.String()},
		{kind: replacementHierarchyWriteGrow, rel: "test-reclaim/numa-2", target: expectedReclaim.String()},
	}, retryWrites)
	require.Equal(t, expectedPrimary, hierarchy.entries["test-primary"].CPUs)
	require.Equal(t, expectedReclaim, hierarchy.entries["test-reclaim"].CPUs)
	require.Equal(t, expectedReclaim, hierarchy.entries["test-reclaim/numa-2"].CPUs)
	require.Equal(t, len(firstAttempt)+len(retryWrites), len(hierarchy.writes),
		"applied-marker cleanup failure must not replay hierarchy writes")
	require.Same(t, target, policy.currentAdvisorPostCommitTarget(),
		"writer fence must remain until WAL cleanup succeeds")
	require.True(t, target.cleanupPending)
	marked, err := loadAdvisorPostCommitTarget(activePath, policy.machine.machineInfo.CPUTopology)
	require.NoError(t, err)
	require.True(t, marked.applied, "applied marker must be durable before cleanup")
	require.Equal(t, target.revision, marked.revision)
	require.True(t, proto.Equal(resp, marked.response))
	requireMigrationCheckpointTransitionEqual(
		t, target.migrationCheckpointTransition, marked.migrationCheckpointTransition)
	require.Error(t, policy.state.SetMachineState(policy.state.GetMachineState(), false),
		"cleanup failure must retain the writer fence")

	require.NoError(t, os.Remove(filepath.Join(stagingPath, "block-cleanup")))
	require.NoError(t, os.Remove(stagingPath))
	require.NoError(t, policy.allocateByCPUAdvisor(request, resp, featureGates))
	require.Equal(t, reconciledRevision, policy.state.GetRevision(),
		"cleanup-only retry must not advance the committed revision")
	require.Equal(t, len(firstAttempt)+len(retryWrites), len(hierarchy.writes),
		"cleanup retry must not replay the physical target")
	require.Nil(t, policy.currentAdvisorPostCommitTarget())
	require.NoFileExists(t, activePath)
	require.NoFileExists(t, stagingPath)
	require.NoError(t, policy.ensureCPUStateWriterAllowed(
		policy.state.GetRevision(), "replacement cleanup", nil),
		"writer fence must be released only after cleanup")
}

func TestAdvisorReplacementSolverFailureDoesNotStageOrCommit(t *testing.T) {
	t.Parallel()

	policy, resp, topology, reclaimBefore, _ := productionReplacementSourcePoolFixture(t)
	entries := policy.state.GetPodEntries()
	entries["dedicated-pod"]["main"].RampUp = false
	require.NoError(t, policy.state.SetPodEntries(entries, false))
	setHardReclaimReplacementOptionsForTest(t, topology,
		func(options *hardReclaimReplacementOptions) {
			options.maxPartitionAssignmentEdges = 1
		})
	hierarchy := newReplacementRecordingHierarchyDriver(
		topology, reclaimBefore, "test-primary", "test-reclaim", "test-reclaim/numa-")
	policy.adjustment.cpuSetAdjustmentHandlers = map[string]cpusetutil.CPUSetAdjustmentHandler{
		"recording-hierarchy": func(ctx context.Context, _ cpusetutil.CPUSetAdjustmentHandlerCtx) error {
			entry := hierarchy.entries["test-reclaim"]
			return hierarchy.WriteCPUs(ctx, entry.Rel, entry.Identity, entry.CPUs)
		},
	}
	request := &advisorapi.GetAdviceRequest{
		Entries: map[string]*advisorapi.ContainerAllocationInfoEntries{
			"dedicated-pod": {
				Entries: map[string]*advisorapi.ContainerAllocationInfo{
					"main": {
						Metadata: &advisorsvc.ContainerMetadata{
							PodUid:        "dedicated-pod",
							ContainerName: "main",
							QosLevel:      apiconsts.PodAnnotationQoSLevelDedicatedCores,
						},
						AllocationInfo: &advisorapi.AllocationInfo{RampUp: false},
					},
				},
			},
		},
	}
	featureGates := map[string]*advisorsvc.FeatureGate{
		feature_cpu.NegotiationFeatureGateDedicatedReclaimDisjointPartition: {
			Name: feature_cpu.NegotiationFeatureGateDedicatedReclaimDisjointPartition,
		},
	}

	require.NoError(t, policy.state.StoreState())
	checkpointPath := filepath.Join(
		policy.advisorPostCommitCheckpointDir, cpuPluginStateFileName)
	beforeCheckpoint, err := os.ReadFile(checkpointPath)
	require.NoError(t, err)
	canonicalSnapshot := func() struct {
		revision       uint64
		entries        state.PodEntries
		machine        state.NUMANodeMap
		numaHeadroom   map[int]float64
		allowOverlap   bool
		disableOverlap bool
	} {
		return struct {
			revision       uint64
			entries        state.PodEntries
			machine        state.NUMANodeMap
			numaHeadroom   map[int]float64
			allowOverlap   bool
			disableOverlap bool
		}{
			revision:       policy.state.GetRevision(),
			entries:        policy.state.GetPodEntries(),
			machine:        policy.state.GetMachineState(),
			numaHeadroom:   policy.state.GetNUMAHeadroom(),
			allowOverlap:   policy.state.GetAllowSharedCoresOverlapReclaimedCores(),
			disableOverlap: policy.state.GetDisableDedicatedCoresOverlapReclaimedCores(),
		}
	}
	beforeCanonical := canonicalSnapshot()
	beforeTarget := policy.currentAdvisorPostCommitTarget()
	activePath := policy.advisorPostCommitCheckpointPath()
	stagingPath := policy.advisorPostCommitStagingPath()
	require.NoFileExists(t, activePath)
	require.NoFileExists(t, stagingPath)

	err = policy.allocateByCPUAdvisor(request, resp, featureGates)

	require.ErrorIs(t, err, errPartitionAssignmentEdgeBudget)
	var budgetErr *hardReclaimSearchBudgetExceeded
	require.ErrorAs(t, err, &budgetErr)
	require.Equal(t, hardReclaimBudgetGraphEdges, budgetErr.Budget)
	require.False(t, budgetErr.Diagnostics.Complete)
	require.Equal(t, beforeCanonical, canonicalSnapshot())
	afterCheckpoint, readErr := os.ReadFile(checkpointPath)
	require.NoError(t, readErr)
	require.Equal(t, beforeCheckpoint, afterCheckpoint)
	require.True(t, advisorPostCommitTargetsEqual(
		beforeTarget, policy.currentAdvisorPostCommitTarget()))
	require.NoFileExists(t, activePath)
	require.NoFileExists(t, stagingPath)
	require.Empty(t, hierarchy.writes)
}

type replacementHierarchyWriteKind string

const (
	replacementHierarchyWriteDrain replacementHierarchyWriteKind = "drain"
	replacementHierarchyWriteGrow  replacementHierarchyWriteKind = "expand"
)

type replacementHierarchyWrite struct {
	kind   replacementHierarchyWriteKind
	rel    string
	target string
}

type replacementRecordingHierarchyDriver struct {
	entries       map[string]bulkheadtopology.EntryState
	children      map[string][]bulkheadtopology.ChildRef
	roots         []bulkheadtopology.RootRef
	writes        []replacementHierarchyWrite
	failFirstGrow bool
}

func newReplacementRecordingHierarchyDriver(
	cpuTopology *machine.CPUTopology,
	reclaimBefore machine.CPUSet,
	primaryRel, reclaimRel, numaPrefix string,
) *replacementRecordingHierarchyDriver {
	allCPUs := cpuTopology.CPUDetails.CPUs()
	driver := &replacementRecordingHierarchyDriver{
		entries:  make(map[string]bulkheadtopology.EntryState),
		children: make(map[string][]bulkheadtopology.ChildRef),
	}
	driver.add(primaryRel, allCPUs.Difference(reclaimBefore), "0-3")
	driver.add(reclaimRel, reclaimBefore, "0-3")
	driver.add("sys", allCPUs, "0-3")
	driver.roots = []bulkheadtopology.RootRef{
		{Rel: primaryRel, Identity: driver.entries[primaryRel].Identity},
		{Rel: reclaimRel, Identity: driver.entries[reclaimRel].Identity},
		{Rel: "sys", Identity: driver.entries["sys"].Identity},
	}
	for _, numaID := range cpuTopology.CPUDetails.NUMANodes().ToSliceInt() {
		rel := fmt.Sprintf("%s%d", numaPrefix, numaID)
		driver.add(rel, reclaimBefore.Intersection(cpuTopology.CPUDetails.CPUsInNUMANodes(numaID)),
			fmt.Sprintf("%d", numaID))
		driver.children[reclaimRel] = append(driver.children[reclaimRel],
			bulkheadtopology.ChildRef{
				Name: filepath.Base(rel), Identity: driver.entries[rel].Identity,
			})
	}
	return driver
}

func (d *replacementRecordingHierarchyDriver) add(rel string, cpus machine.CPUSet, mems string) {
	d.entries[rel] = bulkheadtopology.EntryState{
		Rel: rel, Identity: replacementHierarchyIdentity(rel),
		CPUs: cpus.Clone(), ConfiguredCPUs: cpus.Clone(),
		Mems: mems, ConfiguredMems: mems,
	}
}

func (d *replacementRecordingHierarchyDriver) Close() error { return nil }

func (d *replacementRecordingHierarchyDriver) Roots(context.Context) ([]bulkheadtopology.RootRef, error) {
	return append([]bulkheadtopology.RootRef(nil), d.roots...), nil
}

func (d *replacementRecordingHierarchyDriver) StatIdentity(
	_ context.Context, rel string,
) (bulkheadtopology.CgroupIdentity, error) {
	entry, ok := d.entries[rel]
	if !ok {
		return bulkheadtopology.CgroupIdentity{}, os.ErrNotExist
	}
	return entry.Identity, nil
}

func (d *replacementRecordingHierarchyDriver) ReadEntry(
	_ context.Context, rel string,
) (bulkheadtopology.EntryState, error) {
	entry, ok := d.entries[rel]
	if !ok {
		return bulkheadtopology.EntryState{}, os.ErrNotExist
	}
	return cloneReplacementHierarchyEntry(entry), nil
}

func (d *replacementRecordingHierarchyDriver) ReadEntryWithActivity(
	ctx context.Context,
	rel string,
	identity bulkheadtopology.CgroupIdentity,
) (bulkheadtopology.EntryState, error) {
	entry, err := d.ReadEntry(ctx, rel)
	if err != nil {
		return bulkheadtopology.EntryState{}, err
	}
	if entry.Identity != identity {
		return bulkheadtopology.EntryState{}, bulkheadtopology.ErrCgroupIdentityChanged
	}
	entry.Activity = bulkheadtopology.CgroupActivity{
		TasksEmpty: true, CgroupProcsEmpty: true, Childless: len(d.children[rel]) == 0,
	}
	return entry, nil
}

func (d *replacementRecordingHierarchyDriver) ListChildren(
	_ context.Context, rel string,
) ([]bulkheadtopology.ChildRef, error) {
	return append([]bulkheadtopology.ChildRef(nil), d.children[rel]...), nil
}

func (d *replacementRecordingHierarchyDriver) WriteCPUs(
	_ context.Context,
	rel string,
	identity bulkheadtopology.CgroupIdentity,
	cpus machine.CPUSet,
) error {
	entry, ok := d.entries[rel]
	if !ok {
		return os.ErrNotExist
	}
	if entry.Identity != identity {
		return bulkheadtopology.ErrCgroupIdentityChanged
	}
	kind := replacementHierarchyWriteDrain
	if cpus.Size() > entry.ConfiguredCPUs.Size() {
		kind = replacementHierarchyWriteGrow
	}
	d.writes = append(d.writes, replacementHierarchyWrite{
		kind: kind, rel: rel, target: cpus.String(),
	})
	if kind == replacementHierarchyWriteGrow && d.failFirstGrow {
		d.failFirstGrow = false
		return fmt.Errorf("injected hierarchy grow failure")
	}
	entry.CPUs = cpus.Clone()
	entry.ConfiguredCPUs = cpus.Clone()
	d.entries[rel] = entry
	return nil
}

func (d *replacementRecordingHierarchyDriver) WriteMems(
	_ context.Context,
	rel string,
	identity bulkheadtopology.CgroupIdentity,
	mems string,
) error {
	entry, ok := d.entries[rel]
	if !ok {
		return os.ErrNotExist
	}
	if entry.Identity != identity {
		return bulkheadtopology.ErrCgroupIdentityChanged
	}
	entry.Mems = mems
	entry.ConfiguredMems = mems
	d.entries[rel] = entry
	return nil
}

func (*replacementRecordingHierarchyDriver) Classify(
	err error, _ bulkheadtopology.HierarchyOperation,
) bulkheadtopology.HierarchyErrorClass {
	if os.IsNotExist(err) {
		return bulkheadtopology.HierarchyErrorStale
	}
	return bulkheadtopology.HierarchyErrorInvalid
}

func (*replacementRecordingHierarchyDriver) Capabilities() bulkheadtopology.HierarchyCapabilities {
	return bulkheadtopology.HierarchyCapabilities{
		StableIdentity: true, KernelParentContainment: true,
	}
}

type replacementRecordingCgroupClient struct {
	cgroupclient.FakeCgroupClient
	driver *replacementRecordingHierarchyDriver
}

func (c *replacementRecordingCgroupClient) Version(context.Context) cgroupclient.CgroupVersion {
	return cgroupclient.CgroupVersionV1
}

func (c *replacementRecordingCgroupClient) SnapshotDriver() bulkheadtopology.HierarchyDriver {
	return c.driver
}

func (c *replacementRecordingCgroupClient) StatDir(
	ctx context.Context, rel string,
) (time.Time, error) {
	_, err := c.driver.StatIdentity(ctx, rel)
	return time.Time{}, err
}

func (c *replacementRecordingCgroupClient) ListChildren(
	ctx context.Context, rel string,
) ([]string, error) {
	children, err := c.driver.ListChildren(ctx, rel)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(children))
	for _, child := range children {
		names = append(names, child.Name)
	}
	return names, nil
}

func replacementHierarchyIdentity(rel string) bulkheadtopology.CgroupIdentity {
	var inode uint64 = 1
	for _, value := range []byte(rel) {
		inode = inode*131 + uint64(value)
	}
	return bulkheadtopology.CgroupIdentity{Device: 1, Inode: inode}
}

func cloneReplacementHierarchyEntry(
	entry bulkheadtopology.EntryState,
) bulkheadtopology.EntryState {
	entry.CPUs = entry.CPUs.Clone()
	entry.ConfiguredCPUs = entry.ConfiguredCPUs.Clone()
	return entry
}

func requireMigrationCheckpointTransitionEqual(
	t *testing.T,
	expected, actual steadyFakeNUMAMigrationCheckpointTransition,
) {
	t.Helper()
	require.Equal(t, expected.kind, actual.kind)
	if expected.target == nil {
		require.Nil(t, actual.target)
		return
	}
	require.NotNil(t, actual.target)
	require.Equal(t, expected.target.constraintDigest, actual.target.constraintDigest)
	require.Equal(t, expected.target.target, actual.target.target)
}

func TestAllocateByCPUAdvisorConcurrentFrameCannotOvertakePendingPostCommitApply(t *testing.T) {
	t.Parallel()
	policy, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()

	policy.adjustment.cpuSetAdjustmentRetryMu.Lock()
	policy.adjustment.cpuSetAdjustmentRetryStopping = true
	policy.adjustment.cpuSetAdjustmentRetryMu.Unlock()

	failApply := true
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	policy.adjustment.cpuSetAdjustmentHandlers = map[string]cpusetutil.CPUSetAdjustmentHandler{
		"controlled": func(context.Context, cpusetutil.CPUSetAdjustmentHandlerCtx) error {
			if failApply {
				return fmt.Errorf("injected post-commit apply failure")
			}
			entered <- struct{}{}
			<-release
			return nil
		},
	}
	resp := &advisorapi.ListAndWatchResponse{Entries: map[string]*advisorapi.CalculationEntries{}}
	require.Error(t, policy.allocateByCPUAdvisor(nil, resp, nil))
	stagedRevision := policy.state.GetRevision()
	failApply = false

	firstDone := make(chan error, 1)
	go func() {
		firstDone <- policy.allocateByCPUAdvisor(nil, resp, nil)
	}()
	<-entered

	secondStarted := make(chan struct{})
	secondDone := make(chan error, 1)
	go func() {
		close(secondStarted)
		secondDone <- policy.allocateByCPUAdvisor(nil, resp, nil)
	}()
	<-secondStarted
	require.Equal(t, stagedRevision, policy.state.GetRevision(),
		"a concurrent frame must not advance while the prior post-commit apply is blocked")

	close(release)
	require.NoError(t, <-firstDone)
	require.NoError(t, <-secondDone)
	require.Equal(t, stagedRevision, policy.state.GetRevision())
	require.False(t, policy.hasAnyPendingAdvisorPostCommitTarget())
}

func TestAdvisorAndRetrySameTargetShareExecutionLease(t *testing.T) {
	t.Parallel()
	policy, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()

	policy.adjustment.cpuSetAdjustmentRetryMu.Lock()
	policy.adjustment.cpuSetAdjustmentRetryStopping = true
	policy.adjustment.cpuSetAdjustmentRetryMu.Unlock()

	firstHandlerEntered := make(chan struct{})
	releaseFirstHandler := make(chan struct{})
	var callsMu sync.Mutex
	headroomCalls := 0
	handlerCalls := 0
	policy.adjustment.cpuSetAdjustmentHandlers = map[string]cpusetutil.CPUSetAdjustmentHandler{
		"controlled": func(context.Context, cpusetutil.CPUSetAdjustmentHandlerCtx) error {
			callsMu.Lock()
			handlerCalls++
			call := handlerCalls
			callsMu.Unlock()
			if call == 1 {
				close(firstHandlerEntered)
				<-releaseFirstHandler
			}
			return nil
		},
	}
	policy.applyHeadroomHook = func(*advisorapi.ListAndWatchResponse) error {
		callsMu.Lock()
		headroomCalls++
		callsMu.Unlock()
		return nil
	}
	resp := &advisorapi.ListAndWatchResponse{Entries: map[string]*advisorapi.CalculationEntries{}}

	advisorDone := make(chan error, 1)
	go func() {
		advisorDone <- policy.allocateByCPUAdvisor(nil, resp, nil)
	}()
	<-firstHandlerEntered

	target := policy.currentAdvisorPostCommitTarget()
	require.NotNil(t, target)
	require.FileExists(t, policy.advisorPostCommitCheckpointPath())

	retryLocked := make(chan struct{})
	continueRetry := make(chan struct{})
	retryDone := make(chan error, 1)
	go func() {
		policy.Lock()
		close(retryLocked)
		<-continueRetry
		retryDone <- policy.retryLatestCPUSetAdjustment(
			context.Background(), cpusetutil.CPUSetAdjustmentModeRetry)
		policy.Unlock()
	}()
	<-retryLocked
	close(continueRetry)

	// Once this lock is available, the retry has reached the execution
	// lease wait. It must not have replayed any target side effect. Holding the
	// policy lock also acts as the rendezvous barrier here (it blocks until the
	// retry goroutine has released the execution lease).
	policy.Lock()
	callsMu.Lock()
	require.Equal(t, 1, headroomCalls)
	require.Equal(t, 1, handlerCalls)
	callsMu.Unlock()
	policy.Unlock()

	close(releaseFirstHandler)
	require.NoError(t, <-advisorDone)
	require.NoError(t, <-retryDone)

	callsMu.Lock()
	require.Equal(t, 1, headroomCalls, "the retry must not re-enter a completed target")
	require.Equal(t, 1, handlerCalls, "the retry must not replay cpuset side effects")
	callsMu.Unlock()
	require.Nil(t, policy.currentAdvisorPostCommitTarget())
	require.NoFileExists(t, policy.advisorPostCommitCheckpointPath(),
		"a stale retry must not resurrect the cleaned active WAL")
	require.NoFileExists(t, policy.advisorPostCommitStagingPath())

	select {
	case policy.adjustment.cpuSetAdjustmentExecution <- struct{}{}:
		<-policy.adjustment.cpuSetAdjustmentExecution
	default:
		t.Fatal("execution lease was not released")
	}
}

func TestAllocateByCPUAdvisorExecutionLeaseTimeoutLeavesCommittedTransactionUntouched(t *testing.T) {
	t.Parallel()
	policy, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()

	policy.adjustment.cpuSetAdjustmentRetryMu.Lock()
	policy.adjustment.cpuSetAdjustmentRetryStopping = true
	policy.adjustment.cpuSetAdjustmentRetryMu.Unlock()
	policy.config.conf.CPUQRMPluginConfig.BulkheadConfiguration.
		TopologyConvergenceBudget.DeadlineDuration = time.Millisecond

	entered := make(chan struct{})
	release := make(chan struct{})
	var enteredOnce sync.Once
	policy.adjustment.cpuSetAdjustmentHandlers = map[string]cpusetutil.CPUSetAdjustmentHandler{
		"block-first-advisor": func(context.Context, cpusetutil.CPUSetAdjustmentHandlerCtx) error {
			enteredOnce.Do(func() { close(entered) })
			<-release
			return nil
		},
	}
	resp := &advisorapi.ListAndWatchResponse{Entries: map[string]*advisorapi.CalculationEntries{}}

	firstDone := make(chan error, 1)
	go func() {
		firstDone <- policy.allocateByCPUAdvisor(nil, resp, nil)
	}()
	<-entered

	revision := policy.state.GetRevision()
	entries := policy.state.GetPodEntries()
	machineState := policy.state.GetMachineState()
	target := policy.currentAdvisorPostCommitTarget()
	require.NotNil(t, target)
	activePath := policy.advisorPostCommitCheckpointPath()
	stagingPath := policy.advisorPostCommitStagingPath()
	activeWAL, err := os.ReadFile(activePath)
	require.NoError(t, err)
	require.NoFileExists(t, stagingPath)

	err = policy.allocateByCPUAdvisor(nil, resp, nil)
	require.Equal(t, context.DeadlineExceeded, err)
	require.Equal(t, revision, policy.state.GetRevision())
	require.Equal(t, entries, policy.state.GetPodEntries())
	require.Equal(t, machineState, policy.state.GetMachineState())
	require.Same(t, target, policy.currentAdvisorPostCommitTarget())
	require.NoFileExists(t, stagingPath)
	gotActiveWAL, readErr := os.ReadFile(activePath)
	require.NoError(t, readErr)
	require.Equal(t, activeWAL, gotActiveWAL)

	close(release)
	require.NoError(t, <-firstDone)
}

func TestValidateHardPartitionReclaimDistribution(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 2)
	require.NoError(t, err)
	allCPUs := topology.CPUDetails.CPUs()
	numa0OneCore := coresInNUMA(topology, 0, 0, 1)
	numa0TwoCores := coresInNUMA(topology, 0, 0, 2)
	numa1OneCore := coresInNUMA(topology, 1, 0, 1)
	fragmentedFull := numa0OneCore.Union(numa1OneCore)
	fragmented := fragmentedFull.Difference(machine.NewCPUSet(fragmentedFull.ToSliceInt()[0]))

	for _, tc := range []struct {
		name     string
		reclaim  machine.CPUSet
		eligible machine.CPUSet
		skip     sets.Int
		wantErr  string
	}{
		{
			name:     "two per NUMA",
			reclaim:  numa0OneCore.Union(numa1OneCore),
			eligible: allCPUs,
		},
		{
			name:     "fragmented physical core",
			reclaim:  fragmented,
			eligible: allCPUs,
			wantErr:  "not core-aligned",
		},
		{
			name:     "four and zero",
			reclaim:  numa0TwoCores,
			eligible: allCPUs,
			wantErr:  "NUMA 1 has 0 CPUs, minimum is 2",
		},
		{
			name:     "three and one",
			reclaim:  machine.NewCPUSet(0, 1, 2, 4),
			eligible: allCPUs,
			wantErr:  "not core-aligned",
		},
		{
			name:     "eligible missing physical NUMA",
			reclaim:  numa0OneCore,
			eligible: topology.CPUDetails.CPUsInNUMANodes(0),
		},
		{
			name:     "empty eligible",
			reclaim:  machine.NewCPUSet(),
			eligible: machine.NewCPUSet(),
			wantErr:  "NUMA 0 has 0 CPUs, minimum is 2",
		},
		{
			name:    "fragmented eligible NUMA skipped",
			reclaim: numa0OneCore,
			eligible: numa0OneCore.Union(machine.NewCPUSet(
				numa1OneCore.ToSliceInt()[0])),
		},
		{
			name:     "outside eligible",
			reclaim:  numa0OneCore.Union(numa1OneCore),
			eligible: allCPUs.Difference(numa1OneCore),
			wantErr:  "outside eligible CPUs",
		},
		{
			name:     "outside machine",
			reclaim:  machine.NewCPUSet(0, 1, 4, 5, 8),
			eligible: machine.NewCPUSet(0, 1, 4, 5, 8),
			wantErr:  "outside machine topology: 8",
		},
		{
			// a steady non-reclaim exclusive DNB owns NUMA 1 and keeps only its
			// finalized reserve there; without the carve-out the imbalance guard
			// rejects every other ramp-up QoS on the node.
			name:     "steady exclusive numa skipped avoids minimum",
			reclaim:  numa0TwoCores,
			eligible: allCPUs,
			skip:     sets.NewInt(1),
		},
		{
			name:     "steady exclusive numa skipped avoids imbalance",
			reclaim:  numa0TwoCores.Union(numa1OneCore),
			eligible: allCPUs,
			skip:     sets.NewInt(1),
		},
		{
			name:     "non-skipped numa still enforces minimum",
			reclaim:  numa0TwoCores,
			eligible: allCPUs,
			skip:     sets.NewInt(0),
			wantErr:  "NUMA 1 has 0 CPUs, minimum is 2",
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := validateHardPartitionReclaimDistribution(
				tc.reclaim, tc.eligible, topology, tc.skip,
				minimumHardReclaimCoresPerNUMA*topology.CPUsPerCore())
			if tc.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tc.wantErr)
		})
	}
}

func TestValidateHardPartitionReclaimDistributionAllowsCapacitySaturatedImbalance(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(16, 1, 2)
	require.NoError(t, err)
	reclaim := coresInNUMA(topology, 0, 0, 1).
		Union(coresInNUMA(topology, 1, 0, 3))

	require.NoError(t, validateHardPartitionReclaimDistribution(
		reclaim,
		reclaim,
		topology,
		nil,
		topology.CPUsPerCore(),
	))
}

func TestValidateHardPartitionBlockPlanSkipsWhenDisabled(t *testing.T) {
	t.Parallel()

	policy := &DynamicPolicy{}
	require.NoError(t, policy.validateHardPartitionBlockPlan(nil, nil, false))
}

func TestGenerateBlockCPUSetDisjointPlannerUsesJointRPEligibility(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 2)
	require.NoError(t, err)
	policy, err := getTestDynamicPolicyWithoutInitialization(topology, t.TempDir())
	require.NoError(t, err)

	legacyOverlap := coresInNUMA(topology, 0, 0, 1)
	policy.state.SetPodEntries(state.PodEntries{
		"pod-dedicated": {
			"main": &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "pod-dedicated",
					ContainerName: "main",
					OwnerPoolName: commonstate.PoolNameDedicated,
				},
				AllocationResult: legacyOverlap,
				TopologyAwareAssignments: map[int]machine.CPUSet{
					0: legacyOverlap,
				},
			},
		},
		commonstate.PoolNameReclaim: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult: legacyOverlap,
			},
		},
	}, false)
	machineState := policy.state.GetMachineState()
	machineState[0].ResourcePackageStates = map[string]*state.ResourcePackageState{
		"rp-a": {PinnedCPUSet: legacyOverlap},
	}
	policy.state.SetMachineState(machineState, false)

	resp := &advisorapi.ListAndWatchResponse{
		DisableDedicatedCoresOverlapReclaimedCores: true,
		Entries: map[string]*advisorapi.CalculationEntries{
			"pod-dedicated": {
				Entries: map[string]*advisorapi.CalculationInfo{
					"main": {
						OwnerPoolName: resourcepackage.WrapOwnerPoolName(commonstate.PoolNameDedicated, "rp-a"),
						CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
							0: {Blocks: []*advisorapi.Block{{BlockId: "dedicated-rotated", Result: 2}}},
						},
					},
				},
			},
			commonstate.PoolNameReclaim: {
				Entries: map[string]*advisorapi.CalculationInfo{
					commonstate.FakedContainerName: {
						OwnerPoolName: commonstate.PoolNameReclaim,
						CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
							0: {Blocks: []*advisorapi.Block{{BlockId: "reclaim-rotated", Result: 2}}},
						},
					},
				},
			},
		},
	}
	featureGates := map[string]*advisorsvc.FeatureGate{
		feature_cpu.NegotiationFeatureGateDedicatedReclaimDisjointPartition: {
			Name: feature_cpu.NegotiationFeatureGateDedicatedReclaimDisjointPartition,
		},
	}

	got, err := policy.generateBlockCPUSet(resp, featureGates, false)
	require.NoError(t, err)
	require.Equal(t, legacyOverlap, got["dedicated-rotated"])
	require.True(t, got["dedicated-rotated"].Intersection(got["reclaim-rotated"]).IsEmpty())
	require.True(t, got["reclaim-rotated"].IsSubsetOf(topology.CPUDetails.CPUsInNUMANodes(0)))
	require.True(t, got["reclaim-rotated"].Intersection(legacyOverlap).IsEmpty())

	resp.Entries["pod-dedicated"].Entries["main"].
		CalculationResultsByNumas[0].Blocks[0].Result = 1
	shrunk, err := policy.generateBlockCPUSet(resp, featureGates, false)
	require.NoError(t, err)
	require.True(t, shrunk["dedicated-rotated"].IsSubsetOf(got["dedicated-rotated"]))
	require.Equal(t, 1, got["dedicated-rotated"].Difference(shrunk["dedicated-rotated"]).
		Union(shrunk["dedicated-rotated"].Difference(got["dedicated-rotated"])).Size())
}

func TestGenerateBlockCPUSetDisjointPlannerJointlySolvesRemainingShared(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(16, 2, 2)
	require.NoError(t, err)
	policy, err := getTestDynamicPolicyWithoutInitialization(topology, t.TempDir())
	require.NoError(t, err)

	numa0 := topology.CPUDetails.CPUsInNUMANodes(0)
	pinned := coresInNUMA(topology, 0, 0, 2)
	require.Equal(t, 4, pinned.Size())
	unpinned := numa0.Difference(pinned)
	machineState := policy.state.GetMachineState()
	machineState[0].ResourcePackageStates = map[string]*state.ResourcePackageState{
		"rp-a": {PinnedCPUSet: pinned},
	}
	policy.state.SetMachineState(machineState, false)

	resp := advisorBlockTestResponse([]advisorBlockTestAlias{
		{
			entry: "pod-dedicated", subEntry: "main",
			owner:  resourcepackage.WrapOwnerPoolName(commonstate.PoolNameDedicated, "rp-a"),
			numaID: 0, blockID: "dedicated", quantity: 2,
		},
		{
			entry: commonstate.PoolNameReclaim, subEntry: commonstate.FakedContainerName,
			owner:  commonstate.PoolNameReclaim,
			numaID: 0, blockID: "reclaim", quantity: 2,
		},
		{
			entry: "latency-sensitive-share", subEntry: commonstate.FakedContainerName,
			owner:  "latency-sensitive-share",
			numaID: 0, blockID: "remaining-shared", quantity: 4,
		},
	}, rand.New(rand.NewSource(13)))
	resp.DisableDedicatedCoresOverlapReclaimedCores = true
	featureGates := map[string]*advisorsvc.FeatureGate{
		feature_cpu.NegotiationFeatureGateDedicatedReclaimDisjointPartition: {
			Name: feature_cpu.NegotiationFeatureGateDedicatedReclaimDisjointPartition,
		},
	}

	got, err := policy.generateBlockCPUSet(resp, featureGates, false)
	require.NoError(t, err)
	require.Equal(t, unpinned, got["remaining-shared"])
	require.True(t, got["dedicated"].IsSubsetOf(pinned))
	require.True(t, got["reclaim"].IsSubsetOf(pinned))
	require.True(t, got["dedicated"].Intersection(got["reclaim"]).IsEmpty())
	require.Equal(t, numa0, got["dedicated"].Union(got["reclaim"]).Union(got["remaining-shared"]))
}

func TestSolveAdvisorDescriptorPhaseDoesNotStarveNarrowShared(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(256, 2, 8)
	require.NoError(t, err)
	policy, err := getTestDynamicPolicyWithoutInitialization(topology, t.TempDir())
	require.NoError(t, err)

	allCPUs := topology.CPUDetails.CPUs()
	numa0 := topology.CPUDetails.CPUsInNUMANodes(0)
	numa0CPUs := numa0.ToSliceInt()
	require.Len(t, numa0CPUs, 32)

	base := []advisorBlockDescriptor{
		{
			BlockID: "reclaim-numa0", Owners: []string{"reclaim"},
			Class: advisorBlockClassMandatoryReclaim, NUMAID: 0, Quantity: 28,
			ComponentKey: "reclaim-numa0", Eligible: numa0,
			OldPreferred: machine.NewCPUSet(numa0CPUs[:28]...),
		},
		{
			BlockID: "reclaim-global", Owners: []string{"reclaim"},
			Class: advisorBlockClassMandatoryReclaim, NUMAID: commonstate.FakedNUMAID, Quantity: 220,
			ComponentKey: "reclaim-global", Eligible: allCPUs,
			OldPreferred: allCPUs.Difference(machine.NewCPUSet(numa0CPUs[28:]...)),
		},
		{
			BlockID: "shared-numa0", Owners: []string{"shared-numa0"},
			Class: advisorBlockClassShared, NUMAID: 0, Quantity: 4,
			ComponentKey: "shared-numa0", Eligible: numa0,
		},
	}

	var want advisorapi.BlockCPUSet
	for seed := int64(0); seed < 20; seed++ {
		descriptors := append([]advisorBlockDescriptor(nil), base...)
		rand.New(rand.NewSource(seed)).Shuffle(len(descriptors), func(i, j int) {
			descriptors[i], descriptors[j] = descriptors[j], descriptors[i]
		})
		got := advisorapi.BlockCPUSet{}
		remaining, solveErr := policy.solveAdvisorDescriptorPhase(descriptors, allCPUs, got, true, false)
		require.NoError(t, solveErr, "seed %d", seed)
		require.Equal(t, 28, got["reclaim-numa0"].Size(), "seed %d", seed)
		require.Equal(t, 220, got["reclaim-global"].Size(), "seed %d", seed)
		require.Equal(t, 4, got["shared-numa0"].Size(), "seed %d", seed)
		require.True(t, got["shared-numa0"].IsSubsetOf(numa0), "seed %d", seed)
		require.Equal(t, 4, remaining.Size(), "seed %d", seed)
		require.True(t, remaining.Intersection(numa0).IsEmpty(), "seed %d", seed)
		require.LessOrEqual(t,
			got["reclaim-numa0"].Union(got["reclaim-global"]).Intersection(numa0).Size(),
			28,
			"seed %d", seed)
		if seed == 0 {
			want = got
			continue
		}
		require.Equal(t, want, got, "seed %d", seed)
	}
}

func TestSolveAdvisorDescriptorPhaseHandles24Plus178Plus4(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(256, 2, 8)
	require.NoError(t, err)
	policy, err := getTestDynamicPolicyWithoutInitialization(topology, t.TempDir())
	require.NoError(t, err)

	numa0 := topology.CPUDetails.CPUsInNUMANodes(0)
	outside := topology.CPUDetails.CPUs().Difference(numa0).ToSliceInt()
	available := numa0.Union(machine.NewCPUSet(outside[:176]...))
	numa0CPUs := numa0.ToSliceInt()
	descriptors := []advisorBlockDescriptor{
		{
			BlockID: "reclaim-numa0", Owners: []string{"reclaim"},
			Class: advisorBlockClassMandatoryReclaim, NUMAID: 0, Quantity: 24,
			ComponentKey: "reclaim-numa0", Eligible: numa0,
			OldPreferred: machine.NewCPUSet(numa0CPUs[:24]...),
		},
		{
			BlockID: "reclaim-global", Owners: []string{"reclaim"},
			Class: advisorBlockClassMandatoryReclaim, NUMAID: commonstate.FakedNUMAID, Quantity: 178,
			ComponentKey: "reclaim-global", Eligible: available,
			OldPreferred: available.Difference(machine.NewCPUSet(numa0CPUs[24:]...)),
		},
		{
			BlockID: "shared-numa0", Owners: []string{"shared-numa0"},
			Class: advisorBlockClassShared, NUMAID: 0, Quantity: 4,
			ComponentKey: "shared-numa0", Eligible: numa0,
		},
	}

	got := advisorapi.BlockCPUSet{}
	remaining, err := policy.solveAdvisorDescriptorPhase(descriptors, available, got, true, false)

	require.Equal(t, available, remaining)
	require.ErrorContains(t, err, "insufficient aggregate capacity for quantity 178")
	require.Empty(t, got)
}

func TestSolveAdvisorDescriptorPhaseFailureIsAtomic(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(256, 2, 8)
	require.NoError(t, err)
	policy, err := getTestDynamicPolicyWithoutInitialization(topology, t.TempDir())
	require.NoError(t, err)

	allCPUs := topology.CPUDetails.CPUs()
	numa0 := topology.CPUDetails.CPUsInNUMANodes(0)
	result := advisorapi.BlockCPUSet{"sentinel": machine.NewCPUSet(255)}
	before := result["sentinel"].Clone()
	descriptors := []advisorBlockDescriptor{
		{
			BlockID: "reclaim-numa0", Owners: []string{"reclaim"},
			Class: advisorBlockClassMandatoryReclaim, NUMAID: 0, Quantity: 30,
			ComponentKey: "reclaim-numa0", Eligible: numa0,
		},
		{
			BlockID: "shared-numa0", Owners: []string{"shared-numa0"},
			Class: advisorBlockClassShared, NUMAID: 0, Quantity: 4,
			ComponentKey: "shared-numa0", Eligible: numa0,
		},
	}

	remaining, err := policy.solveAdvisorDescriptorPhase(descriptors, allCPUs, result, true, false)

	require.Error(t, err)
	require.Equal(t, allCPUs, remaining)
	require.Equal(t, advisorapi.BlockCPUSet{"sentinel": before}, result)
}

func TestGenerateBlockCPUSetLegacyIgnoresNegotiatedDisjointPlanner(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 2)
	require.NoError(t, err)
	policy, err := getTestDynamicPolicyWithoutInitialization(topology, t.TempDir())
	require.NoError(t, err)
	resp := &advisorapi.ListAndWatchResponse{
		Entries: map[string]*advisorapi.CalculationEntries{
			"ignored-by-legacy": {
				Entries: map[string]*advisorapi.CalculationInfo{"nil": nil},
			},
		},
	}
	featureGates := map[string]*advisorsvc.FeatureGate{
		feature_cpu.NegotiationFeatureGateDedicatedReclaimDisjointPartition: {
			Name: feature_cpu.NegotiationFeatureGateDedicatedReclaimDisjointPartition,
		},
	}

	got, err := policy.generateBlockCPUSet(resp, featureGates, false)
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestExclusiveDisjointPartitionLifecycleAndFlagTransitions(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 2)
	require.NoError(t, err)
	checkpointDir := t.TempDir()
	policy, err := getTestDynamicPolicyWithoutInitialization(topology, checkpointDir)
	require.NoError(t, err)
	policy.advisorPostCommitCheckpointDir = checkpointDir

	wholeNUMA := topology.CPUDetails.CPUsInNUMANodes(0)
	initialDedicated := coresInNUMA(
		topology, 0, 0, wholeNUMA.Size()/(2*topology.CPUsPerCore()))
	policy.state.SetPodEntries(state.PodEntries{
		"pod-dedicated": {
			"main": &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "pod-dedicated",
					ContainerName: "main",
					OwnerPoolName: commonstate.PoolNameDedicated,
					QoSLevel:      apiconsts.PodAnnotationQoSLevelDedicatedCores,
				},
				AllocationResult: initialDedicated,
				TopologyAwareAssignments: map[int]machine.CPUSet{
					0: initialDedicated,
				},
				RampUp: true,
			},
		},
		commonstate.PoolNameReclaim: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult: wholeNUMA.Clone(),
				TopologyAwareAssignments: map[int]machine.CPUSet{
					0: wholeNUMA.Clone(),
				},
			},
		},
	}, false)
	machineState, err := state.GenerateMachineStateFromPodEntries(
		topology, policy.state.GetPodEntries(), policy.state.GetMachineState())
	require.NoError(t, err)
	policy.state.SetMachineState(machineState, false)

	disjointResponse := func(suffix string, allowSharedOverlap bool) *advisorapi.ListAndWatchResponse {
		return &advisorapi.ListAndWatchResponse{
			AllowSharedCoresOverlapReclaimedCores:      allowSharedOverlap,
			DisableDedicatedCoresOverlapReclaimedCores: true,
			Entries: map[string]*advisorapi.CalculationEntries{
				"pod-dedicated": {
					Entries: map[string]*advisorapi.CalculationInfo{
						"main": {
							OwnerPoolName: commonstate.PoolNameDedicated,
							CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
								0: {Blocks: []*advisorapi.Block{{
									BlockId: "dedicated-" + suffix,
									Result:  uint64(wholeNUMA.Size() / 2),
								}}},
							},
						},
					},
				},
				commonstate.PoolNameReclaim: {
					Entries: map[string]*advisorapi.CalculationInfo{
						commonstate.FakedContainerName: {
							OwnerPoolName: commonstate.PoolNameReclaim,
							CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
								0: {Blocks: []*advisorapi.Block{{
									BlockId: "reclaim-" + suffix,
									Result:  uint64(wholeNUMA.Size() / 2),
								}}},
							},
						},
					},
				},
			},
		}
	}
	featureGates := map[string]*advisorsvc.FeatureGate{
		feature_cpu.NegotiationFeatureGateDedicatedReclaimDisjointPartition: {
			Name: feature_cpu.NegotiationFeatureGateDedicatedReclaimDisjointPartition,
		},
	}
	var reconciling *advisorapi.ListAndWatchResponse
	appliedFrames := 0
	policy.adjustment.cpuSetAdjustmentHandlers = map[string]cpusetutil.CPUSetAdjustmentHandler{
		"observe-pending": func(_ context.Context, _ cpusetutil.CPUSetAdjustmentHandlerCtx) error {
			target := policy.currentAdvisorPostCommitTarget()
			require.NotNil(t, target, "target must remain pending while reconcile is running")
			require.Equal(t, policy.state.GetRevision(), target.revision)
			require.Equal(t, reconciling, target.response)
			require.FileExists(t, filepath.Join(checkpointDir, advisorPostCommitCheckpointName),
				"WAL must remain durable until reconcile is applied")
			appliedFrames++
			return nil
		},
	}
	activeRequest := func() *advisorapi.GetAdviceRequest {
		rampUp := policy.state.GetAllocationInfo("pod-dedicated", "main").RampUp
		return &advisorapi.GetAdviceRequest{
			Entries: map[string]*advisorapi.ContainerAllocationInfoEntries{
				"pod-dedicated": {
					Entries: map[string]*advisorapi.ContainerAllocationInfo{
						"main": {
							Metadata: &advisorsvc.ContainerMetadata{
								PodUid:        "pod-dedicated",
								ContainerName: "main",
								QosLevel:      apiconsts.PodAnnotationQoSLevelDedicatedCores,
							},
							AllocationInfo: &advisorapi.AllocationInfo{RampUp: rampUp},
						},
					},
				},
			},
		}
	}
	applyFrame := func(
		req *advisorapi.GetAdviceRequest,
		resp *advisorapi.ListAndWatchResponse,
		gates map[string]*advisorsvc.FeatureGate,
	) advisorapi.BlockCPUSet {
		t.Helper()
		reconciling = resp
		if req != nil {
			req = activeRequest()
		}
		revision := policy.state.GetRevision()
		require.NoError(t, policy.allocateByCPUAdvisor(req, resp, gates))
		require.Equal(t, revision+1, policy.state.GetRevision(),
			"each frame must CAS-commit desired state exactly once")
		require.False(t, policy.hasAnyPendingAdvisorPostCommitTarget(),
			"successfully applied frame must clear pending target")
		require.NoFileExists(t, filepath.Join(checkpointDir, advisorPostCommitCheckpointName),
			"successfully applied frame must remove WAL")
		blocks, generateErr := policy.generateBlockCPUSet(resp, gates, false)
		require.NoError(t, generateErr)
		return blocks
	}

	first := disjointResponse("first", false)
	revisionBeforeNegotiation := policy.state.GetRevision()
	err = policy.allocateByCPUAdvisor(activeRequest(), first, nil)
	require.ErrorContains(t, err,
		feature_cpu.NegotiationFeatureGateDedicatedReclaimDisjointPartition)
	require.Equal(t, revisionBeforeNegotiation, policy.state.GetRevision(),
		"an unnegotiated DD frame must not change desired state")
	require.False(t, policy.hasAnyPendingAdvisorPostCommitTarget())
	require.NoFileExists(t, filepath.Join(checkpointDir, advisorPostCommitCheckpointName))

	firstBlocks := applyFrame(&advisorapi.GetAdviceRequest{}, first, featureGates)
	firstDedicated := firstBlocks["dedicated-first"]
	firstReclaim := firstBlocks["reclaim-first"]
	require.True(t, policy.state.GetAllocationInfo("pod-dedicated", "main").RampUp,
		"dedicated ramp-up must remain active until the shared transition period expires")
	require.True(t, firstDedicated.Intersection(firstReclaim).IsEmpty())
	require.Equal(t, wholeNUMA, firstDedicated.Union(firstReclaim))
	require.True(t, policy.state.GetDisableDedicatedCoresOverlapReclaimedCores())
	require.False(t, policy.state.GetAllowSharedCoresOverlapReclaimedCores())

	second := disjointResponse("second", true)
	secondBlocks := applyFrame(&advisorapi.GetAdviceRequest{}, second, featureGates)
	require.Equal(t, firstDedicated, secondBlocks["dedicated-second"],
		"clearing RampUp must not move the dedicated partition on the second advice")
	require.Equal(t, firstReclaim, secondBlocks["reclaim-second"],
		"clearing RampUp must not move the reclaim partition on the second advice")
	require.True(t, policy.state.GetDisableDedicatedCoresOverlapReclaimedCores(),
		"changing AS must not overwrite DD")
	require.True(t, policy.state.GetAllowSharedCoresOverlapReclaimedCores())

	legacy := &advisorapi.ListAndWatchResponse{
		AllowSharedCoresOverlapReclaimedCores: true,
		Entries: map[string]*advisorapi.CalculationEntries{
			"pod-dedicated": {
				Entries: map[string]*advisorapi.CalculationInfo{
					"main": {
						OwnerPoolName: commonstate.PoolNameDedicated,
						CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
							0: {Blocks: []*advisorapi.Block{{
								BlockId: "legacy-dedicated",
								Result:  uint64(wholeNUMA.Size() / 2),
							}}},
						},
					},
				},
			},
			commonstate.PoolNameReclaim: {
				Entries: map[string]*advisorapi.CalculationInfo{
					commonstate.FakedContainerName: {
						OwnerPoolName: commonstate.PoolNameReclaim,
						CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
							0: {Blocks: []*advisorapi.Block{{
								BlockId: "legacy-reclaim",
								Result:  uint64(wholeNUMA.Size() / 2),
							}}},
						},
					},
				},
			},
		},
	}
	legacyBlocks := applyFrame(nil, legacy, map[string]*advisorsvc.FeatureGate{})
	require.False(t, policy.state.GetDisableDedicatedCoresOverlapReclaimedCores())
	legacyDedicated := legacyBlocks["legacy-dedicated"]
	legacyReclaim := legacyBlocks["legacy-reclaim"]
	require.Equal(t, legacyDedicated,
		policy.state.GetAllocationInfo("pod-dedicated", "main").AllocationResult)
	require.Equal(t, legacyReclaim, policy.state.GetAllocationInfo(
		commonstate.PoolNameReclaim, commonstate.FakedContainerName).AllocationResult)
	require.True(t, policy.state.GetAllowSharedCoresOverlapReclaimedCores(),
		"legacy transition must preserve the independent AS flag")
	require.Equal(t, 3, appliedFrames,
		"both DD frames and the legacy transition must reach post-commit apply")
}

type advisorCommitRecordingState struct {
	state.State
	calls int
	err   error
}

type advisorCommitGuardState struct {
	state.State
	unconditionalCommitCalls int
	conditionalCommitCalls   int
	conditionalRevision      uint64
}

type staleAdvisorCommitState struct {
	state.State
	injected bool
}

func prepareAndCommitAdvisorBlocks(
	policy *DynamicPolicy,
	blockCPUSet advisorapi.BlockCPUSet,
	resp *advisorapi.ListAndWatchResponse,
	allowOverlap bool,
) error {
	pending, err := policy.applyBlocksWithDynamicConfig(blockCPUSet, resp, allowOverlap, false, policy.config.dynamicConfig.GetDynamicConfiguration())
	if err != nil {
		return err
	}
	return policy.commitPendingAdvisorState(pending)
}

func TestDynamicPolicyApplyBlocksUsesNegotiatedReclaimPlanWhenFreeCPUsCannotMeetFloor(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 1)
	require.NoError(t, err)
	policy, err := getTestDynamicPolicyWithoutInitialization(topology, t.TempDir())
	require.NoError(t, err)
	policy.config.reservedCPUs = machine.NewCPUSet()
	policy.config.dynamicConfig.GetDynamicConfiguration().EnableReclaim = true
	policy.config.dynamicConfig.GetDynamicConfiguration().EnableRampUpReclaimHardPartition = true
	policy.config.dynamicConfig.GetDynamicConfiguration().InitialRampUpReclaimCPUSetRatio = 0.25

	entries := state.PodEntries{
		"dedicated-pod": {
			"main": &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "dedicated-pod",
					ContainerName: "main",
					OwnerPoolName: commonstate.PoolNameDedicated,
					QoSLevel:      apiconsts.PodAnnotationQoSLevelDedicatedCores,
				},
				AllocationResult: machine.NewCPUSet(1, 2, 3, 5, 6, 7),
				RequestQuantity:  6,
			},
		},
		commonstate.PoolNameReclaim: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult: machine.NewCPUSet(0, 4),
			},
		},
	}
	policy.state.SetPodEntries(entries, false)
	policy.state.SetMachineState(state.NUMANodeMap{
		0: {
			DefaultCPUSet: machine.NewCPUSet(0, 4),
			PodEntries:    entries,
		},
	}, false)
	resp := &advisorapi.ListAndWatchResponse{
		DisableDedicatedCoresOverlapReclaimedCores: true,
		Entries: map[string]*advisorapi.CalculationEntries{
			"dedicated-pod": {Entries: map[string]*advisorapi.CalculationInfo{
				"main": {
					OwnerPoolName: commonstate.PoolNameDedicated,
					CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
						0: {Blocks: []*advisorapi.Block{{BlockId: "dedicated", Result: 6}}},
					},
				},
			}},
			commonstate.PoolNameReclaim: {Entries: map[string]*advisorapi.CalculationInfo{
				commonstate.FakedContainerName: {
					OwnerPoolName: commonstate.PoolNameReclaim,
					CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
						0: {Blocks: []*advisorapi.Block{{BlockId: "reclaim", Result: 2}}},
					},
				},
			}},
		},
	}

	pending, err := policy.applyBlocksWithDynamicConfig(advisorapi.BlockCPUSet{
		"dedicated": machine.NewCPUSet(1, 2, 3, 5, 6, 7),
		"reclaim":   machine.NewCPUSet(0, 4),
	}, resp, false, false, policy.config.dynamicConfig.GetDynamicConfiguration())
	require.NoError(t, err)
	require.True(t, pending.entries[commonstate.PoolNameReclaim][commonstate.FakedContainerName].
		AllocationResult.Equals(machine.NewCPUSet(0, 4)))
}

func TestDynamicPolicyApplyBlocksMaterializesDefaultShareFromResidual(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 1)
	require.NoError(t, err)
	policy, err := getTestDynamicPolicyWithoutInitialization(topology, t.TempDir())
	require.NoError(t, err)
	emitter := &recordingMetricEmitter{}
	policy.emitter.emitter = emitter
	dynamicConf := policy.config.dynamicConfig.GetDynamicConfiguration()
	dynamicConf.FillDefaultSharePoolWithNonReclaimCPUs = true
	dynamicConf.EnableReclaim = true
	dynamicConf.EnableRampUpReclaimHardPartition = true
	dynamicConf.InitialRampUpReclaimCPUSetRatio = 0.5

	policy.state.SetPodEntries(state.PodEntries{
		commonstate.PoolNameReclaim: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult: machine.NewCPUSet(0),
			},
		},
		"custom": {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta("custom"),
				AllocationResult: machine.NewCPUSet(1),
			},
		},
		"custom-pod": {
			"main": &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "custom-pod",
					ContainerName: "main",
					OwnerPoolName: "custom",
					QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
				},
				AllocationResult: machine.NewCPUSet(1),
			},
		},
		"ramp-up-pod": {
			"main": &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "ramp-up-pod",
					ContainerName: "main",
					QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
				},
				RampUp:           true,
				AllocationResult: machine.NewCPUSet(0, 1, 2, 3, 4, 5, 6, 7),
			},
		},
	}, false)

	resp := &advisorapi.ListAndWatchResponse{
		Entries: map[string]*advisorapi.CalculationEntries{
			commonstate.PoolNameShare: {
				Entries: map[string]*advisorapi.CalculationInfo{
					commonstate.FakedContainerName: {
						OwnerPoolName: commonstate.PoolNameShare,
						CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
							commonstate.FakedNUMAID: {
								Blocks: []*advisorapi.Block{{BlockId: "share", Result: 8}},
							},
						},
					},
				},
			},
			commonstate.PoolNameReclaim: {
				Entries: map[string]*advisorapi.CalculationInfo{
					commonstate.FakedContainerName: {
						OwnerPoolName: commonstate.PoolNameReclaim,
						CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
							commonstate.FakedNUMAID: {
								Blocks: []*advisorapi.Block{{BlockId: "reclaim", Result: 1}},
							},
						},
					},
				},
			},
			"custom": {
				Entries: map[string]*advisorapi.CalculationInfo{
					commonstate.FakedContainerName: {
						OwnerPoolName: "custom",
						CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
							commonstate.FakedNUMAID: {
								Blocks: []*advisorapi.Block{{BlockId: "custom", Result: 1}},
							},
						},
					},
				},
			},
		},
	}
	blockCPUSet := advisorapi.BlockCPUSet{
		"reclaim": machine.NewCPUSet(0),
		"custom":  machine.NewCPUSet(1),
	}

	pending, err := policy.applyBlocksWithDynamicConfig(blockCPUSet, resp, false, true, policy.config.dynamicConfig.GetDynamicConfiguration())
	require.NoError(t, err)
	share := pending.entries[commonstate.PoolNameShare][commonstate.FakedContainerName].AllocationResult
	// Default share is materialized from the residual CPUs left after the fixed
	// reclaim ({0}) and custom ({1}) pools; the ramp-up hard-active region no
	// longer carves an extra legacy floor out of the residual, so share absorbs
	// the full residual {2..7}.
	require.True(t, share.Equals(machine.NewCPUSet(2, 3, 4, 5, 6, 7)),
		"default share should absorb the full residual after fixed pools, got %s", share)
	require.NoError(t, policy.commitPendingAdvisorState(pending))
	requirePoolSizeMetric(t, emitter.records, commonstate.PoolNameShare, commonstate.PoolNameShare, 0, int64(share.Size()))
}

func TestDynamicPolicyApplyBlocksExcludesDNBFromDefaultShareResidual(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 1)
	require.NoError(t, err)
	policy, err := getTestDynamicPolicyWithoutInitialization(topology, t.TempDir())
	require.NoError(t, err)
	policy.config.dynamicConfig.GetDynamicConfiguration().FillDefaultSharePoolWithNonReclaimCPUs = true

	dnbCPUSet := machine.NewCPUSet(2, 6)
	policy.state.SetPodEntries(state.PodEntries{
		"dedicated-pod": {
			"main": &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "dedicated-pod",
					ContainerName: "main",
					OwnerPoolName: commonstate.PoolNameDedicated,
					QoSLevel:      apiconsts.PodAnnotationQoSLevelDedicatedCores,
					Annotations: map[string]string{
						apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
					},
				},
				AllocationResult: dnbCPUSet.Clone(),
				RequestQuantity:  float64(dnbCPUSet.Size()),
			},
		},
		commonstate.PoolNameReclaim: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult: machine.NewCPUSet(0, 4),
			},
		},
	}, false)

	resp := &advisorapi.ListAndWatchResponse{
		Entries: map[string]*advisorapi.CalculationEntries{
			"dedicated-pod": {
				Entries: map[string]*advisorapi.CalculationInfo{
					"main": {
						OwnerPoolName: commonstate.PoolNameDedicated,
						CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
							0: {Blocks: []*advisorapi.Block{{BlockId: "dedicated", Result: 2}}},
						},
					},
				},
			},
			commonstate.PoolNameShare: {
				Entries: map[string]*advisorapi.CalculationInfo{
					commonstate.FakedContainerName: {
						OwnerPoolName: commonstate.PoolNameShare,
						CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
							commonstate.FakedNUMAID: {
								Blocks: []*advisorapi.Block{{BlockId: "share", Result: 8}},
							},
						},
					},
				},
			},
			commonstate.PoolNameReclaim: {
				Entries: map[string]*advisorapi.CalculationInfo{
					commonstate.FakedContainerName: {
						OwnerPoolName: commonstate.PoolNameReclaim,
						CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
							commonstate.FakedNUMAID: {
								Blocks: []*advisorapi.Block{{BlockId: "reclaim", Result: 2}},
							},
						},
					},
				},
			},
		},
	}

	pending, err := policy.applyBlocksWithDynamicConfig(advisorapi.BlockCPUSet{
		"dedicated": dnbCPUSet.Clone(),
		"reclaim":   machine.NewCPUSet(0, 4),
	}, resp, false, false, policy.config.dynamicConfig.GetDynamicConfiguration())
	require.NoError(t, err)
	shareCPUSet := pending.entries[commonstate.PoolNameShare][commonstate.FakedContainerName].AllocationResult
	require.True(t, shareCPUSet.Equals(machine.NewCPUSet(3, 5, 7)))
	require.True(t, shareCPUSet.Intersection(dnbCPUSet).IsEmpty())
}

func TestDynamicPolicyApplyBlocksUsesCandidateEntriesForRNBTopology(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 2)
	require.NoError(t, err)
	policy, err := getTestDynamicPolicyWithoutInitialization(topology, t.TempDir())
	require.NoError(t, err)

	numa0 := topology.CPUDetails.CPUsInNUMANodes(0)
	numa1 := topology.CPUDetails.CPUsInNUMANodes(1)
	allCPUs := numa0.Union(numa1)
	policy.state.SetPodEntries(state.PodEntries{
		commonstate.PoolNameReclaim: {
			commonstate.FakedContainerName: {
				AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult: allCPUs.Clone(),
			},
		},
		"rnb": {
			"main": {
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "rnb",
					ContainerName: "main",
					OwnerPoolName: commonstate.PoolNameReclaim,
					QoSLevel:      apiconsts.PodAnnotationQoSLevelReclaimedCores,
					Annotations: map[string]string{
						apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
						cpuconsts.CPUStateAnnotationKeyNUMAHint:             "0",
					},
				},
				AllocationResult: numa0.Clone(),
			},
		},
		"non-rnb": {
			"main": {
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "non-rnb",
					ContainerName: "main",
					OwnerPoolName: commonstate.PoolNameReclaim,
					QoSLevel:      apiconsts.PodAnnotationQoSLevelReclaimedCores,
				},
				AllocationResult: allCPUs.Clone(),
			},
		},
	}, false)

	resp := &advisorapi.ListAndWatchResponse{
		Entries: map[string]*advisorapi.CalculationEntries{
			commonstate.PoolNameReclaim: {
				Entries: map[string]*advisorapi.CalculationInfo{
					commonstate.FakedContainerName: {
						OwnerPoolName: commonstate.PoolNameReclaim,
						CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
							0: {Blocks: []*advisorapi.Block{{BlockId: "reclaim-0", Result: uint64(numa0.Size())}}},
							1: {Blocks: []*advisorapi.Block{{BlockId: "reclaim-1", Result: uint64(numa1.Size())}}},
						},
					},
				},
			},
		},
	}
	pending, err := policy.applyBlocksWithDynamicConfig(advisorapi.BlockCPUSet{
		"reclaim-0": numa0.Clone(),
		"reclaim-1": numa1.Clone(),
	}, resp, false, false, policy.config.dynamicConfig.GetDynamicConfiguration())
	require.NoError(t, err)

	got := pending.entries["non-rnb"]["main"].AllocationResult
	require.True(t, got.Equals(numa1),
		"non-RNB allocation must exclude the candidate RNB target NUMA, got %s", got)
}

func TestDynamicPolicyValidatesDefaultShareAsUpperBoundWhenBackfillEnabled(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 1)
	require.NoError(t, err)
	policy, err := getTestDynamicPolicyWithoutInitialization(topology, t.TempDir())
	require.NoError(t, err)
	policy.config.dynamicConfig.GetDynamicConfiguration().FillDefaultSharePoolWithNonReclaimCPUs = true
	policy.state.SetPodEntries(state.PodEntries{
		commonstate.PoolNameShare: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameShare),
				AllocationResult: machine.NewCPUSet(0, 1, 2, 3),
			},
		},
		commonstate.PoolNameReclaim: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult: machine.NewCPUSet(4, 5, 6, 7),
			},
		},
	}, false)

	resp := &advisorapi.ListAndWatchResponse{
		Entries: map[string]*advisorapi.CalculationEntries{
			commonstate.PoolNameShare: {
				Entries: map[string]*advisorapi.CalculationInfo{
					commonstate.FakedContainerName: {
						OwnerPoolName: commonstate.PoolNameShare,
						CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
							commonstate.FakedNUMAID: {
								Blocks: []*advisorapi.Block{{BlockId: "share-upper-bound", Result: 6}},
							},
						},
					},
				},
			},
			commonstate.PoolNameReclaim: {
				Entries: map[string]*advisorapi.CalculationInfo{
					commonstate.FakedContainerName: {
						OwnerPoolName: commonstate.PoolNameReclaim,
						CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
							commonstate.FakedNUMAID: {
								Blocks: []*advisorapi.Block{{BlockId: "reclaim", Result: 4}},
							},
						},
					},
				},
			},
		},
	}

	require.NoError(t, policy.validateAdvisorResponse(resp))
}

func TestDynamicPolicyApplyBlocksPrunesEmptyDefaultShareAfterLastOwnerMoves(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 1)
	require.NoError(t, err)
	policy, err := getTestDynamicPolicyWithoutInitialization(topology, t.TempDir())
	require.NoError(t, err)
	policy.config.dynamicConfig.GetDynamicConfiguration().FillDefaultSharePoolWithNonReclaimCPUs = true

	previousShare := machine.NewCPUSet(2, 3)
	policy.state.SetPodEntries(state.PodEntries{
		commonstate.PoolNameShare: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameShare),
				AllocationResult: previousShare.Clone(),
			},
		},
		commonstate.PoolNameReclaim: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult: machine.NewCPUSet(0, 1, 2, 3, 4, 5, 6, 7),
			},
		},
		"custom": {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta("custom"),
				AllocationResult: machine.NewCPUSet(0),
			},
		},
		"moving-share-pod": {
			"main": &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "moving-share-pod",
					ContainerName: "main",
					OwnerPoolName: commonstate.PoolNameShare,
					QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
				},
				AllocationResult: previousShare.Clone(),
			},
		},
	}, false)

	resp := &advisorapi.ListAndWatchResponse{
		Entries: map[string]*advisorapi.CalculationEntries{
			commonstate.PoolNameShare: {
				Entries: map[string]*advisorapi.CalculationInfo{
					commonstate.FakedContainerName: {
						OwnerPoolName: commonstate.PoolNameShare,
						CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
							commonstate.FakedNUMAID: {
								Blocks: []*advisorapi.Block{{BlockId: "share", Result: 1}},
							},
						},
					},
				},
			},
			commonstate.PoolNameReclaim: {
				Entries: map[string]*advisorapi.CalculationInfo{
					commonstate.FakedContainerName: {
						OwnerPoolName: commonstate.PoolNameReclaim,
						CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
							commonstate.FakedNUMAID: {
								Blocks: []*advisorapi.Block{{BlockId: "reclaim", Result: 8}},
							},
						},
					},
				},
			},
			"custom": {
				Entries: map[string]*advisorapi.CalculationInfo{
					commonstate.FakedContainerName: {
						OwnerPoolName: "custom",
						CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
							commonstate.FakedNUMAID: {
								Blocks: []*advisorapi.Block{{BlockId: "custom", Result: 1}},
							},
						},
					},
				},
			},
			"moving-share-pod": {
				Entries: map[string]*advisorapi.CalculationInfo{
					"main": {
						OwnerPoolName: "custom",
					},
				},
			},
		},
	}
	blockCPUSet := advisorapi.BlockCPUSet{
		"share":   machine.NewCPUSet(0),
		"reclaim": machine.NewCPUSet(0, 1, 2, 3, 4, 5, 6, 7),
		"custom":  machine.NewCPUSet(0),
	}

	pending, err := policy.applyBlocksWithDynamicConfig(blockCPUSet, resp, false, false, policy.config.dynamicConfig.GetDynamicConfiguration())
	require.NoError(t, err)
	require.NotNil(t, pending)
	require.NotContains(t, pending.entries, commonstate.PoolNameShare)
	require.Equal(t, "custom", pending.entries["moving-share-pod"]["main"].OwnerPoolName)
	currentShare := policy.state.GetPodEntries()[commonstate.PoolNameShare][commonstate.FakedContainerName].AllocationResult
	require.True(t, currentShare.Equals(previousShare), "prepared update changed current share, got %s", currentShare.String())
}

func TestValidateEmptyRampUpCPUReuseRejectsOverlapWithHardReclaimFloor(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                 string
		hardPartitionEnabled bool
		oldAllocation        machine.CPUSet
		reclaimFloor         machine.CPUSet
		wantErr              string
	}{
		{
			name:                 "hard partition disabled allows legacy reuse",
			hardPartitionEnabled: false,
			oldAllocation:        machine.NewCPUSet(1, 2),
			reclaimFloor:         machine.NewCPUSet(2, 3),
		},
		{
			name:                 "hard partition enabled allows disjoint reuse",
			hardPartitionEnabled: true,
			oldAllocation:        machine.NewCPUSet(1, 2),
			reclaimFloor:         machine.NewCPUSet(3, 4),
		},
		{
			name:                 "hard partition enabled rejects overlapping reuse",
			hardPartitionEnabled: true,
			oldAllocation:        machine.NewCPUSet(1, 2),
			reclaimFloor:         machine.NewCPUSet(2, 3),
			wantErr:              "overlaps reclaim hard floor",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := validateEmptyRampUpCPUReuse(tt.hardPartitionEnabled, tt.oldAllocation, tt.reclaimFloor)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
		})
	}
}

// newRampUpOverlapTestPolicy builds a DynamicPolicy on the 2-NUMA dummy
// topology (NUMA0={0,1,4,5}, NUMA1={2,3,6,7}; SMT pairs (0,4)(1,5)(2,6)(3,7))
// with reserved core {0,4} and the ramp-up reclaim hard partition switch set
// to hardPartitionEnabled. All CPUs are 1 whole core each:
//
//	core{0,4}, core{1,5} in NUMA0; core{2,6}, core{3,7} in NUMA1.
func newRampUpOverlapTestPolicy(t *testing.T, hardPartitionEnabled bool) (*DynamicPolicy, *machine.CPUTopology) {
	t.Helper()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 2)
	require.NoError(t, err)
	policy, err := getTestDynamicPolicyWithoutInitialization(topology, t.TempDir())
	require.NoError(t, err)
	policy.config.reservedCPUs = machine.NewCPUSet(0, 4)
	policy.adjustment.cpuSetAdjustmentHandlers = map[string]cpusetutil.CPUSetAdjustmentHandler{
		"noop": func(context.Context, cpusetutil.CPUSetAdjustmentHandlerCtx) error {
			return nil
		},
	}

	dynamicConf := policy.config.dynamicConfig.GetDynamicConfiguration()
	dynamicConf.EnableReclaim = true
	dynamicConf.EnableRampUpReclaimHardPartition = hardPartitionEnabled

	return policy, topology
}

// rampUpOverlapTestEntries seeds one plain shared ramp-up pod (initially
// holding NUMA1 core {2,6}) and the reclaim pool ({2,6}).
func rampUpOverlapTestEntries() state.PodEntries {
	return state.PodEntries{
		"ramp-up-pod": {
			"main": &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "ramp-up-pod",
					ContainerName: "main",
					QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
				},
				RampUp:           true,
				AllocationResult: machine.NewCPUSet(2, 6),
				RequestQuantity:  1,
			},
		},
		commonstate.PoolNameReclaim: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult: machine.NewCPUSet(2, 6),
			},
		},
	}
}

// rampUpOverlapTestResponse advises the reclaim pool with CPUs {2,6}.
// FakedNUMAID is used deliberately: a single real-NUMA reclaim entry would be
// misclassified as a shared-NUMA-binding pool by IsSharedNUMABindingPoolEntry,
// causing all CPUs in that NUMA to be excluded from rampUpCPUs.
func rampUpOverlapTestResponse(allowOverlap bool) (*advisorapi.ListAndWatchResponse, advisorapi.BlockCPUSet) {
	resp := &advisorapi.ListAndWatchResponse{
		AllowSharedCoresOverlapReclaimedCores: allowOverlap,
		Entries: map[string]*advisorapi.CalculationEntries{
			commonstate.PoolNameReclaim: {
				Entries: map[string]*advisorapi.CalculationInfo{
					commonstate.FakedContainerName: {
						OwnerPoolName: commonstate.PoolNameReclaim,
						CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
							commonstate.FakedNUMAID: {Blocks: []*advisorapi.Block{{BlockId: "reclaim-1", Result: 2}}},
						},
					},
				},
			},
		},
	}
	return resp, advisorapi.BlockCPUSet{
		"reclaim-1": machine.NewCPUSet(2, 6),
	}
}

// TestDynamicPolicyApplyBlocksRampUpSharedReclaimOverlapQuadrants covers the
// HP x AO quadrant matrix for the plain shared ramp-up / final reclaim
// overlap:
//   - HP=false: the overlap is legal; applyBlocks keeps rampUpCPUs verbatim
//     (no rematerialization) and the pre-commit guard accepts it.
//   - HP=true: applyBlocks rematerializes the ramp-up shared allocation
//     canonically against the FINAL reclaim pool, regardless of AO. The
//     HP=true && AO=true case pins the D1 fix: the AO early-return in the
//     guard no longer bypasses the mutual exclusion.
//
// With reserved={0,4} and reclaim={2,6}: rampUpCPUs={1,2,3,5,6,7}; the
// canonical rematerialization keeps whole cores {1,5} (NUMA0) and {3,7}
// (NUMA1) only, i.e. {1,3,5,7}.
func TestDynamicPolicyApplyBlocksRampUpSharedReclaimOverlapQuadrants(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name                 string
		hardPartition        bool
		allowOverlap         bool
		wantRampUpAllocation machine.CPUSet
	}{
		{
			name:                 "hp disabled ao disabled keeps overlap",
			hardPartition:        false,
			allowOverlap:         false,
			wantRampUpAllocation: machine.NewCPUSet(1, 2, 3, 5, 6, 7),
		},
		{
			name:                 "hp disabled ao enabled keeps overlap",
			hardPartition:        false,
			allowOverlap:         true,
			wantRampUpAllocation: machine.NewCPUSet(1, 2, 3, 5, 6, 7),
		},
		{
			name:                 "hp enabled ao disabled rematerializes",
			hardPartition:        true,
			allowOverlap:         false,
			wantRampUpAllocation: machine.NewCPUSet(1, 3, 5, 7),
		},
		{
			name:                 "hp enabled ao enabled rematerializes (D1)",
			hardPartition:        true,
			allowOverlap:         true,
			wantRampUpAllocation: machine.NewCPUSet(1, 3, 5, 7),
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			policy, topology := newRampUpOverlapTestPolicy(t, tc.hardPartition)
			policy.state.SetPodEntries(rampUpOverlapTestEntries(), false)
			resp, blockCPUSet := rampUpOverlapTestResponse(tc.allowOverlap)

			require.NoError(t, prepareAndCommitAdvisorBlocks(policy, blockCPUSet, resp, tc.allowOverlap))

			reclaim := policy.state.GetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName)
			require.NotNil(t, reclaim)
			require.True(t, reclaim.AllocationResult.Equals(machine.NewCPUSet(2, 6)),
				"final reclaim pool: %s", reclaim.AllocationResult.String())

			got := policy.state.GetAllocationInfo("ramp-up-pod", "main")
			require.NotNil(t, got)
			require.True(t, got.AllocationResult.Equals(tc.wantRampUpAllocation),
				"ramp-up shared allocation: %s, want %s",
				got.AllocationResult.String(), tc.wantRampUpAllocation.String())

			if tc.hardPartition {
				// HP=true: mutual exclusion, whole-core, NUMA-aware.
				require.True(t, got.AllocationResult.Intersection(reclaim.AllocationResult).IsEmpty(),
					"ramp-up shared allocation must be mutually exclusive with the final reclaim pool")
				requireCoreAligned(t, topology, got.AllocationResult)
				require.Equal(t, machine.NewCPUSet(1, 5), got.TopologyAwareAssignments[0])
				require.Equal(t, machine.NewCPUSet(3, 7), got.TopologyAwareAssignments[1])
				require.True(t, got.OriginalAllocationResult.Equals(tc.wantRampUpAllocation))

				// Zero churn across frames: re-applying the same advisor frame
				// must converge to the same allocation instead of oscillating.
				require.NoError(t, prepareAndCommitAdvisorBlocks(policy, blockCPUSet, resp, tc.allowOverlap))
				gotAgain := policy.state.GetAllocationInfo("ramp-up-pod", "main")
				require.NotNil(t, gotAgain)
				require.True(t, gotAgain.AllocationResult.Equals(tc.wantRampUpAllocation),
					"second frame must be idempotent, got %s", gotAgain.AllocationResult.String())
			}
		})
	}
}

// TestValidateAdvisorPartitionBeforeCommitRampUpSharedOverlapMatrix pins the
// M1 guard split at the validator level: the ramp-up/reclaim mutual exclusion
// is driven by the hard partition switch only (and runs before the AO
// early-return), while the steady shared guard keeps rejecting overlaps no
// matter what the hard partition switch says.
func TestValidateAdvisorPartitionBeforeCommitRampUpSharedOverlapMatrix(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name          string
		hardPartition bool
		allowShared   bool
		rampUp        bool
		numaBinding   bool
		allocation    machine.CPUSet
		wantErr       string
	}{
		{
			name:          "hp disabled ao disabled ramp-up shared overlap legal",
			hardPartition: false,
			allowShared:   false,
			rampUp:        true,
			allocation:    machine.NewCPUSet(2, 6),
		},
		{
			name:          "hp disabled ao enabled ramp-up shared overlap legal",
			hardPartition: false,
			allowShared:   true,
			rampUp:        true,
			allocation:    machine.NewCPUSet(2, 6),
		},
		{
			name:          "hp enabled ao disabled ramp-up shared overlap rejected",
			hardPartition: true,
			allowShared:   false,
			rampUp:        true,
			allocation:    machine.NewCPUSet(2, 6),
			wantErr:       "ramp-up shared allocation overlaps the final reclaim pool",
		},
		{
			name:          "hp enabled ao enabled ramp-up shared overlap rejected (D1)",
			hardPartition: true,
			allowShared:   true,
			rampUp:        true,
			allocation:    machine.NewCPUSet(2, 6),
			wantErr:       "ramp-up shared allocation overlaps the final reclaim pool",
		},
		{
			name:          "hp enabled ao enabled SNB ramp-up overlap rejected",
			hardPartition: true,
			allowShared:   true,
			rampUp:        true,
			numaBinding:   true,
			allocation:    machine.NewCPUSet(2, 6),
			wantErr:       "ramp-up shared allocation overlaps the final reclaim pool",
		},
		{
			name:          "hp enabled ramp-up shared disjoint legal",
			hardPartition: true,
			allowShared:   false,
			rampUp:        true,
			allocation:    machine.NewCPUSet(3, 7),
		},
		{
			name:          "steady shared overlap rejected regardless of hp",
			hardPartition: false,
			allowShared:   false,
			rampUp:        false,
			allocation:    machine.NewCPUSet(2, 6),
			wantErr:       "reclaim pool overlaps disallowed shared partition before commit",
		},
		{
			name:          "steady shared overlap rejected with hp enabled",
			hardPartition: true,
			allowShared:   false,
			rampUp:        false,
			allocation:    machine.NewCPUSet(2, 6),
			wantErr:       "reclaim pool overlaps disallowed shared partition before commit",
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			policy, _ := newRampUpOverlapTestPolicy(t, tc.hardPartition)

			annotations := map[string]string{}
			if tc.numaBinding {
				annotations[apiconsts.PodAnnotationMemoryEnhancementNumaBinding] = apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable
			}
			newEntries := state.PodEntries{
				"overlap-pod": {
					"main": &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:        "overlap-pod",
							ContainerName: "main",
							QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
							Annotations:   annotations,
						},
						RampUp:           tc.rampUp,
						AllocationResult: tc.allocation.Clone(),
					},
				},
				commonstate.PoolNameReclaim: {
					commonstate.FakedContainerName: &state.AllocationInfo{
						AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
						AllocationResult: machine.NewCPUSet(2, 6),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							1: machine.NewCPUSet(2, 6),
						},
					},
				},
			}

			err := policy.validateAdvisorPartitionBeforeCommitWithDynamicConfig(
				newEntries,
				policy.state.GetMachineState(),
				tc.allowShared,
				false,
				policy.config.dynamicConfig.GetDynamicConfiguration())
			if tc.wantErr == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tc.wantErr)
			}
		})
	}
}

// TestRematerializeRampUpSharedAgainstFinalReclaim unit-tests the canonical
// rematerialization primitive directly: zero-churn skip, whole-core
// re-selection, idempotence, both fail-closed errors, and the unconditional SNB
// skip (SNB ramp-up is owned by the bulkhead partition view and is never
// rematerialized here).
func TestRematerializeRampUpSharedAgainstFinalReclaim(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 2)
	require.NoError(t, err)
	policy, err := getTestDynamicPolicyWithoutInitialization(topology, t.TempDir())
	require.NoError(t, err)

	rampUpEntry := func(allocation machine.CPUSet, numaBinding bool) *state.AllocationInfo {
		annotations := map[string]string{}
		if numaBinding {
			annotations[apiconsts.PodAnnotationMemoryEnhancementNumaBinding] = apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable
		}
		assignments, err := machine.GetNumaAwareAssignments(topology, allocation)
		require.NoError(t, err)
		return &state.AllocationInfo{
			AllocationMeta: commonstate.AllocationMeta{
				PodUid:        "ramp-up-pod",
				ContainerName: "main",
				QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
				Annotations:   annotations,
			},
			RampUp:                           true,
			AllocationResult:                 allocation.Clone(),
			OriginalAllocationResult:         allocation.Clone(),
			TopologyAwareAssignments:         assignments,
			OriginalTopologyAwareAssignments: machine.DeepcopyCPUAssignment(assignments),
		}
	}
	reclaimEntry := func(allocation machine.CPUSet) *state.AllocationInfo {
		return &state.AllocationInfo{
			AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
			AllocationResult: allocation.Clone(),
		}
	}

	t.Run("zero churn when disjoint", func(t *testing.T) {
		t.Parallel()
		entries := state.PodEntries{
			"ramp-up-pod": {"main": rampUpEntry(machine.NewCPUSet(1, 3, 5, 7), false)},
			commonstate.PoolNameReclaim: {
				commonstate.FakedContainerName: reclaimEntry(machine.NewCPUSet(2, 6)),
			},
		}
		require.NoError(t, policy.rematerializeRampUpSharedAgainstFinalReclaim(entries))
		got := entries["ramp-up-pod"]["main"]
		require.True(t, got.AllocationResult.Equals(machine.NewCPUSet(1, 3, 5, 7)))
		require.True(t, got.OriginalAllocationResult.Equals(machine.NewCPUSet(1, 3, 5, 7)))
	})

	t.Run("rematerializes to whole cores and is idempotent", func(t *testing.T) {
		t.Parallel()
		entries := state.PodEntries{
			"ramp-up-pod": {"main": rampUpEntry(machine.NewCPUSet(1, 2, 3, 5, 6, 7), false)},
			commonstate.PoolNameReclaim: {
				commonstate.FakedContainerName: reclaimEntry(machine.NewCPUSet(2, 6)),
			},
		}
		require.NoError(t, policy.rematerializeRampUpSharedAgainstFinalReclaim(entries))
		got := entries["ramp-up-pod"]["main"]
		require.True(t, got.AllocationResult.Equals(machine.NewCPUSet(1, 3, 5, 7)),
			"got %s", got.AllocationResult.String())
		require.True(t, got.OriginalAllocationResult.Equals(machine.NewCPUSet(1, 3, 5, 7)))
		require.Equal(t, machine.NewCPUSet(1, 5), got.TopologyAwareAssignments[0])
		require.Equal(t, machine.NewCPUSet(3, 7), got.TopologyAwareAssignments[1])
		require.Equal(t, machine.NewCPUSet(1, 5), got.OriginalTopologyAwareAssignments[0])
		require.Equal(t, machine.NewCPUSet(3, 7), got.OriginalTopologyAwareAssignments[1])

		// second call must not churn the already-canonical allocation
		require.NoError(t, policy.rematerializeRampUpSharedAgainstFinalReclaim(entries))
		require.True(t, entries["ramp-up-pod"]["main"].AllocationResult.Equals(machine.NewCPUSet(1, 3, 5, 7)))
	})

	t.Run("completely covered fails closed", func(t *testing.T) {
		t.Parallel()
		entries := state.PodEntries{
			"ramp-up-pod": {"main": rampUpEntry(machine.NewCPUSet(2, 6), false)},
			commonstate.PoolNameReclaim: {
				commonstate.FakedContainerName: reclaimEntry(machine.NewCPUSet(2, 6)),
			},
		}
		err := policy.rematerializeRampUpSharedAgainstFinalReclaim(entries)
		require.ErrorContains(t, err, "completely covered by the final reclaim pool")
	})

	t.Run("no complete physical core fails closed", func(t *testing.T) {
		t.Parallel()
		entries := state.PodEntries{
			"ramp-up-pod": {"main": rampUpEntry(machine.NewCPUSet(0, 4), false)},
			commonstate.PoolNameReclaim: {
				commonstate.FakedContainerName: reclaimEntry(machine.NewCPUSet(0)),
			},
		}
		err := policy.rematerializeRampUpSharedAgainstFinalReclaim(entries)
		require.ErrorContains(t, err, "no complete physical core remains")
	})

	t.Run("SNB ramp-up is skipped unconditionally", func(t *testing.T) {
		t.Parallel()
		entries := state.PodEntries{
			"ramp-up-pod": {"main": rampUpEntry(machine.NewCPUSet(2, 6), true)},
			commonstate.PoolNameReclaim: {
				commonstate.FakedContainerName: reclaimEntry(machine.NewCPUSet(2, 6)),
			},
		}
		// SNB ramp-up is owned by the bulkhead partition view; the rematerialization
		// never touches it, even when its cpuset overlaps the final reclaim pool. A
		// residual overlap is surfaced as a typed failure upstream by
		// validateRampUpReclaimMutualExclusion, not papered over here.
		require.NoError(t, policy.rematerializeRampUpSharedAgainstFinalReclaim(entries))
		require.True(t, entries["ramp-up-pod"]["main"].AllocationResult.Equals(machine.NewCPUSet(2, 6)))
	})

	t.Run("SNB ramp-up overlapping reclaim is left untouched for upstream validation", func(t *testing.T) {
		t.Parallel()
		entries := state.PodEntries{
			"ramp-up-pod": {"main": rampUpEntry(machine.NewCPUSet(2, 3, 6, 7), true)},
			commonstate.PoolNameReclaim: {
				commonstate.FakedContainerName: reclaimEntry(machine.NewCPUSet(2, 6)),
			},
		}
		// No advised/preserved distinction any more: SNB ramp-up is skipped on every
		// path, so the overlapping allocation stays as-is and the residual overlap
		// becomes the upstream typed invariant.
		require.NoError(t, policy.rematerializeRampUpSharedAgainstFinalReclaim(entries))
		got := entries["ramp-up-pod"]["main"]
		require.True(t, got.AllocationResult.Equals(machine.NewCPUSet(2, 3, 6, 7)),
			"SNB ramp-up must not be rematerialized here, got %s", got.AllocationResult.String())
	})

	t.Run("empty final reclaim is a no-op", func(t *testing.T) {
		t.Parallel()
		entries := state.PodEntries{
			"ramp-up-pod": {"main": rampUpEntry(machine.NewCPUSet(1, 2, 3, 5, 6, 7), false)},
			commonstate.PoolNameReclaim: {
				commonstate.FakedContainerName: reclaimEntry(machine.NewCPUSet()),
			},
		}
		require.NoError(t, policy.rematerializeRampUpSharedAgainstFinalReclaim(entries))
		require.True(t, entries["ramp-up-pod"]["main"].AllocationResult.Equals(machine.NewCPUSet(1, 2, 3, 5, 6, 7)))
	})
}

// TestDynamicPolicyApplyBlocksRampUpSharedFailClosedWhenReclaimCoversRampUp
// verifies both fail-closed branches of the rematerialization through the
// real applyBlocks path (HP=true via the dynamic configuration, hardActive
// parameter kept false so the ramp-up reclaim floor stays empty and the
// scenario is deterministic):
//   - reclaim covering rampUpCPUs entirely -> "completely covered" error;
//   - only a lone SMT sibling surviving -> "no complete physical core" error.
func TestDynamicPolicyApplyBlocksRampUpSharedFailClosedWhenReclaimCoversRampUp(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		reserved machine.CPUSet
		wantErr  string
	}{
		{
			name:     "ramp-up CPUs completely covered by final reclaim",
			reserved: machine.NewCPUSet(0, 4),
			wantErr:  "completely covered by the final reclaim pool",
		},
		{
			name:     "only a lone SMT sibling survives the final reclaim",
			reserved: machine.NewCPUSet(0),
			wantErr:  "no complete physical core remains",
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			policy, _ := newRampUpOverlapTestPolicy(t, true)
			policy.config.reservedCPUs = tc.reserved
			policy.state.SetPodEntries(rampUpOverlapTestEntries(), false)

			// reclaim spans NUMA0 core {1,5} and both NUMA1 cores {2,6},{3,7};
			// with reserved={0,4} this covers rampUpCPUs completely, with
			// reserved={0} only the lone sibling 4 of core {0,4} survives.
			resp := &advisorapi.ListAndWatchResponse{
				Entries: map[string]*advisorapi.CalculationEntries{
					commonstate.PoolNameReclaim: {
						Entries: map[string]*advisorapi.CalculationInfo{
							commonstate.FakedContainerName: {
								OwnerPoolName: commonstate.PoolNameReclaim,
								CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
									0: {Blocks: []*advisorapi.Block{{BlockId: "reclaim-0", Result: 2}}},
									1: {Blocks: []*advisorapi.Block{{BlockId: "reclaim-1", Result: 4}}},
								},
							},
						},
					},
				},
			}
			blockCPUSet := advisorapi.BlockCPUSet{
				"reclaim-0": machine.NewCPUSet(1, 5),
				"reclaim-1": machine.NewCPUSet(2, 3, 6, 7),
			}

			pending, err := policy.applyBlocksWithDynamicConfig(blockCPUSet, resp, false, false, policy.config.dynamicConfig.GetDynamicConfiguration())
			require.Nil(t, pending)
			require.ErrorContains(t, err, tc.wantErr)
			require.ErrorContains(t, err, "rematerialize ramp-up shared allocations against final reclaim pool")
		})
	}
}

// TestDynamicPolicyApplyBlocksReusesRampUpAllocationWhenRampUpCPUsEmptyWithoutHardPartition
// pins the HP=false empty-set reuse semantics: when rampUpCPUs is empty the
// ramp-up pod keeps its previous allocation, even when that allocation
// overlaps the reclaim pool, and the commit still succeeds.
func TestDynamicPolicyApplyBlocksReusesRampUpAllocationWhenRampUpCPUsEmptyWithoutHardPartition(t *testing.T) {
	t.Parallel()

	policy, _ := newRampUpOverlapTestPolicy(t, false)
	// every CPU reserved -> rampUpCPUs is empty
	policy.config.reservedCPUs = policy.machine.machineInfo.CPUDetails.CPUs()
	policy.state.SetPodEntries(rampUpOverlapTestEntries(), false)
	resp, blockCPUSet := rampUpOverlapTestResponse(false)

	require.NoError(t, prepareAndCommitAdvisorBlocks(policy, blockCPUSet, resp, false))

	got := policy.state.GetAllocationInfo("ramp-up-pod", "main")
	require.NotNil(t, got)
	require.True(t, got.AllocationResult.Equals(machine.NewCPUSet(2, 6)),
		"empty rampUpCPUs must reuse the old allocation without tightening, got %s",
		got.AllocationResult.String())
}

// TestDynamicPolicyApplyBlocksResidualSNBRampUpOverlapFailsClosed covers the
// unadvised SNB ramp-up branch: with AO=true no adjustment commit override is
// built, so the raw advisor reclaim stays in place and overlaps the SNB
// allocation. The rematerialization now skips SNB ramp-up unconditionally (it is
// owned by the bulkhead partition view), so a residual overlap is no longer
// papered over here and instead fails closed as a typed pre-commit partition
// invariant.
func TestDynamicPolicyApplyBlocksResidualSNBRampUpOverlapFailsClosed(t *testing.T) {
	t.Parallel()

	policy, topology := newRampUpOverlapTestPolicy(t, true)
	numa1CPUs := topology.CPUDetails.CPUsInNUMANodes(1)
	require.True(t, numa1CPUs.Equals(machine.NewCPUSet(2, 3, 6, 7)))

	assignments, err := machine.GetNumaAwareAssignments(topology, numa1CPUs)
	require.NoError(t, err)
	policy.state.SetPodEntries(state.PodEntries{
		"snb-ramp-up-pod": {
			"main": &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "snb-ramp-up-pod",
					ContainerName: "main",
					QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
					Annotations: map[string]string{
						apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
					},
				},
				RampUp:                           true,
				AllocationResult:                 numa1CPUs.Clone(),
				OriginalAllocationResult:         numa1CPUs.Clone(),
				TopologyAwareAssignments:         assignments,
				OriginalTopologyAwareAssignments: machine.DeepcopyCPUAssignment(assignments),
				RequestQuantity:                  2,
			},
		},
		commonstate.PoolNameReclaim: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult: machine.NewCPUSet(2, 6),
			},
		},
	}, false)

	// AO=true && DD=false: no commit override is built, the advisor reclaim
	// {2,6} overlaps the preserved SNB allocation {2,3,6,7}. Rematerialization
	// skips SNB, so the residual overlap fails the pre-commit partition invariant.
	resp, blockCPUSet := rampUpOverlapTestResponse(true)

	err = prepareAndCommitAdvisorBlocks(policy, blockCPUSet, resp, true)
	require.ErrorContains(t, err, "ramp-up shared allocation overlaps the final reclaim pool")

	got := policy.state.GetAllocationInfo("snb-ramp-up-pod", "main")
	require.NotNil(t, got)
	// The SNB allocation was never rewritten by the skipped rematerialization.
	require.True(t, got.AllocationResult.Equals(machine.NewCPUSet(2, 3, 6, 7)),
		"SNB ramp-up allocation must not be rematerialized, got %s", got.AllocationResult.String())
}

// TestDynamicPolicyApplyBlocksRampUpRematerializationWithDefaultSharePool
// verifies the rematerialization coexists with the default share pool
// materialization (FillDefaultSharePoolWithNonReclaimCPUs): the share pool is
// materialized from the residual and the ramp-up shared allocation is still
// rematerialized against the final reclaim pool.
func TestDynamicPolicyApplyBlocksRampUpRematerializationWithDefaultSharePool(t *testing.T) {
	t.Parallel()

	policy, topology := newRampUpOverlapTestPolicy(t, true)
	policy.config.dynamicConfig.GetDynamicConfiguration().FillDefaultSharePoolWithNonReclaimCPUs = true
	policy.state.SetPodEntries(rampUpOverlapTestEntries(), false)

	resp, blockCPUSet := rampUpOverlapTestResponse(false)
	resp.Entries[commonstate.PoolNameShare] = &advisorapi.CalculationEntries{
		Entries: map[string]*advisorapi.CalculationInfo{
			commonstate.FakedContainerName: {
				OwnerPoolName: commonstate.PoolNameShare,
				CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
					commonstate.FakedNUMAID: {
						Blocks: []*advisorapi.Block{{BlockId: "share", Result: 8}},
					},
				},
			},
		},
	}

	pending, err := policy.applyBlocksWithDynamicConfig(blockCPUSet, resp, false, false, policy.config.dynamicConfig.GetDynamicConfiguration())
	require.NoError(t, err)
	require.NotNil(t, pending)

	reclaim := pending.entries[commonstate.PoolNameReclaim][commonstate.FakedContainerName]
	require.NotNil(t, reclaim)
	require.True(t, reclaim.AllocationResult.Equals(machine.NewCPUSet(2, 6)))

	got := pending.entries["ramp-up-pod"]["main"]
	require.NotNil(t, got)
	require.True(t, got.AllocationResult.Equals(machine.NewCPUSet(1, 3, 5, 7)),
		"ramp-up shared allocation: %s", got.AllocationResult.String())
	require.True(t, got.AllocationResult.Intersection(reclaim.AllocationResult).IsEmpty())
	requireCoreAligned(t, topology, got.AllocationResult)
	require.Equal(t, machine.NewCPUSet(1, 5), got.TopologyAwareAssignments[0])
	require.Equal(t, machine.NewCPUSet(3, 7), got.TopologyAwareAssignments[1])

	share := pending.entries[commonstate.PoolNameShare][commonstate.FakedContainerName]
	if share != nil {
		require.True(t, share.AllocationResult.Intersection(reclaim.AllocationResult).IsEmpty(),
			"default share pool must stay disjoint from the final reclaim pool: %s",
			share.AllocationResult.String())
	}
}

// TestDynamicPolicyApplyBlocksRampUpRematerializationWithDisableDedicatedOverlap
// covers the HP=true && AO=true && DisableDedicatedCoresOverlapReclaimedCores
// corner: the adjustment commit override is reclaim-minus-dedicated, and the
// ramp-up shared allocation must still be rematerialized against that final
// reclaim pool.
func TestDynamicPolicyApplyBlocksRampUpRematerializationWithDisableDedicatedOverlap(t *testing.T) {
	t.Parallel()

	policy, topology := newRampUpOverlapTestPolicy(t, true)
	policy.state.SetPodEntries(state.PodEntries{
		"ramp-up-pod": {
			"main": &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "ramp-up-pod",
					ContainerName: "main",
					QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
				},
				RampUp:           true,
				AllocationResult: machine.NewCPUSet(2, 3, 6, 7),
				RequestQuantity:  1,
			},
		},
		"dedicated-pod": {
			"main": &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "dedicated-pod",
					ContainerName: "main",
					OwnerPoolName: commonstate.PoolNameDedicated,
					QoSLevel:      apiconsts.PodAnnotationQoSLevelDedicatedCores,
				},
				AllocationResult: machine.NewCPUSet(1, 5),
				RequestQuantity:  2,
			},
		},
		commonstate.PoolNameReclaim: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult: machine.NewCPUSet(1, 2, 5, 6),
			},
		},
	}, false)

	resp := &advisorapi.ListAndWatchResponse{
		AllowSharedCoresOverlapReclaimedCores:      true,
		DisableDedicatedCoresOverlapReclaimedCores: true,
		Entries: map[string]*advisorapi.CalculationEntries{
			"dedicated-pod": {
				Entries: map[string]*advisorapi.CalculationInfo{
					"main": {
						OwnerPoolName: commonstate.PoolNameDedicated,
						CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
							0: {Blocks: []*advisorapi.Block{{BlockId: "dedicated", Result: 2}}},
						},
					},
				},
			},
			commonstate.PoolNameReclaim: {
				Entries: map[string]*advisorapi.CalculationInfo{
					commonstate.FakedContainerName: {
						OwnerPoolName: commonstate.PoolNameReclaim,
						CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
							0: {Blocks: []*advisorapi.Block{{BlockId: "reclaim-0", Result: 2}}},
							1: {Blocks: []*advisorapi.Block{{BlockId: "reclaim-1", Result: 2}}},
						},
					},
				},
			},
		},
	}
	blockCPUSet := advisorapi.BlockCPUSet{
		"dedicated": machine.NewCPUSet(1, 5),
		"reclaim-0": machine.NewCPUSet(1, 5),
		"reclaim-1": machine.NewCPUSet(2, 6),
	}

	require.NoError(t, prepareAndCommitAdvisorBlocks(policy, blockCPUSet, resp, true))

	// DD=true: the final reclaim pool is reclaim-minus-dedicated.
	reclaim := policy.state.GetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName)
	require.NotNil(t, reclaim)
	require.True(t, reclaim.AllocationResult.Equals(machine.NewCPUSet(2, 6)),
		"final reclaim pool: %s", reclaim.AllocationResult.String())

	dedicated := policy.state.GetAllocationInfo("dedicated-pod", "main")
	require.NotNil(t, dedicated)
	require.True(t, dedicated.AllocationResult.Equals(machine.NewCPUSet(1, 5)))

	got := policy.state.GetAllocationInfo("ramp-up-pod", "main")
	require.NotNil(t, got)
	require.True(t, got.AllocationResult.Equals(machine.NewCPUSet(3, 7)),
		"ramp-up shared allocation: %s", got.AllocationResult.String())
	require.True(t, got.AllocationResult.Intersection(reclaim.AllocationResult).IsEmpty())
	require.True(t, got.AllocationResult.Intersection(dedicated.AllocationResult).IsEmpty())
	requireCoreAligned(t, topology, got.AllocationResult)
}

func TestDynamicPolicyApplyBlocksPrunesOwnerlessEmptyDefaultShareWithoutPreviousPool(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 1)
	require.NoError(t, err)
	policy, err := getTestDynamicPolicyWithoutInitialization(topology, t.TempDir())
	require.NoError(t, err)
	policy.config.dynamicConfig.GetDynamicConfiguration().FillDefaultSharePoolWithNonReclaimCPUs = true

	policy.state.SetPodEntries(state.PodEntries{
		commonstate.PoolNameReclaim: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult: machine.NewCPUSet(0, 1, 2, 3, 4, 5, 6, 7),
			},
		},
	}, false)

	resp := &advisorapi.ListAndWatchResponse{
		Entries: map[string]*advisorapi.CalculationEntries{
			commonstate.PoolNameShare: {
				Entries: map[string]*advisorapi.CalculationInfo{
					commonstate.FakedContainerName: {
						OwnerPoolName: commonstate.PoolNameShare,
						CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
							commonstate.FakedNUMAID: {
								Blocks: []*advisorapi.Block{{BlockId: "share", Result: 1}},
							},
						},
					},
				},
			},
			commonstate.PoolNameReclaim: {
				Entries: map[string]*advisorapi.CalculationInfo{
					commonstate.FakedContainerName: {
						OwnerPoolName: commonstate.PoolNameReclaim,
						CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
							commonstate.FakedNUMAID: {
								Blocks: []*advisorapi.Block{{BlockId: "reclaim", Result: 8}},
							},
						},
					},
				},
			},
		},
	}

	pending, err := policy.applyBlocksWithDynamicConfig(advisorapi.BlockCPUSet{
		"share":   machine.NewCPUSet(0),
		"reclaim": machine.NewCPUSet(0, 1, 2, 3, 4, 5, 6, 7),
	}, resp, false, false, policy.config.dynamicConfig.GetDynamicConfiguration())
	require.NoError(t, err)
	require.NotNil(t, pending)
	require.NotContains(t, pending.entries, commonstate.PoolNameShare)
}

func TestDynamicPolicyApplyBlocksRejectsEmptyDefaultShareWhenOwnerMovesIn(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 1)
	require.NoError(t, err)
	policy, err := getTestDynamicPolicyWithoutInitialization(topology, t.TempDir())
	require.NoError(t, err)
	policy.config.dynamicConfig.GetDynamicConfiguration().FillDefaultSharePoolWithNonReclaimCPUs = true

	policy.state.SetPodEntries(state.PodEntries{
		"custom": {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta("custom"),
				AllocationResult: machine.NewCPUSet(2, 3),
			},
		},
		commonstate.PoolNameReclaim: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult: machine.NewCPUSet(0, 1, 2, 3, 4, 5, 6, 7),
			},
		},
		"moving-share-pod": {
			"main": &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "moving-share-pod",
					ContainerName: "main",
					OwnerPoolName: "custom",
					QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
				},
				AllocationResult: machine.NewCPUSet(2, 3),
			},
		},
	}, false)

	resp := &advisorapi.ListAndWatchResponse{
		Entries: map[string]*advisorapi.CalculationEntries{
			commonstate.PoolNameShare: {
				Entries: map[string]*advisorapi.CalculationInfo{
					commonstate.FakedContainerName: {
						OwnerPoolName: commonstate.PoolNameShare,
						CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
							commonstate.FakedNUMAID: {
								Blocks: []*advisorapi.Block{{BlockId: "share", Result: 1}},
							},
						},
					},
				},
			},
			commonstate.PoolNameReclaim: {
				Entries: map[string]*advisorapi.CalculationInfo{
					commonstate.FakedContainerName: {
						OwnerPoolName: commonstate.PoolNameReclaim,
						CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
							commonstate.FakedNUMAID: {
								Blocks: []*advisorapi.Block{{BlockId: "reclaim", Result: 8}},
							},
						},
					},
				},
			},
			"moving-share-pod": {
				Entries: map[string]*advisorapi.CalculationInfo{
					"main": {
						OwnerPoolName: commonstate.PoolNameShare,
					},
				},
			},
		},
	}

	pending, err := policy.applyBlocksWithDynamicConfig(advisorapi.BlockCPUSet{
		"share":   machine.NewCPUSet(0),
		"reclaim": machine.NewCPUSet(0, 1, 2, 3, 4, 5, 6, 7),
	}, resp, false, false, policy.config.dynamicConfig.GetDynamicConfiguration())
	require.Nil(t, pending)
	require.ErrorContains(t, err, "default share residual is empty with live owner")
}

func TestProjectDefaultShareOwnershipEntriesRejectsNilAdvisorInfo(t *testing.T) {
	t.Parallel()

	current := state.PodEntries{
		"shared-pod": {
			"main": {
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "shared-pod",
					ContainerName: "main",
					OwnerPoolName: commonstate.PoolNameShare,
					QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
				},
			},
		},
	}
	resp := &advisorapi.ListAndWatchResponse{
		Entries: map[string]*advisorapi.CalculationEntries{
			"shared-pod": {
				Entries: map[string]*advisorapi.CalculationInfo{
					"main": nil,
				},
			},
		},
	}

	entries, err := projectDefaultShareOwnershipEntries(current, nil, resp)

	require.Nil(t, entries)
	require.ErrorContains(t, err, "nil calculation info for shared owner projection")
}

func TestGetOwnerPoolNameFromAdvisorRejectsNilInfo(t *testing.T) {
	t.Parallel()

	allocationInfo := &state.AllocationInfo{
		AllocationMeta: commonstate.AllocationMeta{
			PodUid:        "ramp-up-shared-pod",
			ContainerName: "main",
			QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
		},
		RampUp: true,
	}
	resp := &advisorapi.ListAndWatchResponse{
		Entries: map[string]*advisorapi.CalculationEntries{
			allocationInfo.PodUid: {
				Entries: map[string]*advisorapi.CalculationInfo{
					allocationInfo.ContainerName: nil,
				},
			},
		},
	}

	ownerPoolName, err := (&DynamicPolicy{}).getOwnerPoolNameFromAdvisor(allocationInfo, resp)

	require.Empty(t, ownerPoolName)
	require.ErrorContains(t, err, "nil calculation info")
}

func TestDynamicPolicyApplyBlocksRematerializesDefaultShareAfterReclaimPadding(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 1)
	require.NoError(t, err)
	policy, err := getTestDynamicPolicyWithoutInitialization(topology, t.TempDir())
	require.NoError(t, err)
	policy.config.reservedCPUs = machine.NewCPUSet()
	dynamicConf := policy.config.dynamicConfig.GetDynamicConfiguration()
	dynamicConf.FillDefaultSharePoolWithNonReclaimCPUs = true
	dynamicConf.EnableReclaim = true
	dynamicConf.AdminQoSConfiguration.CPUPluginConfiguration.EnableRampUpReclaimHardPartition = true
	dynamicConf.AdminQoSConfiguration.CPUPluginConfiguration.BulkheadConfig.Enable = true
	dynamicConf.AdminQoSConfiguration.CPUPluginConfiguration.BulkheadConfig.NonReclaimPoolMinSize = 4

	policy.state.SetPodEntries(state.PodEntries{
		commonstate.PoolNameReclaim: {
			commonstate.FakedContainerName: {
				AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult: machine.NewCPUSet(0, 1, 2, 4, 5, 6),
			},
		},
	}, false)

	resp := &advisorapi.ListAndWatchResponse{
		DisableDedicatedCoresOverlapReclaimedCores: true,
		Entries: map[string]*advisorapi.CalculationEntries{
			commonstate.PoolNameShare: {
				Entries: map[string]*advisorapi.CalculationInfo{
					commonstate.FakedContainerName: {
						OwnerPoolName: commonstate.PoolNameShare,
						CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
							commonstate.FakedNUMAID: {
								Blocks: []*advisorapi.Block{{BlockId: "share", Result: 8}},
							},
						},
					},
				},
			},
			commonstate.PoolNameReclaim: {
				Entries: map[string]*advisorapi.CalculationInfo{
					commonstate.FakedContainerName: {
						OwnerPoolName: commonstate.PoolNameReclaim,
						CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
							commonstate.FakedNUMAID: {
								Blocks: []*advisorapi.Block{{BlockId: "reclaim", Result: 6}},
							},
						},
					},
				},
			},
		},
	}

	pending, err := policy.applyBlocksWithDynamicConfig(advisorapi.BlockCPUSet{
		"share":   machine.NewCPUSet(3, 7),
		"reclaim": machine.NewCPUSet(0, 1, 2, 4, 5, 6),
	}, resp, false, false, policy.config.dynamicConfig.GetDynamicConfiguration())
	require.NoError(t, err)
	require.NotNil(t, pending)

	share := pending.entries[commonstate.PoolNameShare][commonstate.FakedContainerName].AllocationResult
	reclaim := pending.entries[commonstate.PoolNameReclaim][commonstate.FakedContainerName].AllocationResult
	require.Equal(t, 6, share.Size())
	require.Equal(t, 2, reclaim.Size())
	require.NoError(t, assertCoreAligned(reclaim, topology))
	require.True(t, share.Intersection(reclaim).IsEmpty())
	require.True(t, share.Union(reclaim).Equals(topology.CPUDetails.CPUs()))
}

func TestDynamicPolicyApplyBlocksRejectsDefaultShareResidualLargerThanAdviceAtomically(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 1)
	require.NoError(t, err)
	policy, err := getTestDynamicPolicyWithoutInitialization(topology, t.TempDir())
	require.NoError(t, err)
	policy.config.dynamicConfig.GetDynamicConfiguration().FillDefaultSharePoolWithNonReclaimCPUs = true

	policy.state.SetPodEntries(state.PodEntries{
		commonstate.PoolNameReclaim: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult: machine.NewCPUSet(0, 1),
			},
		},
	}, false)
	initialEntries := policy.state.GetPodEntries()
	initialMachineState := policy.state.GetMachineState()
	initialRevision := policy.state.GetRevision()

	resp := &advisorapi.ListAndWatchResponse{
		Entries: map[string]*advisorapi.CalculationEntries{
			commonstate.PoolNameShare: {
				Entries: map[string]*advisorapi.CalculationInfo{
					commonstate.FakedContainerName: {
						OwnerPoolName: commonstate.PoolNameShare,
						CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
							commonstate.FakedNUMAID: {
								Blocks: []*advisorapi.Block{{BlockId: "share", Result: 5}},
							},
						},
					},
				},
			},
			commonstate.PoolNameReclaim: {
				Entries: map[string]*advisorapi.CalculationInfo{
					commonstate.FakedContainerName: {
						OwnerPoolName: commonstate.PoolNameReclaim,
						CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
							commonstate.FakedNUMAID: {
								Blocks: []*advisorapi.Block{{BlockId: "reclaim", Result: 2}},
							},
						},
					},
				},
			},
		},
	}
	err = prepareAndCommitAdvisorBlocks(policy, advisorapi.BlockCPUSet{
		"share":   machine.NewCPUSet(2, 3, 4, 5, 6),
		"reclaim": machine.NewCPUSet(0, 1),
	}, resp, false)
	require.ErrorContains(t, err, "default share quantity 5 is smaller than residual cpuset size 6")
	require.Equal(t, initialRevision, policy.state.GetRevision())
	require.Equal(t, initialEntries, policy.state.GetPodEntries())
	require.Equal(t, initialMachineState, policy.state.GetMachineState())
}

func TestDefaultShareQuantityFromAdvisorResponseRequiresUniqueFakedNUMAAdvice(t *testing.T) {
	t.Parallel()

	valid := func() *advisorapi.ListAndWatchResponse {
		return &advisorapi.ListAndWatchResponse{
			Entries: map[string]*advisorapi.CalculationEntries{
				commonstate.PoolNameShare: {
					Entries: map[string]*advisorapi.CalculationInfo{
						commonstate.FakedContainerName: {
							OwnerPoolName: commonstate.PoolNameShare,
							CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
								commonstate.FakedNUMAID: {
									Blocks: []*advisorapi.Block{
										{BlockId: "share-0", Result: 3},
										{BlockId: "share-1", Result: 4},
									},
								},
							},
						},
					},
				},
			},
		}
	}

	resp := valid()
	got, err := defaultShareQuantityFromAdvisorResponse(resp)
	require.NoError(t, err)
	require.Equal(t, 7, got)

	resp = valid()
	resp.Entries[commonstate.PoolNameShare].Entries["duplicate"] = &advisorapi.CalculationInfo{
		OwnerPoolName: commonstate.PoolNameShare,
	}
	_, err = defaultShareQuantityFromAdvisorResponse(resp)
	require.ErrorContains(t, err, "exactly one pool entry")

	resp = valid()
	resp.Entries[commonstate.PoolNameShare].Entries[commonstate.FakedContainerName].
		CalculationResultsByNumas[0] = &advisorapi.NumaCalculationResult{}
	_, err = defaultShareQuantityFromAdvisorResponse(resp)
	require.ErrorContains(t, err, "only faked numa id")
}

func TestDefaultShareEligibleCPUSetUsesCurrentMachineStateGuards(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 1)
	require.NoError(t, err)
	policy, err := getTestDynamicPolicyWithoutInitialization(topology, t.TempDir())
	require.NoError(t, err)
	policy.config.reservedCPUs = machine.NewCPUSet(0)
	policy.reclaim.reservedReclaimedCPUSet = machine.NewCPUSet(4, 5)
	policy.reclaim.reservedReclaimedCPUsSize = 2
	policy.config.dynamicConfig.GetDynamicConfiguration().EnableReclaim = true
	policy.config.dynamicConfig.GetDynamicConfiguration().EnableRampUpReclaimHardPartition = true
	policy.config.dynamicConfig.GetDynamicConfiguration().MinReclaimedResourceForAllocate = v1.ResourceList{
		v1.ResourceCPU: *resource2.NewQuantity(4, resource2.DecimalSI),
	}

	entries := state.PodEntries{
		commonstate.PoolNameInterrupt: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameInterrupt),
				AllocationResult: machine.NewCPUSet(1),
			},
		},
		"system-test": {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta("system-test"),
				AllocationResult: machine.NewCPUSet(2),
			},
		},
		"ramp-pod": {
			"main": &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "ramp-pod",
					ContainerName: "main",
					QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
				},
				RampUp: true,
			},
		},
	}
	policy.state.SetPodEntries(entries, false)
	policy.state.SetMachineState(state.NUMANodeMap{
		0: {
			DefaultCPUSet: topology.CPUDetails.CPUs(),
			ResourcePackageStates: map[string]*state.ResourcePackageState{
				"pinned": {PinnedCPUSet: machine.NewCPUSet(3)},
			},
			PodEntries: entries,
		},
	}, false)

	machineState := policy.state.GetMachineState()
	rampFloor, err := policy.deriveRampUpReclaimFloor(machineState, policy.state.GetPodEntries(), sets.NewInt())
	require.NoError(t, err)
	got := policy.buildDefaultShareEligibleCPUSet(entries, machineState, rampFloor)
	require.True(t, got.Equals(machine.NewCPUSet(7)),
		"eligible must exclude reserved, forbidden, system, pinned, and ramp-floor CPUs, got %s", got)
}

func (s *advisorCommitRecordingState) CommitAdvisorState(
	entries state.PodEntries,
	machineState state.NUMANodeMap,
	allowOverlap bool,
	disableDedicatedOverlap bool,
	persist bool,
	permits ...*state.WritePermit,
) error {
	s.calls++
	if s.err != nil {
		return s.err
	}
	return s.State.CommitAdvisorState(entries, machineState, allowOverlap, disableDedicatedOverlap, persist, permits...)
}

func (s *advisorCommitRecordingState) CommitAdvisorStateIfRevision(
	expectedRevision uint64,
	entries state.PodEntries,
	machineState state.NUMANodeMap,
	allowOverlap bool,
	disableDedicatedOverlap bool,
	persist bool,
	permits ...*state.WritePermit,
) error {
	s.calls++
	if s.err != nil {
		return s.err
	}
	return s.State.CommitAdvisorStateIfRevision(
		expectedRevision, entries, machineState, allowOverlap, disableDedicatedOverlap, persist, permits...)
}

func (s *advisorCommitGuardState) CommitAdvisorState(
	state.PodEntries,
	state.NUMANodeMap,
	bool,
	bool,
	bool,
	...*state.WritePermit,
) error {
	s.unconditionalCommitCalls++
	return fmt.Errorf("advisor applyBlocks must use CommitAdvisorStateIfRevision")
}

func (s *advisorCommitGuardState) CommitAdvisorStateIfRevision(
	expectedRevision uint64,
	entries state.PodEntries,
	machineState state.NUMANodeMap,
	allowOverlap bool,
	disableDedicatedOverlap bool,
	persist bool,
	permits ...*state.WritePermit,
) error {
	s.conditionalCommitCalls++
	s.conditionalRevision = expectedRevision
	return s.State.CommitAdvisorStateIfRevision(
		expectedRevision, entries, machineState, allowOverlap, disableDedicatedOverlap, persist, permits...)
}

func (s *staleAdvisorCommitState) CommitAdvisorStateIfRevision(
	expectedRevision uint64,
	entries state.PodEntries,
	machineState state.NUMANodeMap,
	allowOverlap bool,
	disableDedicatedOverlap bool,
	persist bool,
	permits ...*state.WritePermit,
) error {
	if !s.injected {
		s.injected = true
		s.State.SetAllowSharedCoresOverlapReclaimedCores(
			!s.State.GetAllowSharedCoresOverlapReclaimedCores(), false)
	}
	return s.State.CommitAdvisorStateIfRevision(
		expectedRevision, entries, machineState, allowOverlap, disableDedicatedOverlap, persist, permits...)
}

func TestDynamicPolicy_checkAndApplyIfCgroupV1(t *testing.T) {
	t.Parallel()

	mockPod := &v1.Pod{
		Spec: v1.PodSpec{
			Containers: []v1.Container{
				{
					Name: "test-container",
					Resources: v1.ResourceRequirements{
						Requests: v1.ResourceList{
							v1.ResourceCPU: resource2.MustParse("2"),
						},
					},
				},
			},
		},
	}

	mockPodPathMap := map[string]*v1.Pod{
		"test-pod-1": mockPod,
	}

	resources := &common.CgroupResources{
		CpuQuota:  1000,
		CpuPeriod: 1000,
	}

	mockBG := &common.CPUStats{
		CpuQuota:  1000,
		CpuPeriod: 1000,
	}

	mockBG2 := &common.CPUStats{
		CpuQuota:  500,
		CpuPeriod: 1000,
	}

	mockBytes, _ := json.Marshal(resources)

	mockCal := &advisorsvc.CalculationInfo{
		CgroupPath: "test_cgroup_path",
		CalculationResult: &advisorsvc.CalculationResult{
			Values: map[string]string{
				string(advisorapi.ControlKnobKeyCgroupConfig): string(mockBytes),
			},
		},
	}

	newPolicy := func(t *testing.T, bg *common.CPUStats) *DynamicPolicy {
		t.Helper()
		p := &DynamicPolicy{meta: metaComponent{metaServer: &metaserver.MetaServer{
			MetaAgent: &agent.MetaAgent{
				PodFetcher: &pod.PodFetcherStub{},
			},
		}}}
		p.cgroupOps = &cgroupResourceOps{
			checkCgroup2UnifiedMode: func() bool { return false },
			getCPUWithRelativePath:  func(string) (*common.CPUStats, error) { return bg, nil },
			getCurrentPathAllPodsDirAndMap: func(string) (map[string]*v1.Pod, []string, error) {
				return mockPodPathMap, []string{"advisor-test-pod-1"}, nil
			},
			getPodAndRelativePath: func(string, string, map[string]*v1.Pod) (*v1.Pod, string, error) {
				return mockPod, "test_relative_path", nil
			},
			checkAndApplyAllPodsQuota: func(*advisorsvc.CalculationInfo, int64) error { return nil },
		}
		return p
	}

	t.Run("test cgroup v1 resource", func(t *testing.T) {
		t.Parallel()
		p := newPolicy(t, mockBG)
		err := p.checkAndApplyIfCgroupV1(mockCal, resources)
		require.NoError(t, err)
	})

	t.Run("test cgroup v1 resource 2", func(t *testing.T) {
		t.Parallel()
		p := newPolicy(t, mockBG2)
		err := p.checkAndApplyIfCgroupV1(mockCal, resources)
		require.NoError(t, err)
	})
}

func TestDynamicPolicy_getAllDirs(t *testing.T) {
	t.Parallel()

	t.Run("basic path", func(t *testing.T) {
		t.Parallel()
		p := &DynamicPolicy{}
		p.cgroupOps = &cgroupResourceOps{
			readDir: func(string) ([]os.DirEntry, error) {
				return []os.DirEntry{
					mockDirEntry{"foo", true},
					mockDirEntry{"bar", true},
				}, nil
			},
		}

		dirs, err := p.getAllDirs("/fake/path")
		assert.NoError(t, err)
		assert.ElementsMatch(t, dirs, []string{"foo", "bar"})
	})
}

type mockDirEntry struct {
	name  string
	isDir bool
}

func (m mockDirEntry) Name() string               { return m.name }
func (m mockDirEntry) IsDir() bool                { return m.isDir }
func (m mockDirEntry) Type() os.FileMode          { return 0 }
func (m mockDirEntry) Info() (os.FileInfo, error) { return nil, nil }

func TestDynamic_getCurrentPathAllPodsDirAndMap(t *testing.T) {
	t.Parallel()

	mockPodPathMap := map[string]*v1.Pod{
		"test-pod-1": {
			Spec: v1.PodSpec{
				Containers: []v1.Container{
					{
						Name: "test-container",
						Resources: v1.ResourceRequirements{
							Requests: v1.ResourceList{
								v1.ResourceCPU: resource2.MustParse("2"),
							},
						},
					},
				},
			},
		},
	}

	p := &DynamicPolicy{meta: metaComponent{metaServer: &metaserver.MetaServer{
		MetaAgent: &agent.MetaAgent{
			PodFetcher: &pod.PodFetcherStub{PodList: []*v1.Pod{mockPodPathMap["test-pod-1"]}},
		},
	}}}
	p.cgroupOps = &cgroupResourceOps{
		getPodAbsCgroupPath: func(string, string) (string, error) { return "test-pod-1-abs", nil },
		getAbsCgroupPath:    func(string, string) string { return "fake-abs-path" },
		readDir: func(string) ([]os.DirEntry, error) {
			return []os.DirEntry{mockDirEntry{"advisor-test-pod-1", true}}, nil
		},
	}

	resultMap, dirs, err := p.getCurrentPathAllPodsDirAndMap("test_group_path")
	require.NoError(t, err)
	require.NotNil(t, resultMap)
	require.NotEmpty(t, dirs)
}

func TestDynamicPolicy_getPodAndRelativePath(t *testing.T) {
	t.Parallel()

	currentPath := "test"
	dirs := "test-dir"
	podPathMap := map[string]*v1.Pod{
		common.GetAbsCgroupPath(common.DefaultSelectedSubsys, filepath.Join(currentPath, dirs)): {
			Spec: v1.PodSpec{
				Containers: []v1.Container{
					{
						Name: "test-container",
						Resources: v1.ResourceRequirements{
							Requests: v1.ResourceList{
								v1.ResourceCPU: resource2.MustParse("2"),
							},
						},
					},
				},
			},
		},
	}

	p := &DynamicPolicy{meta: metaComponent{metaServer: &metaserver.MetaServer{
		MetaAgent: &agent.MetaAgent{
			PodFetcher: &pod.PodFetcherStub{},
		},
	}}}

	_, _, err := p.getPodAndRelativePath(currentPath, dirs, podPathMap)
	require.NoError(t, err)
}

func TestDynamicPolicy_getAllPodsPathMap(t *testing.T) {
	t.Parallel()

	mockPods := []*v1.Pod{
		{
			Spec: v1.PodSpec{
				Containers: []v1.Container{
					{
						Name: "test-container",
						Resources: v1.ResourceRequirements{
							Requests: v1.ResourceList{
								v1.ResourceCPU: resource2.MustParse("2"),
							},
						},
					},
				},
			},
		},
	}

	p := &DynamicPolicy{meta: metaComponent{metaServer: &metaserver.MetaServer{
		MetaAgent: &agent.MetaAgent{
			PodFetcher: &pod.PodFetcherStub{PodList: mockPods},
		},
	}}}
	p.cgroupOps = &cgroupResourceOps{
		getPodAbsCgroupPath: func(string, string) (string, error) { return "test-pod-1-path", nil },
	}

	podPathMap, err := p.getAllPodsPathMap()

	require.NoError(t, err)
	require.Len(t, podPathMap, len(mockPods))
	require.Equal(t, mockPods[0], podPathMap["test-pod-1-path"])
}

func TestDynamicPolicy_getAllContainersRelativePathMap(t *testing.T) {
	t.Parallel()

	mockPod := &v1.Pod{
		Spec: v1.PodSpec{
			Containers: []v1.Container{
				{
					Name: "test-container",
					Resources: v1.ResourceRequirements{
						Requests: v1.ResourceList{
							v1.ResourceCPU: resource2.MustParse("2"),
						},
					},
				},
			},
		},
	}

	p := &DynamicPolicy{meta: metaComponent{metaServer: &metaserver.MetaServer{
		MetaAgent: &agent.MetaAgent{
			PodFetcher: &pod.PodFetcherStub{},
		},
	}}}
	p.cgroupOps = &cgroupResourceOps{
		getContainerID:                 func(*v1.Pod, string) (string, error) { return "test-container-ID", nil },
		getContainerRelativeCgroupPath: func(string, string) (string, error) { return "test-container-relative-path", nil },
	}

	testMap := p.getAllContainersRelativePathMap(mockPod)

	require.Len(t, testMap, 1)
	require.Equal(t, mockPod.Spec.Containers[0].Name, testMap["test-container-relative-path"].Name)
}

func TestDynamicPolicy_checkAllPodsQuota(t *testing.T) {
	t.Parallel()

	p := &DynamicPolicy{meta: metaComponent{metaServer: &metaserver.MetaServer{
		MetaAgent: &agent.MetaAgent{
			PodFetcher: &pod.PodFetcherStub{},
		},
	}}}

	mockPod := &v1.Pod{
		Spec: v1.PodSpec{
			Containers: []v1.Container{
				{
					Name: "test-container",
					Resources: v1.ResourceRequirements{
						Requests: v1.ResourceList{
							v1.ResourceCPU: resource2.MustParse("2"),
						},
						Limits: v1.ResourceList{
							v1.ResourceCPU: resource2.MustParse("4"),
						},
					},
				},
			},
		},
	}

	mockPodPathMap := map[string]*v1.Pod{
		"test-pod-1": mockPod,
	}

	mockPodDirs := []string{
		"test-pod-1-dir",
	}

	resources := &common.CgroupResources{
		CpuQuota:  1000,
		CpuPeriod: 1000,
	}

	mockBG := &common.CPUStats{
		CpuQuota:  1000,
		CpuPeriod: 1000,
	}

	mockBG2 := &common.CPUStats{
		CpuQuota:  2000,
		CpuPeriod: 1000,
	}

	mockBG3 := &common.CPUStats{
		CpuQuota:  2000000,
		CpuPeriod: 1000,
	}

	mockBytes, _ := json.Marshal(resources)

	mockCal := &advisorsvc.CalculationInfo{
		CgroupPath: "test_cgroup_path",
		CalculationResult: &advisorsvc.CalculationResult{
			Values: map[string]string{
				string(advisorapi.ControlKnobKeyCgroupConfig): string(mockBytes),
			},
		},
	}

	mockErr := fmt.Errorf("mock error")

	var (
		podDiscoveryErr  error
		podPathErr       error
		cgroupReadErr    error
		containerErr     error
		cgroupWriteErr   error
		cgroupReadCalls  int
		containerCalls   int
		cgroupWriteCalls int
	)

	p.cgroupOps = &cgroupResourceOps{
		getCurrentPathAllPodsDirAndMap: func(string) (map[string]*v1.Pod, []string, error) {
			return mockPodPathMap, mockPodDirs, podDiscoveryErr
		},
		getPodAndRelativePath: func(string, string, map[string]*v1.Pod) (*v1.Pod, string, error) {
			return mockPod, "test_relative_path", podPathErr
		},
		applyAllContainersQuota: func(*v1.Pod, bool) error {
			containerCalls++
			return containerErr
		},
		getCPUWithRelativePath: func(string) (*common.CPUStats, error) {
			cgroupReadCalls++
			return mockBG, cgroupReadErr
		},
		applyCPUWithRelativePath: func(string, *common.CPUData) error {
			cgroupWriteCalls++
			return cgroupWriteErr
		},
	}

	for _, quota := range []int64{mockBG.CpuQuota, mockBG2.CpuQuota, mockBG3.CpuQuota} {
		assert.NoError(t, p.checkAndApplyAllPodsQuota(mockCal, quota), "applies pod quota")
	}

	podDiscoveryErr = mockErr
	assert.Error(t, p.checkAndApplyAllPodsQuota(mockCal, mockBG.CpuQuota), "returns pod discovery failure")
	podDiscoveryErr = nil

	podPathErr = mockErr
	cgroupReadCallsBefore := cgroupReadCalls
	containerCallsBefore := containerCalls
	cgroupWriteCallsBefore := cgroupWriteCalls
	assert.NoError(t, p.checkAndApplyAllPodsQuota(mockCal, mockBG.CpuQuota), "skips unresolved pod path")
	assert.Equal(t, cgroupReadCallsBefore, cgroupReadCalls, "does not read cgroup after unresolved pod path")
	assert.Equal(t, containerCallsBefore, containerCalls, "does not update containers after unresolved pod path")
	assert.Equal(t, cgroupWriteCallsBefore, cgroupWriteCalls, "does not write cgroup after unresolved pod path")
	podPathErr = nil

	cgroupReadErr = mockErr
	assert.Error(t, p.checkAndApplyAllPodsQuota(mockCal, mockBG.CpuQuota), "returns cgroup read failure")
	cgroupReadErr = nil

	containerErr = mockErr
	for _, quota := range []int64{mockBG.CpuQuota, mockBG2.CpuQuota, mockBG3.CpuQuota} {
		assert.NoError(t, p.checkAndApplyAllPodsQuota(mockCal, quota), "skips container quota failure")
	}
	containerErr = nil

	cgroupWriteErr = mockErr
	for _, quota := range []int64{mockBG.CpuQuota, mockBG2.CpuQuota, mockBG3.CpuQuota} {
		assert.Error(t, p.checkAndApplyAllPodsQuota(mockCal, quota), "returns cgroup write failure")
	}
}

func TestDynamicPolicy_applyAllContainersQuota(t *testing.T) {
	t.Parallel()

	p := &DynamicPolicy{meta: metaComponent{metaServer: &metaserver.MetaServer{
		MetaAgent: &agent.MetaAgent{
			PodFetcher: &pod.PodFetcherStub{},
		},
	}}}

	containerPathMap := map[string]*v1.Container{
		"container1": {
			Name: "container1",
			Resources: v1.ResourceRequirements{
				Requests: v1.ResourceList{
					v1.ResourceCPU: resource2.MustParse("1"),
				},
			},
		},
		"container2": {
			Name: "container2",
			Resources: v1.ResourceRequirements{
				Requests: v1.ResourceList{
					v1.ResourceCPU: resource2.MustParse("2"),
				},
			},
		},
	}

	pod := &v1.Pod{
		Spec: v1.PodSpec{
			Containers: []v1.Container{
				{
					Name: "container1",
					Resources: v1.ResourceRequirements{
						Requests: v1.ResourceList{
							v1.ResourceCPU: resource2.MustParse("1"),
						},
					},
				},
				{
					Name: "container2",
					Resources: v1.ResourceRequirements{
						Requests: v1.ResourceList{
							v1.ResourceCPU: resource2.MustParse("2"),
						},
					},
				},
			},
		},
	}

	mockCPU := &common.CPUStats{
		CpuQuota:  1,
		CpuPeriod: 1000,
	}

	applyCalls := 0
	p.cgroupOps = &cgroupResourceOps{
		getAllContainersRelativePathMap: func(*v1.Pod) map[string]*v1.Container { return containerPathMap },
		applyAllSubCgroupQuotaToUnLimit: func(string) error { return nil },
		getCPUWithRelativePath:          func(string) (*common.CPUStats, error) { return mockCPU, nil },
		applyCPUWithRelativePath: func(string, *common.CPUData) error {
			applyCalls++
			return nil
		},
	}

	err := p.applyAllContainersQuota(pod, true)
	require.NoError(t, err)
	require.Equal(t, 2, applyCalls)

	err = p.applyAllContainersQuota(pod, false)
	require.NoError(t, err)
}

func TestDynamicPolicy_checkAndApplySubCgroupPath(t *testing.T) {
	t.Parallel()

	t.Run("non-dir and negative-quota", func(t *testing.T) {
		t.Parallel()
		p := &DynamicPolicy{}
		getCalls := 0
		p.cgroupOps = &cgroupResourceOps{
			getCPUWithAbsolutePath: func(string) (*common.CPUStats, error) {
				getCalls++
				return &common.CPUStats{CpuQuota: -1}, nil
			},
		}

		d1 := mockDirEntry{"path1", false}
		err1 := p.checkAndApplySubCgroupPath("path1", d1, nil)
		require.NoError(t, err1)

		d2 := mockDirEntry{"path2", true}
		err2 := p.checkAndApplySubCgroupPath("path2", d2, nil)
		require.NoError(t, err2)
		require.Equal(t, 1, getCalls)
	})

	t.Run("positive-quota-applies-1", func(t *testing.T) {
		t.Parallel()
		p := &DynamicPolicy{}
		p.cgroupOps = &cgroupResourceOps{
			getCPUWithAbsolutePath:   func(string) (*common.CPUStats, error) { return &common.CPUStats{CpuQuota: 1000}, nil },
			applyCPUWithAbsolutePath: func(string, *common.CPUData) error { return nil },
		}

		d3 := mockDirEntry{"path3", true}
		err3 := p.checkAndApplySubCgroupPath("path3", d3, nil)
		require.NoError(t, err3)
	})
}

func TestDynamicPolicyApplyBlocksOverridesReclaimDedicatedOverlapBeforeCommit(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 2)
	require.NoError(t, err)
	tmpDir, err := os.MkdirTemp("", "checkpoint-applyblocks-overlap")
	require.NoError(t, err)
	defer func() { _ = os.RemoveAll(tmpDir) }()

	policy, err := getTestDynamicPolicyWithoutInitialization(topology, tmpDir)
	require.NoError(t, err)
	policy.state.SetPodEntries(state.PodEntries{
		"pod-dedicated": {
			"main": &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "pod-dedicated",
					ContainerName: "main",
					OwnerPoolName: commonstate.PoolNameDedicated,
					QoSLevel:      apiconsts.PodAnnotationQoSLevelDedicatedCores,
				},
				AllocationResult: machine.NewCPUSet(2),
				TopologyAwareAssignments: map[int]machine.CPUSet{
					1: machine.NewCPUSet(2),
				},
			},
		},
		commonstate.PoolNameReclaim: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult: machine.NewCPUSet(3, 6),
				TopologyAwareAssignments: map[int]machine.CPUSet{
					0: machine.NewCPUSet(3),
					1: machine.NewCPUSet(6),
				},
			},
		},
	}, false)
	policy.state.SetAllowSharedCoresOverlapReclaimedCores(true, false)

	resp := &advisorapi.ListAndWatchResponse{
		Entries: map[string]*advisorapi.CalculationEntries{
			"pod-dedicated": {
				Entries: map[string]*advisorapi.CalculationInfo{
					"main": {
						OwnerPoolName: commonstate.PoolNameDedicated,
						CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
							0: {Blocks: []*advisorapi.Block{{BlockId: "dedicated-block", Result: 1}}},
						},
					},
				},
			},
			commonstate.PoolNameReclaim: {
				Entries: map[string]*advisorapi.CalculationInfo{
					commonstate.FakedContainerName: {
						OwnerPoolName: commonstate.PoolNameReclaim,
						CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
							0: {Blocks: []*advisorapi.Block{{BlockId: "reclaim-0", Result: 1}}},
							1: {Blocks: []*advisorapi.Block{{BlockId: "reclaim-1", Result: 1}}},
						},
					},
				},
			},
		},
	}
	blockCPUSet := advisorapi.BlockCPUSet{
		"dedicated-block": machine.NewCPUSet(2),
		"reclaim-0":       machine.NewCPUSet(0),
		"reclaim-1":       machine.NewCPUSet(2),
	}

	err = prepareAndCommitAdvisorBlocks(policy, blockCPUSet, resp, resp.AllowSharedCoresOverlapReclaimedCores)
	require.NoError(t, err)

	reclaimPool := policy.state.GetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName)
	require.NotNil(t, reclaimPool)
	require.True(t, reclaimPool.AllocationResult.Equals(machine.NewCPUSet(0)),
		"applyBlocks must commit the pending non-overlap reclaim effective set")
	require.False(t, policy.state.GetAllowSharedCoresOverlapReclaimedCores(),
		"applyBlocks must commit the pending overlap mode with pod and machine state")

	policy.state.SetAllowSharedCoresOverlapReclaimedCores(false, false)
	resp.AllowSharedCoresOverlapReclaimedCores = true
	require.NoError(t, prepareAndCommitAdvisorBlocks(policy, blockCPUSet, resp, resp.AllowSharedCoresOverlapReclaimedCores))
	require.True(t, policy.state.GetAllowSharedCoresOverlapReclaimedCores(),
		"advisor overlap mode must be committed in the same checkpoint transaction as pod and machine state")
}

func TestDynamicPolicyApplyBlocksPreservesLegalHardPartitionDespiteBulkheadPadding(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 2)
	require.NoError(t, err)
	policy, err := getTestDynamicPolicyWithoutInitialization(topology, t.TempDir())
	require.NoError(t, err)

	dynamicConf := policy.config.dynamicConfig.GetDynamicConfiguration()
	dynamicConf.EnableReclaim = true
	dynamicConf.AdminQoSConfiguration.CPUPluginConfiguration.EnableRampUpReclaimHardPartition = true
	dynamicConf.AdminQoSConfiguration.CPUPluginConfiguration.BulkheadConfig.Enable = true
	dynamicConf.AdminQoSConfiguration.CPUPluginConfiguration.BulkheadConfig.NonReclaimPoolMinSize = 4

	policy.state.SetPodEntries(state.PodEntries{
		"reclaimed-pod": {
			"main": &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "reclaimed-pod",
					ContainerName: "main",
					OwnerPoolName: commonstate.PoolNameReclaim,
					QoSLevel:      apiconsts.PodAnnotationQoSLevelReclaimedCores,
				},
				AllocationResult: machine.NewCPUSet(3, 7),
				RampUp:           true,
			},
		},
		commonstate.PoolNameReserve: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReserve),
				AllocationResult: machine.NewCPUSet(0, 4),
			},
		},
		commonstate.PoolNameReclaim: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult: machine.NewCPUSet(3, 7),
			},
		},
	}, false)
	resp := &advisorapi.ListAndWatchResponse{
		Entries: map[string]*advisorapi.CalculationEntries{
			commonstate.PoolNameReserve: {
				Entries: map[string]*advisorapi.CalculationInfo{
					commonstate.FakedContainerName: {
						OwnerPoolName: commonstate.PoolNameReserve,
						CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
							0: {Blocks: []*advisorapi.Block{{BlockId: "reserve", Result: 2}}},
						},
					},
				},
			},
			commonstate.PoolNameReclaim: {
				Entries: map[string]*advisorapi.CalculationInfo{
					commonstate.FakedContainerName: {
						OwnerPoolName: commonstate.PoolNameReclaim,
						CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
							0: {Blocks: []*advisorapi.Block{{BlockId: "reclaim-0", Result: 2}}},
							1: {Blocks: []*advisorapi.Block{{BlockId: "reclaim-1", Result: 2}}},
						},
					},
				},
			},
		},
	}
	blockCPUSet := advisorapi.BlockCPUSet{
		"reserve":   machine.NewCPUSet(0, 4),
		"reclaim-0": machine.NewCPUSet(1, 5),
		"reclaim-1": machine.NewCPUSet(2, 6),
	}

	pending, err := policy.applyBlocksWithDynamicConfig(blockCPUSet, resp, false, false, policy.config.dynamicConfig.GetDynamicConfiguration())
	require.NoError(t, err)
	require.NotNil(t, pending)
	reclaim := pending.entries[commonstate.PoolNameReclaim][commonstate.FakedContainerName]
	require.NotNil(t, reclaim)
	require.Equal(t, 2, reclaim.TopologyAwareAssignments[0].Size())
	require.Equal(t, 2, reclaim.TopologyAwareAssignments[1].Size())
}

func TestAllocateByCPUAdvisorPreservesLegalHardPartitionDespiteBulkheadPadding(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name         string
		minSize      int64
		wantHookCall int
	}{
		{name: "raw 2/2 padding 0", minSize: 2, wantHookCall: 1},
		{name: "raw 2/2 padding 2", minSize: 4, wantHookCall: 1},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			topology, err := machine.GenerateDummyCPUTopology(8, 1, 2)
			require.NoError(t, err)
			checkpointDir := t.TempDir()
			policy, err := getTestDynamicPolicyWithoutInitialization(topology, checkpointDir)
			require.NoError(t, err)
			policy.advisorPostCommitCheckpointDir = checkpointDir

			dynamicConf := policy.config.dynamicConfig.GetDynamicConfiguration()
			dynamicConf.EnableReclaim = true
			dynamicConf.AdminQoSConfiguration.CPUPluginConfiguration.EnableRampUpReclaimHardPartition = true
			dynamicConf.AdminQoSConfiguration.CPUPluginConfiguration.BulkheadConfig.Enable = true
			dynamicConf.AdminQoSConfiguration.CPUPluginConfiguration.BulkheadConfig.NonReclaimPoolMinSize = tc.minSize

			policy.state.SetPodEntries(state.PodEntries{
				"reclaimed-pod": {
					"main": &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:        "reclaimed-pod",
							ContainerName: "main",
							OwnerPoolName: commonstate.PoolNameReclaim,
							QoSLevel:      apiconsts.PodAnnotationQoSLevelReclaimedCores,
						},
						AllocationResult: machine.NewCPUSet(3, 7),
						RampUp:           true,
					},
				},
				commonstate.PoolNameReserve: {
					commonstate.FakedContainerName: &state.AllocationInfo{
						AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReserve),
						AllocationResult: machine.NewCPUSet(0, 4),
					},
				},
				commonstate.PoolNameReclaim: {
					commonstate.FakedContainerName: &state.AllocationInfo{
						AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
						AllocationResult: machine.NewCPUSet(3, 7),
					},
				},
			}, false)
			hookCalls := 0
			policy.RegisterAllocationHook(func(_, _ *state.AllocationInfo) error {
				hookCalls++
				return nil
			})

			resp := &advisorapi.ListAndWatchResponse{
				Entries: map[string]*advisorapi.CalculationEntries{
					commonstate.PoolNameReserve: {
						Entries: map[string]*advisorapi.CalculationInfo{
							commonstate.FakedContainerName: {
								OwnerPoolName: commonstate.PoolNameReserve,
								CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
									commonstate.FakedNUMAID: {Blocks: []*advisorapi.Block{{BlockId: "reserve", Result: 2}}},
								},
							},
						},
					},
					commonstate.PoolNameReclaim: {
						Entries: map[string]*advisorapi.CalculationInfo{
							commonstate.FakedContainerName: {
								OwnerPoolName: commonstate.PoolNameReclaim,
								CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
									0: {Blocks: []*advisorapi.Block{{BlockId: "reclaim-0", Result: 2}}},
									1: {Blocks: []*advisorapi.Block{{BlockId: "reclaim-1", Result: 2}}},
								},
							},
						},
					},
				},
			}

			err = policy.allocateByCPUAdvisor(nil, resp, map[string]*advisorsvc.FeatureGate{})
			require.NoError(t, err)
			require.Equal(t, tc.wantHookCall, hookCalls)
			reclaim := policy.state.GetAllocationInfo(
				commonstate.PoolNameReclaim, commonstate.FakedContainerName)
			require.NotNil(t, reclaim)
			require.Len(t, reclaim.TopologyAwareAssignments, 2)
			require.Equal(t, 2, reclaim.TopologyAwareAssignments[0].Size())
			require.Equal(t, 2, reclaim.TopologyAwareAssignments[1].Size())
		})
	}
}

func TestDynamicPolicyApplyBlocksPreservesExplicitDisjointReclaim(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 2)
	require.NoError(t, err)
	tmpDir, err := os.MkdirTemp("", "checkpoint-applyblocks-disjoint-reclaim")
	require.NoError(t, err)
	defer func() { _ = os.RemoveAll(tmpDir) }()

	policy, err := getTestDynamicPolicyWithoutInitialization(topology, tmpDir)
	require.NoError(t, err)
	policy.reclaim.reservedReclaimedCPUSet = machine.NewCPUSet(4, 6)
	policy.reclaim.reservedReclaimedTopologyAwareAssignments = map[int]machine.CPUSet{
		0: machine.NewCPUSet(4),
		1: machine.NewCPUSet(6),
	}
	policy.state.SetPodEntries(state.PodEntries{
		"pod-dedicated": {
			"main": &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "pod-dedicated",
					ContainerName: "main",
					OwnerPoolName: commonstate.PoolNameDedicated,
					QoSLevel:      apiconsts.PodAnnotationQoSLevelDedicatedCores,
				},
				AllocationResult: machine.NewCPUSet(2),
				TopologyAwareAssignments: map[int]machine.CPUSet{
					1: machine.NewCPUSet(2),
				},
			},
		},
		commonstate.PoolNameReclaim: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult: machine.NewCPUSet(4, 6),
			},
		},
	}, false)

	resp := &advisorapi.ListAndWatchResponse{
		AllowSharedCoresOverlapReclaimedCores:      true,
		DisableDedicatedCoresOverlapReclaimedCores: true,
		Entries: map[string]*advisorapi.CalculationEntries{
			"pod-dedicated": {
				Entries: map[string]*advisorapi.CalculationInfo{
					"main": {
						OwnerPoolName: commonstate.PoolNameDedicated,
						CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
							1: {Blocks: []*advisorapi.Block{{BlockId: "dedicated", Result: 1}}},
						},
					},
				},
			},
			commonstate.PoolNameReclaim: {
				Entries: map[string]*advisorapi.CalculationInfo{
					commonstate.FakedContainerName: {
						OwnerPoolName: commonstate.PoolNameReclaim,
						CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
							0: {Blocks: []*advisorapi.Block{{BlockId: "reclaim", Result: 2}}},
						},
					},
				},
			},
		},
	}
	blocks := advisorapi.BlockCPUSet{
		"dedicated": machine.NewCPUSet(2),
		"reclaim":   machine.NewCPUSet(0, 4),
	}

	require.NoError(t, prepareAndCommitAdvisorBlocks(policy, blocks, resp, true))
	reclaim := policy.state.GetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName)
	require.NotNil(t, reclaim)
	require.True(t, reclaim.AllocationResult.Equals(machine.NewCPUSet(0, 4)),
		"disjoint apply must not revise or fallback-expand planner reclaim, got %s", reclaim.AllocationResult)
}

func TestDynamicPolicyApplyBlocksRejectsMissingDisjointReclaim(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 2)
	require.NoError(t, err)
	tmpDir, err := os.MkdirTemp("", "checkpoint-applyblocks-missing-disjoint-reclaim")
	require.NoError(t, err)
	defer func() { _ = os.RemoveAll(tmpDir) }()
	policy, err := getTestDynamicPolicyWithoutInitialization(topology, tmpDir)
	require.NoError(t, err)

	resp := &advisorapi.ListAndWatchResponse{
		DisableDedicatedCoresOverlapReclaimedCores: true,
		Entries: map[string]*advisorapi.CalculationEntries{},
	}
	_, err = policy.applyBlocksWithDynamicConfig(advisorapi.BlockCPUSet{}, resp, false, false, policy.config.dynamicConfig.GetDynamicConfiguration())
	require.ErrorContains(t, err,
		"disjoint advisor response has no reclaim partition")
}

func TestDynamicPolicyApplyBlocksUsesResponseModeWhenDisablingOverlap(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 2)
	require.NoError(t, err)
	tmpDir, err := os.MkdirTemp("", "checkpoint-applyblocks-disable-overlap")
	require.NoError(t, err)
	defer func() { _ = os.RemoveAll(tmpDir) }()

	policy, err := getTestDynamicPolicyWithoutInitialization(topology, tmpDir)
	require.NoError(t, err)
	policy.reclaim.reservedReclaimedCPUSet = machine.NewCPUSet(0, 2)
	policy.reclaim.reservedReclaimedTopologyAwareAssignments = map[int]machine.CPUSet{
		0: machine.NewCPUSet(0),
		1: machine.NewCPUSet(2),
	}
	policy.state.SetAllowSharedCoresOverlapReclaimedCores(true, false)
	policy.state.SetPodEntries(state.PodEntries{
		"pod-dedicated": {
			"main": &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "pod-dedicated",
					ContainerName: "main",
					OwnerPoolName: commonstate.PoolNameDedicated,
					QoSLevel:      apiconsts.PodAnnotationQoSLevelDedicatedCores,
				},
				AllocationResult: machine.NewCPUSet(2),
				TopologyAwareAssignments: map[int]machine.CPUSet{
					1: machine.NewCPUSet(2),
				},
			},
		},
		commonstate.PoolNameReclaim: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult: machine.NewCPUSet(0, 2),
				TopologyAwareAssignments: map[int]machine.CPUSet{
					0: machine.NewCPUSet(0),
					1: machine.NewCPUSet(2),
				},
			},
		},
	}, false)

	resp := &advisorapi.ListAndWatchResponse{
		AllowSharedCoresOverlapReclaimedCores: false,
		Entries: map[string]*advisorapi.CalculationEntries{
			"pod-dedicated": {
				Entries: map[string]*advisorapi.CalculationInfo{
					"main": {
						OwnerPoolName: commonstate.PoolNameDedicated,
						CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
							1: {Blocks: []*advisorapi.Block{{BlockId: "dedicated-block", Result: 1}}},
						},
					},
				},
			},
			commonstate.PoolNameReclaim: {
				Entries: map[string]*advisorapi.CalculationInfo{
					commonstate.FakedContainerName: {
						OwnerPoolName: commonstate.PoolNameReclaim,
						CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
							0: {Blocks: []*advisorapi.Block{{BlockId: "reclaim-block", Result: 1}}},
						},
					},
				},
			},
		},
	}
	blockCPUSet := advisorapi.BlockCPUSet{
		"dedicated-block": machine.NewCPUSet(2),
		"reclaim-block":   machine.NewCPUSet(0),
	}

	recordingState := &advisorCommitRecordingState{
		State: policy.state,
		err:   fmt.Errorf("commit failed"),
	}
	policy.state = recordingState

	err = prepareAndCommitAdvisorBlocks(policy, blockCPUSet, resp, resp.AllowSharedCoresOverlapReclaimedCores)
	require.EqualError(t, err, "commit failed")
	require.Equal(t, 1, recordingState.calls)
	require.True(t, policy.state.GetAllowSharedCoresOverlapReclaimedCores())
	unchangedReclaimPool := policy.state.GetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName)
	require.NotNil(t, unchangedReclaimPool)
	require.True(t, unchangedReclaimPool.AllocationResult.Equals(machine.NewCPUSet(0, 2)))

	recordingState.err = nil
	require.NoError(t, prepareAndCommitAdvisorBlocks(policy, blockCPUSet, resp, resp.AllowSharedCoresOverlapReclaimedCores))
	require.Equal(t, 2, recordingState.calls)
	require.False(t, policy.state.GetAllowSharedCoresOverlapReclaimedCores())
	reclaimPool := policy.state.GetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName)
	require.NotNil(t, reclaimPool)
	require.True(t, reclaimPool.AllocationResult.Equals(machine.NewCPUSet(0)))
}

func TestDynamicPolicyApplyBlocksUsesRevisionGuard(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 2)
	require.NoError(t, err)
	tmpDir, err := os.MkdirTemp("", "checkpoint-applyblocks-revision-guard")
	require.NoError(t, err)
	defer func() { _ = os.RemoveAll(tmpDir) }()

	policy, err := getTestDynamicPolicyWithoutInitialization(topology, tmpDir)
	require.NoError(t, err)
	policy.reclaim.reservedReclaimedCPUSet = machine.NewCPUSet(0, 2)
	policy.reclaim.reservedReclaimedTopologyAwareAssignments = map[int]machine.CPUSet{
		0: machine.NewCPUSet(0),
		1: machine.NewCPUSet(2),
	}
	policy.state.SetPodEntries(state.PodEntries{
		"pod-dedicated": {
			"main": &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "pod-dedicated",
					ContainerName: "main",
					OwnerPoolName: commonstate.PoolNameDedicated,
					QoSLevel:      apiconsts.PodAnnotationQoSLevelDedicatedCores,
				},
				AllocationResult: machine.NewCPUSet(2),
				TopologyAwareAssignments: map[int]machine.CPUSet{
					1: machine.NewCPUSet(2),
				},
			},
		},
		commonstate.PoolNameReclaim: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult: machine.NewCPUSet(0, 2),
				TopologyAwareAssignments: map[int]machine.CPUSet{
					0: machine.NewCPUSet(0),
					1: machine.NewCPUSet(2),
				},
			},
		},
	}, false)

	resp := &advisorapi.ListAndWatchResponse{
		Entries: map[string]*advisorapi.CalculationEntries{
			"pod-dedicated": {
				Entries: map[string]*advisorapi.CalculationInfo{
					"main": {
						OwnerPoolName: commonstate.PoolNameDedicated,
						CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
							1: {Blocks: []*advisorapi.Block{{BlockId: "dedicated-block", Result: 1}}},
						},
					},
				},
			},
			commonstate.PoolNameReclaim: {
				Entries: map[string]*advisorapi.CalculationInfo{
					commonstate.FakedContainerName: {
						OwnerPoolName: commonstate.PoolNameReclaim,
						CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
							0: {Blocks: []*advisorapi.Block{{BlockId: "reclaim-block", Result: 1}}},
						},
					},
				},
			},
		},
	}
	blockCPUSet := advisorapi.BlockCPUSet{
		"dedicated-block": machine.NewCPUSet(2),
		"reclaim-block":   machine.NewCPUSet(0),
	}
	guardState := &advisorCommitGuardState{State: policy.state}
	policy.state = guardState

	require.NoError(t, prepareAndCommitAdvisorBlocks(policy, blockCPUSet, resp, resp.AllowSharedCoresOverlapReclaimedCores))
	require.Equal(t, 0, guardState.unconditionalCommitCalls)
	require.Equal(t, 1, guardState.conditionalCommitCalls)
	require.NotZero(t, guardState.conditionalRevision)
	reclaimPool := policy.state.GetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName)
	require.NotNil(t, reclaimPool)
	require.True(t, reclaimPool.AllocationResult.Equals(machine.NewCPUSet(0)))
}

func TestDynamicPolicyApplyBlocksRejectsStaleFrameWithoutOverwritingDesiredState(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 2)
	require.NoError(t, err)
	policy, err := getTestDynamicPolicyWithoutInitialization(topology, t.TempDir())
	require.NoError(t, err)

	oldDedicated := coresInNUMA(topology, 0, 0, 2)
	oldReclaim := coresInNUMA(topology, 0, 2, 4)
	policy.state.SetPodEntries(state.PodEntries{
		"pod-dedicated": {
			"main": &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "pod-dedicated",
					ContainerName: "main",
					OwnerPoolName: commonstate.PoolNameDedicated,
					QoSLevel:      apiconsts.PodAnnotationQoSLevelDedicatedCores,
				},
				AllocationResult: oldDedicated,
				TopologyAwareAssignments: map[int]machine.CPUSet{
					0: oldDedicated,
				},
			},
		},
		commonstate.PoolNameReclaim: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult: oldReclaim,
				TopologyAwareAssignments: map[int]machine.CPUSet{
					0: oldReclaim,
				},
			},
		},
	}, false)
	machineState, err := state.GenerateMachineStateFromPodEntries(
		topology, policy.state.GetPodEntries(), policy.state.GetMachineState())
	require.NoError(t, err)
	policy.state.SetMachineState(machineState, false)
	policy.state.SetDisableDedicatedCoresOverlapReclaimedCores(true, false)

	resp := &advisorapi.ListAndWatchResponse{
		DisableDedicatedCoresOverlapReclaimedCores: true,
		Entries: map[string]*advisorapi.CalculationEntries{
			"pod-dedicated": {
				Entries: map[string]*advisorapi.CalculationInfo{
					"main": {
						OwnerPoolName: commonstate.PoolNameDedicated,
						CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
							0: {Blocks: []*advisorapi.Block{{BlockId: "new-dedicated", Result: 4}}},
						},
					},
				},
			},
			commonstate.PoolNameReclaim: {
				Entries: map[string]*advisorapi.CalculationInfo{
					commonstate.FakedContainerName: {
						OwnerPoolName: commonstate.PoolNameReclaim,
						CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
							0: {Blocks: []*advisorapi.Block{{BlockId: "new-reclaim", Result: 4}}},
						},
					},
				},
			},
		},
	}
	policy.state = &staleAdvisorCommitState{State: policy.state}

	err = prepareAndCommitAdvisorBlocks(policy, advisorapi.BlockCPUSet{
		"new-dedicated": oldReclaim,
		"new-reclaim":   oldDedicated,
	}, resp, false)
	require.ErrorContains(t, err, "stale cpu plugin state revision")
	require.Equal(t, oldDedicated,
		policy.state.GetAllocationInfo("pod-dedicated", "main").AllocationResult)
	require.Equal(t, oldReclaim,
		policy.state.GetAllocationInfo(
			commonstate.PoolNameReclaim, commonstate.FakedContainerName).AllocationResult)
	require.True(t, policy.state.GetDisableDedicatedCoresOverlapReclaimedCores())
	require.True(t, policy.state.GetAllowSharedCoresOverlapReclaimedCores(),
		"the newer desired state that made the frame stale must be retained")
}

func TestDynamicPolicyReviseReclaimPoolUsesResponseMode(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name         string
		oldMode      bool
		responseMode bool
		expected     machine.CPUSet
	}{
		{
			name:         "true to false",
			oldMode:      true,
			responseMode: false,
			expected:     machine.NewCPUSet(0, 3, 4, 7),
		},
		{
			name:         "false to true",
			oldMode:      false,
			responseMode: true,
			expected:     machine.NewCPUSet(0, 2, 4, 6),
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			topology, err := machine.GenerateDummyCPUTopology(8, 1, 2)
			require.NoError(t, err)
			tmpDir, err := os.MkdirTemp("", "checkpoint-revise-reclaim-mode")
			require.NoError(t, err)
			defer func() { _ = os.RemoveAll(tmpDir) }()

			policy, err := getTestDynamicPolicyWithoutInitialization(topology, tmpDir)
			require.NoError(t, err)
			policy.reclaim.reservedReclaimedCPUSet = machine.NewCPUSet(0, 2, 4, 6)
			policy.reclaim.reservedReclaimedTopologyAwareAssignments = map[int]machine.CPUSet{
				0: machine.NewCPUSet(0, 4),
				1: machine.NewCPUSet(2, 6),
			}
			policy.state.SetAllowSharedCoresOverlapReclaimedCores(tc.oldMode, false)

			newEntries := state.PodEntries{
				"pod-dedicated": {
					"main": &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:        "pod-dedicated",
							ContainerName: "main",
							OwnerPoolName: commonstate.PoolNameDedicated,
							QoSLevel:      apiconsts.PodAnnotationQoSLevelDedicatedCores,
						},
						AllocationResult: machine.NewCPUSet(2, 6),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							1: machine.NewCPUSet(2, 6),
						},
					},
				},
				commonstate.PoolNameReclaim: {
					commonstate.FakedContainerName: &state.AllocationInfo{
						AllocationMeta:           commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
						AllocationResult:         machine.NewCPUSet(0, 4),
						OriginalAllocationResult: machine.NewCPUSet(0, 4),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(0, 4),
						},
						OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(0, 4),
						},
					},
				},
			}

			err = policy.reviseReclaimPool(
				newEntries,
				machine.NewCPUSet(),
				machine.NewCPUSet(2, 6),
				tc.responseMode,
			)
			require.NoError(t, err)
			reclaimPool := newEntries[commonstate.PoolNameReclaim][commonstate.FakedContainerName]
			require.True(t, reclaimPool.AllocationResult.Equals(tc.expected))
			requireCoreAligned(t, topology, reclaimPool.AllocationResult)
			require.Equal(t, tc.oldMode, policy.state.GetAllowSharedCoresOverlapReclaimedCores())
		})
	}
}

func TestDynamicPolicyValidateAdvisorPartitionBeforeCommitRejectsReclaimNonReclaimOverlap(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name             string
		podUID           string
		ownerPoolName    string
		qosLevel         string
		allowShared      bool
		disableDedicated bool
		wantErr          string
	}{
		{
			name:          "dedicated overlap allowed",
			podUID:        "pod-dedicated",
			ownerPoolName: commonstate.PoolNameDedicated,
			qosLevel:      apiconsts.PodAnnotationQoSLevelDedicatedCores,
		},
		{
			name:             "dedicated overlap disabled",
			podUID:           "pod-dedicated",
			ownerPoolName:    commonstate.PoolNameDedicated,
			qosLevel:         apiconsts.PodAnnotationQoSLevelDedicatedCores,
			disableDedicated: true,
			wantErr:          "overlaps reclaim pool",
		},
		{
			name:          "shared overlap rejected",
			podUID:        "pod-shared",
			ownerPoolName: commonstate.PoolNameShare,
			qosLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
			wantErr:       "reclaim pool overlaps disallowed shared partition before commit",
		},
		{
			name:          "shared overlap allowed",
			podUID:        "pod-shared",
			ownerPoolName: commonstate.PoolNameShare,
			qosLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
			allowShared:   true,
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			topology, err := machine.GenerateDummyCPUTopology(8, 1, 2)
			require.NoError(t, err)
			tmpDir, err := os.MkdirTemp("", "checkpoint-validate-partition-overlap")
			require.NoError(t, err)
			defer func() { _ = os.RemoveAll(tmpDir) }()
			policy, err := getTestDynamicPolicyWithoutInitialization(topology, tmpDir)
			require.NoError(t, err)

			newEntries := state.PodEntries{
				tc.podUID: {
					"main": &state.AllocationInfo{
						AllocationMeta: commonstate.AllocationMeta{
							PodUid:        tc.podUID,
							ContainerName: "main",
							OwnerPoolName: tc.ownerPoolName,
							QoSLevel:      tc.qosLevel,
						},
						AllocationResult: machine.NewCPUSet(2),
					},
				},
				commonstate.PoolNameReclaim: {
					commonstate.FakedContainerName: &state.AllocationInfo{
						AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
						AllocationResult: machine.NewCPUSet(0, 2),
						TopologyAwareAssignments: map[int]machine.CPUSet{
							0: machine.NewCPUSet(0),
							1: machine.NewCPUSet(2),
						},
					},
				},
			}

			err = policy.validateAdvisorPartitionBeforeCommitWithDynamicConfig(
				newEntries,
				policy.state.GetMachineState(),
				tc.allowShared,
				tc.disableDedicated,
				policy.config.dynamicConfig.GetDynamicConfiguration())
			if tc.wantErr == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tc.wantErr)
			}
		})
	}
}

// system_cores containers may legally overlap the reclaim pool: they are never
// part of the non-reclaim partition. When their specified pool is missing they
// fall back to the whole machine cpuset, which must not trip the reclaim/non-
// reclaim overlap guard regardless of the bulkhead hard-partition switch.
func TestDynamicPolicyValidateAdvisorPartitionBeforeCommitAllowsSystemCoresReclaimOverlap(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 2)
	require.NoError(t, err)
	tmpDir, err := os.MkdirTemp("", "checkpoint-validate-system-overlap")
	require.NoError(t, err)
	defer func() { _ = os.RemoveAll(tmpDir) }()
	policy, err := getTestDynamicPolicyWithoutInitialization(topology, tmpDir)
	require.NoError(t, err)

	// system_cores container whose specified pool is absent falls back to the
	// whole machine cpuset (0-7), fully covering the reclaim pool below.
	newEntries := state.PodEntries{
		"pod-system": {
			"main": &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "pod-system",
					ContainerName: "main",
					OwnerPoolName: commonstate.PoolNameShare,
					QoSLevel:      apiconsts.PodAnnotationQoSLevelSystemCores,
				},
				AllocationResult: topology.CPUDetails.CPUs(),
			},
		},
		commonstate.PoolNameReclaim: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult: machine.NewCPUSet(0, 2),
				TopologyAwareAssignments: map[int]machine.CPUSet{
					0: machine.NewCPUSet(0),
					1: machine.NewCPUSet(2),
				},
			},
		},
	}

	// bulkhead hard-partition disabled (allow=false, disableDedicated=false):
	// the overlap must be tolerated, not rejected.
	err = policy.validateAdvisorPartitionBeforeCommitWithDynamicConfig(newEntries, policy.state.GetMachineState(), false, false, policy.config.dynamicConfig.GetDynamicConfiguration())
	require.NoError(t, err)
}

func TestDynamicPolicyValidateAdvisorPartitionBeforeCommitChecksPhysicalNUMAWhenOverlapEnabled(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 2)
	require.NoError(t, err)
	tmpDir, err := os.MkdirTemp("", "checkpoint-validate-partition-numa")
	require.NoError(t, err)
	defer func() { _ = os.RemoveAll(tmpDir) }()
	policy, err := getTestDynamicPolicyWithoutInitialization(topology, tmpDir)
	require.NoError(t, err)

	newEntries := state.PodEntries{
		commonstate.PoolNameReclaim: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult: machine.NewCPUSet(0, 4),
				TopologyAwareAssignments: map[int]machine.CPUSet{
					0: machine.NewCPUSet(4),
					1: machine.NewCPUSet(0),
				},
			},
		},
	}

	err = policy.validateAdvisorPartitionBeforeCommitWithDynamicConfig(newEntries, policy.state.GetMachineState(), true, false, policy.config.dynamicConfig.GetDynamicConfiguration())
	require.Error(t, err)
	require.Contains(t, err.Error(), "reclaim pool assignment crosses NUMA")
}

func TestDynamicPolicyValidateAdvisorPartitionBeforeCommitRequiresExclusiveNUMACoverage(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(8, 1, 2)
	require.NoError(t, err)
	tmpDir, err := os.MkdirTemp("", "checkpoint-validate-exclusive-coverage")
	require.NoError(t, err)
	defer func() { _ = os.RemoveAll(tmpDir) }()
	policy, err := getTestDynamicPolicyWithoutInitialization(topology, tmpDir)
	require.NoError(t, err)
	policy.config.reservedCPUs = machine.NewCPUSet()

	eligible := topology.CPUDetails.CPUsInNUMANodes(0)
	cpus := eligible.ToSliceInt()
	require.GreaterOrEqual(t, len(cpus), 4)
	dedicated := machine.NewCPUSet(cpus[0], cpus[1])
	reclaim := machine.NewCPUSet(cpus[2])
	newEntries := state.PodEntries{
		"pod-dedicated": {
			"main": &state.AllocationInfo{
				AllocationMeta: commonstate.AllocationMeta{
					PodUid:        "pod-dedicated",
					ContainerName: "main",
					OwnerPoolName: commonstate.PoolNameDedicated,
					QoSLevel:      apiconsts.PodAnnotationQoSLevelDedicatedCores,
					Annotations: map[string]string{
						apiconsts.PodAnnotationMemoryEnhancementNumaBinding:   apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
						apiconsts.PodAnnotationMemoryEnhancementNumaExclusive: apiconsts.PodAnnotationMemoryEnhancementNumaExclusiveEnable,
					},
				},
				AllocationResult: dedicated,
				OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
					0: eligible.Clone(),
				},
			},
		},
		commonstate.PoolNameReclaim: {
			commonstate.FakedContainerName: &state.AllocationInfo{
				AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult: reclaim,
				TopologyAwareAssignments: map[int]machine.CPUSet{
					0: reclaim,
				},
			},
		},
	}

	require.ErrorContains(t, policy.validateAdvisorPartitionBeforeCommitWithDynamicConfig(newEntries, policy.state.GetMachineState(), true, true, policy.config.dynamicConfig.GetDynamicConfiguration()),
		"exclusive dedicated/reclaim partition does not cover eligible NUMA")
	reclaim = eligible.Difference(dedicated)
	newEntries[commonstate.PoolNameReclaim][commonstate.FakedContainerName].AllocationResult = reclaim
	newEntries[commonstate.PoolNameReclaim][commonstate.FakedContainerName].TopologyAwareAssignments[0] = reclaim
	require.NoError(t, policy.validateAdvisorPartitionBeforeCommitWithDynamicConfig(newEntries, policy.state.GetMachineState(), true, true, policy.config.dynamicConfig.GetDynamicConfiguration()))
}

// TestDynamicPolicy_generateBlockCPUSet verifies the block CPUSet generation logic.
// It uses a table-driven approach to test various scenarios including:
// - Two-phase allocation: Dedicated/Share blocks first, Reclaim blocks second.
// - Non-reclaimable CPU deduction: Ensuring reclaim blocks do not overlap with pinned CPUSets from resource packages marked as disable-reclaim.
// - Parallel execution: Ensuring no race conditions exist in the policy's read-only operations.
func TestDynamicPolicy_generateBlockCPUSet(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name                   string
		disableReclaimSelector string
		// setupMachineState prepares the mock machine state, e.g., resource packages, existing pod allocations.
		setupMachineState func(state state.State, topo *machine.CPUTopology)
		// advisorResponse simulates the response from the CPU advisor containing blocks to be allocated.
		advisorResponse  *advisorapi.ListAndWatchResponse
		expectedError    bool
		expectedErrorStr string
		// validateResult contains custom assertions for the resulting BlockCPUSet.
		validateResult func(t *testing.T, blockCPUSet advisorapi.BlockCPUSet, topo *machine.CPUTopology)
	}

	testCases := []testCase{
		{
			// Scenario: A single reclaim block without a specific NUMA ID (FakedNUMAID).
			// It should be allocated from the global available pool minus any global non-reclaimable CPUs.
			name:                   "basic reclaim block with faked NUMA ID",
			disableReclaimSelector: "disable-reclaim=true",
			setupMachineState: func(st state.State, topo *machine.CPUTopology) {
				machineState := st.GetMachineState()
				// NUMA 0 has a non-reclaimable package using CPUs 0,1,2,3
				machineState[0].ResourcePackageStates = map[string]*state.ResourcePackageState{
					"pkg1": {
						Attributes:   map[string]string{"disable-reclaim": "true"},
						PinnedCPUSet: machine.NewCPUSet(0, 1, 2, 3),
					},
				}
				// NUMA 1 has a reclaimable package using CPUs 8,9
				machineState[1].ResourcePackageStates = map[string]*state.ResourcePackageState{
					"pkg2": {
						Attributes:   map[string]string{"disable-reclaim": "false"},
						PinnedCPUSet: machine.NewCPUSet(8, 9),
					},
				}
				st.SetMachineState(machineState, false)
			},
			advisorResponse: &advisorapi.ListAndWatchResponse{
				Entries: map[string]*advisorapi.CalculationEntries{
					"reclaim": {
						Entries: map[string]*advisorapi.CalculationInfo{
							"reclaim-entry": {
								OwnerPoolName: "reclaim",
								CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
									-1: { // FakedNUMAID
										Blocks: []*advisorapi.Block{
											{BlockId: "block-reclaim-1", Result: 4},
										},
									},
								},
							},
						},
					},
				},
			},
			expectedError: false,
			validateResult: func(t *testing.T, blockCPUSet advisorapi.BlockCPUSet, topo *machine.CPUTopology) {
				as := assert.New(t)
				res := blockCPUSet["block-reclaim-1"]
				as.Equal(4, res.Size())
				as.True(res.Intersection(machine.NewCPUSet(0, 1, 2, 3)).IsEmpty(), "reclaim block should not use non-reclaimable pinned CPUs")
			},
		},
		{
			// Scenario: Multiple NUMA-aware reclaim blocks.
			// Reclaim blocks tied to specific NUMA nodes must avoid the non-reclaimable CPUs on their respective nodes.
			name:                   "NUMA-aware reclaim block allocation",
			disableReclaimSelector: "disable-reclaim=true",
			setupMachineState: func(st state.State, topo *machine.CPUTopology) {
				machineState := st.GetMachineState()
				machineState[0].ResourcePackageStates = map[string]*state.ResourcePackageState{
					"pkg1": {
						Attributes:   map[string]string{"disable-reclaim": "true"},
						PinnedCPUSet: machine.NewCPUSet(0, 1, 2, 3), // NUMA 0
					},
				}
				machineState[1].ResourcePackageStates = map[string]*state.ResourcePackageState{
					"pkg2": {
						Attributes:   map[string]string{"disable-reclaim": "true"},
						PinnedCPUSet: machine.NewCPUSet(4, 5), // NUMA 1 (CPUs 4,5,6,7,12,13,14,15)
					},
				}
				st.SetMachineState(machineState, false)
			},
			advisorResponse: &advisorapi.ListAndWatchResponse{
				Entries: map[string]*advisorapi.CalculationEntries{
					"reclaim": {
						Entries: map[string]*advisorapi.CalculationInfo{
							"reclaim-entry": {
								OwnerPoolName: "reclaim",
								CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
									0: {
										Blocks: []*advisorapi.Block{{BlockId: "block-reclaim-numa0", Result: 2}},
									},
									1: {
										Blocks: []*advisorapi.Block{{BlockId: "block-reclaim-numa1", Result: 4}},
									},
								},
							},
						},
					},
				},
			},
			expectedError: false,
			validateResult: func(t *testing.T, blockCPUSet advisorapi.BlockCPUSet, topo *machine.CPUTopology) {
				as := assert.New(t)
				res0 := blockCPUSet["block-reclaim-numa0"]
				as.Equal(2, res0.Size())
				as.True(res0.Intersection(machine.NewCPUSet(0, 1, 2, 3)).IsEmpty())
				as.True(res0.IsSubsetOf(topo.CPUDetails.CPUsInNUMANodes(0)))

				res1 := blockCPUSet["block-reclaim-numa1"]
				as.Equal(4, res1.Size())
				as.True(res1.Intersection(machine.NewCPUSet(4, 5)).IsEmpty())
				as.True(res1.IsSubsetOf(topo.CPUDetails.CPUsInNUMANodes(1)))
			},
		},
		{
			// Scenario: Verifying two-phase allocation logic.
			// Dedicated and Share blocks should be allocated first. Then, Reclaim blocks should be allocated
			// from the remaining CPUs, while also avoiding the non-reclaimable CPUs.
			name:                   "mixed dedicated, share, and reclaim blocks",
			disableReclaimSelector: "disable-reclaim=true",
			setupMachineState: func(st state.State, topo *machine.CPUTopology) {
				// Set up a pre-allocated dedicated pod on NUMA 0
				podEntries := state.PodEntries{
					"pod-dedicated": state.ContainerEntries{
						"container-1": &state.AllocationInfo{
							AllocationResult:         machine.NewCPUSet(8, 9), // NUMA 0
							OriginalAllocationResult: machine.NewCPUSet(8, 9),
							TopologyAwareAssignments: map[int]machine.CPUSet{
								0: machine.NewCPUSet(8, 9),
							},
							AllocationMeta: commonstate.AllocationMeta{
								QoSLevel: apiconsts.PodAnnotationQoSLevelDedicatedCores,
							},
						},
					},
					"pod-shared": state.ContainerEntries{
						"container-1": &state.AllocationInfo{
							AllocationResult:         machine.NewCPUSet(2, 3, 10, 11), // NUMA 0
							OriginalAllocationResult: machine.NewCPUSet(2, 3, 10, 11),
							TopologyAwareAssignments: map[int]machine.CPUSet{
								0: machine.NewCPUSet(2, 3, 10, 11),
							},
							AllocationMeta: commonstate.AllocationMeta{
								QoSLevel: apiconsts.PodAnnotationQoSLevelSharedCores,
							},
						},
					},
				}
				st.SetPodEntries(podEntries, false)

				machineState, _ := state.GenerateMachineStateFromPodEntries(topo, podEntries, nil)
				// Add non-reclaimable package on NUMA 0 (CPUs 0, 1)
				machineState[0].ResourcePackageStates = map[string]*state.ResourcePackageState{
					"pkg1": {
						Attributes:   map[string]string{"disable-reclaim": "true"},
						PinnedCPUSet: machine.NewCPUSet(0, 1),
					},
				}
				machineState[1].ResourcePackageStates = map[string]*state.ResourcePackageState{
					"pkg2": {
						Attributes:   map[string]string{"disable-reclaim": "false"},
						PinnedCPUSet: machine.NewCPUSet(4, 5, 6, 7, 12, 13, 14, 15), // NUMA 1 (CPUs 4,5,6,7,12,13,14,15)
					},
				}
				st.SetMachineState(machineState, false)
			},
			advisorResponse: &advisorapi.ListAndWatchResponse{
				Entries: map[string]*advisorapi.CalculationEntries{
					"pod-dedicated": {
						Entries: map[string]*advisorapi.CalculationInfo{
							"container-1": {
								OwnerPoolName: "dedicated",
								CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
									0: {Blocks: []*advisorapi.Block{{BlockId: "block-dedicated-1", Result: 2}}},
								},
							},
						},
					},
					"share": {
						Entries: map[string]*advisorapi.CalculationInfo{
							"share-entry": {
								OwnerPoolName: "share",
								CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
									-1: {Blocks: []*advisorapi.Block{{BlockId: "block-share-1", Result: 4}}},
								},
							},
						},
					},
					"reclaim": {
						Entries: map[string]*advisorapi.CalculationInfo{
							"reclaim-entry": {
								OwnerPoolName: "reclaim",
								CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
									// NUMA 0 is excluded from global availableCPUs by the dedicated pod,
									// so global Reclaim (FakedNUMAID) must be allocated on NUMA 1 to succeed.
									-1: {
										Blocks: []*advisorapi.Block{
											{BlockId: "block-reclaim-1", Result: 4},
										},
									},
									// However, NUMA-aware Reclaim on NUMA 0 can still use the remaining
									// reclaimable CPUs on NUMA 0 (i.e. CPUs occupied by shared pods).
									0: {
										Blocks: []*advisorapi.Block{
											{BlockId: "block-reclaim-2", Result: 4},
										},
									},
								},
							},
						},
					},
				},
			},
			expectedError: false,
			validateResult: func(t *testing.T, blockCPUSet advisorapi.BlockCPUSet, topo *machine.CPUTopology) {
				as := assert.New(t)
				ded := blockCPUSet["block-dedicated-1"]
				as.True(ded.Equals(machine.NewCPUSet(8, 9)), "dedicated block should reuse existing allocation")

				share := blockCPUSet["block-share-1"]
				as.Equal(4, share.Size())
				as.True(share.IsSubsetOf(topo.CPUDetails.CPUsInNUMANodes(1)), "share block must be on NUMA 1 because NUMA 0 is excluded")

				rec := blockCPUSet["block-reclaim-1"]
				as.Equal(4, rec.Size())
				as.True(rec.IsSubsetOf(topo.CPUDetails.CPUsInNUMANodes(1)), "reclaim block must be on NUMA 1")
				as.True(rec.Intersection(share).IsEmpty(), "reclaim block must avoid share CPUs")

				rec2 := blockCPUSet["block-reclaim-2"]
				as.Equal(4, rec2.Size())
				as.True(rec2.IsSubsetOf(topo.CPUDetails.CPUsInNUMANodes(0)), "reclaim block must be on NUMA 0")
				as.True(rec2.Intersection(ded).IsEmpty(), "reclaim block must avoid dedicated CPUs")
				as.True(rec2.Intersection(machine.NewCPUSet(0, 1)).IsEmpty(), "reclaim block must avoid non-reclaimable pkg CPUs on NUMA 0")
			},
		},
		{
			// Scenario: Verifying priority of isolation pool over normal share pool.
			// Isolation pools (starts with "isolation") should be in Phase 1,
			// while normal share pools should be in Phase 2.
			name: "isolation pool priority over normal share pool",
			setupMachineState: func(st state.State, topo *machine.CPUTopology) {
				// No special machine state needed
			},
			advisorResponse: &advisorapi.ListAndWatchResponse{
				Entries: map[string]*advisorapi.CalculationEntries{
					"isolation": {
						Entries: map[string]*advisorapi.CalculationInfo{
							"isolation-entry": {
								OwnerPoolName: "isolation-1",
								CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
									0: {Blocks: []*advisorapi.Block{{BlockId: "block-isolation-1", Result: 8}}},
								},
							},
						},
					},
					"share": {
						Entries: map[string]*advisorapi.CalculationInfo{
							"share-entry": {
								OwnerPoolName: "share",
								CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
									-1: {Blocks: []*advisorapi.Block{{BlockId: "block-share-1", Result: 2}}},
								},
							},
						},
					},
				},
			},
			expectedError: false,
			validateResult: func(t *testing.T, blockCPUSet advisorapi.BlockCPUSet, topo *machine.CPUTopology) {
				as := assert.New(t)
				// NUMA 0 has 8 CPUs (0,1,2,3, 8,9,10,11)
				iso := blockCPUSet["block-isolation-1"]
				as.Equal(8, iso.Size())
				as.True(iso.IsSubsetOf(topo.CPUDetails.CPUsInNUMANodes(0)))

				// share block should be allocated from remaining CPUs on the node or globally.
				// Since NUMA 0 is exhausted by isolation, it must be allocated from NUMA 1.
				share := blockCPUSet["block-share-1"]
				as.Equal(2, share.Size())
				as.True(share.Intersection(iso).IsEmpty())
				as.True(share.IsSubsetOf(topo.CPUDetails.CPUsInNUMANodes(1)))
			},
		},
		{
			// Scenario: Verifying that share block (normal) now avoids only globalNonReclaimableCPUSet.
			// Previously it avoided allPinnedCPUSets.
			name:                   "share block can use reclaimable pinned CPUs",
			disableReclaimSelector: "disable-reclaim=true",
			setupMachineState: func(st state.State, topo *machine.CPUTopology) {
				machineState := st.GetMachineState()
				// pkg1 is reclaimable (disable-reclaim=false)
				machineState[0].ResourcePackageStates = map[string]*state.ResourcePackageState{
					"pkg1": {
						Attributes:   map[string]string{"disable-reclaim": "false"},
						PinnedCPUSet: machine.NewCPUSet(0, 1, 2, 3),
					},
				}
				st.SetMachineState(machineState, false)
			},
			advisorResponse: &advisorapi.ListAndWatchResponse{
				Entries: map[string]*advisorapi.CalculationEntries{
					"share": {
						Entries: map[string]*advisorapi.CalculationInfo{
							"share-entry": {
								OwnerPoolName: "share",
								CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
									-1: {Blocks: []*advisorapi.Block{{BlockId: "block-share-1", Result: 2}}},
								},
							},
						},
					},
				},
			},
			expectedError: false,
			validateResult: func(t *testing.T, blockCPUSet advisorapi.BlockCPUSet, topo *machine.CPUTopology) {
				as := assert.New(t)
				share := blockCPUSet["block-share-1"]
				as.Equal(2, share.Size())
				// Since pkg1 is reclaimable, and share is now in Phase 2 (treated like reclaim),
				// it is NOT excluded from the available pool. The CPU allocator picks CPUs
				// in numerical order, so it will allocate CPUs 0 and 1, which overlap with pkg1.
				as.True(share.Intersection(machine.NewCPUSet(0, 1, 2, 3)).Size() > 0, "share block should be able to use reclaimable pinned CPUs")
			},
		},
		{
			// Scenario: Verifying NUMA-binding share pool priority.
			name: "NUMA-binding share pool priority",
			setupMachineState: func(st state.State, topo *machine.CPUTopology) {
				// No special machine state needed
			},
			advisorResponse: &advisorapi.ListAndWatchResponse{
				Entries: map[string]*advisorapi.CalculationEntries{
					"share-NUMA": {
						Entries: map[string]*advisorapi.CalculationInfo{
							"share-entry": {
								OwnerPoolName: "share-NUMA0",
								CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
									0: {Blocks: []*advisorapi.Block{{BlockId: "block-share-numa-1", Result: 8}}},
								},
							},
						},
					},
					"share-normal": {
						Entries: map[string]*advisorapi.CalculationInfo{
							"share-entry": {
								OwnerPoolName: "share",
								CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
									-1: {Blocks: []*advisorapi.Block{{BlockId: "block-share-normal-1", Result: 2}}},
								},
							},
						},
					},
				},
			},
			expectedError: false,
			validateResult: func(t *testing.T, blockCPUSet advisorapi.BlockCPUSet, topo *machine.CPUTopology) {
				as := assert.New(t)
				// NUMA 0 has 8 CPUs.
				shareNuma := blockCPUSet["block-share-numa-1"]
				as.Equal(8, shareNuma.Size())
				as.True(shareNuma.IsSubsetOf(topo.CPUDetails.CPUsInNUMANodes(0)))

				// normal share should be pushed to Phase 2 and find CPUs elsewhere.
				shareNormal := blockCPUSet["block-share-normal-1"]
				as.Equal(2, shareNormal.Size())
				as.True(shareNormal.Intersection(shareNuma).IsEmpty())
				as.True(shareNormal.IsSubsetOf(topo.CPUDetails.CPUsInNUMANodes(1)))
			},
		},
		{
			// Scenario: Verifying that NUMA-binding pod excludes the whole NUMA node from global available pool.
			name: "NUMA-binding pod excludes NUMA node from global pool",
			setupMachineState: func(st state.State, topo *machine.CPUTopology) {
				// Dedicated pod must be present in state for allocateDedicatedBlocks to work
				podEntries := state.PodEntries{
					"dedicated": state.ContainerEntries{
						"dedicated-entry": &state.AllocationInfo{
							AllocationResult:         machine.NewCPUSet(0, 1, 2, 3),
							OriginalAllocationResult: machine.NewCPUSet(0, 1, 2, 3),
							TopologyAwareAssignments: map[int]machine.CPUSet{
								0: machine.NewCPUSet(0, 1, 2, 3),
							},
							AllocationMeta: commonstate.AllocationMeta{
								QoSLevel: apiconsts.PodAnnotationQoSLevelDedicatedCores,
							},
						},
					},
				}
				st.SetPodEntries(podEntries, false)
			},
			advisorResponse: &advisorapi.ListAndWatchResponse{
				Entries: map[string]*advisorapi.CalculationEntries{
					"dedicated": {
						Entries: map[string]*advisorapi.CalculationInfo{
							"dedicated-entry": {
								OwnerPoolName: "dedicated",
								CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
									0: {Blocks: []*advisorapi.Block{{BlockId: "block-dedicated-1", Result: 4}}},
								},
							},
						},
					},
					"share": {
						Entries: map[string]*advisorapi.CalculationInfo{
							"share-entry": {
								OwnerPoolName: "share",
								CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
									-1: {Blocks: []*advisorapi.Block{{BlockId: "block-share-1", Result: 6}}},
								},
							},
						},
					},
				},
			},
			expectedError: false,
			validateResult: func(t *testing.T, blockCPUSet advisorapi.BlockCPUSet, topo *machine.CPUTopology) {
				as := assert.New(t)
				// NUMA 0 has 8 CPUs. Dedicated takes 4.
				ded := blockCPUSet["block-dedicated-1"]
				as.Equal(4, ded.Size())
				as.True(ded.IsSubsetOf(topo.CPUDetails.CPUsInNUMANodes(0)))

				// The remaining 4 CPUs on NUMA 0 should be excluded from global available pool
				// because NUMA 0 now has a NUMA-binding pod.
				// share block (non-NUMA aware) needs 6 CPUs.
				// If it could use the remaining 4 on NUMA 0, it might take 4 from NUMA 0 and 2 from NUMA 1.
				// But since NUMA 0 is excluded, it MUST take all 6 from NUMA 1.
				share := blockCPUSet["block-share-1"]
				as.Equal(6, share.Size())
				as.True(share.IsSubsetOf(topo.CPUDetails.CPUsInNUMANodes(1)), "share block should be entirely on NUMA 1")
			},
		},
		{
			// Scenario: Verifying that dedicated pod returns error if its size is same but some CPUs are unavailable.
			name: "dedicated pod size same but CPUs unavailable returns error",
			setupMachineState: func(st state.State, topo *machine.CPUTopology) {
				// Dedicated pod on CPUs 0,1,2,3
				podEntries := state.PodEntries{
					"dedicated": state.ContainerEntries{
						"dedicated-entry": &state.AllocationInfo{
							AllocationResult:         machine.NewCPUSet(0, 1, 2, 3),
							OriginalAllocationResult: machine.NewCPUSet(0, 1, 2, 3),
							TopologyAwareAssignments: map[int]machine.CPUSet{
								0: machine.NewCPUSet(0, 1, 2, 3),
							},
							AllocationMeta: commonstate.AllocationMeta{
								QoSLevel: apiconsts.PodAnnotationQoSLevelDedicatedCores,
							},
						},
					},
					"reserve": state.ContainerEntries{
						"": &state.AllocationInfo{
							AllocationResult: machine.NewCPUSet(0),
						},
					},
				}
				st.SetPodEntries(podEntries, false)
			},
			advisorResponse: &advisorapi.ListAndWatchResponse{
				Entries: map[string]*advisorapi.CalculationEntries{
					"dedicated": {
						Entries: map[string]*advisorapi.CalculationInfo{
							"dedicated-entry": {
								OwnerPoolName: "dedicated",
								CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
									0: {Blocks: []*advisorapi.Block{{BlockId: "block-dedicated-1", Result: 4}}},
								},
							},
						},
					},
					"reserve": {
						Entries: map[string]*advisorapi.CalculationInfo{
							"": {
								OwnerPoolName: "reserve",
								CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
									-1: {Blocks: []*advisorapi.Block{{BlockId: "block-reserve", Result: 1}}},
								},
							},
						},
					},
				},
			},
			expectedError:    true,
			expectedErrorStr: "size not changed, but some CPUs are not available",
		},
		{
			// Scenario: Exhaustion of CPUs for reclaim.
			// After deducting dedicated, share, and non-reclaimable CPUs, if there are not enough
			// CPUs left for a reclaim block, an error should be returned.
			name:                   "not enough CPUs for reclaim block after deducting non-reclaimable",
			disableReclaimSelector: "disable-reclaim=true",
			setupMachineState: func(st state.State, topo *machine.CPUTopology) {
				machineState := st.GetMachineState()
				// NUMA 0 has 8 CPUs (0,1,2,3, 8,9,10,11). We pin 7 of them as non-reclaimable.
				machineState[0].ResourcePackageStates = map[string]*state.ResourcePackageState{
					"pkg1": {
						Attributes:   map[string]string{"disable-reclaim": "true"},
						PinnedCPUSet: machine.NewCPUSet(0, 1, 2, 3, 8, 9, 10), // only CPU 11 is available
					},
				}
				st.SetMachineState(machineState, false)
			},
			advisorResponse: &advisorapi.ListAndWatchResponse{
				Entries: map[string]*advisorapi.CalculationEntries{
					"reclaim": {
						Entries: map[string]*advisorapi.CalculationInfo{
							"reclaim-entry": {
								OwnerPoolName: "reclaim",
								CalculationResultsByNumas: map[int64]*advisorapi.NumaCalculationResult{
									0: {Blocks: []*advisorapi.Block{{BlockId: "block-reclaim-1", Result: 2}}}, // Requests 2, but only 1 available
								},
							},
						},
					},
				},
			},
			expectedError:    true,
			expectedErrorStr: "allocate cpuset for NUMA Aware reclaim block: block-reclaim-1 in NUMA: 0 failed",
			validateResult: func(t *testing.T, blockCPUSet advisorapi.BlockCPUSet, topo *machine.CPUTopology) {
				// No validation needed if error is expected
			},
		},
		{
			name:                   "test invalid disable reclaim selector",
			disableReclaimSelector: "disable-reclaim=true,,invalid",
			expectedError:          true,
		},
	}

	for _, tc := range testCases {
		tc := tc // capture range variable for parallel execution
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			as := assert.New(t)

			// Initialize a clean topology for each parallel test (16 CPUs, 2 Sockets, 2 NUMA nodes)
			// NUMA 0: 0, 1, 2, 3, 8, 9, 10, 11
			// NUMA 1: 4, 5, 6, 7, 12, 13, 14, 15
			topo, err := machine.GenerateDummyCPUTopology(16, 2, 2)
			as.NoError(err)

			conf := generateTestConfiguration(t, "", "")
			if tc.disableReclaimSelector != "" {
				conf.GetDynamicConfiguration().DisableReclaimPinnedCPUSetResourcePackageSelector = tc.disableReclaimSelector
			}

			// Strict isolation using a fresh temp directory
			st, _ := state.NewCheckpointState(&statedirectory.StateDirectoryConfiguration{StateFileDirectory: t.TempDir()}, "test", "test", topo, false, state.GenerateMachineStateFromPodEntries, metrics.DummyMetrics{})

			// Prepare initial machine state (e.g. static pools)
			machineState, _ := state.GenerateMachineStateFromPodEntries(topo, nil, nil)
			st.SetMachineState(machineState, false)

			if tc.setupMachineState != nil {
				tc.setupMachineState(st, topo)
			}

			policy := &DynamicPolicy{
				state: st, machine: machineComponent{machineInfo: &machine.KatalystMachineInfo{
					CPUTopology: topo,
				}}, config: configComponent{conf: conf, dynamicConfig: conf.DynamicAgentConfiguration},
			}

			blockCPUSet, err := policy.generateBlockCPUSet(tc.advisorResponse, nil, false)
			if tc.expectedError {
				as.Error(err)
				if tc.expectedErrorStr != "" {
					as.Contains(err.Error(), tc.expectedErrorStr)
				}
				return
			}
			as.NoError(err)
			if tc.validateResult != nil {
				tc.validateResult(t, blockCPUSet, topo)
			}
		})
	}
}
