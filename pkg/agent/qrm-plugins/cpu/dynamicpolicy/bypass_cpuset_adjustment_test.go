/*
Copyright 2026 The Katalyst Authors.

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
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	cpuconsts "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/qrm/statedirectory"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// makeInfo builds an allocationInfo for the given QoS.
func makeInfo(qosLevel string, value *string) *state.AllocationInfo {
	req := &pluginapi.ResourceRequest{
		PodUid:        "pod-" + qosLevel,
		PodNamespace:  "default",
		PodName:       "pod-" + qosLevel,
		ContainerName: "main",
		Annotations:   map[string]string{apiconsts.PodAnnotationQoSLevelKey: qosLevel},
	}
	meta := commonstate.GenerateGenericContainerAllocationMeta(req, commonstate.EmptyOwnerPoolName, qosLevel)
	if value != nil {
		if meta.Annotations == nil {
			meta.Annotations = map[string]string{}
		}
		meta.Annotations[cpuconsts.CPUSetOmissionAllowedAnnotationKey] = *value
	}
	return &state.AllocationInfo{AllocationMeta: meta}
}

func strptr(s string) *string { return &s }

// newOmissionPolicy builds an initialized dynamic policy on the standard dummy
// 16-CPU/2-NUMA topology with the live bypass switch set as given.
func newOmissionPolicy(t testing.TB, liveEnabled bool) *DynamicPolicy {
	t.Helper()
	topology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithInitialization(topology, t.TempDir())
	require.NoError(t, err)
	p.config.dynamicConfig.GetDynamicConfiguration().EnableBypassCPUSetAdjustment = liveEnabled
	return p
}

// seedSharedMain writes a pre-existing shared-cores MAIN allocationInfo with a
// non-empty CPUSet. When anno is non-nil it sets the cpuset-omission-allowed
// key to that value; nil leaves the key absent (a pre-existing container).
func seedSharedMain(t testing.TB, p *DynamicPolicy, podUID string, anno *string) {
	t.Helper()
	cpus := machine.NewCPUSet(2, 3)
	info := &state.AllocationInfo{
		AllocationMeta: commonstate.AllocationMeta{
			PodUid:        podUID,
			PodName:       podUID,
			ContainerName: "main",
			ContainerType: pluginapi.ContainerType_MAIN.String(),
			QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
			OwnerPoolName: commonstate.PoolNameShare,
		},
		AllocationResult:         cpus,
		OriginalAllocationResult: cpus.Clone(),
		TopologyAwareAssignments: map[int]machine.CPUSet{0: cpus},
		RequestQuantity:          float64(cpus.Size()),
		InitTimestamp:            "2026-01-01 00:00:00.000000000 +0800 CST",
	}
	if anno != nil {
		info.Annotations = map[string]string{cpuconsts.CPUSetOmissionAllowedAnnotationKey: *anno}
	}
	require.NoError(t, p.state.SetAllocationInfo(podUID, "main", info, false))
}

// --- unit tests: pure functions ---

func TestCPUSetOmissionAllowed(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		qos         string
		value       *string
		liveEnabled bool
		want        bool
	}{
		{"missing key shared + live on", apiconsts.PodAnnotationQoSLevelSharedCores, nil, true, true},
		{"missing key shared + live off", apiconsts.PodAnnotationQoSLevelSharedCores, nil, false, false},
		{"true shared + live on", apiconsts.PodAnnotationQoSLevelSharedCores, strptr(cpusetOmissionTrue), true, true},
		{"true shared + live off", apiconsts.PodAnnotationQoSLevelSharedCores, strptr(cpusetOmissionTrue), false, false},
		{"false shared + live on", apiconsts.PodAnnotationQoSLevelSharedCores, strptr(cpusetOmissionFalse), true, false},
		{"invalid shared + live on", apiconsts.PodAnnotationQoSLevelSharedCores, strptr("invalid"), true, false},
		{"missing key dedicated + live on", apiconsts.PodAnnotationQoSLevelDedicatedCores, nil, true, false},
		{"true dedicated + live on", apiconsts.PodAnnotationQoSLevelDedicatedCores, strptr(cpusetOmissionTrue), true, false},
		{"missing key reclaimed + live on", apiconsts.PodAnnotationQoSLevelReclaimedCores, nil, true, true},
		{"missing key system + live on", apiconsts.PodAnnotationQoSLevelSystemCores, nil, true, true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			info := makeInfo(tt.qos, tt.value)
			got := cpusetOmissionAllowed(info, tt.liveEnabled)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestInitialCPUSetOmissionAllowed(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		qos          string
		requestValue *string
		liveEnabled  bool
		want         bool
	}{
		{"shared live on, no request value", apiconsts.PodAnnotationQoSLevelSharedCores, nil, true, true},
		{"shared live off, no request value", apiconsts.PodAnnotationQoSLevelSharedCores, nil, false, false},
		{"shared live on, request true", apiconsts.PodAnnotationQoSLevelSharedCores, strptr(cpusetOmissionTrue), true, true},
		{"shared live on, request false", apiconsts.PodAnnotationQoSLevelSharedCores, strptr(cpusetOmissionFalse), true, false},
		{"shared live on, request invalid", apiconsts.PodAnnotationQoSLevelSharedCores, strptr("bad"), true, false},
		{"dedicated live on", apiconsts.PodAnnotationQoSLevelDedicatedCores, nil, true, false},
		{"dedicated live on, request true", apiconsts.PodAnnotationQoSLevelDedicatedCores, strptr(cpusetOmissionTrue), true, false},
		{"reclaimed live on", apiconsts.PodAnnotationQoSLevelReclaimedCores, nil, true, true},
		{"system live on", apiconsts.PodAnnotationQoSLevelSystemCores, nil, true, true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			reqAnno := map[string]string{}
			if tt.requestValue != nil {
				reqAnno[cpuconsts.CPUSetOmissionAllowedAnnotationKey] = *tt.requestValue
			}
			got := initialCPUSetOmissionAllowed(tt.qos, reqAnno, tt.liveEnabled)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestMarkCPUSetExposed(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		qos         string
		value       *string
		wantChanged bool
		wantValue   string
	}{
		{"shared missing key", apiconsts.PodAnnotationQoSLevelSharedCores, nil, true, cpusetOmissionFalse},
		{"shared true", apiconsts.PodAnnotationQoSLevelSharedCores, strptr(cpusetOmissionTrue), true, cpusetOmissionFalse},
		{"shared false", apiconsts.PodAnnotationQoSLevelSharedCores, strptr(cpusetOmissionFalse), false, cpusetOmissionFalse},
		{"shared invalid", apiconsts.PodAnnotationQoSLevelSharedCores, strptr("bad"), true, cpusetOmissionFalse},
		{"dedicated missing key", apiconsts.PodAnnotationQoSLevelDedicatedCores, nil, false, ""},
		{"dedicated true", apiconsts.PodAnnotationQoSLevelDedicatedCores, strptr(cpusetOmissionTrue), false, cpusetOmissionTrue},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			info := makeInfo(tt.qos, tt.value)
			got := markCPUSetExposed(info)
			assert.Equal(t, tt.wantChanged, got)
			if tt.wantChanged {
				assert.Equal(t, tt.wantValue, info.Annotations[cpuconsts.CPUSetOmissionAllowedAnnotationKey])
			}
		})
	}
}

// TestMarkAndInherit_NoOpInputsUnchanged pins that the no-op paths neither
// allocate maps nor mutate the caller's input: dedicated QoS and an already
// "false" value must leave the Annotations map untouched.
func TestMarkAndInherit_NoOpInputsUnchanged(t *testing.T) {
	t.Parallel()

	// makeInfo's meta already carries the qos_level annotation; the no-op paths
	// must leave that map alone — never add the omission key.
	t.Run("dedicated does not add omission key", func(t *testing.T) {
		t.Parallel()
		info := makeInfo(apiconsts.PodAnnotationQoSLevelDedicatedCores, nil)
		beforeLen := len(info.Annotations)
		require.False(t, markCPUSetExposed(info))
		_, present := info.Annotations[cpuconsts.CPUSetOmissionAllowedAnnotationKey]
		assert.False(t, present, "dedicated mark must not add the omission key")
		assert.Len(t, info.Annotations, beforeLen, "dedicated mark must not mutate the annotations map")
	})

	t.Run("already false map untouched", func(t *testing.T) {
		t.Parallel()
		info := makeInfo(apiconsts.PodAnnotationQoSLevelSharedCores, strptr(cpusetOmissionFalse))
		require.NotNil(t, info.Annotations)
		beforeLen := len(info.Annotations)
		require.False(t, markCPUSetExposed(info))
		assert.Len(t, info.Annotations, beforeLen, "no-op mark must not add entries")
		assert.Equal(t, cpusetOmissionFalse, info.Annotations[cpuconsts.CPUSetOmissionAllowedAnnotationKey])
	})

	t.Run("inherit nil origin does not add omission key", func(t *testing.T) {
		t.Parallel()
		dst := makeInfo(apiconsts.PodAnnotationQoSLevelSharedCores, nil)
		beforeLen := len(dst.Annotations)
		inheritCPUSetOmission(dst, nil)
		_, present := dst.Annotations[cpuconsts.CPUSetOmissionAllowedAnnotationKey]
		assert.False(t, present, "inherit from nil must not add the omission key")
		assert.Len(t, dst.Annotations, beforeLen, "inherit from nil must not mutate the annotations map")
	})
}

func TestInheritCPUSetOmission(t *testing.T) {
	t.Parallel()
	mkInfo := func(qos string, anno map[string]string) *state.AllocationInfo {
		info := makeInfo(qos, nil)
		if anno != nil {
			info.Annotations = anno
		}
		return info
	}

	t.Run("origin has true", func(t *testing.T) {
		t.Parallel()
		dst := mkInfo(apiconsts.PodAnnotationQoSLevelSharedCores, map[string]string{})
		origin := mkInfo(apiconsts.PodAnnotationQoSLevelSharedCores, map[string]string{
			cpuconsts.CPUSetOmissionAllowedAnnotationKey: cpusetOmissionTrue,
		})
		inheritCPUSetOmission(dst, origin)
		assert.Equal(t, cpusetOmissionTrue, dst.Annotations[cpuconsts.CPUSetOmissionAllowedAnnotationKey])
	})

	t.Run("origin has false", func(t *testing.T) {
		t.Parallel()
		dst := mkInfo(apiconsts.PodAnnotationQoSLevelSharedCores, map[string]string{
			cpuconsts.CPUSetOmissionAllowedAnnotationKey: cpusetOmissionTrue,
		})
		origin := mkInfo(apiconsts.PodAnnotationQoSLevelSharedCores, map[string]string{
			cpuconsts.CPUSetOmissionAllowedAnnotationKey: cpusetOmissionFalse,
		})
		inheritCPUSetOmission(dst, origin)
		assert.Equal(t, cpusetOmissionFalse, dst.Annotations[cpuconsts.CPUSetOmissionAllowedAnnotationKey])
	})

	t.Run("origin missing key deletes dst key", func(t *testing.T) {
		t.Parallel()
		dst := mkInfo(apiconsts.PodAnnotationQoSLevelSharedCores, map[string]string{
			cpuconsts.CPUSetOmissionAllowedAnnotationKey: cpusetOmissionTrue,
		})
		origin := mkInfo(apiconsts.PodAnnotationQoSLevelSharedCores, nil)
		inheritCPUSetOmission(dst, origin)
		_, present := dst.Annotations[cpuconsts.CPUSetOmissionAllowedAnnotationKey]
		assert.False(t, present)
	})

	t.Run("origin nil deletes dst key", func(t *testing.T) {
		t.Parallel()
		dst := mkInfo(apiconsts.PodAnnotationQoSLevelSharedCores, map[string]string{
			cpuconsts.CPUSetOmissionAllowedAnnotationKey: cpusetOmissionTrue,
		})
		inheritCPUSetOmission(dst, nil)
		_, present := dst.Annotations[cpuconsts.CPUSetOmissionAllowedAnnotationKey]
		assert.False(t, present)
	})

	t.Run("dst annotations nil creates map", func(t *testing.T) {
		t.Parallel()
		dst := mkInfo(apiconsts.PodAnnotationQoSLevelSharedCores, nil)
		origin := mkInfo(apiconsts.PodAnnotationQoSLevelSharedCores, map[string]string{
			cpuconsts.CPUSetOmissionAllowedAnnotationKey: cpusetOmissionFalse,
		})
		inheritCPUSetOmission(dst, origin)
		require.NotNil(t, dst.Annotations)
		assert.Equal(t, cpusetOmissionFalse, dst.Annotations[cpuconsts.CPUSetOmissionAllowedAnnotationKey])
	})
}

// --- end-to-end tests ---

func TestFirstAllocate_Matrix(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name            string
		liveEnabled     bool
		requestAnno     *string // optional explicit cpuset-omission-allowed request value
		wantAnno        string  // persisted annotation value
		wantCPUSetEmpty bool    // outgoing response must omit X
	}{
		{"live true, no request key", true, nil, cpusetOmissionTrue, true},
		{"live false, no request key", false, nil, cpusetOmissionFalse, false},
		{"live true, request false overrides", true, strptr(cpusetOmissionFalse), cpusetOmissionFalse, false},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			topology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
			require.NoError(t, err)
			p, err := getTestDynamicPolicyWithInitialization(topology, t.TempDir())
			require.NoError(t, err)
			p.config.dynamicConfig.GetDynamicConfiguration().EnableBypassCPUSetAdjustment = tt.liveEnabled

			annos := map[string]string{apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelSharedCores}
			if tt.requestAnno != nil {
				annos[cpuconsts.CPUSetOmissionAllowedAnnotationKey] = *tt.requestAnno
			}
			req := &pluginapi.ResourceRequest{
				PodUid: "pod-first", PodNamespace: "default", PodName: "pod-first",
				ContainerName: "main", ContainerType: pluginapi.ContainerType_MAIN, ContainerIndex: 0,
				ResourceName:     string(v1.ResourceCPU),
				ResourceRequests: map[string]float64{string(v1.ResourceCPU): 2},
				Labels:           map[string]string{apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelSharedCores},
				Annotations:      annos,
			}
			resp, err := p.Allocate(context.Background(), req)
			require.NoError(t, err)
			cpuInfo := resp.AllocationResult.ResourceAllocation[string(v1.ResourceCPU)]
			if tt.wantCPUSetEmpty {
				assert.Empty(t, cpuInfo.AllocationResult, "must omit CPUSet")
			} else {
				assert.NotEmpty(t, cpuInfo.AllocationResult, "must return non-empty CPUSet")
			}

			ai := p.state.GetAllocationInfo("pod-first", "main")
			require.NotNil(t, ai)
			assert.Equal(t, tt.wantAnno, ai.Annotations[cpuconsts.CPUSetOmissionAllowedAnnotationKey])
		})
	}
}

// TestFirstAllocate_QoSMatrix verifies every bypassable QoS records its initial
// decision at first Allocate (shared + reclaimed through the real Allocate
// entry; dedicated is pinned separately by TestDedicatedNUMA_FirstAllocate_WritesFalse,
// system by TestSystemReAdmit_NewContainerWritesTrue).
func TestFirstAllocate_QoSMatrix(t *testing.T) {
	t.Parallel()
	for _, qos := range []string{
		apiconsts.PodAnnotationQoSLevelSharedCores,
		apiconsts.PodAnnotationQoSLevelReclaimedCores,
	} {
		qos := qos
		t.Run(qos, func(t *testing.T) {
			t.Parallel()
			p := newOmissionPolicy(t, true)

			req := &pluginapi.ResourceRequest{
				PodUid: "qos-matrix", PodNamespace: "default", PodName: "qos-matrix",
				ContainerName: "main", ContainerType: pluginapi.ContainerType_MAIN, ContainerIndex: 0,
				ResourceName:     string(v1.ResourceCPU),
				ResourceRequests: map[string]float64{string(v1.ResourceCPU): 2},
				Labels:           map[string]string{apiconsts.PodAnnotationQoSLevelKey: qos},
				Annotations:      map[string]string{apiconsts.PodAnnotationQoSLevelKey: qos},
			}
			resp, err := p.Allocate(context.Background(), req)
			require.NoError(t, err)
			assert.Empty(t,
				resp.AllocationResult.ResourceAllocation[string(v1.ResourceCPU)].AllocationResult,
				"bypassable QoS live on must omit CPUSet")

			ai := p.state.GetAllocationInfo("qos-matrix", "main")
			require.NotNil(t, ai)
			assert.Equal(t, cpusetOmissionTrue, ai.Annotations[cpuconsts.CPUSetOmissionAllowedAnnotationKey])
		})
	}
}

func TestMissingKey_LiveTrue_OmitsWithoutWriting(t *testing.T) {
	t.Parallel()
	p := newOmissionPolicy(t, true)

	seedSharedMain(t, p, "pod-missing-key", nil)

	gra, err := p.GetResourcesAllocation(context.Background(), &pluginapi.GetResourcesAllocationRequest{})
	require.NoError(t, err)
	cpuInfo := gra.PodResources["pod-missing-key"].ContainerResources["main"].ResourceAllocation[string(v1.ResourceCPU)]
	assert.Empty(t, cpuInfo.AllocationResult, "missing key + live=true must omit CPUSet")

	ai := p.state.GetAllocationInfo("pod-missing-key", "main")
	require.NotNil(t, ai)
	_, present := ai.Annotations[cpuconsts.CPUSetOmissionAllowedAnnotationKey]
	assert.False(t, present, "GRA must not backfill a missing key")
}

func TestMissingKey_LiveFalse_DemotesThenStaysFalse(t *testing.T) {
	t.Parallel()
	p := newOmissionPolicy(t, false)

	seedSharedMain(t, p, "pod-demote", nil)

	gra, err := p.GetResourcesAllocation(context.Background(), &pluginapi.GetResourcesAllocationRequest{})
	require.NoError(t, err)
	cpuInfo := gra.PodResources["pod-demote"].ContainerResources["main"].ResourceAllocation[string(v1.ResourceCPU)]
	assert.NotEmpty(t, cpuInfo.AllocationResult, "live=false must return non-empty X")

	ai := p.state.GetAllocationInfo("pod-demote", "main")
	require.NotNil(t, ai)
	assert.Equal(t, cpusetOmissionFalse, ai.Annotations[cpuconsts.CPUSetOmissionAllowedAnnotationKey])

	p.config.dynamicConfig.GetDynamicConfiguration().EnableBypassCPUSetAdjustment = true
	gra2, err := p.GetResourcesAllocation(context.Background(), &pluginapi.GetResourcesAllocationRequest{})
	require.NoError(t, err)
	cpuInfo2 := gra2.PodResources["pod-demote"].ContainerResources["main"].ResourceAllocation[string(v1.ResourceCPU)]
	assert.NotEmpty(t, cpuInfo2.AllocationResult, "after demotion, live=true must not re-enable omission")
}

func TestTrueFalseTrueCycle(t *testing.T) {
	t.Parallel()
	p := newOmissionPolicy(t, true)
	req := &pluginapi.ResourceRequest{
		PodUid: "pod-cycle", PodNamespace: "default", PodName: "pod-cycle",
		ContainerName: "main", ContainerType: pluginapi.ContainerType_MAIN, ContainerIndex: 0,
		ResourceName:     string(v1.ResourceCPU),
		ResourceRequests: map[string]float64{string(v1.ResourceCPU): 2},
		Labels:           map[string]string{apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelSharedCores},
		Annotations:      map[string]string{apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelSharedCores},
	}
	resp, err := p.Allocate(context.Background(), req)
	require.NoError(t, err)
	assert.Empty(t, resp.AllocationResult.ResourceAllocation[string(v1.ResourceCPU)].AllocationResult)

	p.config.dynamicConfig.GetDynamicConfiguration().EnableBypassCPUSetAdjustment = false
	gra, err := p.GetResourcesAllocation(context.Background(), &pluginapi.GetResourcesAllocationRequest{})
	require.NoError(t, err)
	cpuInfo := gra.PodResources["pod-cycle"].ContainerResources["main"].ResourceAllocation[string(v1.ResourceCPU)]
	assert.NotEmpty(t, cpuInfo.AllocationResult)

	ai := p.state.GetAllocationInfo("pod-cycle", "main")
	require.NotNil(t, ai)
	assert.Equal(t, cpusetOmissionFalse, ai.Annotations[cpuconsts.CPUSetOmissionAllowedAnnotationKey])

	p.config.dynamicConfig.GetDynamicConfiguration().EnableBypassCPUSetAdjustment = true
	gra2, err := p.GetResourcesAllocation(context.Background(), &pluginapi.GetResourcesAllocationRequest{})
	require.NoError(t, err)
	cpuInfo2 := gra2.PodResources["pod-cycle"].ContainerResources["main"].ResourceAllocation[string(v1.ResourceCPU)]
	assert.NotEmpty(t, cpuInfo2.AllocationResult, "after demotion, live=true must not re-enable omission")
}

func TestReuse_DoesNotUpgradeFalse(t *testing.T) {
	t.Parallel()
	p := newOmissionPolicy(t, true)

	req := &pluginapi.ResourceRequest{
		PodUid: "pod-reuse", PodNamespace: "default", PodName: "pod-reuse",
		ContainerName: "main", ContainerType: pluginapi.ContainerType_MAIN, ContainerIndex: 0,
		ResourceName:     string(v1.ResourceCPU),
		ResourceRequests: map[string]float64{string(v1.ResourceCPU): 2},
		Labels:           map[string]string{apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelSharedCores},
		Annotations: map[string]string{
			apiconsts.PodAnnotationQoSLevelKey:           apiconsts.PodAnnotationQoSLevelSharedCores,
			cpuconsts.CPUSetOmissionAllowedAnnotationKey: cpusetOmissionFalse,
		},
	}
	_, err := p.Allocate(context.Background(), req)
	require.NoError(t, err)
	ai := p.state.GetAllocationInfo("pod-reuse", "main")
	require.NotNil(t, ai)
	assert.Equal(t, cpusetOmissionFalse, ai.Annotations[cpuconsts.CPUSetOmissionAllowedAnnotationKey])

	_, err = p.Allocate(context.Background(), req)
	require.NoError(t, err)
	ai = p.state.GetAllocationInfo("pod-reuse", "main")
	require.NotNil(t, ai)
	assert.Equal(t, cpusetOmissionFalse, ai.Annotations[cpuconsts.CPUSetOmissionAllowedAnnotationKey],
		"reuse path must not upgrade explicit false to true")
}

// TestReuseFastReturn_OmissionConsistent pins the fast-return branch: with a
// persisted "true" and live on, reuse both omits in the response and leaves the
// persisted value "true" (state and response decision agree after commit).
func TestReuseFastReturn_OmissionConsistent(t *testing.T) {
	t.Parallel()
	p := newOmissionPolicy(t, true)

	req := &pluginapi.ResourceRequest{
		PodUid: "pod-consistent", PodNamespace: "default", PodName: "pod-consistent",
		ContainerName: "main", ContainerType: pluginapi.ContainerType_MAIN, ContainerIndex: 0,
		ResourceName:     string(v1.ResourceCPU),
		ResourceRequests: map[string]float64{string(v1.ResourceCPU): 2},
		Labels:           map[string]string{apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelSharedCores},
		Annotations:      map[string]string{apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelSharedCores},
	}
	_, err := p.Allocate(context.Background(), req)
	require.NoError(t, err)

	resp, err := p.Allocate(context.Background(), req)
	require.NoError(t, err)
	assert.Empty(t,
		resp.AllocationResult.ResourceAllocation[string(v1.ResourceCPU)].AllocationResult,
		"fast-return with persisted true must omit CPUSet")

	ai := p.state.GetAllocationInfo("pod-consistent", "main")
	require.NotNil(t, ai)
	assert.Equal(t, cpusetOmissionTrue, ai.Annotations[cpuconsts.CPUSetOmissionAllowedAnnotationKey],
		"fast-return must not demote a persisted true")
}

// TestExistingPodUntouchedByNewAdmission pins the previous "no backfill on GRA"
// and "first Allocate only writes its own container" invariants: a pre-existing
// pod must be untouched by both a GRA pass and a brand-new admission.
func TestExistingPodUntouchedByNewAdmission(t *testing.T) {
	t.Parallel()
	p := newOmissionPolicy(t, true)

	seedSharedMain(t, p, "existing-pod", nil)

	_, err := p.GetResourcesAllocation(context.Background(), &pluginapi.GetResourcesAllocationRequest{})
	require.NoError(t, err)
	ai := p.state.GetAllocationInfo("existing-pod", "main")
	require.NotNil(t, ai)
	_, present := ai.Annotations[cpuconsts.CPUSetOmissionAllowedAnnotationKey]
	assert.False(t, present, "GRA must not backfill a missing key when live=true")

	req := &pluginapi.ResourceRequest{
		PodUid: "new-pod", PodNamespace: "default", PodName: "new-pod",
		ContainerName: "main", ContainerType: pluginapi.ContainerType_MAIN, ContainerIndex: 0,
		ResourceName:     string(v1.ResourceCPU),
		ResourceRequests: map[string]float64{string(v1.ResourceCPU): 2},
		Labels:           map[string]string{apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelSharedCores},
		Annotations:      map[string]string{apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelSharedCores},
	}
	_, err = p.Allocate(context.Background(), req)
	require.NoError(t, err)

	ai = p.state.GetAllocationInfo("existing-pod", "main")
	require.NotNil(t, ai)
	_, present = ai.Annotations[cpuconsts.CPUSetOmissionAllowedAnnotationKey]
	assert.False(t, present, "first Allocate of a new pod must not touch existing entries")

	newAi := p.state.GetAllocationInfo("new-pod", "main")
	require.NotNil(t, newAi)
	assert.Equal(t, cpusetOmissionTrue, newAi.Annotations[cpuconsts.CPUSetOmissionAllowedAnnotationKey])
}

func TestGRA_DemotesSidecarToo(t *testing.T) {
	t.Parallel()
	p := newOmissionPolicy(t, false)

	seedSharedMain(t, p, "sidecar-pod", strptr(cpusetOmissionTrue))

	sidecarCpus := machine.NewCPUSet(4)
	sidecarInfo := &state.AllocationInfo{
		AllocationMeta: commonstate.AllocationMeta{
			PodUid: "sidecar-pod", PodName: "sidecar-pod", ContainerName: "sidecar",
			ContainerType: pluginapi.ContainerType_SIDECAR.String(),
			QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
		},
		AllocationResult:         sidecarCpus,
		OriginalAllocationResult: sidecarCpus.Clone(),
		TopologyAwareAssignments: map[int]machine.CPUSet{0: sidecarCpus},
		RequestQuantity:          float64(sidecarCpus.Size()),
	}
	sidecarInfo.Annotations = map[string]string{cpuconsts.CPUSetOmissionAllowedAnnotationKey: cpusetOmissionTrue}
	require.NoError(t, p.state.SetAllocationInfo("sidecar-pod", "sidecar", sidecarInfo, false))

	_, err := p.GetResourcesAllocation(context.Background(), &pluginapi.GetResourcesAllocationRequest{})
	require.NoError(t, err)

	mainAi := p.state.GetAllocationInfo("sidecar-pod", "main")
	require.NotNil(t, mainAi)
	assert.Equal(t, cpusetOmissionFalse, mainAi.Annotations[cpuconsts.CPUSetOmissionAllowedAnnotationKey])

	sidecarAi := p.state.GetAllocationInfo("sidecar-pod", "sidecar")
	require.NotNil(t, sidecarAi)
	assert.Equal(t, cpusetOmissionFalse, sidecarAi.Annotations[cpuconsts.CPUSetOmissionAllowedAnnotationKey])
}

func TestDefaultKeepsAllowKey_FiltersRandom(t *testing.T) {
	t.Parallel()
	p := newOmissionPolicy(t, false)

	const randomAnnoKey = "random.example.com/not-a-kept-key"
	req := &pluginapi.ResourceRequest{
		PodUid: "pod-filter", PodNamespace: "default", PodName: "pod-filter",
		ContainerName: "main", ContainerType: pluginapi.ContainerType_MAIN, ContainerIndex: 0,
		ResourceName:     string(v1.ResourceCPU),
		ResourceRequests: map[string]float64{string(v1.ResourceCPU): 2},
		Labels:           map[string]string{apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelSharedCores},
		Annotations: map[string]string{
			apiconsts.PodAnnotationQoSLevelKey:           apiconsts.PodAnnotationQoSLevelSharedCores,
			cpuconsts.CPUSetOmissionAllowedAnnotationKey: cpusetOmissionTrue,
			randomAnnoKey: "should-be-stripped",
		},
	}
	_, err := p.Allocate(context.Background(), req)
	require.NoError(t, err)

	ai := p.state.GetAllocationInfo("pod-filter", "main")
	require.NotNil(t, ai)
	require.NotNil(t, ai.Annotations)
	assert.Contains(t, ai.Annotations, cpuconsts.CPUSetOmissionAllowedAnnotationKey)
	assert.NotContains(t, ai.Annotations, randomAnnoKey)
}

// --- GRA batch demotion: all pods demoted in one call ---

func TestGRA_BatchDemotion_AllPodsDemoted(t *testing.T) {
	t.Parallel()
	p := newOmissionPolicy(t, false)

	// Seed 3 pods with missing key, live=false, non-empty CPUSet
	for i := 0; i < 3; i++ {
		seedSharedMain(t, p, fmt.Sprintf("batch-pod-%d", i), nil)
	}

	_, err := p.GetResourcesAllocation(context.Background(), &pluginapi.GetResourcesAllocationRequest{})
	require.NoError(t, err)

	// All 3 should now be "false"
	for i := 0; i < 3; i++ {
		podUID := fmt.Sprintf("batch-pod-%d", i)
		ai := p.state.GetAllocationInfo(podUID, "main")
		require.NotNil(t, ai)
		assert.Equal(t, cpusetOmissionFalse, ai.Annotations[cpuconsts.CPUSetOmissionAllowedAnnotationKey],
			"pod %d should be demoted to false", i)
	}

	// Second GRA call: already false, should stay false
	_, err = p.GetResourcesAllocation(context.Background(), &pluginapi.GetResourcesAllocationRequest{})
	require.NoError(t, err)
	for i := 0; i < 3; i++ {
		podUID := fmt.Sprintf("batch-pod-%d", i)
		ai := p.state.GetAllocationInfo(podUID, "main")
		require.NotNil(t, ai)
		assert.Equal(t, cpusetOmissionFalse, ai.Annotations[cpuconsts.CPUSetOmissionAllowedAnnotationKey],
			"pod %d should remain false after second GRA", i)
	}
}

// --- live=true with invalid value ---

func TestInvalidValue_LiveTrue_DemotesToFalse(t *testing.T) {
	t.Parallel()
	p := newOmissionPolicy(t, true)

	seedSharedMain(t, p, "invalid-pod", strptr("maybe"))

	gra, err := p.GetResourcesAllocation(context.Background(), &pluginapi.GetResourcesAllocationRequest{})
	require.NoError(t, err)
	cpuInfo := gra.PodResources["invalid-pod"].ContainerResources["main"].ResourceAllocation[string(v1.ResourceCPU)]
	assert.NotEmpty(t, cpuInfo.AllocationResult, "invalid value must return X even with live=true")

	ai := p.state.GetAllocationInfo("invalid-pod", "main")
	require.NotNil(t, ai)
	assert.Equal(t, cpusetOmissionFalse, ai.Annotations[cpuconsts.CPUSetOmissionAllowedAnnotationKey],
		"invalid value must be demoted to false")
}

// --- Resize doesn't upgrade test ---

func TestResize_DoesNotUpgradeFalse(t *testing.T) {
	t.Parallel()
	p := newOmissionPolicy(t, true)

	// First allocate with explicit false
	req := &pluginapi.ResourceRequest{
		PodUid: "resize-pod", PodNamespace: "default", PodName: "resize-pod",
		ContainerName: "main", ContainerType: pluginapi.ContainerType_MAIN, ContainerIndex: 0,
		ResourceName:     string(v1.ResourceCPU),
		ResourceRequests: map[string]float64{string(v1.ResourceCPU): 2},
		Labels:           map[string]string{apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelSharedCores},
		Annotations: map[string]string{
			apiconsts.PodAnnotationQoSLevelKey:           apiconsts.PodAnnotationQoSLevelSharedCores,
			cpuconsts.CPUSetOmissionAllowedAnnotationKey: cpusetOmissionFalse,
		},
	}
	_, err := p.Allocate(context.Background(), req)
	require.NoError(t, err)
	ai := p.state.GetAllocationInfo("resize-pod", "main")
	require.NotNil(t, ai)
	assert.Equal(t, cpusetOmissionFalse, ai.Annotations[cpuconsts.CPUSetOmissionAllowedAnnotationKey])

	// Re-allocate with request carrying "true" (should not upgrade)
	req2 := &pluginapi.ResourceRequest{
		PodUid: "resize-pod", PodNamespace: "default", PodName: "resize-pod",
		ContainerName: "main", ContainerType: pluginapi.ContainerType_MAIN, ContainerIndex: 0,
		ResourceName:     string(v1.ResourceCPU),
		ResourceRequests: map[string]float64{string(v1.ResourceCPU): 3},
		Labels:           map[string]string{apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelSharedCores},
		Annotations: map[string]string{
			apiconsts.PodAnnotationQoSLevelKey:           apiconsts.PodAnnotationQoSLevelSharedCores,
			cpuconsts.CPUSetOmissionAllowedAnnotationKey: cpusetOmissionTrue,
		},
	}
	_, err = p.Allocate(context.Background(), req2)
	require.NoError(t, err)
	ai = p.state.GetAllocationInfo("resize-pod", "main")
	require.NotNil(t, ai)
	assert.Equal(t, cpusetOmissionFalse, ai.Annotations[cpuconsts.CPUSetOmissionAllowedAnnotationKey],
		"resize/reallocate must not upgrade false to true")
}

// TestReclaimedInplaceResize_TrueToFalse_PersistsFalse pins the reclaimed
// inplace-resize branch: a persisted "true" with live off must (a) return a
// non-empty CPUSet and (b) durably persist the demoted "false" — the mark must
// be folded into the same commit, not left on a discarded local clone.
func TestReclaimedInplaceResize_TrueToFalse_PersistsFalse(t *testing.T) {
	t.Parallel()
	p := newOmissionPolicy(t, false)

	reclaimCpus := machine.NewCPUSet(4, 5)
	info := &state.AllocationInfo{
		AllocationMeta: commonstate.AllocationMeta{
			PodUid:        "reclaim-resize-pod",
			PodNamespace:  "default",
			PodName:       "reclaim-resize-pod",
			ContainerName: "main",
			ContainerType: pluginapi.ContainerType_MAIN.String(),
			QoSLevel:      apiconsts.PodAnnotationQoSLevelReclaimedCores,
			Annotations:   map[string]string{cpuconsts.CPUSetOmissionAllowedAnnotationKey: cpusetOmissionTrue},
		},
		AllocationResult:         reclaimCpus,
		OriginalAllocationResult: reclaimCpus.Clone(),
		TopologyAwareAssignments: map[int]machine.CPUSet{0: reclaimCpus},
		RequestQuantity:          2,
		InitTimestamp:            "2026-01-01 00:00:00.000000000 +0800 CST",
	}
	require.NoError(t, p.state.SetAllocationInfo("reclaim-resize-pod", "main", info, false))

	resizeReq := &pluginapi.ResourceRequest{
		PodUid: "reclaim-resize-pod", PodNamespace: "default", PodName: "reclaim-resize-pod",
		ContainerName: "main", ContainerType: pluginapi.ContainerType_MAIN, ContainerIndex: 0,
		ResourceName:     string(v1.ResourceCPU),
		ResourceRequests: map[string]float64{string(v1.ResourceCPU): 3},
		Labels:           map[string]string{apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelReclaimedCores},
		Annotations: map[string]string{
			apiconsts.PodAnnotationQoSLevelKey:              apiconsts.PodAnnotationQoSLevelReclaimedCores,
			apiconsts.PodAnnotationInplaceUpdateResizingKey: "true",
		},
	}
	resp, err := p.reclaimedCoresAllocationHandler(context.Background(), resizeReq, false)
	require.NoError(t, err)
	cpuInfo := resp.AllocationResult.ResourceAllocation[string(v1.ResourceCPU)]
	assert.NotEmpty(t, cpuInfo.AllocationResult, "live=false must return non-empty CPUSet")

	ai := p.state.GetAllocationInfo("reclaim-resize-pod", "main")
	require.NotNil(t, ai)
	assert.Equal(t, cpusetOmissionFalse, ai.Annotations[cpuconsts.CPUSetOmissionAllowedAnnotationKey],
		"reclaimed inplace resize must durably persist demoted false")
}

// --- Sidecar request leak test ---

func TestSidecar_RequestTrue_DoesNotLeakFromMainFalse(t *testing.T) {
	t.Parallel()
	p := newOmissionPolicy(t, true)

	// Set up main with "false"
	seedSharedMain(t, p, "sidecar-leak-pod", strptr(cpusetOmissionFalse))

	// Sidecar request carries "true" annotation
	sidecarReq := &pluginapi.ResourceRequest{
		PodUid: "sidecar-leak-pod", PodNamespace: "default", PodName: "sidecar-leak-pod",
		ContainerName: "sidecar", ContainerType: pluginapi.ContainerType_SIDECAR, ContainerIndex: 1,
		ResourceName:     string(v1.ResourceCPU),
		ResourceRequests: map[string]float64{string(v1.ResourceCPU): 1},
		Labels:           map[string]string{apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelSharedCores},
		Annotations: map[string]string{
			apiconsts.PodAnnotationQoSLevelKey:           apiconsts.PodAnnotationQoSLevelSharedCores,
			cpuconsts.CPUSetOmissionAllowedAnnotationKey: cpusetOmissionTrue,
		},
	}
	_, err := p.Allocate(context.Background(), sidecarReq)
	require.NoError(t, err)

	sidecarAi := p.state.GetAllocationInfo("sidecar-leak-pod", "sidecar")
	require.NotNil(t, sidecarAi)
	assert.Equal(t, cpusetOmissionFalse, sidecarAi.Annotations[cpuconsts.CPUSetOmissionAllowedAnnotationKey],
		"sidecar must inherit false from main, not leak true from request")
}

// TestSidecarFalse_MainTrue_MonotonicAcrossSyncAndReadmit pins the one-way
// invariant for sidecars even when main says "true" (live on, so the sidecar
// mark must not do the work): a sidecar already stamped "false" must keep
// "false" across (a) a real prepare/commit partition cycle that syncs
// sidecars from mains and (b) a fresh sidecar re-admit.
func TestSidecarFalse_MainTrue_MonotonicAcrossSyncAndReadmit(t *testing.T) {
	t.Parallel()
	p := newOmissionPolicy(t, true)

	seedSharedMainEntry(t, p, "mono-pod", strptr(cpusetOmissionTrue), nil)

	sidecarCpus := sharedPoolCPUSet.Clone()
	sidecarInfo := &state.AllocationInfo{
		AllocationMeta: commonstate.AllocationMeta{
			PodUid:        "mono-pod",
			PodNamespace:  "default",
			PodName:       "mono-pod",
			ContainerName: "sidecar",
			ContainerType: pluginapi.ContainerType_SIDECAR.String(),
			QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
			OwnerPoolName: commonstate.PoolNameShare,
			Annotations:   map[string]string{cpuconsts.CPUSetOmissionAllowedAnnotationKey: cpusetOmissionFalse},
		},
		AllocationResult:         sidecarCpus,
		OriginalAllocationResult: sidecarCpus.Clone(),
		TopologyAwareAssignments: map[int]machine.CPUSet{0: sidecarCpus},
		RequestQuantity:          float64(sidecarCpus.Size()),
	}
	require.NoError(t, p.state.SetAllocationInfo("mono-pod", "sidecar", sidecarInfo, false))

	// (a) Real prepare/commit partition cycle: sidecar sync must not overwrite
	// the sidecar's persisted "false" with the main's "true".
	ensureSharePoolEntry(t, p)
	candidate := p.state.GetPodEntries()
	prepared, err := p.preparePendingCPUPartition(pendingCPUPartition{
		expectedRevision: p.state.GetRevision(),
		entries:          candidate,
		dynamicConfig:    p.config.dynamicConfig.GetDynamicConfiguration(),
		persist:          false,
		source:           "sidecar-monotonic-omission",
		validate:         func(state.PodEntries, state.NUMANodeMap, bool, bool) error { return nil },
	})
	require.NoError(t, err)
	require.NoError(t, p.commitPreparedCPUPartition(prepared))

	sideAi := p.state.GetAllocationInfo("mono-pod", "sidecar")
	require.NotNil(t, sideAi)
	assert.Equal(t, cpusetOmissionFalse, sideAi.Annotations[cpuconsts.CPUSetOmissionAllowedAnnotationKey],
		"sidecar false must survive partition sync from main true")

	// (b) Fresh sidecar re-admit carrying a request "true": persisted "false"
	// must still not be upgraded.
	sidecarReq := &pluginapi.ResourceRequest{
		PodUid: "mono-pod", PodNamespace: "default", PodName: "mono-pod",
		ContainerName: "sidecar", ContainerType: pluginapi.ContainerType_SIDECAR, ContainerIndex: 1,
		ResourceName:     string(v1.ResourceCPU),
		ResourceRequests: map[string]float64{string(v1.ResourceCPU): 1},
		Labels:           map[string]string{apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelSharedCores},
		Annotations: map[string]string{
			apiconsts.PodAnnotationQoSLevelKey:           apiconsts.PodAnnotationQoSLevelSharedCores,
			cpuconsts.CPUSetOmissionAllowedAnnotationKey: cpusetOmissionTrue,
		},
	}
	_, err = p.Allocate(context.Background(), sidecarReq)
	require.NoError(t, err)

	sideAi = p.state.GetAllocationInfo("mono-pod", "sidecar")
	require.NotNil(t, sideAi)
	assert.Equal(t, cpusetOmissionFalse, sideAi.Annotations[cpuconsts.CPUSetOmissionAllowedAnnotationKey],
		"sidecar re-admit must not upgrade persisted false")
}

// TestDemoteCommit_MachineStateUnchanged pins that a metadata-only single-
// container demote does not recompute pools/ownership or touch siblings: machine
// state topology is unchanged, and revision advances only for a real transition.
func TestDemoteCommit_MachineStateUnchanged(t *testing.T) {
	t.Parallel()
	p := newOmissionPolicy(t, false)
	seedSharedMain(t, p, "ms-pod", nil)
	seedSidecarWith(t, p, "ms-pod", machine.NewCPUSet(4), strptr(cpusetOmissionTrue), nil)

	machineBefore := p.state.GetMachineState()
	revBefore := p.state.GetRevision()

	require.NoError(t, p.commitCPUSetExposureForContainer("ms-pod", "main", false))

	ai := p.state.GetAllocationInfo("ms-pod", "main")
	require.NotNil(t, ai)
	assert.Equal(t, cpusetOmissionFalse, ai.Annotations[cpuconsts.CPUSetOmissionAllowedAnnotationKey])
	sidecar := p.state.GetAllocationInfo("ms-pod", "sidecar")
	require.NotNil(t, sidecar)
	assert.Equal(t, cpusetOmissionTrue, sidecar.Annotations[cpuconsts.CPUSetOmissionAllowedAnnotationKey],
		"Allocate reuse demotion must not mark an unreturned sibling as exposed")
	assert.Greater(t, p.state.GetRevision(), revBefore, "actual demote must advance revision")
	assert.Equal(t, machineBefore, p.state.GetMachineState(),
		"metadata-only demote commit must not recompute machine state")

	// Second call with nothing to demote: no commit, revision must not advance.
	revAfterNoop := p.state.GetRevision()
	require.NoError(t, p.commitCPUSetExposureForContainer("ms-pod", "main", false))
	assert.Equal(t, revAfterNoop, p.state.GetRevision(), "no-op demote must not commit")
}

func TestCommitCPUSetExposureForContainer_MissingTargetFailsClosed(t *testing.T) {
	t.Parallel()
	p := newOmissionPolicy(t, false)

	err := p.commitCPUSetExposureForContainer("missing-pod", "main", false)
	require.ErrorContains(t, err, "missing or is a pool entry")

	seedSharedMain(t, p, "present-pod", nil)
	err = p.commitCPUSetExposureForContainer("present-pod", "missing-container", false)
	require.ErrorContains(t, err, "container \"present-pod\"/\"missing-container\" is missing")
}

// --- tracked state wrapper for counting commit calls ---

type bypassTrackingState struct {
	state.State
	commitIfRevisionCalls int
	bumpBeforeCommit      bool
}

func (s *bypassTrackingState) CommitAdvisorStateIfRevision(
	expectedRevision uint64,
	podEntries state.PodEntries,
	machineState state.NUMANodeMap,
	allowOverlap, disableDedicatedOverlap, persist bool,
	permits ...*state.WritePermit,
) error {
	s.commitIfRevisionCalls++
	if s.bumpBeforeCommit {
		// Advance revision by doing an unrelated write before the CAS check.
		// transientState releases the lock between calls, so no deadlock.
		s.State.SetAllocationInfo("bump-pod", "bump-container", &state.AllocationInfo{
			AllocationMeta: commonstate.AllocationMeta{PodUid: "bump-pod", ContainerName: "bump-container"},
		}, false)
	}
	return s.State.CommitAdvisorStateIfRevision(
		expectedRevision, podEntries, machineState, allowOverlap, disableDedicatedOverlap, persist, permits...)
}

// --- A) Persistence failure via SetWritePermit ---

func TestGRA_PersistenceFailure_ReturnsErrorAndNilResponse(t *testing.T) {
	t.Parallel()
	p := newOmissionPolicy(t, false)

	seedSharedMain(t, p, "fail-pod", nil)

	// Inject write failure on CommitAdvisorStateIfRevision
	injectedErr := fmt.Errorf("injected checkpoint failure")
	p.state.SetWritePermit(func(rev uint64, op string, _ *state.WritePermit) error {
		if op == "CommitAdvisorStateIfRevision" {
			return injectedErr
		}
		return nil
	})

	resp, err := p.GetResourcesAllocation(context.Background(), &pluginapi.GetResourcesAllocationRequest{})
	require.Error(t, err)
	assert.Nil(t, resp, "on persistence failure, response must be nil")

	// State should still have missing key (not demoted)
	ai := p.state.GetAllocationInfo("fail-pod", "main")
	require.NotNil(t, ai)
	_, present := ai.Annotations[cpuconsts.CPUSetOmissionAllowedAnnotationKey]
	assert.False(t, present, "on persistence failure, annotation should not be written")
}

// --- E) GRA batch: exactly 1 CommitAdvisorStateIfRevision call ---

func TestGRA_BatchDemotion_OneCommitCall(t *testing.T) {
	t.Parallel()
	p := newOmissionPolicy(t, false)

	// Wrap state with tracking wrapper
	tracked := &bypassTrackingState{State: p.state}
	p.state = tracked

	// Seed 3 pods with missing key
	for i := 0; i < 3; i++ {
		seedSharedMain(t, p, fmt.Sprintf("commit-count-pod-%d", i), nil)
	}

	tracked.commitIfRevisionCalls = 0
	_, err := p.GetResourcesAllocation(context.Background(), &pluginapi.GetResourcesAllocationRequest{})
	require.NoError(t, err)

	// Should have exactly 1 CommitAdvisorStateIfRevision call for the batch demotion
	assert.Equal(t, 1, tracked.commitIfRevisionCalls,
		"batch demotion should make exactly 1 CommitAdvisorStateIfRevision call")

	// Second GRA: already false, 0 new calls
	tracked.commitIfRevisionCalls = 0
	_, err = p.GetResourcesAllocation(context.Background(), &pluginapi.GetResourcesAllocationRequest{})
	require.NoError(t, err)
	assert.Equal(t, 0, tracked.commitIfRevisionCalls,
		"second GRA with nothing to demote should make 0 CommitAdvisorStateIfRevision calls")
}

// --- 1) reuse fast-return persistence failure ---

func TestReuseFastReturn_PersistenceFailure_ReturnsError(t *testing.T) {
	t.Parallel()
	p := newOmissionPolicy(t, true)

	req := &pluginapi.ResourceRequest{
		PodUid: "reuse-fail-pod", PodNamespace: "default", PodName: "reuse-fail-pod",
		ContainerName: "main", ContainerType: pluginapi.ContainerType_MAIN, ContainerIndex: 0,
		ResourceName:     string(v1.ResourceCPU),
		ResourceRequests: map[string]float64{string(v1.ResourceCPU): 2},
		Labels:           map[string]string{apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelSharedCores},
		Annotations:      map[string]string{apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelSharedCores},
	}
	_, err := p.Allocate(context.Background(), req)
	require.NoError(t, err)
	ai := p.state.GetAllocationInfo("reuse-fail-pod", "main")
	require.NotNil(t, ai)
	assert.Equal(t, cpusetOmissionTrue, ai.Annotations[cpuconsts.CPUSetOmissionAllowedAnnotationKey])

	// Switch live to false, inject write failure
	p.config.dynamicConfig.GetDynamicConfiguration().EnableBypassCPUSetAdjustment = false
	injectedErr := fmt.Errorf("injected commit failure")
	p.state.SetWritePermit(func(rev uint64, op string, _ *state.WritePermit) error {
		if op == "CommitAdvisorStateIfRevision" {
			return injectedErr
		}
		return nil
	})

	// Re-allocate same request: fast-return path will try to demote
	resp, err := p.Allocate(context.Background(), req)
	require.Error(t, err)
	assert.Nil(t, resp, "on persistence failure, response must be nil")

	// State should still have "true" (not demoted)
	ai = p.state.GetAllocationInfo("reuse-fail-pod", "main")
	require.NotNil(t, ai)
	assert.Equal(t, cpusetOmissionTrue, ai.Annotations[cpuconsts.CPUSetOmissionAllowedAnnotationKey],
		"on persistence failure, annotation should remain unchanged")
}

// --- 2) revision conflict ---

func TestGRA_RevisionConflict_ReturnsStaleRevision(t *testing.T) {
	t.Parallel()
	p := newOmissionPolicy(t, false)

	seedSharedMain(t, p, "conflict-pod", nil)

	// Wrap state with revision bump injection
	tracked := &bypassTrackingState{State: p.state, bumpBeforeCommit: true}
	p.state = tracked

	resp, err := p.GetResourcesAllocation(context.Background(), &pluginapi.GetResourcesAllocationRequest{})
	require.Error(t, err)
	assert.True(t, errors.Is(err, state.ErrStaleStateRevision),
		"expected ErrStaleStateRevision, got: %v", err)
	assert.Nil(t, resp, "on revision conflict, response must be nil")
}

// --- 3) dedicated NUMA first Allocate e2e ---

func TestDedicatedNUMA_FirstAllocate_WritesFalse(t *testing.T) {
	t.Parallel()
	topology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithInitialization(topology, t.TempDir())
	require.NoError(t, err)
	p.config.dynamicConfig.GetDynamicConfiguration().EnableBypassCPUSetAdjustment = true

	req := &pluginapi.ResourceRequest{
		PodUid:         "dedicated-numa-pod",
		PodNamespace:   "default",
		PodName:        "dedicated-numa-pod",
		ContainerName:  "main",
		ContainerType:  pluginapi.ContainerType_MAIN,
		ContainerIndex: 0,
		ResourceName:   string(v1.ResourceCPU),
		Hint: &pluginapi.TopologyHint{
			Nodes:     []uint64{0},
			Preferred: true,
		},
		ResourceRequests: map[string]float64{string(v1.ResourceCPU): 2},
		Annotations: map[string]string{
			apiconsts.PodAnnotationQoSLevelKey:          apiconsts.PodAnnotationQoSLevelDedicatedCores,
			apiconsts.PodAnnotationMemoryEnhancementKey: `{"numa_binding": "true", "numa_exclusive": "true"}`,
		},
		Labels: map[string]string{
			apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelDedicatedCores,
		},
	}
	resp, err := p.Allocate(context.Background(), req)
	require.NoError(t, err)
	cpuInfo := resp.AllocationResult.ResourceAllocation[string(v1.ResourceCPU)]
	assert.NotEmpty(t, cpuInfo.AllocationResult, "dedicated must always return non-empty CPUSet")

	ai := p.state.GetAllocationInfo("dedicated-numa-pod", "main")
	require.NotNil(t, ai)
	assert.Equal(t, cpusetOmissionFalse, ai.Annotations[cpuconsts.CPUSetOmissionAllowedAnnotationKey],
		"dedicated must write false on first Allocate even with live=true")
}

// --- 4) real inplace resize ---

func TestInplaceResize_DoesNotUpgradeFalse(t *testing.T) {
	t.Parallel()
	topology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithInitialization(topology, t.TempDir())
	require.NoError(t, err)
	// Add our annotation key to kept keys so request annotation passes through
	p.config.podAnnotationKeptKeys = []string{
		apiconsts.PodAnnotationMemoryEnhancementNumaBinding,
		apiconsts.PodAnnotationInplaceUpdateResizingKey,
		cpuconsts.CPUSetOmissionAllowedAnnotationKey,
	}
	p.config.transitionPeriod = 10 * time.Millisecond

	// First Allocate with live=false: writes "false"
	p.config.dynamicConfig.GetDynamicConfiguration().EnableBypassCPUSetAdjustment = false
	req := &pluginapi.ResourceRequest{
		PodUid: "resize-nu-pod", PodNamespace: "default", PodName: "resize-nu-pod",
		ContainerName: "main", ContainerType: pluginapi.ContainerType_MAIN, ContainerIndex: 0,
		ResourceName:     string(v1.ResourceCPU),
		ResourceRequests: map[string]float64{string(v1.ResourceCPU): 2},
		Labels:           map[string]string{apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelSharedCores},
		Annotations: map[string]string{
			apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelSharedCores,
		},
	}
	_, err = p.Allocate(context.Background(), req)
	require.NoError(t, err)
	// Wait for ramp-up to finish, then call GRA to trigger ramp-up cleanup
	time.Sleep(20 * time.Millisecond)
	_, err = p.GetResourcesAllocation(context.Background(), &pluginapi.GetResourcesAllocationRequest{})
	require.NoError(t, err)
	ai := p.state.GetAllocationInfo("resize-nu-pod", "main")
	require.NotNil(t, ai)
	assert.Equal(t, cpusetOmissionFalse, ai.Annotations[cpuconsts.CPUSetOmissionAllowedAnnotationKey])

	// Switch live=true, resize request carries "true"
	p.config.dynamicConfig.GetDynamicConfiguration().EnableBypassCPUSetAdjustment = true
	resizeReq := &pluginapi.ResourceRequest{
		PodUid: "resize-nu-pod", PodNamespace: "default", PodName: "resize-nu-pod",
		ContainerName: "main", ContainerType: pluginapi.ContainerType_MAIN, ContainerIndex: 0,
		ResourceName:     string(v1.ResourceCPU),
		ResourceRequests: map[string]float64{string(v1.ResourceCPU): 3},
		Labels:           map[string]string{apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelSharedCores},
		Annotations: map[string]string{
			apiconsts.PodAnnotationQoSLevelKey:              apiconsts.PodAnnotationQoSLevelSharedCores,
			apiconsts.PodAnnotationInplaceUpdateResizingKey: "true",
			cpuconsts.CPUSetOmissionAllowedAnnotationKey:    cpusetOmissionTrue,
		},
	}
	resp, err := p.Allocate(context.Background(), resizeReq)
	require.NoError(t, err)
	cpuInfo := resp.AllocationResult.ResourceAllocation[string(v1.ResourceCPU)]
	assert.NotEmpty(t, cpuInfo.AllocationResult, "resize must return non-empty CPUSet")

	ai = p.state.GetAllocationInfo("resize-nu-pod", "main")
	require.NotNil(t, ai)
	assert.Equal(t, cpusetOmissionFalse, ai.Annotations[cpuconsts.CPUSetOmissionAllowedAnnotationKey],
		"resize must not upgrade false to true")
}

// --- H1 monotonicity: non-resize re-admit must not upgrade a persisted "false" ---

// newSharedNUMARequest builds a shared-cores NUMA-binding main request.
func newSharedNUMARequest(podUID string, qosAnno map[string]string) *pluginapi.ResourceRequest {
	req := &pluginapi.ResourceRequest{
		PodUid:           podUID,
		PodNamespace:     "default",
		PodName:          podUID,
		ContainerName:    "main",
		ContainerType:    pluginapi.ContainerType_MAIN,
		ContainerIndex:   0,
		ResourceName:     string(v1.ResourceCPU),
		ResourceRequests: map[string]float64{string(v1.ResourceCPU): 2},
		Labels: map[string]string{
			apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelSharedCores,
		},
		Annotations: map[string]string{
			apiconsts.PodAnnotationQoSLevelKey:                  apiconsts.PodAnnotationQoSLevelSharedCores,
			apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
		},
		Hint: &pluginapi.TopologyHint{Nodes: []uint64{0}, Preferred: true},
	}
	for k, v := range qosAnno {
		req.Annotations[k] = v
	}
	return req
}

// TestSharedNUMAReAdmit_DoesNotUpgradeFalse exercises the non-resize else branch of
// allocateSharedNumaBindingCPUs: an existing SNB container re-admitted with a request
// carrying "true" while live=true must inherit the persisted "false", not re-init to "true".
func TestSharedNUMAReAdmit_DoesNotUpgradeFalse(t *testing.T) {
	t.Parallel()
	topology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithInitialization(topology, t.TempDir())
	require.NoError(t, err)
	p.config.dynamicConfig.GetDynamicConfiguration().EnableBypassCPUSetAdjustment = true

	// Seed an existing SNB main container already demoted to "false".
	origin := &state.AllocationInfo{
		AllocationMeta: commonstate.AllocationMeta{
			PodUid:        "snb-rereadmit",
			PodNamespace:  "default",
			PodName:       "snb-rereadmit",
			ContainerName: "main",
			QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
			Annotations: map[string]string{
				apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
				cpuconsts.CPUSetOmissionAllowedAnnotationKey:        cpusetOmissionFalse,
			},
		},
		RequestQuantity:          2,
		AllocationResult:         machine.NewCPUSet(0, 1),
		OriginalAllocationResult: machine.NewCPUSet(0, 1),
		TopologyAwareAssignments: map[int]machine.CPUSet{0: machine.NewCPUSet(0, 1)},
	}
	origin.SetSpecifiedNUMABindingNUMAID([]uint64{0})
	require.NoError(t, p.state.SetAllocationInfo("snb-rereadmit", "main", origin, false))

	// Re-admit (non-resize) carrying request annotation "true". live=true.
	req := newSharedNUMARequest("snb-rereadmit", map[string]string{
		cpuconsts.CPUSetOmissionAllowedAnnotationKey: cpusetOmissionTrue,
	})
	_, err = p.allocateSharedNumaBindingCPUs(context.Background(), req, req.Hint, false, true)
	require.NoError(t, err)

	got := p.state.GetAllocationInfo("snb-rereadmit", "main")
	require.NotNil(t, got)
	assert.Equal(t, cpusetOmissionFalse, got.Annotations[cpuconsts.CPUSetOmissionAllowedAnnotationKey],
		"non-resize re-admit of an existing SNB container must inherit persisted false, not upgrade to true")
}

// TestSharedNUMAReAdmit_NewContainerWritesTrue covers the origin==nil branch: a truly
// new SNB container records the initial omission decision.
func TestSharedNUMAReAdmit_NewContainerWritesTrue(t *testing.T) {
	t.Parallel()
	topology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithInitialization(topology, t.TempDir())
	require.NoError(t, err)
	p.config.dynamicConfig.GetDynamicConfiguration().EnableBypassCPUSetAdjustment = true

	req := newSharedNUMARequest("snb-brandnew", nil)
	_, err = p.allocateSharedNumaBindingCPUs(context.Background(), req, req.Hint, false, true)
	require.NoError(t, err)

	got := p.state.GetAllocationInfo("snb-brandnew", "main")
	require.NotNil(t, got)
	assert.Equal(t, cpusetOmissionTrue, got.Annotations[cpuconsts.CPUSetOmissionAllowedAnnotationKey],
		"brand-new SNB container with live=true must record true")
}

// TestSystemReAdmit_DoesNotUpgradeFalse exercises systemCoresAllocationHandler: an
// existing system main container re-admitted with a "true" request while live=true
// must inherit the persisted "false".
func TestSystemReAdmit_DoesNotUpgradeFalse(t *testing.T) {
	t.Parallel()
	topology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithInitialization(topology, t.TempDir())
	require.NoError(t, err)
	p.config.dynamicConfig.GetDynamicConfiguration().EnableBypassCPUSetAdjustment = true

	// Seed an existing system main container already demoted to "false".
	origin := &state.AllocationInfo{
		AllocationMeta: commonstate.AllocationMeta{
			PodUid:        "sys-rereadmit",
			PodNamespace:  "default",
			PodName:       "sys-rereadmit",
			ContainerName: "main",
			QoSLevel:      apiconsts.PodAnnotationQoSLevelSystemCores,
			Annotations: map[string]string{
				cpuconsts.CPUSetOmissionAllowedAnnotationKey: cpusetOmissionFalse,
			},
		},
		RequestQuantity:          2,
		AllocationResult:         machine.NewCPUSet(0, 1),
		OriginalAllocationResult: machine.NewCPUSet(0, 1),
		TopologyAwareAssignments: map[int]machine.CPUSet{0: machine.NewCPUSet(0, 1)},
	}
	require.NoError(t, p.state.SetAllocationInfo("sys-rereadmit", "main", origin, false))

	req := &pluginapi.ResourceRequest{
		PodUid:           "sys-rereadmit",
		PodNamespace:     "default",
		PodName:          "sys-rereadmit",
		ContainerName:    "main",
		ContainerType:    pluginapi.ContainerType_MAIN,
		ContainerIndex:   0,
		ResourceName:     string(v1.ResourceCPU),
		ResourceRequests: map[string]float64{string(v1.ResourceCPU): 2},
		Labels: map[string]string{
			apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelSystemCores,
		},
		Annotations: map[string]string{
			apiconsts.PodAnnotationQoSLevelKey:           apiconsts.PodAnnotationQoSLevelSystemCores,
			cpuconsts.CPUSetOmissionAllowedAnnotationKey: cpusetOmissionTrue,
		},
	}
	_, err = p.systemCoresAllocationHandler(context.Background(), req, false)
	require.NoError(t, err)

	got := p.state.GetAllocationInfo("sys-rereadmit", "main")
	require.NotNil(t, got)
	assert.Equal(t, cpusetOmissionFalse, got.Annotations[cpuconsts.CPUSetOmissionAllowedAnnotationKey],
		"non-resize re-admit of an existing system container must inherit persisted false, not upgrade to true")
}

// seedSharedMainEntry writes a share-pool-owned shared MAIN entry with the given
// omission annotation (nil = key absent) plus optional extra annotations.
func seedSharedMainEntry(t *testing.T, p *DynamicPolicy, podUID string, anno *string, extraAnno map[string]string) {
	t.Helper()
	mainCpus := sharedPoolCPUSet.Clone()
	info := &state.AllocationInfo{
		AllocationMeta: commonstate.AllocationMeta{
			PodUid:        podUID,
			PodNamespace:  "default",
			PodName:       podUID,
			ContainerName: "main",
			ContainerType: pluginapi.ContainerType_MAIN.String(),
			QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
			OwnerPoolName: commonstate.PoolNameShare,
		},
		AllocationResult:         mainCpus,
		OriginalAllocationResult: mainCpus.Clone(),
		TopologyAwareAssignments: map[int]machine.CPUSet{0: mainCpus},
		RequestQuantity:          float64(mainCpus.Size()),
		InitTimestamp:            "2026-01-01 00:00:00.000000000 +0800 CST",
	}
	if anno != nil || len(extraAnno) > 0 {
		info.Annotations = map[string]string{}
		if anno != nil {
			info.Annotations[cpuconsts.CPUSetOmissionAllowedAnnotationKey] = *anno
		}
		for k, v := range extraAnno {
			info.Annotations[k] = v
		}
	}
	require.NoError(t, p.state.SetAllocationInfo(podUID, "main", info, false))
}

// seedSidecarWith writes a sidecar allocationInfo; anno nil leaves the key absent.
func seedSidecarWith(t *testing.T, p *DynamicPolicy, podUID string, cpus machine.CPUSet, anno *string, extraAnno map[string]string) {
	t.Helper()
	info := &state.AllocationInfo{
		AllocationMeta: commonstate.AllocationMeta{
			PodUid:        podUID,
			PodName:       podUID,
			ContainerName: "sidecar",
			ContainerType: pluginapi.ContainerType_SIDECAR.String(),
			QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
			OwnerPoolName: commonstate.PoolNameShare,
		},
		AllocationResult:         cpus,
		OriginalAllocationResult: cpus.Clone(),
		TopologyAwareAssignments: map[int]machine.CPUSet{0: cpus},
		RequestQuantity:          float64(cpus.Size()),
	}
	if anno != nil || len(extraAnno) > 0 {
		info.Annotations = map[string]string{}
		if anno != nil {
			info.Annotations[cpuconsts.CPUSetOmissionAllowedAnnotationKey] = *anno
		}
		for k, v := range extraAnno {
			info.Annotations[k] = v
		}
	}
	require.NoError(t, p.state.SetAllocationInfo(podUID, "sidecar", info, false))
}

// sharedPoolCPUSet is the AllocationResult shared by the share pool entry and
// every seeded shared member: the ownership validator requires each shared
// member's AllocationResult to equal the pool's.
var sharedPoolCPUSet = machine.NewCPUSet(2, 3, 4)

// ensureSharePoolEntry seeds the share pool entry required by the ownership
// validator when shared members are committed through a prepared partition.
func ensureSharePoolEntry(t *testing.T, p *DynamicPolicy) {
	t.Helper()
	if p.state.GetPodEntries()[commonstate.PoolNameShare].GetPoolEntry() != nil {
		return
	}
	require.NoError(t, p.state.SetAllocationInfo(commonstate.PoolNameShare, commonstate.FakedContainerName, &state.AllocationInfo{
		AllocationMeta:           commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameShare),
		AllocationResult:         sharedPoolCPUSet.Clone(),
		OriginalAllocationResult: sharedPoolCPUSet.Clone(),
		TopologyAwareAssignments: map[int]machine.CPUSet{0: machine.NewCPUSet(2, 3), 1: machine.NewCPUSet(4)},
	}, false))
}

// runSidecarSyncPrepareCommit prepares the current entries, commits them, and
// returns the sidecar allocationInfo afterwards. This exercises the real
// partition sync hook (applySidecarAllocationInfoFromMainContainer with the
// sticky omission merge inside the core apply) end to end.
func runSidecarSyncPrepareCommit(t *testing.T, p *DynamicPolicy, podUID string) *state.AllocationInfo {
	t.Helper()
	ensureSharePoolEntry(t, p)
	prepared, err := p.preparePendingCPUPartition(pendingCPUPartition{
		expectedRevision: p.state.GetRevision(),
		entries:          p.state.GetPodEntries(),
		dynamicConfig:    p.config.dynamicConfig.GetDynamicConfiguration(),
		persist:          false,
		source:           "sidecar-sticky-omission",
		validate:         func(state.PodEntries, state.NUMANodeMap, bool, bool) error { return nil },
	})
	require.NoError(t, err)
	require.NoError(t, p.commitPreparedCPUPartition(prepared))
	return p.state.GetAllocationInfo(podUID, "sidecar")
}

// TestSidecarFalse_MainMissingKey_Sticky: a sidecar already stamped "false" must
// not be resurrected by a main container that lost/never persisted the key — the
// sync must leave the sidecar at "false" even when live=true.
func TestSidecarFalse_MainMissingKey_Sticky(t *testing.T) {
	t.Parallel()
	p := newOmissionPolicy(t, true)

	// main: key entirely absent
	seedSharedMainEntry(t, p, "nomainkey-pod", nil, nil)
	seedSidecarWith(t, p, "nomainkey-pod", sharedPoolCPUSet.Clone(), strptr(cpusetOmissionFalse), nil)

	sideAi := runSidecarSyncPrepareCommit(t, p, "nomainkey-pod")
	require.NotNil(t, sideAi)
	assert.Equal(t, cpusetOmissionFalse, sideAi.Annotations[cpuconsts.CPUSetOmissionAllowedAnnotationKey],
		"sidecar false must survive sync when main lacks the omission key")
}

// TestSidecarTrue_MainFalse_DowngradesSidecar: the allowed downgrade direction —
// main "false" syncing down onto a sidecar "true" must flip the sidecar to
// "false" (sticky only blocks main->sidecar upgrades, never downgrades).
func TestSidecarTrue_MainFalse_DowngradesSidecar(t *testing.T) {
	t.Parallel()
	p := newOmissionPolicy(t, true)

	seedSharedMainEntry(t, p, "downgrade-pod", strptr(cpusetOmissionFalse), nil)
	seedSidecarWith(t, p, "downgrade-pod", sharedPoolCPUSet.Clone(), strptr(cpusetOmissionTrue), nil)

	sideAi := runSidecarSyncPrepareCommit(t, p, "downgrade-pod")
	require.NotNil(t, sideAi)
	assert.Equal(t, cpusetOmissionFalse, sideAi.Annotations[cpuconsts.CPUSetOmissionAllowedAnnotationKey],
		"main false must downgrade a sidecar true via sync")
}

// TestGRA_ResponseAnnotationsSync: the per-container annotations echoed back in
// GetResourcesAllocation must match persisted state — including when the CPUSet
// itself is cleared by omission. This pins the resource-annotation sync path.
func TestGRA_ResponseAnnotationsSync(t *testing.T) {
	t.Parallel()

	t.Run("omitted response still carries true annotation", func(t *testing.T) {
		t.Parallel()
		p := newOmissionPolicy(t, true)
		seedSharedMain(t, p, "anno-sync-pod", strptr(cpusetOmissionTrue))

		gra, err := p.GetResourcesAllocation(context.Background(), &pluginapi.GetResourcesAllocationRequest{})
		require.NoError(t, err)
		info := gra.PodResources["anno-sync-pod"].ContainerResources["main"].ResourceAllocation[string(v1.ResourceCPU)]
		assert.Empty(t, info.AllocationResult, "omitted")
		assert.Equal(t, cpusetOmissionTrue, info.Annotations[cpuconsts.CPUSetOmissionAllowedAnnotationKey])
	})

	t.Run("non-omitted response carries false annotation", func(t *testing.T) {
		t.Parallel()
		p := newOmissionPolicy(t, false)
		seedSharedMain(t, p, "anno-sync-pod-2", strptr(cpusetOmissionFalse))

		gra, err := p.GetResourcesAllocation(context.Background(), &pluginapi.GetResourcesAllocationRequest{})
		require.NoError(t, err)
		info := gra.PodResources["anno-sync-pod-2"].ContainerResources["main"].ResourceAllocation[string(v1.ResourceCPU)]
		assert.NotEmpty(t, info.AllocationResult, "non-omitted")
		assert.Equal(t, cpusetOmissionFalse, info.Annotations[cpuconsts.CPUSetOmissionAllowedAnnotationKey])
	})
}

// TestSidecarNumaNestedMetadata_Preserved: under main-driven sync, the sidecar's
// nested metadata (nested numa-binding enhancement annotations + topology-aware
// assignments) must be properly reconciled while the sticky omission "false" is
// preserved. Regression guard against flattening nested metadata during sync.
func TestSidecarNumaNestedMetadata_Preserved(t *testing.T) {
	t.Parallel()
	p := newOmissionPolicy(t, true)

	seedSharedMainEntry(t, p, "nested-pod", strptr(cpusetOmissionTrue), map[string]string{
		apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
	})

	sidecarCpus := sharedPoolCPUSet.Clone()
	seedSidecarWith(t, p, "nested-pod", sidecarCpus, strptr(cpusetOmissionFalse),
		map[string]string{
			apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
		})

	sideAi := runSidecarSyncPrepareCommit(t, p, "nested-pod")
	require.NotNil(t, sideAi)
	assert.Equal(t, cpusetOmissionFalse, sideAi.Annotations[cpuconsts.CPUSetOmissionAllowedAnnotationKey],
		"sticky sidecar false must survive nested-metadata sync")
	assert.Equal(t, apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
		sideAi.Annotations[apiconsts.PodAnnotationMemoryEnhancementNumaBinding],
		"nested memory-enhancement metadata must be preserved")
	assert.NotEmpty(t, sideAi.TopologyAwareAssignments, "topology-aware assignments must survive sync")
}

// TestGRA_SidecarFalse_MainTrue_StickyAcrossGRA pins the GRA in-loop sidecar
// sync (policy.go non-ramp-up path calls applySidecarAllocationInfoFromMainContainer
// directly, bypassing the prepare/commit sticky hook): a sidecar already stamped
// "false" must stay "false" when main says "true", even through a plain
// GetResourcesAllocation pass with live=true.
func TestGRA_SidecarFalse_MainTrue_StickyAcrossGRA(t *testing.T) {
	t.Parallel()
	p := newOmissionPolicy(t, true)

	seedSharedMainEntry(t, p, "gra-sidecar-pod", strptr(cpusetOmissionTrue), nil)
	seedSidecarWith(t, p, "gra-sidecar-pod", sharedPoolCPUSet.Clone(), strptr(cpusetOmissionFalse), nil)
	// give the sidecar a parseable timestamp so the GRA loop reaches its sync branch
	sideAi := p.state.GetAllocationInfo("gra-sidecar-pod", "sidecar")
	sideAi.InitTimestamp = "2026-01-01 00:00:00.000000000 +0800 CST"
	require.NoError(t, p.state.SetAllocationInfo("gra-sidecar-pod", "sidecar", sideAi, false))

	_, err := p.GetResourcesAllocation(context.Background(), &pluginapi.GetResourcesAllocationRequest{})
	require.NoError(t, err)

	after := p.state.GetAllocationInfo("gra-sidecar-pod", "sidecar")
	require.NotNil(t, after)
	assert.Equal(t, cpusetOmissionFalse, after.Annotations[cpuconsts.CPUSetOmissionAllowedAnnotationKey],
		"GRA in-loop sync must not resurrect a sidecar false from main true")
}

// TestSharedNUMAReAdmit_FullAllocate_Consistent drives the full Allocate entry
// (outer dispatch reads live, inner allocateSharedNumaBindingCPUs reads it
// again): a persisted "false" must stay "false" and the response must carry a
// non-empty CPUSet for the whole operation. This pins the two live reads to a
// single consistent decision on one re-admit.
func TestSharedNUMAReAdmit_FullAllocate_Consistent(t *testing.T) {
	t.Parallel()
	topology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithInitialization(topology, t.TempDir())
	require.NoError(t, err)
	p.config.dynamicConfig.GetDynamicConfiguration().EnableBypassCPUSetAdjustment = true

	origin := &state.AllocationInfo{
		AllocationMeta: commonstate.AllocationMeta{
			PodUid:        "snb-full",
			PodNamespace:  "default",
			PodName:       "snb-full",
			ContainerName: "main",
			QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
			OwnerPoolName: commonstate.PoolNameShare,
			Annotations: map[string]string{
				apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
				cpuconsts.CPUSetOmissionAllowedAnnotationKey:        cpusetOmissionFalse,
			},
		},
		RequestQuantity:          2,
		AllocationResult:         machine.NewCPUSet(0, 1),
		OriginalAllocationResult: machine.NewCPUSet(0, 1),
		TopologyAwareAssignments: map[int]machine.CPUSet{0: machine.NewCPUSet(0, 1)},
	}
	origin.SetSpecifiedNUMABindingNUMAID([]uint64{0})
	require.NoError(t, p.state.SetAllocationInfo("snb-full", "main", origin, false))

	req := newSharedNUMARequest("snb-full", map[string]string{
		cpuconsts.CPUSetOmissionAllowedAnnotationKey: cpusetOmissionTrue,
	})
	resp, err := p.Allocate(context.Background(), req)
	require.NoError(t, err)
	assert.NotEmpty(t,
		resp.AllocationResult.ResourceAllocation[string(v1.ResourceCPU)].AllocationResult,
		"re-admit of persisted false must return non-empty CPUSet")

	got := p.state.GetAllocationInfo("snb-full", "main")
	require.NotNil(t, got)
	assert.Equal(t, cpusetOmissionFalse, got.Annotations[cpuconsts.CPUSetOmissionAllowedAnnotationKey],
		"full Allocate re-admit must keep persisted false")
}

// TestSharedNUMAReAdmit_ConfigFlipMidOperation pins the live snapshot: the
// dynamic flag is flipped true->false from inside an AllocationHook that fires
// during this very operation, yet the whole operation must honor the snapshot it
// started with — response omitted, persisted annotation "true" — and only the
// *next* GRA pass (with the now-false flag) demotes. A window that re-reads the
// flag mid-operation would already have returned a non-empty CPUSet here.
func TestSharedNUMAReAdmit_ConfigFlipMidOperation(t *testing.T) {
	t.Parallel()
	topology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	require.NoError(t, err)
	p, err := getTestDynamicPolicyWithInitialization(topology, t.TempDir())
	require.NoError(t, err)
	p.config.dynamicConfig.GetDynamicConfiguration().EnableBypassCPUSetAdjustment = true

	p.RegisterAllocationHook(func(_, newInfo *state.AllocationInfo) error {
		if newInfo != nil && newInfo.PodUid == "flip-pod" {
			p.config.dynamicConfig.GetDynamicConfiguration().EnableBypassCPUSetAdjustment = false
		}
		return nil
	})

	req := newSharedNUMARequest("flip-pod", nil)
	resp, err := p.Allocate(context.Background(), req)
	require.NoError(t, err)
	assert.Empty(t,
		resp.AllocationResult.ResourceAllocation[string(v1.ResourceCPU)].AllocationResult,
		"snapshot=true must govern this operation even though the hook flipped the flag")

	ai := p.state.GetAllocationInfo("flip-pod", "main")
	require.NotNil(t, ai)
	assert.Equal(t, cpusetOmissionTrue, ai.Annotations[cpuconsts.CPUSetOmissionAllowedAnnotationKey])
	assert.False(t, p.config.dynamicConfig.GetDynamicConfiguration().EnableBypassCPUSetAdjustment,
		"hook flip must have landed for the next operation")

	// Next GRA with the now-false flag: demotes this container.
	gra, err := p.GetResourcesAllocation(context.Background(), &pluginapi.GetResourcesAllocationRequest{})
	require.NoError(t, err)
	assert.NotEmpty(t,
		gra.PodResources["flip-pod"].ContainerResources["main"].ResourceAllocation[string(v1.ResourceCPU)].AllocationResult)
	ai = p.state.GetAllocationInfo("flip-pod", "main")
	require.NotNil(t, ai)
	assert.Equal(t, cpusetOmissionFalse, ai.Annotations[cpuconsts.CPUSetOmissionAllowedAnnotationKey])
}

// TestSystem_FirstAllocate_WritesTrue pins the brand-new system main container
// origin==nil branch through the real handler: with live=true it records the
// initial omission decision.
func TestSystem_FirstAllocate_WritesTrue(t *testing.T) {
	t.Parallel()
	p := newOmissionPolicy(t, true)

	req := &pluginapi.ResourceRequest{
		PodUid:           "sys-fresh",
		PodNamespace:     "default",
		PodName:          "sys-fresh",
		ContainerName:    "main",
		ContainerType:    pluginapi.ContainerType_MAIN,
		ContainerIndex:   0,
		ResourceName:     string(v1.ResourceCPU),
		ResourceRequests: map[string]float64{string(v1.ResourceCPU): 2},
		Labels: map[string]string{
			apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelSystemCores,
		},
		Annotations: map[string]string{
			apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelSystemCores,
		},
	}
	_, err := p.systemCoresAllocationHandler(context.Background(), req, false)
	require.NoError(t, err)

	ai := p.state.GetAllocationInfo("sys-fresh", "main")
	require.NotNil(t, ai)
	assert.Equal(t, cpusetOmissionTrue, ai.Annotations[cpuconsts.CPUSetOmissionAllowedAnnotationKey],
		"brand-new system main with live=true must record initial true")
}

// TestDedicatedNonNUMABinding_Unsupported asserts the actual production result:
// dedicated without NUMA binding is rejected with the unsupported error and
// never writes an omission annotation.
func TestDedicatedNonNUMABinding_Unsupported(t *testing.T) {
	t.Parallel()
	p := newOmissionPolicy(t, true)

	req := &pluginapi.ResourceRequest{
		PodUid:           "ded-nonuma",
		PodNamespace:     "default",
		PodName:          "ded-nonuma",
		ContainerName:    "main",
		ContainerType:    pluginapi.ContainerType_MAIN,
		ContainerIndex:   0,
		ResourceName:     string(v1.ResourceCPU),
		ResourceRequests: map[string]float64{string(v1.ResourceCPU): 2},
		Labels: map[string]string{
			apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelDedicatedCores,
		},
		Annotations: map[string]string{
			apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelDedicatedCores,
			// no numa-binding enhancement annotation
		},
	}
	_, err := p.Allocate(context.Background(), req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not support dedicated_cores without NUMA binding")
	assert.Nil(t, p.state.GetAllocationInfo("ded-nonuma", "main"),
		"unsupported admission must not persist any allocation info")
}

// readCountingState wraps state.State to count the atomic snapshot and podEntries
// reads without any production testing hook.
type readCountingState struct {
	state.State
	getPodEntriesCalls int
	getSnapshotCalls   int
	getMachineCalls    int
	getRevisionCalls   int
	commitCalls        int
}

func (s *readCountingState) GetPodEntries() state.PodEntries {
	s.getPodEntriesCalls++
	return s.State.GetPodEntries()
}

func (s *readCountingState) GetAdvisorStateSnapshot() (state.PodEntries, state.NUMANodeMap, uint64) {
	s.getSnapshotCalls++
	return s.State.GetAdvisorStateSnapshot()
}

func (s *readCountingState) GetMachineState() state.NUMANodeMap {
	s.getMachineCalls++
	return s.State.GetMachineState()
}

func (s *readCountingState) GetRevision() uint64 {
	s.getRevisionCalls++
	return s.State.GetRevision()
}

func (s *readCountingState) CommitAdvisorStateIfRevision(
	expectedRevision uint64,
	podEntries state.PodEntries,
	machineState state.NUMANodeMap,
	allowOverlap, disableDedicatedOverlap, persist bool,
	permits ...*state.WritePermit,
) error {
	s.commitCalls++
	return s.State.CommitAdvisorStateIfRevision(
		expectedRevision, podEntries, machineState, allowOverlap, disableDedicatedOverlap, persist, permits...)
}

func TestGRAStateSnapshotLifecycle(t *testing.T) {
	t.Parallel()

	t.Run("stable state uses one atomic snapshot", func(t *testing.T) {
		t.Parallel()
		p := newOmissionPolicy(t, false)
		seedSharedMain(t, p, "stable-pod", strptr(cpusetOmissionFalse))

		wrapped := &readCountingState{State: p.state}
		p.state = wrapped

		resp, err := p.GetResourcesAllocation(context.Background(), &pluginapi.GetResourcesAllocationRequest{})
		require.NoError(t, err)
		require.NotNil(t, resp.PodResources["stable-pod"])
		assert.Equal(t, 1, wrapped.getSnapshotCalls)
		assert.Zero(t, wrapped.getPodEntriesCalls)
		assert.Zero(t, wrapped.getMachineCalls)
		assert.Zero(t, wrapped.getRevisionCalls)
		assert.Zero(t, wrapped.commitCalls)
	})

	t.Run("sidecar repair refreshes once before response", func(t *testing.T) {
		t.Parallel()
		p := newOmissionPolicy(t, true)
		seedSharedMain(t, p, "repair-pod", strptr(cpusetOmissionTrue))
		seedSidecarWith(t, p, "repair-pod", machine.NewCPUSet(4), strptr(cpusetOmissionFalse), nil)

		wrapped := &readCountingState{State: p.state}
		p.state = wrapped

		resp, err := p.GetResourcesAllocation(context.Background(), &pluginapi.GetResourcesAllocationRequest{})
		require.NoError(t, err)
		require.NotNil(t, resp.PodResources["repair-pod"])
		assert.Equal(t, 2, wrapped.getSnapshotCalls,
			"state repair must refresh exactly once before omission demotion and response packing")
		assert.Equal(t, 1, wrapped.getPodEntriesCalls,
			"machine-state regeneration reads current entries once")
		assert.Zero(t, wrapped.getMachineCalls)
		assert.Zero(t, wrapped.getRevisionCalls)
		assert.Zero(t, wrapped.commitCalls,
			"live=true with sticky false sidecar needs no omission CAS after repair")

		wantCPUSet := machine.NewCPUSet(2, 3)
		persisted := p.state.GetAllocationInfo("repair-pod", "sidecar")
		require.NotNil(t, persisted)
		assert.True(t, persisted.AllocationResult.Equals(wantCPUSet))
		assert.Equal(t, cpusetOmissionFalse,
			persisted.Annotations[cpuconsts.CPUSetOmissionAllowedAnnotationKey])
		cpuInfo := resp.PodResources["repair-pod"].ContainerResources["sidecar"].ResourceAllocation[string(v1.ResourceCPU)]
		assert.Equal(t, wantCPUSet.String(), cpuInfo.AllocationResult,
			"response must be packed from the refreshed persisted state")
	})
}

func demoteCPUSetExposureFromState(
	p *DynamicPolicy,
	liveEnabled bool,
	persist bool,
) (state.PodEntries, error) {
	entries, machineState, revision := p.state.GetAdvisorStateSnapshot()
	return p.demoteCPUSetExposureOnSnapshot(
		entries, machineState, revision, liveEnabled, persist)
}

// TestDemoteSnapshotLifecycle pins the explicit snapshot contract: the caller
// obtains one generation, while the demote helper performs no additional state
// reads. A no-op performs no commit; an active transition commits exactly once.
func TestDemoteSnapshotLifecycle(t *testing.T) {
	t.Parallel()
	p := newOmissionPolicy(t, false)
	seedSharedMain(t, p, "noop-pod", strptr(cpusetOmissionFalse))

	wrapped := &readCountingState{State: p.state}
	p.state = wrapped

	p.Lock()
	noopEntries, err := demoteCPUSetExposureFromState(p, false, false)
	p.Unlock()
	require.NoError(t, err)
	require.NotNil(t, noopEntries, "no-op demote must return the probe entries")
	assert.Equal(t, 0, wrapped.commitCalls, "no-op demote must not commit")
	assert.Zero(t, wrapped.getPodEntriesCalls, "demote helper must not read podEntries separately")
	assert.Equal(t, 1, wrapped.getSnapshotCalls, "caller must acquire one state generation")
	assert.Equal(t, 0, wrapped.getMachineCalls, "demote helper must not read machine separately")
	assert.Equal(t, 0, wrapped.getRevisionCalls, "demote helper must not read revision separately")

	// Active demote (a key-less container gets marked): exactly one atomic
	// snapshot, no separate state reads, and the returned candidate carries the
	// persisted flip.
	seedSharedMain(t, p, "flip-pod", nil)
	wrapped.getPodEntriesCalls = 0
	wrapped.getSnapshotCalls = 0
	wrapped.getMachineCalls = 0
	wrapped.getRevisionCalls = 0
	wrapped.commitCalls = 0
	p.Lock()
	activeEntries, err := demoteCPUSetExposureFromState(p, false, false)
	p.Unlock()
	require.NoError(t, err)
	assert.Equal(t, 1, wrapped.commitCalls, "active demote must commit exactly once")
	assert.Zero(t, wrapped.getPodEntriesCalls, "demote helper must not read podEntries separately")
	assert.Equal(t, 1, wrapped.getSnapshotCalls, "caller must acquire one state generation")
	assert.Zero(t, wrapped.getMachineCalls, "demote helper must not read machine separately")
	assert.Zero(t, wrapped.getRevisionCalls, "demote helper must not read revision separately")
	require.NotNil(t, activeEntries["flip-pod"])
	assert.Equal(t, cpusetOmissionFalse,
		activeEntries["flip-pod"]["main"].Annotations[cpuconsts.CPUSetOmissionAllowedAnnotationKey])
}

// TestDemoteNeedsTable_SnapshotAndCommitCounts pins the full predicate matrix:
// each caller obtains exactly one atomic state generation, while the helper
// performs no independent reads and commits only when the annotation changes.
func TestDemoteNeedsTable_SnapshotAndCommitCounts(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		live        bool
		currentAnno *string
		wantCommit  int
		wantValue   string
	}{
		{"live=false existing false noop", false, strptr(cpusetOmissionFalse), 0, cpusetOmissionFalse},
		{"live=false existing true demotes", false, strptr(cpusetOmissionTrue), 1, cpusetOmissionFalse},
		{"live=false keyless demotes", false, nil, 1, cpusetOmissionFalse},
		{"live=false invalid demotes", false, strptr("maybe"), 1, cpusetOmissionFalse},
		{"live=true existing false noop", true, strptr(cpusetOmissionFalse), 0, cpusetOmissionFalse},
		{"live=true existing true noop", true, strptr(cpusetOmissionTrue), 0, cpusetOmissionTrue},
		{"live=true keyless noop", true, nil, 0, ""},
		{"live=true invalid demotes", true, strptr("maybe"), 1, cpusetOmissionFalse},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := newOmissionPolicy(t, tt.live)
			seedSharedMain(t, p, "needs-pod", tt.currentAnno)

			wrapped := &readCountingState{State: p.state}
			p.state = wrapped

			p.Lock()
			entries, err := demoteCPUSetExposureFromState(p, tt.live, false)
			p.Unlock()
			require.NoError(t, err)
			assert.Zero(t, wrapped.getPodEntriesCalls, "demote helper must not read podEntries separately")
			assert.Equal(t, 1, wrapped.getSnapshotCalls, "caller must acquire one state generation")
			assert.Zero(t, wrapped.getMachineCalls, "demote helper must not read machine separately")
			assert.Zero(t, wrapped.getRevisionCalls, "demote helper must not read revision separately")
			assert.Equal(t, tt.wantCommit, wrapped.commitCalls)

			if tt.wantValue == "" {
				_, present := entries["needs-pod"]["main"].Annotations[cpuconsts.CPUSetOmissionAllowedAnnotationKey]
				assert.False(t, present, "keyless omit must stay keyless")
			} else {
				assert.Equal(t, tt.wantValue,
					entries["needs-pod"]["main"].Annotations[cpuconsts.CPUSetOmissionAllowedAnnotationKey])
			}
		})
	}
}

// TestAllocateReuse_FeatureZeroIncrement pins the omission *increment* on the
// real Allocate reuse path. The first Allocate establishes Allocate's own
// rollback baseline (pre-clone + machine/rev reads); counters are reset and the
// same reuse Allocate runs again. The omission path on an already-"false" pod
// must add zero machine, zero revision, and zero commit beyond that baseline —
// the reuse pass must read machine no more than the baseline Allocate did.
func TestAllocateReuse_FeatureZeroIncrement(t *testing.T) {
	t.Parallel()
	p := newOmissionPolicy(t, true)
	seedSharedMainEntry(t, p, "reuse-pod", strptr(cpusetOmissionFalse), nil)

	wrapped := &readCountingState{State: p.state}
	p.state = wrapped

	req := &pluginapi.ResourceRequest{
		PodUid: "reuse-pod", PodNamespace: "default", PodName: "reuse-pod",
		ContainerName: "main", ContainerType: pluginapi.ContainerType_MAIN, ContainerIndex: 0,
		ResourceName:     string(v1.ResourceCPU),
		ResourceRequests: map[string]float64{string(v1.ResourceCPU): 2},
		Labels:           map[string]string{apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelSharedCores},
		Annotations:      map[string]string{apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelSharedCores},
	}
	_, err := p.Allocate(context.Background(), req)
	require.NoError(t, err)

	baselineMachine := wrapped.getMachineCalls
	baselineRev := wrapped.getRevisionCalls
	require.Equal(t, 1, baselineMachine, "Allocate rollback baseline reads machine exactly once")
	require.Equal(t, 1, baselineRev, "Allocate rollback baseline reads revision exactly once")

	wrapped.getPodEntriesCalls = 0
	wrapped.getMachineCalls = 0
	wrapped.getRevisionCalls = 0
	wrapped.commitCalls = 0

	_, err = p.Allocate(context.Background(), req)
	require.NoError(t, err)

	assert.Equal(t, 1, wrapped.getPodEntriesCalls,
		"reuse pass re-reads entries once via Allocate rollback; omission adds none")
	assert.Equal(t, baselineMachine, wrapped.getMachineCalls,
		"reuse pass machine reads must equal the rollback baseline; omission adds none")
	assert.Equal(t, baselineRev, wrapped.getRevisionCalls,
		"reuse pass revision reads must equal the rollback baseline; omission adds none")
	assert.Zero(t, wrapped.getSnapshotCalls,
		"reuse omission must not acquire a full snapshot")
	assert.Equal(t, 0, wrapped.commitCalls,
		"reuse noop omission must add no advisor commit")
}

func TestGRAFinishRampUpRefreshesBeforeDemotion(t *testing.T) {
	t.Parallel()
	p := newOmissionPolicy(t, false)
	seedSharedMainEntry(t, p, "ramp-exit-pod", strptr(cpusetOmissionTrue), nil)
	ai := p.state.GetAllocationInfo("ramp-exit-pod", "main")
	require.NotNil(t, ai)
	ai.RampUp = true
	ai.InitTimestamp = time.Now().
		Add(-p.config.transitionPeriod - time.Minute).
		Format("2006-01-02 15:04:05.999999999 -0700 MST")
	require.NoError(t, p.state.SetAllocationInfo("ramp-exit-pod", "main", ai, true))

	wrapped := &readCountingState{State: p.state}
	p.state = wrapped
	resp, err := p.GetResourcesAllocation(context.Background(), &pluginapi.GetResourcesAllocationRequest{})
	require.NoError(t, err)
	require.NotNil(t, resp.PodResources["ramp-exit-pod"])
	cpuInfo := resp.PodResources["ramp-exit-pod"].ContainerResources["main"].ResourceAllocation[string(v1.ResourceCPU)]
	require.NotEmpty(t, cpuInfo.AllocationResult)
	persisted := p.state.GetAllocationInfo("ramp-exit-pod", "main")
	require.NotNil(t, persisted)
	assert.False(t, persisted.RampUp)
	assert.Equal(t, cpusetOmissionFalse,
		persisted.Annotations[cpuconsts.CPUSetOmissionAllowedAnnotationKey])
	assert.GreaterOrEqual(t, wrapped.getSnapshotCalls, 2,
		"ramp-up batch commit must refresh before demotion and response packing")
}

// TestSidecarMissingKey_RequestValues_StaysMissing pins missing-key compatibility
// across all request values: when main and the existing sidecar both lack the
// omission key and live=true, a sidecar re-admit — whatever the request carries —
// must not backfill either entry. The fresh sidecar must drop the request key
// first, then defer to main/existing authority.
func TestSidecarMissingKey_RequestValues_StaysMissing(t *testing.T) {
	t.Parallel()
	for _, reqValue := range []string{cpusetOmissionTrue, cpusetOmissionFalse, "invalid"} {
		reqValue := reqValue
		t.Run(reqValue, func(t *testing.T) {
			t.Parallel()
			p := newOmissionPolicy(t, true)

			seedSharedMain(t, p, "keyless-pod", nil)
			seedSidecarWith(t, p, "keyless-pod", sharedPoolCPUSet.Clone(), nil, nil)

			sidecarReq := &pluginapi.ResourceRequest{
				PodUid: "keyless-pod", PodNamespace: "default", PodName: "keyless-pod",
				ContainerName: "sidecar", ContainerType: pluginapi.ContainerType_SIDECAR, ContainerIndex: 1,
				ResourceName:     string(v1.ResourceCPU),
				ResourceRequests: map[string]float64{string(v1.ResourceCPU): 1},
				Labels:           map[string]string{apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelSharedCores},
				Annotations: map[string]string{
					apiconsts.PodAnnotationQoSLevelKey:           apiconsts.PodAnnotationQoSLevelSharedCores,
					cpuconsts.CPUSetOmissionAllowedAnnotationKey: reqValue,
				},
			}
			_, err := p.Allocate(context.Background(), sidecarReq)
			require.NoError(t, err)

			mainAi := p.state.GetAllocationInfo("keyless-pod", "main")
			require.NotNil(t, mainAi)
			_, mainPresent := mainAi.Annotations[cpuconsts.CPUSetOmissionAllowedAnnotationKey]
			assert.False(t, mainPresent, "main must stay missing-key")

			sideAi := p.state.GetAllocationInfo("keyless-pod", "sidecar")
			require.NotNil(t, sideAi)
			_, sidePresent := sideAi.Annotations[cpuconsts.CPUSetOmissionAllowedAnnotationKey]
			assert.False(t, sidePresent, "sidecar must stay missing-key, request value %q must not be backfilled", reqValue)
		})
	}
}

// TestSidecarEdgeCases_MonotonicSticky pins boundary shapes that must never
// resurrect or mis-mark a sidecar: a missing main (real GRA path skips the
// container), an empty-CPUSet sidecar (skipped by sync), and an illegal
// annotation value (demoted to canonical "false").
func TestSidecarEdgeCases_MonotonicSticky(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		live      bool
		mainNil   bool
		mainAnno  *string
		sideAnno  *string
		sideEmpty bool
		viaGRA    bool
		wantSide  string
	}{
		{"nil main via GRA keeps sidecar false", true, true, nil, strptr(cpusetOmissionFalse), false, true, cpusetOmissionFalse},
		{"empty cpuset sidecar not resurrected", true, false, strptr(cpusetOmissionTrue), strptr(cpusetOmissionFalse), true, false, cpusetOmissionFalse},
		{"illegal sidecar value demoted via GRA", false, false, strptr(cpusetOmissionFalse), strptr("maybe"), false, true, cpusetOmissionFalse},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := newOmissionPolicy(t, tt.live)

			if !tt.mainNil {
				seedSharedMainEntry(t, p, "edge-pod", tt.mainAnno, nil)
			}
			sideCpus := sharedPoolCPUSet.Clone()
			if tt.sideEmpty {
				sideCpus = machine.NewCPUSet()
			}
			seedSidecarWith(t, p, "edge-pod", sideCpus, tt.sideAnno, nil)

			if tt.viaGRA {
				_, err := p.GetResourcesAllocation(context.Background(), &pluginapi.GetResourcesAllocationRequest{})
				require.NoError(t, err)
			} else {
				runSidecarSyncPrepareCommit(t, p, "edge-pod")
			}

			after := p.state.GetAllocationInfo("edge-pod", "sidecar")
			require.NotNil(t, after)
			assert.Equal(t, tt.wantSide, after.Annotations[cpuconsts.CPUSetOmissionAllowedAnnotationKey])
		})
	}
}

// buildRealNUMAMirror generates and installs a real NUMA machineState mirror
// (per-pod topology entries) over the current podEntries, so benchmarks compare
// the machineState deep-clone cost against a representative mirror rather than
// an empty one.
func buildRealNUMAMirror(b *testing.B, p *DynamicPolicy) {
	mirror, err := generateMachineStateFromPodEntries(p.machine.machineInfo.CPUTopology, p.state.GetPodEntries(), nil)
	if err != nil {
		b.Fatal(err)
	}
	if err := p.state.SetMachineState(mirror, false); err != nil {
		b.Fatal(err)
	}
}

// BenchmarkGRANoop_Scaling measures the required one-snapshot no-op path over
// N already-false shared pods with a representative NUMA mirror.
func BenchmarkGRANoop_Scaling(b *testing.B) {
	for _, n := range []int{100, 1000} {
		b.Run(fmt.Sprintf("pods=%d", n), func(b *testing.B) {
			p := newOmissionPolicy(b, false)
			for i := 0; i < n; i++ {
				seedSharedMain(b, p, fmt.Sprintf("noop-%d", i), strptr(cpusetOmissionFalse))
			}
			buildRealNUMAMirror(b, p)
			b.ResetTimer()
			p.Lock()
			for i := 0; i < b.N; i++ {
				if _, err := demoteCPUSetExposureFromState(p, false, false); err != nil {
					b.Fatal(err)
				}
			}
			p.Unlock()
		})
	}
}

func infoWithAnnos(qos string, annos map[string]string) *state.AllocationInfo {
	req := &pluginapi.ResourceRequest{
		Annotations: map[string]string{apiconsts.PodAnnotationQoSLevelKey: qos},
	}
	meta := commonstate.GenerateGenericContainerAllocationMeta(req, commonstate.EmptyOwnerPoolName, qos)
	meta.Annotations = annos
	return &state.AllocationInfo{AllocationMeta: meta}
}

func TestCPUSetOmissionPrecedenceMatrix(t *testing.T) {
	t.Parallel()
	shared := apiconsts.PodAnnotationQoSLevelSharedCores
	dedicated := apiconsts.PodAnnotationQoSLevelDedicatedCores
	tests := []struct {
		name      string
		qos       string
		annot     map[string]string
		live      bool
		wantAllow bool
	}{
		{"missing key shared live on derives true", shared, nil, true, true},
		{"new true shared live on", shared, map[string]string{cpuconsts.CPUSetOmissionAllowedAnnotationKey: cpusetOmissionTrue}, true, true},
		{"new false shared live on stays false", shared, map[string]string{cpuconsts.CPUSetOmissionAllowedAnnotationKey: cpusetOmissionFalse}, true, false},
		{"new invalid shared live on fails closed", shared, map[string]string{cpuconsts.CPUSetOmissionAllowedAnnotationKey: "weird"}, true, false},
		{"new true shared live off", shared, map[string]string{cpuconsts.CPUSetOmissionAllowedAnnotationKey: cpusetOmissionTrue}, false, false},
		{"new true dedicated never bypasses", dedicated, map[string]string{cpuconsts.CPUSetOmissionAllowedAnnotationKey: cpusetOmissionTrue}, true, false},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := cpusetOmissionAllowed(infoWithAnnos(tt.qos, tt.annot), tt.live)
			assert.Equal(t, tt.wantAllow, got)
		})
	}
}

func TestInitialCPUSetOmissionRequestMatrix(t *testing.T) {
	t.Parallel()
	shared := apiconsts.PodAnnotationQoSLevelSharedCores
	tests := []struct {
		name string
		req  map[string]string
		want bool
	}{
		{"request missing derives true", sharedReq(nil), true},
		{"request new true permits", sharedReq(map[string]string{cpuconsts.CPUSetOmissionAllowedAnnotationKey: cpusetOmissionTrue}), true},
		{"request new false stays false", sharedReq(map[string]string{cpuconsts.CPUSetOmissionAllowedAnnotationKey: cpusetOmissionFalse}), false},
		{"request new invalid fails closed", sharedReq(map[string]string{cpuconsts.CPUSetOmissionAllowedAnnotationKey: "nope"}), false},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := initialCPUSetOmissionAllowed(shared, tt.req, true)
			assert.Equal(t, tt.want, got)
		})
	}
}

func sharedReq(annos map[string]string) map[string]string { return annos }

// driftSource exposes a coherent atomic triple through GetAdvisorStateSnapshot but
// has its independent GetPodEntries/GetRevision return a later, divergent generation,
// proving the snapshot constructor must read the triple atomically rather than mixing
// independent getters.
type driftSource struct {
	state.State
	entries       state.PodEntries
	machine       state.NUMANodeMap
	revision      uint64
	driftRevision uint64
}

func (d *driftSource) GetAdvisorStateSnapshot() (state.PodEntries, state.NUMANodeMap, uint64) {
	return d.entries.Clone(), d.machine.Clone(), d.revision
}
func (d *driftSource) GetPodEntries() state.PodEntries                     { return state.PodEntries{"drift": nil} }
func (d *driftSource) GetRevision() uint64                                 { return d.driftRevision }
func (d *driftSource) GetNUMAHeadroom() map[int]float64                    { return nil }
func (d *driftSource) GetAllowSharedCoresOverlapReclaimedCores() bool      { return false }
func (d *driftSource) GetDisableDedicatedCoresOverlapReclaimedCores() bool { return false }

func TestNewCPUSetAdjustmentStateSnapshotNoGenerationMixing(t *testing.T) {
	t.Parallel()
	entries := state.PodEntries{"pod": state.ContainerEntries{"main": &state.AllocationInfo{}}}
	machine := state.NUMANodeMap{0: &state.NUMANodeState{DefaultCPUSet: machine.NewCPUSet(0)}}
	src := &driftSource{entries: entries, machine: machine, revision: 42, driftRevision: 99}

	snap := newCPUSetAdjustmentStateSnapshot(src)
	require.NotNil(t, snap)
	assert.Equal(t, uint64(42), snap.revision, "snapshot revision must come from the atomic generation, not the drifted getter")
	assert.Contains(t, snap.podEntries, "pod", "snapshot entries must come from the atomic generation, not the drifted getter")
	assert.NotContains(t, snap.podEntries, "drift")
}

// alwaysStaleSource fails every CAS commit with ErrStaleStateRevision so the bounded
// demote retry loop must give up after the fixed attempt budget instead of looping
// forever or swallowing the error.
type alwaysStaleSource struct {
	state.State
	entries  state.PodEntries
	machine  state.NUMANodeMap
	revision uint64
	commits  int
}

func (s *alwaysStaleSource) GetAdvisorStateSnapshot() (state.PodEntries, state.NUMANodeMap, uint64) {
	return s.entries.Clone(), s.machine.Clone(), s.revision
}
func (s *alwaysStaleSource) GetAllowSharedCoresOverlapReclaimedCores() bool      { return false }
func (s *alwaysStaleSource) GetDisableDedicatedCoresOverlapReclaimedCores() bool { return false }
func (s *alwaysStaleSource) CommitAdvisorStateIfRevision(uint64, state.PodEntries, state.NUMANodeMap, bool, bool, bool, ...*state.WritePermit) error {
	s.commits++
	return fmt.Errorf("%w: expected=%d actual=%d", state.ErrStaleStateRevision, 1, s.revision)
}

func TestDemoteRetryExhaustionFailsClosedAndBounded(t *testing.T) {
	t.Parallel()
	cpus := machine.NewCPUSet(2)
	info := &state.AllocationInfo{AllocationMeta: commonstate.AllocationMeta{
		PodUid: "p", ContainerName: "main", QoSLevel: apiconsts.PodAnnotationQoSLevelSharedCores,
	}, AllocationResult: cpus}
	annos := map[string]string{cpuconsts.CPUSetOmissionAllowedAnnotationKey: cpusetOmissionTrue}
	info.Annotations = annos
	entries := state.PodEntries{"p": state.ContainerEntries{"main": info}}
	src := &alwaysStaleSource{entries: entries, machine: state.NUMANodeMap{}}

	p := &DynamicPolicy{state: src}
	// live=false forces the persisted "true" entry to need a one-way demote, so the
	// CAS commit is attempted on every retry. Use a fresh clone for the first attempt
	// so in-place marking never mutates the fake's own (still-"true") state.
	firstEntries, firstMachine, firstRev := src.GetAdvisorStateSnapshot()
	_, err := p.demoteCPUSetExposureWithRetry(firstEntries, firstMachine, firstRev, false, true)
	require.Error(t, err)
	assert.True(t, errors.Is(err, state.ErrStaleStateRevision), "must still be a stale-revision failure, got %v", err)
	assert.Equal(t, cpusetOmissionDemoteMaxAttempts, src.commits, "commit attempts must be bounded, got %d", src.commits)
}

// --- acceptance: real checkpoint persistence, concurrent CAS, clone isolation ---

func newOmissionTopology(t testing.TB) *machine.CPUTopology {
	topology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	require.NoError(t, err)
	return topology
}

// omissionSeeder covers both *state.cpuPluginState and the checkpoint wrapper.
type omissionSeeder interface {
	SetAllocationInfo(podUID, containerName string, info *state.AllocationInfo, persist bool) error
}

func seedSharedMainAnnos(t testing.TB, st omissionSeeder, podUID string, annos map[string]string) {
	t.Helper()
	cpus := machine.NewCPUSet(2, 3)
	info := &state.AllocationInfo{
		AllocationMeta: commonstate.AllocationMeta{
			PodUid:        podUID,
			PodName:       podUID,
			ContainerName: "main",
			ContainerType: pluginapi.ContainerType_MAIN.String(),
			QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
			OwnerPoolName: commonstate.PoolNameShare,
			Annotations:   annos,
		},
		AllocationResult:         cpus,
		OriginalAllocationResult: cpus.Clone(),
		TopologyAwareAssignments: map[int]machine.CPUSet{0: cpus},
		RequestQuantity:          float64(cpus.Size()),
		InitTimestamp:            "2026-01-01 00:00:00.000000000 +0800 CST",
	}
	require.NoError(t, st.SetAllocationInfo(podUID, "main", info, true))
}

func newCheckpointOmissionState(t testing.TB, dir string) state.State {
	t.Helper()
	st, err := state.NewCheckpointState(
		&statedirectory.StateDirectoryConfiguration{StateFileDirectory: dir},
		"test", "test", newOmissionTopology(t), false,
		state.GenerateMachineStateFromPodEntries, metrics.DummyMetrics{})
	require.NoError(t, err)
	return st
}

// TestOmissionDemotePersistsAndReloads exercises a real checkpoint-backed store:
// a new-key-"true" entry and a missing-key entry are demoted with persist=true,
// then a SECOND state reloads the same checkpoint directory. The one-way "false"
// must survive reload and must never resurrect.
func TestOmissionDemotePersistsAndReloads(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	st := newCheckpointOmissionState(t, dir)

	seedSharedMainAnnos(t, st, "new-true", map[string]string{
		cpuconsts.CPUSetOmissionAllowedAnnotationKey: cpusetOmissionTrue,
	})
	// Missing new key: live=false derives omission off, so it still needs the
	// one-way false write; it carries no other bypass annotation.
	seedSharedMainAnnos(t, st, "missing-key", nil)

	p := &DynamicPolicy{state: st}
	before := st.GetRevision()
	e, m, r := st.GetAdvisorStateSnapshot()
	// live=false: both entries must demote to new=false and persist.
	_, err := p.demoteCPUSetExposureWithRetry(e, m, r, false, true)
	require.NoError(t, err)
	require.Greater(t, st.GetRevision(), before, "demote must advance the revision")

	// Reload a fresh state from the same checkpoint directory.
	reloaded := newCheckpointOmissionState(t, dir)
	for _, pod := range []string{"new-true", "missing-key"} {
		ai := reloaded.GetAllocationInfo(pod, "main")
		require.NotNil(t, ai, "pod %q must survive reload", pod)
		assert.Equal(t, cpusetOmissionFalse,
			ai.Annotations[cpuconsts.CPUSetOmissionAllowedAnnotationKey],
			"pod %q must persist the demoted new key", pod)
	}

	// No resurrection: after reload, another demote is a no-op (revision unchanged).
	rp := &DynamicPolicy{state: reloaded}
	rb := reloaded.GetRevision()
	re, rm, rr := reloaded.GetAdvisorStateSnapshot()
	_, err = rp.demoteCPUSetExposureOnSnapshot(re, rm, rr, false, true)
	require.NoError(t, err)
	assert.Equal(t, rb, reloaded.GetRevision(), "already-false entries must not rewrite the checkpoint")
}

// TestOmissionConcurrentCASExactlyOneWins runs two demote commits from the SAME
// captured generation through a channel barrier (no sleeps). Exactly one CAS
// succeeds and the other observes ErrStaleStateRevision; no stale candidate wins.
func TestOmissionConcurrentCASExactlyOneWins(t *testing.T) {
	t.Parallel()
	st := newCheckpointOmissionState(t, t.TempDir())
	seedSharedMainAnnos(t, st, "p1", map[string]string{cpuconsts.CPUSetOmissionAllowedAnnotationKey: cpusetOmissionTrue})

	p := &DynamicPolicy{state: st}
	// Both goroutines snapshot the SAME generation before either commits.
	e1, m1, rev := st.GetAdvisorStateSnapshot()
	e2, m2, rev2 := st.GetAdvisorStateSnapshot()
	require.Equal(t, rev, rev2)

	start := make(chan struct{})
	done := make(chan error, 2)
	go func() { <-start; _, err := p.demoteCPUSetExposureOnSnapshot(e1, m1, rev, false, true); done <- err }()
	go func() { <-start; _, err := p.demoteCPUSetExposureOnSnapshot(e2, m2, rev2, false, true); done <- err }()
	close(start)

	var successes, stales int
	for i := 0; i < 2; i++ {
		err := <-done
		switch {
		case err == nil:
			successes++
		case errors.Is(err, state.ErrStaleStateRevision):
			stales++
		default:
			require.NoError(t, err, "unexpected non-stale CAS error: %v", err)
		}
	}
	assert.Equal(t, 1, successes, "exactly one CAS commit must win")
	assert.Equal(t, 1, stales, "exactly one CAS must observe stale revision")

	// The winner's candidate is the only persisted one; the entry is demoted exactly once.
	ai := st.GetAllocationInfo("p1", "main")
	require.NotNil(t, ai)
	assert.Equal(t, cpusetOmissionFalse, ai.Annotations[cpuconsts.CPUSetOmissionAllowedAnnotationKey])
}

// TestAdvisorStateSnapshotCloneIsolation mutates the nested maps/machines returned
// by GetAdvisorStateSnapshot and proves neither the source nor a second snapshot is
// affected.
func TestAdvisorStateSnapshotCloneIsolation(t *testing.T) {
	t.Parallel()
	// on a real checkpoint-backed state.
	ck := newCheckpointOmissionState(t, t.TempDir())
	seedSharedMainAnnos(t, ck, "cloned", map[string]string{cpuconsts.CPUSetOmissionAllowedAnnotationKey: cpusetOmissionTrue})

	e, m, _ := ck.GetAdvisorStateSnapshot()
	e["cloned"]["main"].Annotations["tampered"] = "1"
	e["cloned"]["main"].AllocationResult = machine.NewCPUSet(99)
	for _, ns := range m {
		if ns != nil {
			ns.DefaultCPUSet = machine.NewCPUSet(99)
		}
	}

	e2, m2, _ := ck.GetAdvisorStateSnapshot()
	assert.NotContains(t, e2["cloned"]["main"].Annotations, "tampered", "mutating a returned snapshot must not affect the source")
	assert.True(t, e2["cloned"]["main"].AllocationResult.Equals(machine.NewCPUSet(2, 3)), "AllocationResult must be deep-cloned")
	for numaID, ns := range m2 {
		if ns != nil {
			assert.False(t, ns.DefaultCPUSet.Equals(machine.NewCPUSet(99)),
				"machine[%d] mirror must be deep-cloned, not aliased to the mutated snapshot", numaID)
		}
	}
}

// TestSidecarStickyFalseThroughMain confirms the sidecar one-way rule: a sidecar that
// already recorded "false" keeps "false" even when the main it inherits from is "true".
func TestSidecarStickyFalseThroughMain(t *testing.T) {
	t.Parallel()
	sidecar := &state.AllocationInfo{
		AllocationMeta: commonstate.AllocationMeta{
			QoSLevel:    apiconsts.PodAnnotationQoSLevelSharedCores,
			Annotations: map[string]string{cpuconsts.CPUSetOmissionAllowedAnnotationKey: cpusetOmissionFalse},
		},
	}
	main := &state.AllocationInfo{
		AllocationMeta: commonstate.AllocationMeta{
			QoSLevel:    apiconsts.PodAnnotationQoSLevelSharedCores,
			Annotations: map[string]string{cpuconsts.CPUSetOmissionAllowedAnnotationKey: cpusetOmissionTrue},
		},
	}
	p := &DynamicPolicy{}
	p.applySidecarAllocationInfoFromMainContainer(sidecar, main)
	assert.Equal(t, cpusetOmissionFalse,
		sidecar.Annotations[cpuconsts.CPUSetOmissionAllowedAnnotationKey],
		"a persisted sidecar false must stay false even when main is true")
}
