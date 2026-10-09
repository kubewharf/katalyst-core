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

package cpulist

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/util/sets"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	cpuconsts "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/consts"
	bulkheadapi "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/api"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/model"
	bulkheadutils "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/utils"
	cpustate "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	cpusetutil "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/util"
	dynamicconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	qrmresctrl "github.com/kubewharf/katalyst-core/pkg/config/agent/qrm/resctrl"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/util/external/rdt"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	resourcepackage "github.com/kubewharf/katalyst-core/pkg/util/resource-package"
)

type fakeCPUListManager struct {
	clos     []Clos
	writes   []cpuListWrite
	writeErr error
}

type cpuListWrite struct {
	closID string
	target string
}

type fakeCATCapabilityProvider struct {
	capabilities map[int]rdt.CATCapability
	err          error
}

type driftCPUListManager struct {
	*fakeCPUListManager
	matches bool
}

func (p fakeCATCapabilityProvider) GetCATCapabilities() (map[int]rdt.CATCapability, error) {
	return p.capabilities, p.err
}

func supportedCATCapabilityProvider() fakeCATCapabilityProvider {
	return fakeCATCapabilityProvider{capabilities: map[int]rdt.CATCapability{
		0: {CBMMask: 0xffff, MinCBMBits: 1},
	}}
}

func (m *driftCPUListManager) CPUListMatches(context.Context, string, string) (bool, error) {
	return m.matches, nil
}

func (m *fakeCPUListManager) ListManagedClos(context.Context) ([]Clos, error) {
	return append([]Clos(nil), m.clos...), nil
}

func (m *fakeCPUListManager) ApplyCPUList(_ context.Context, closID, target string) error {
	m.writes = append(m.writes, cpuListWrite{closID: closID, target: target})
	return m.writeErr
}

func newTestPlugin(manager *fakeCPUListManager) *CPUListPlugin {
	return NewCPUListPluginWithManager(&qrmresctrl.ResctrlConfig{
		CPUSetPoolToSharedSubgroup: map[string]int{
			"share-a": 3,
			"share-b": 3,
		},
	}, manager, supportedCATCapabilityProvider())
}

func handlerContext(view *model.CPUSetPartitionView) bulkheadapi.HandlerContext {
	return bulkheadapi.HandlerContext{View: view}
}

func TestCPUListPluginSharedPoolsUnionForSameClos(t *testing.T) {
	t.Parallel()
	manager := &fakeCPUListManager{clos: []Clos{{ID: "share-03", Epoch: 1}}}
	plugin := newTestPlugin(manager)
	view := &model.CPUSetPartitionView{
		SharePoolMap: map[string]machine.CPUSet{
			"share-a": machine.NewCPUSet(1, 2),
			"share-b": machine.NewCPUSet(3, 4),
		},
	}

	require.NoError(t, plugin.CPUSetAdjustmentHandler(context.Background(), handlerContext(view)))
	require.Equal(t, []cpuListWrite{{closID: "share-03", target: "1-4"}}, manager.writes)
}

func TestCPUListPluginConsumesLogicalPoolsFromReclaimOnlyAppliedView(t *testing.T) {
	t.Parallel()
	manager := &fakeCPUListManager{clos: []Clos{
		{ID: "share-03", Epoch: 1},
		{ID: consts.ResctrlGroupDedicated, Epoch: 1},
	}}
	plugin := newTestPlugin(manager)
	applied := &model.AppliedView{
		CPUSetPartitionView: model.CPUSetPartitionView{
			SharePoolMap: map[string]machine.CPUSet{
				"share-a": machine.NewCPUSet(0, 1),
				"share-b": machine.NewCPUSet(2),
			},
			Dedicated:        machine.NewCPUSet(3),
			ReclaimEffective: machine.NewCPUSet(),
		},
		Level:         model.AppliedViewLevelReclaimOnly,
		CPUSetByRel:   map[string]machine.CPUSet{},
		RelProofByRel: map[string]model.CgroupRelProof{},
	}

	require.NoError(t, plugin.CPUSetAdjustmentHandler(context.Background(), bulkheadapi.HandlerContext{
		AppliedView: applied,
		View:        &applied.CPUSetPartitionView,
	}))
	require.Equal(t, []cpuListWrite{
		{closID: "share-03", target: "0-2"},
		{closID: consts.ResctrlGroupDedicated, target: "3"},
	}, manager.writes)
}

func TestCPUListPluginRejectsOverlapAcrossFinalClosTargetsBeforeWriting(t *testing.T) {
	t.Parallel()
	manager := &fakeCPUListManager{clos: []Clos{
		{ID: consts.ResctrlGroupDedicated, Epoch: 1},
		{ID: consts.ResctrlGroupDefaultShare, Epoch: 1},
	}}
	plugin := newTestPlugin(manager)
	view := &model.CPUSetPartitionView{
		Dedicated: machine.NewCPUSet(2, 3),
		SharePoolMap: map[string]machine.CPUSet{
			consts.ResctrlGroupShare: machine.NewCPUSet(1, 2),
		},
	}

	err := plugin.CPUSetAdjustmentHandler(context.Background(), handlerContext(view))

	require.EqualError(t, err, `cpu list targets for clos "dedicated" and "shared-50" overlap on cpus "2"`)
	require.Empty(t, manager.writes)
	require.Empty(t, plugin.applied)
}

func TestCPUListPluginAllowsOverlapWithinPoolsGroupedIntoSameClos(t *testing.T) {
	t.Parallel()
	manager := &fakeCPUListManager{clos: []Clos{{ID: "share-03", Epoch: 1}}}
	plugin := newTestPlugin(manager)
	view := &model.CPUSetPartitionView{SharePoolMap: map[string]machine.CPUSet{
		"share-a": machine.NewCPUSet(1, 2),
		"share-b": machine.NewCPUSet(2, 3),
	}}

	require.NoError(t, plugin.CPUSetAdjustmentHandler(context.Background(), handlerContext(view)))
	require.Equal(t, []cpuListWrite{{closID: "share-03", target: "1-3"}}, manager.writes)
}

func TestGateBSNBRampUpWritesDeclaredShareNUMAClos(t *testing.T) {
	t.Parallel()
	state := cpustate.NewCPUPluginState(nil)
	state.SetAllowSharedCoresOverlapReclaimedCores(false)
	state.SetAllocationInfo(commonstate.PoolNameReserve, commonstate.FakedContainerName, &cpustate.AllocationInfo{
		AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReserve),
		AllocationResult: machine.NewCPUSet(0, 4),
	})
	state.SetAllocationInfo(commonstate.PoolNameShare, commonstate.FakedContainerName, &cpustate.AllocationInfo{
		AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameShare),
		AllocationResult: machine.NewCPUSet(1, 5),
	})
	state.SetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName, &cpustate.AllocationInfo{
		AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
		AllocationResult: machine.NewCPUSet(2, 3, 6, 7),
	})
	state.SetAllocationInfo("snb-ramp-up", "main", &cpustate.AllocationInfo{
		AllocationMeta: commonstate.AllocationMeta{
			PodUid:        "snb-ramp-up",
			ContainerName: "main",
			OwnerPoolName: "isolation-snb-ramp-up",
			QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
			Annotations: map[string]string{
				apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
				cpuconsts.CPUStateAnnotationKeyNUMAHint:             "0",
			},
		},
		RampUp:           true,
		AllocationResult: machine.NewCPUSet(2),
	})
	topology := &machine.CPUTopology{CPUDetails: machine.CPUDetails{
		0: {NUMANodeID: 0}, 1: {NUMANodeID: 0}, 2: {NUMANodeID: 0}, 3: {NUMANodeID: 0},
		4: {NUMANodeID: 1}, 5: {NUMANodeID: 1}, 6: {NUMANodeID: 1}, 7: {NUMANodeID: 1},
	}}
	view, _ := bulkheadutils.BuildCPUSetPartitionView(state, topology, bulkheadutils.CPUSetPartitionViewOptions{
		HardPartitionEnabled: true,
	})

	manager := &fakeCPUListManager{clos: []Clos{
		{ID: "share-03", Epoch: 1},
		{ID: "share-07", Epoch: 1},
		{ID: "shared-50", Epoch: 1},
	}}
	plugin := NewCPUListPluginWithManager(&qrmresctrl.ResctrlConfig{
		CPUSetPoolToSharedSubgroup: map[string]int{
			"share-NUMA0":           3,
			"isolation-snb-ramp-up": 7,
		},
	}, manager, supportedCATCapabilityProvider())

	require.NoError(t, plugin.CPUSetAdjustmentHandler(context.Background(), handlerContext(&view.CPUSetPartitionView)))
	require.Equal(t, []cpuListWrite{
		{closID: "share-03", target: "2"},
		{closID: "share-07", target: ""},
		{closID: "shared-50", target: "1,5"},
	}, manager.writes)
	require.NotContains(t, manager.writes[1].target, "2", "default CLOS must not contain ramp-up CPU")
}

func TestGateBSNBRampUpAdjustmentStateWritesWrappedResourcePackageSourcePoolClos(t *testing.T) {
	t.Parallel()
	state := cpustate.NewCPUPluginState(nil)
	state.SetAllowSharedCoresOverlapReclaimedCores(false)
	state.SetAllocationInfo(commonstate.PoolNameReserve, commonstate.FakedContainerName, &cpustate.AllocationInfo{
		AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReserve),
		AllocationResult: machine.NewCPUSet(0, 4),
	})
	state.SetAllocationInfo(commonstate.PoolNameShare, commonstate.FakedContainerName, &cpustate.AllocationInfo{
		AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameShare),
		AllocationResult: machine.NewCPUSet(1, 5),
	})
	state.SetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName, &cpustate.AllocationInfo{
		AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
		AllocationResult: machine.NewCPUSet(2, 3, 6, 7),
	})

	const (
		packageName   = "package-a"
		sourcePool    = "share-NUMA0"
		isolationPool = "isolation-snb-ramp-up"
	)
	wrappedSourcePool := resourcepackage.WrapOwnerPoolName(sourcePool, packageName)
	wrappedIsolationPool := resourcepackage.WrapOwnerPoolName(isolationPool, packageName)
	state.SetAllocationInfo("snb-ramp-up", "main", &cpustate.AllocationInfo{
		AllocationMeta: commonstate.AllocationMeta{
			PodUid:        "snb-ramp-up",
			ContainerName: "main",
			OwnerPoolName: commonstate.EmptyOwnerPoolName,
			QoSLevel:      apiconsts.PodAnnotationQoSLevelSharedCores,
			Annotations: map[string]string{
				apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
				cpuconsts.CPUStateAnnotationKeyNUMAHint:             "0",
				apiconsts.PodAnnotationResourcePackageKey:           packageName,
			},
		},
		RampUp:           true,
		AllocationResult: machine.NewCPUSet(2),
	})
	topology := &machine.CPUTopology{CPUDetails: machine.CPUDetails{
		0: {NUMANodeID: 0}, 1: {NUMANodeID: 0}, 2: {NUMANodeID: 0}, 3: {NUMANodeID: 0},
		4: {NUMANodeID: 1}, 5: {NUMANodeID: 1}, 6: {NUMANodeID: 1}, 7: {NUMANodeID: 1},
	}}
	view, _ := bulkheadutils.BuildCPUSetPartitionView(state, topology, bulkheadutils.CPUSetPartitionViewOptions{
		HardPartitionEnabled: true,
	})
	require.Equal(t, machine.NewCPUSet(2), view.SharePoolMap[wrappedSourcePool])
	require.NotContains(t, view.SharePoolMap, sourcePool)

	manager := &fakeCPUListManager{clos: []Clos{
		{ID: "share-03", Epoch: 1},
		{ID: "share-07", Epoch: 1},
		{ID: "share-09", Epoch: 1},
		{ID: "shared-50", Epoch: 1},
	}}
	plugin := NewCPUListPluginWithManager(&qrmresctrl.ResctrlConfig{
		CPUSetPoolToSharedSubgroup: map[string]int{
			wrappedSourcePool:    3,
			sourcePool:           7,
			wrappedIsolationPool: 9,
		},
	}, manager, supportedCATCapabilityProvider())

	require.NoError(t, plugin.CPUSetAdjustmentHandler(context.Background(), handlerContext(&view.CPUSetPartitionView)))
	require.Equal(t, []cpuListWrite{
		{closID: "share-03", target: "2"},
		{closID: "share-07", target: ""},
		{closID: "share-09", target: ""},
		{closID: "shared-50", target: "1,5"},
	}, manager.writes)
}

func TestCPUListPluginDisabledByTopLevelDisableRDT(t *testing.T) {
	t.Parallel()
	plugin := newTestPlugin(&fakeCPUListManager{})
	conf := dynamicconfig.NewConfiguration()
	conf.AdminQoSConfiguration.CPUPluginConfiguration.BulkheadConfig.BulkheadRDTConfig.EnableCPUList = true
	conf.AdminQoSConfiguration.QRMPluginConfiguration.RDTConfig.DisableRDT = true

	require.False(t, plugin.Enable(bulkheadapi.HandlerContext{
		CPUSetAdjustmentHandlerCtx: cpusetutil.CPUSetAdjustmentHandlerCtx{DynamicConf: conf},
	}))
}

func TestCPUListPluginEnableIgnoresCATUnsupported(t *testing.T) {
	t.Parallel()
	plugin := NewCPUListPluginWithManager(&qrmresctrl.ResctrlConfig{}, &fakeCPUListManager{},
		fakeCATCapabilityProvider{err: rdt.ErrCATUnsupported})
	conf := dynamicconfig.NewConfiguration()
	conf.AdminQoSConfiguration.CPUPluginConfiguration.BulkheadConfig.BulkheadRDTConfig.EnableCPUList = true

	require.True(t, plugin.Enable(bulkheadapi.HandlerContext{
		CPUSetAdjustmentHandlerCtx: cpusetutil.CPUSetAdjustmentHandlerCtx{DynamicConf: conf},
	}))
}

func TestCPUListPluginHandlerNoopsWhenCATUnsupported(t *testing.T) {
	t.Parallel()
	manager := &fakeCPUListManager{clos: []Clos{{ID: "dedicated", Epoch: 1}}}
	plugin := NewCPUListPluginWithManager(&qrmresctrl.ResctrlConfig{}, manager,
		fakeCATCapabilityProvider{err: rdt.ErrCATUnsupported})

	require.NoError(t, plugin.CPUSetAdjustmentHandler(context.Background(), handlerContext(
		&model.CPUSetPartitionView{Dedicated: machine.NewCPUSet(1)},
	)))
	require.Empty(t, manager.writes)
}

func TestCPUListPluginDisabledHandlerNoopsWhenCATUnsupported(t *testing.T) {
	t.Parallel()
	manager := &fakeCPUListManager{clos: []Clos{{ID: "dedicated", Epoch: 1}}}
	plugin := NewCPUListPluginWithManager(&qrmresctrl.ResctrlConfig{}, manager,
		fakeCATCapabilityProvider{err: rdt.ErrCATUnsupported})

	require.NoError(t, plugin.CPUSetAdjustmentDisabledHandler(context.Background(), bulkheadapi.HandlerContext{}))
	require.Empty(t, manager.writes)
}

func TestCPUListPluginRemainsEnabledOnCATCapabilityReadError(t *testing.T) {
	t.Parallel()
	plugin := NewCPUListPluginWithManager(&qrmresctrl.ResctrlConfig{}, &fakeCPUListManager{},
		fakeCATCapabilityProvider{err: errors.New("read L3 cbm_mask: permission denied")})
	conf := dynamicconfig.NewConfiguration()
	conf.AdminQoSConfiguration.CPUPluginConfiguration.BulkheadConfig.BulkheadRDTConfig.EnableCPUList = true

	require.True(t, plugin.Enable(bulkheadapi.HandlerContext{
		CPUSetAdjustmentHandlerCtx: cpusetutil.CPUSetAdjustmentHandlerCtx{DynamicConf: conf},
	}))
}

func TestCPUListPluginWritesDedicatedTarget(t *testing.T) {
	t.Parallel()
	manager := &fakeCPUListManager{clos: []Clos{{ID: "dedicated", Epoch: 1}}}
	plugin := newTestPlugin(manager)
	view := &model.CPUSetPartitionView{Dedicated: machine.NewCPUSet(5, 6)}

	require.NoError(t, plugin.CPUSetAdjustmentHandler(context.Background(), handlerContext(view)))
	require.Equal(t, []cpuListWrite{{closID: "dedicated", target: "5-6"}}, manager.writes)
}

func TestCPUListPluginAppliesMissingTargetClos(t *testing.T) {
	t.Parallel()
	manager := &fakeCPUListManager{}
	plugin := newTestPlugin(manager)
	view := &model.CPUSetPartitionView{Dedicated: machine.NewCPUSet(5, 6)}

	require.NoError(t, plugin.CPUSetAdjustmentHandler(context.Background(), handlerContext(view)))
	require.Equal(t, []cpuListWrite{{closID: "dedicated", target: "5-6"}}, manager.writes)
}

func TestCPUListPluginBuildTargetsSkipsEmptyCPUSets(t *testing.T) {
	t.Parallel()
	plugin := newTestPlugin(&fakeCPUListManager{})
	view := &model.CPUSetPartitionView{
		SharePoolMap: map[string]machine.CPUSet{
			"share-a": machine.NewCPUSet(),
			"share-b": machine.NewCPUSet(1),
		},
		Dedicated: machine.NewCPUSet(),
	}

	require.Equal(t, map[string]string{"share-03": "1"}, plugin.buildTargets(view))
}

func TestCPUListPluginClearsEmptyTarget(t *testing.T) {
	t.Parallel()
	manager := &fakeCPUListManager{clos: []Clos{
		{ID: consts.ResctrlGroupDedicated, Epoch: 1},
		{ID: consts.ResctrlGroupReclaim, Epoch: 1},
	}}
	plugin := newTestPlugin(manager)

	require.NoError(t, plugin.CPUSetAdjustmentHandler(context.Background(), handlerContext(&model.CPUSetPartitionView{})))
	require.Equal(t, []cpuListWrite{
		{closID: consts.ResctrlGroupDedicated, target: ""},
	}, manager.writes)
}

func TestCPUListPluginSkipsUnchangedCanonicalTarget(t *testing.T) {
	t.Parallel()
	manager := &fakeCPUListManager{clos: []Clos{{ID: "dedicated", Epoch: 1}}}
	plugin := newTestPlugin(manager)
	view := &model.CPUSetPartitionView{Dedicated: machine.NewCPUSet(3, 1, 2)}

	require.NoError(t, plugin.CPUSetAdjustmentHandler(context.Background(), handlerContext(view)))
	require.NoError(t, plugin.CPUSetAdjustmentHandler(context.Background(), handlerContext(view)))
	require.Equal(t, []cpuListWrite{{closID: "dedicated", target: "1-3"}}, manager.writes)
}

func TestCPUListPluginRewritesUnchangedTargetAfterLiveDrift(t *testing.T) {
	t.Parallel()
	base := &fakeCPUListManager{clos: []Clos{{ID: "dedicated", Epoch: 1}}}
	manager := &driftCPUListManager{fakeCPUListManager: base, matches: true}
	plugin := newTestPlugin(manager.fakeCPUListManager)
	plugin.manager = manager
	view := &model.CPUSetPartitionView{Dedicated: machine.NewCPUSet(1)}

	require.NoError(t, plugin.CPUSetAdjustmentHandler(context.Background(), handlerContext(view)))
	manager.matches = false
	require.NoError(t, plugin.CPUSetAdjustmentHandler(context.Background(), handlerContext(view)))

	require.Equal(t, []cpuListWrite{
		{closID: "dedicated", target: "1"},
		{closID: "dedicated", target: "1"},
	}, manager.writes)
}

func TestCPUListPluginRetriesFailedWrite(t *testing.T) {
	t.Parallel()
	manager := &fakeCPUListManager{
		clos:     []Clos{{ID: "dedicated", Epoch: 1}},
		writeErr: errors.New("write cpu_list"),
	}
	plugin := newTestPlugin(manager)
	view := &model.CPUSetPartitionView{Dedicated: machine.NewCPUSet(1)}

	require.Error(t, plugin.CPUSetAdjustmentHandler(context.Background(), handlerContext(view)))
	manager.writeErr = nil
	require.NoError(t, plugin.CPUSetAdjustmentHandler(context.Background(), handlerContext(view)))
	require.Equal(t, []cpuListWrite{
		{closID: "dedicated", target: "1"},
		{closID: "dedicated", target: "1"},
	}, manager.writes)
}

func TestCPUListPluginRewritesRecreatedClos(t *testing.T) {
	t.Parallel()
	manager := &fakeCPUListManager{clos: []Clos{{ID: "dedicated", Epoch: 1}}}
	plugin := newTestPlugin(manager)
	view := &model.CPUSetPartitionView{Dedicated: machine.NewCPUSet(1)}

	require.NoError(t, plugin.CPUSetAdjustmentHandler(context.Background(), handlerContext(view)))
	manager.clos = []Clos{{ID: "dedicated", Epoch: 2}}
	require.NoError(t, plugin.CPUSetAdjustmentHandler(context.Background(), handlerContext(view)))
	require.Equal(t, []cpuListWrite{
		{closID: "dedicated", target: "1"},
		{closID: "dedicated", target: "1"},
	}, manager.writes)
}

func TestCPUListPluginDisabledTransitionClearsManagedClos(t *testing.T) {
	t.Parallel()
	manager := &fakeCPUListManager{clos: []Clos{
		{ID: consts.ResctrlGroupDedicated, Epoch: 1},
		{ID: consts.ResctrlGroupReclaim, Epoch: 1},
	}}
	plugin := NewCPUListPluginWithManager(&qrmresctrl.ResctrlConfig{}, manager, supportedCATCapabilityProvider())

	require.NoError(t, plugin.CPUSetAdjustmentDisabledHandler(context.Background(), handlerContext(nil)))
	require.Equal(t, []cpuListWrite{
		{closID: consts.ResctrlGroupDedicated, target: ""},
	}, manager.writes)
}

func TestCPUListPluginDisabledTransitionPreservesExternalClosWithoutExplicitSkip(t *testing.T) {
	t.Parallel()
	manager := &fakeCPUListManager{clos: []Clos{
		{ID: "external", Epoch: 1},
		{ID: "custom", Epoch: 1},
	}}
	plugin := NewCPUListPluginWithManager(&qrmresctrl.ResctrlConfig{}, manager, supportedCATCapabilityProvider())

	require.NoError(t, plugin.CPUSetAdjustmentDisabledHandler(context.Background(), handlerContext(nil)))
	require.Empty(t, manager.writes)
}

func TestCPUListPluginDisabledTransitionDoesNotUseSkipCleanupAsWriteProtection(t *testing.T) {
	t.Parallel()
	manager := &fakeCPUListManager{clos: []Clos{
		{ID: consts.ResctrlGroupDedicated, Epoch: 1},
		{ID: "share-03", Epoch: 1},
	}}
	plugin := NewCPUListPluginWithManager(&qrmresctrl.ResctrlConfig{
		CPUSetPoolToSharedSubgroup: map[string]int{"share-a": 3},
		SkipCleanupClosIDs:         sets.NewString("share-03"),
	}, manager, supportedCATCapabilityProvider())

	require.NoError(t, plugin.CPUSetAdjustmentDisabledHandler(context.Background(), handlerContext(nil)))
	require.Equal(t, []cpuListWrite{
		{closID: consts.ResctrlGroupDedicated, target: ""},
		{closID: "share-03", target: ""},
	}, manager.writes)
}

func TestCPUListPluginScopesSharedClosToConfiguredSubgroups(t *testing.T) {
	t.Parallel()
	clos := []Clos{
		{ID: consts.ResctrlGroupDedicated, Epoch: 1},
		{ID: consts.ResctrlGroupReclaim, Epoch: 1},
		{ID: consts.ResctrlGroupSystem, Epoch: 1},
		{ID: consts.ResctrlGroupShare, Epoch: 1},
		{ID: consts.ResctrlGroupDefaultShare, Epoch: 1},
		{ID: "custom-default", Epoch: 1},
		{ID: "share-03", Epoch: 1},
		{ID: "share-05", Epoch: 1},
		{ID: "share-99", Epoch: 1},
		{ID: "shared-07", Epoch: 1},
		{ID: "shared-foreign", Epoch: 1},
	}
	config := &qrmresctrl.ResctrlConfig{
		CPUSetPoolToSharedSubgroup: map[string]int{"share-a": 3},
		DefaultClosIDs:             []string{"custom-default"},
	}

	t.Run("normal reconcile", func(t *testing.T) {
		t.Parallel()
		manager := &fakeCPUListManager{clos: clos}
		plugin := NewCPUListPluginWithManager(config, manager, supportedCATCapabilityProvider())

		require.NoError(t, plugin.CPUSetAdjustmentHandler(context.Background(), handlerContext(
			&model.CPUSetPartitionView{
				Dedicated: machine.NewCPUSet(1),
				SharePoolMap: map[string]machine.CPUSet{
					"share-a":  machine.NewCPUSet(2),
					"unmapped": machine.NewCPUSet(3),
				},
			},
		)))
		require.Equal(t, []cpuListWrite{
			{closID: consts.ResctrlGroupDedicated, target: "1"},
			{closID: consts.ResctrlGroupDefaultShare, target: "3"},
			{closID: "share-03", target: "2"},
		}, manager.writes)
	})

	t.Run("disabled transition", func(t *testing.T) {
		t.Parallel()
		manager := &fakeCPUListManager{clos: clos}
		plugin := NewCPUListPluginWithManager(config, manager, supportedCATCapabilityProvider())

		require.NoError(t, plugin.CPUSetAdjustmentDisabledHandler(context.Background(), handlerContext(nil)))
		require.Equal(t, []cpuListWrite{
			{closID: consts.ResctrlGroupDedicated, target: ""},
			{closID: consts.ResctrlGroupDefaultShare, target: ""},
			{closID: "share-03", target: ""},
		}, manager.writes)
	})
}
