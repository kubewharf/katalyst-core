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

package cat

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	bulkheadapi "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/api"
	qrmresctrlmanager "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/resctrl"
	dynamicconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	qrmconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/adminqos/qrm"
	qrmresctrl "github.com/kubewharf/katalyst-core/pkg/config/agent/qrm/resctrl"
	"github.com/kubewharf/katalyst-core/pkg/util/external/rdt"
)

type fakeClosManager struct {
	clos []qrmresctrlmanager.CPUListClos
}

func (m *fakeClosManager) ListManagedClos(context.Context) ([]qrmresctrlmanager.CPUListClos, error) {
	return append([]qrmresctrlmanager.CPUListClos(nil), m.clos...), nil
}

type catWrite struct {
	clos string
	mask map[int]uint64
}

type fakeRDTManager struct {
	writes      []catWrite
	invalidated []string
	failClos    string
	failOnce    bool
}

func (*fakeRDTManager) CheckSupportRDT() (bool, error)    { return true, nil }
func (*fakeRDTManager) InitRDT() error                    { return nil }
func (*fakeRDTManager) ApplyTasks(string, []string) error { return nil }
func (m *fakeRDTManager) ApplyCAT(clos string, mask map[int]uint64) error {
	m.writes = append(m.writes, catWrite{clos: clos, mask: mask})
	if clos == m.failClos && m.failOnce {
		m.failOnce = false
		return errors.New("injected CAT write failure")
	}
	return nil
}
func (*fakeRDTManager) ApplyMBA(string, map[int]int) error { return nil }
func (*fakeRDTManager) RunClosResourceUpdate(_ string, update func() (bool, error)) error {
	_, err := update()
	return err
}

func (m *fakeRDTManager) InvalidateClos(clos string) {
	m.invalidated = append(m.invalidated, clos)
}

type fakeCapabilityProvider struct {
	capabilities map[int]rdt.CATCapability
	err          error
}

func (p fakeCapabilityProvider) GetCATCapabilities() (map[int]rdt.CATCapability, error) {
	return p.capabilities, p.err
}

func periodicalContext(conf *dynamicconfig.Configuration) bulkheadapi.PeriodicalHandlerContext {
	return bulkheadapi.PeriodicalHandlerContext{DynamicConf: conf}
}

func catExpr(raw string) qrmconfig.CATWaysExpression {
	expr, err := qrmconfig.ParseCATWaysExpression(raw)
	if err != nil {
		panic(err)
	}
	return expr
}

func TestCATPluginBuildsSymmetricDomainMasksAndPrefersDirectClosOverride(t *testing.T) {
	t.Parallel()
	manager := &fakeRDTManager{}
	plugin := NewCATPluginWithManager(&qrmresctrl.ResctrlConfig{
		CPUSetPoolToSharedSubgroup: map[string]int{"batch": 3},
	}, &fakeClosManager{clos: []qrmresctrlmanager.CPUListClos{
		{ID: "dedicated"}, {ID: "share-03"}, {ID: "external"},
	}}, manager, fakeCapabilityProvider{capabilities: map[int]rdt.CATCapability{
		0: {CBMMask: 0xff, MinCBMBits: 1},
		1: {CBMMask: 0xff, MinCBMBits: 1},
	}})

	_, err := plugin.reconcileConfig(context.Background(), qrmconfig.DynamicBulkheadRDTConfiguration{
		DefaultCATWays: catExpr("2"),
		ClosCATWays: map[string]qrmconfig.CATWaysExpression{
			"batch":    catExpr("2"),
			"share-03": catExpr("2"),
		},
	})

	require.NoError(t, err)
	require.Equal(t, []catWrite{
		{clos: "dedicated", mask: map[int]uint64{0: 0x03, 1: 0x03}},
		{clos: "external", mask: map[int]uint64{0: 0x30, 1: 0x30}},
		{clos: "share-03", mask: map[int]uint64{0: 0x0c, 1: 0x0c}},
	}, manager.writes)
}

func TestCATPluginBuildsExpressionTargets(t *testing.T) {
	t.Parallel()
	manager := &fakeRDTManager{}
	plugin := NewCATPluginWithManager(&qrmresctrl.ResctrlConfig{}, &fakeClosManager{
		clos: []qrmresctrlmanager.CPUListClos{{ID: "share-01"}, {ID: "share-00"}},
	}, manager, fakeCapabilityProvider{capabilities: map[int]rdt.CATCapability{
		0: {CBMMask: 0x7ff, MinCBMBits: 2},
		1: {CBMMask: 0xff, MinCBMBits: 2},
	}})

	applied, err := plugin.reconcileConfig(context.Background(), qrmconfig.DynamicBulkheadRDTConfiguration{
		DefaultCATWays: catExpr("MaxCATWays"),
		ClosCATWays: map[string]qrmconfig.CATWaysExpression{
			"share-00": catExpr("MinCATWays"),
			"share-01": catExpr("MaxCATWays-2"),
		},
	})

	require.NoError(t, err)
	require.True(t, applied)
	require.Equal(t, []catWrite{
		{clos: "share-00", mask: map[int]uint64{0: 0x600, 1: 0xc0}},
		{clos: "share-01", mask: map[int]uint64{0: 0x1ff, 1: 0x3f}},
	}, manager.writes)
}

func TestCATPluginExclusiveClosIDsReserveNonOverlappingWays(t *testing.T) {
	t.Parallel()
	manager := &fakeRDTManager{}
	plugin := NewCATPluginWithManager(&qrmresctrl.ResctrlConfig{}, &fakeClosManager{
		clos: []qrmresctrlmanager.CPUListClos{{ID: "peer-b"}, {ID: "peer-c"}, {ID: "clos-a"}},
	}, manager, fakeCapabilityProvider{capabilities: map[int]rdt.CATCapability{
		0: {CBMMask: 0xff, MinCBMBits: 2},
	}})
	exclusiveClosIDs := []string{"clos-a"}
	conf := qrmconfig.DynamicBulkheadRDTConfiguration{
		DefaultCATWays: catExpr("MinCATWays"),
		CATPolicy: qrmconfig.CATPolicy{
			ExclusiveClosIDs: exclusiveClosIDs,
		},
	}

	applied, err := plugin.reconcileConfig(context.Background(), conf)
	require.NoError(t, err)
	require.True(t, applied)

	require.Equal(t, []catWrite{
		{clos: "clos-a", mask: map[int]uint64{0: 0x03}},
		{clos: "peer-b", mask: map[int]uint64{0: 0x0c}},
		{clos: "peer-c", mask: map[int]uint64{0: 0x30}},
	}, manager.writes)
}

func TestCATPluginExclusiveZeroTargetPreservesRemainingWays(t *testing.T) {
	t.Parallel()
	manager := &fakeRDTManager{}
	plugin := NewCATPluginWithManager(&qrmresctrl.ResctrlConfig{}, &fakeClosManager{
		clos: []qrmresctrlmanager.CPUListClos{{ID: "clos-a"}, {ID: "peer-b"}},
	}, manager, fakeCapabilityProvider{capabilities: map[int]rdt.CATCapability{
		0: {CBMMask: 0xffff, MinCBMBits: 0},
	}})

	_, err := plugin.reconcileConfig(context.Background(), qrmconfig.DynamicBulkheadRDTConfiguration{
		DefaultCATWays: catExpr("MaxCATWays-MinCATWays"),
		ClosCATWays: map[string]qrmconfig.CATWaysExpression{
			"clos-a": catExpr("MinCATWays"),
		},
		CATPolicy: qrmconfig.CATPolicy{
			ExclusiveClosIDs: []string{"clos-a"},
		},
	})

	require.NoError(t, err)
	require.Equal(t, []catWrite{
		{clos: "clos-a", mask: map[int]uint64{0: 0}},
		{clos: "peer-b", mask: map[int]uint64{0: 0xffff}},
	}, manager.writes)
	require.Zero(t, manager.writes[0].mask[0]&manager.writes[1].mask[0])
}

func TestCATPluginExclusiveClosIDsRespectPlacement(t *testing.T) {
	t.Parallel()
	manager := &fakeRDTManager{}
	plugin := NewCATPluginWithManager(&qrmresctrl.ResctrlConfig{}, &fakeClosManager{
		clos: []qrmresctrlmanager.CPUListClos{{ID: "clos-a"}, {ID: "peer-b"}},
	}, manager, fakeCapabilityProvider{capabilities: map[int]rdt.CATCapability{
		0: {CBMMask: 0xff, MinCBMBits: 2, BitUsageByType: map[string]uint64{"S": 0x0f, "X": 0xf0}},
	}})
	exclusiveClosIDs := []string{"clos-a"}

	applied, err := plugin.reconcileConfig(context.Background(), qrmconfig.DynamicBulkheadRDTConfiguration{
		DefaultCATWays: catExpr("MinCATWays"),
		CATPolicy: qrmconfig.CATPolicy{
			ExclusiveClosIDs: exclusiveClosIDs,
			ClosPlacements: map[string]qrmconfig.CATPlacementPolicy{
				"clos-a": {
					AllowedBitUsages: []qrmconfig.CATBitUsage{qrmconfig.CATBitUsageSoftware, qrmconfig.CATBitUsageExclusive},
					Direction:        qrmconfig.CATAllocationDirectionHigh,
				},
			},
		},
	})

	require.NoError(t, err)
	require.True(t, applied)
	require.Equal(t, []catWrite{
		{clos: "clos-a", mask: map[int]uint64{0: 0xc0}},
		{clos: "peer-b", mask: map[int]uint64{0: 0x03}},
	}, manager.writes)
	require.Zero(t, manager.writes[0].mask[0]&manager.writes[1].mask[0])
}

func TestCATPluginAllowedBitUsageAllUsesCBMMask(t *testing.T) {
	t.Parallel()
	manager := &fakeRDTManager{}
	plugin := NewCATPluginWithManager(&qrmresctrl.ResctrlConfig{}, &fakeClosManager{
		clos: []qrmresctrlmanager.CPUListClos{{ID: "clos-a"}},
	}, manager, fakeCapabilityProvider{capabilities: map[int]rdt.CATCapability{
		0: {CBMMask: 0xff, MinCBMBits: 2, BitUsageByType: map[string]uint64{"S": 0x0f}},
	}})

	applied, err := plugin.reconcileConfig(context.Background(), qrmconfig.DynamicBulkheadRDTConfiguration{
		DefaultCATWays: catExpr("MaxCATWays"),
		CATPolicy: qrmconfig.CATPolicy{
			DefaultPlacement: &qrmconfig.CATPlacementPolicy{
				AllowedBitUsages: []qrmconfig.CATBitUsage{qrmconfig.CATBitUsageAll},
			},
		},
	})

	require.NoError(t, err)
	require.True(t, applied)
	require.Equal(t, []catWrite{
		{clos: "clos-a", mask: map[int]uint64{0: 0xff}},
	}, manager.writes)
}

func TestCATPluginExclusiveClosIDsRejectUnmanagedCLOS(t *testing.T) {
	t.Parallel()
	manager := &fakeRDTManager{}
	plugin := NewCATPluginWithManager(&qrmresctrl.ResctrlConfig{}, &fakeClosManager{
		clos: []qrmresctrlmanager.CPUListClos{{ID: "clos-a"}},
	}, manager, fakeCapabilityProvider{capabilities: map[int]rdt.CATCapability{
		0: {CBMMask: 0xff, MinCBMBits: 2, BitUsageByType: map[string]uint64{"S": 0x0f, "X": 0xf0}},
	}})
	exclusiveClosIDs := []string{"peer-b"}

	_, err := plugin.reconcileConfig(context.Background(), qrmconfig.DynamicBulkheadRDTConfiguration{
		DefaultCATWays: catExpr("MinCATWays"),
		CATPolicy: qrmconfig.CATPolicy{
			ExclusiveClosIDs: exclusiveClosIDs,
		},
	})

	require.ErrorContains(t, err, "is not configured")
	require.Empty(t, manager.writes)
}

func TestCATPluginExclusiveClosIDsRejectDuplicateCLOS(t *testing.T) {
	t.Parallel()
	manager := &fakeRDTManager{}
	plugin := NewCATPluginWithManager(&qrmresctrl.ResctrlConfig{}, &fakeClosManager{
		clos: []qrmresctrlmanager.CPUListClos{{ID: "clos-a"}},
	}, manager, fakeCapabilityProvider{capabilities: map[int]rdt.CATCapability{
		0: {CBMMask: 0xff, MinCBMBits: 2, BitUsageByType: map[string]uint64{"S": 0x0f, "X": 0xf0}},
	}})
	exclusiveClosIDs := []string{"clos-a", "clos-a"}

	_, err := plugin.reconcileConfig(context.Background(), qrmconfig.DynamicBulkheadRDTConfiguration{
		DefaultCATWays: catExpr("MinCATWays"),
		CATPolicy: qrmconfig.CATPolicy{
			ExclusiveClosIDs: exclusiveClosIDs,
		},
	})

	require.ErrorContains(t, err, "duplicate exclusive CLOS ID")
	require.Empty(t, manager.writes)
}

func TestCATPluginRejectsAllWritesWhenAnyDomainCannotSatisfyWays(t *testing.T) {
	t.Parallel()
	manager := &fakeRDTManager{}
	plugin := NewCATPluginWithManager(&qrmresctrl.ResctrlConfig{}, &fakeClosManager{
		clos: []qrmresctrlmanager.CPUListClos{{ID: "dedicated"}},
	}, manager, fakeCapabilityProvider{capabilities: map[int]rdt.CATCapability{
		0: {CBMMask: 0x0f, MinCBMBits: 2},
		1: {CBMMask: 0x03, MinCBMBits: 2},
	}})

	_, err := plugin.reconcileConfig(context.Background(), qrmconfig.DynamicBulkheadRDTConfiguration{
		DefaultCATWays: catExpr("3"),
	})

	require.Error(t, err)
	require.Empty(t, manager.writes)
}

func TestCATPluginDisabledRestoresRootAndManagedClosOnly(t *testing.T) {
	t.Parallel()
	manager := &fakeRDTManager{}
	plugin := NewCATPluginWithManager(&qrmresctrl.ResctrlConfig{}, &fakeClosManager{
		clos: []qrmresctrlmanager.CPUListClos{{ID: "dedicated"}, {ID: "external"}},
	}, manager, fakeCapabilityProvider{capabilities: map[int]rdt.CATCapability{
		0: {CBMMask: 0xff, MinCBMBits: 1},
	}})

	conf := dynamicconfig.NewConfiguration()
	conf.AdminQoSConfiguration.CPUPluginConfiguration.BulkheadConfig.BulkheadRDTConfig.DefaultCATWays = catExpr("2")
	handlerCtx := periodicalContext(conf)
	conf.AdminQoSConfiguration.CPUPluginConfiguration.BulkheadConfig.BulkheadRDTConfig.EnableCAT = true
	require.NoError(t, plugin.PeriodicalHandler(context.Background(), handlerCtx))
	manager.writes = nil
	conf.AdminQoSConfiguration.CPUPluginConfiguration.BulkheadConfig.BulkheadRDTConfig.EnableCAT = false
	require.NoError(t, plugin.PeriodicalHandler(context.Background(), handlerCtx))
	require.Equal(t, []catWrite{
		{clos: "dedicated", mask: map[int]uint64{0: 0x03}},
		{clos: "external", mask: map[int]uint64{0: 0x03}},
	}, manager.writes)
}

func TestCATPluginPartialFailureRollsBackEveryManagedClos(t *testing.T) {
	t.Parallel()
	manager := &fakeRDTManager{failClos: "share-03", failOnce: true}
	plugin := NewCATPluginWithManager(&qrmresctrl.ResctrlConfig{}, &fakeClosManager{
		clos: []qrmresctrlmanager.CPUListClos{{ID: "dedicated"}, {ID: "share-03"}},
	}, manager, fakeCapabilityProvider{capabilities: map[int]rdt.CATCapability{
		0: {CBMMask: 0xff, MinCBMBits: 1},
	}})

	_, err := plugin.reconcileConfig(context.Background(), qrmconfig.DynamicBulkheadRDTConfiguration{
		DefaultCATWays: catExpr("2"),
		ClosCATWays: map[string]qrmconfig.CATWaysExpression{
			"dedicated": catExpr("4"),
			"share-03":  catExpr("4"),
		},
	})

	require.ErrorContains(t, err, "apply CAT")
	require.Equal(t, []catWrite{
		{clos: "dedicated", mask: map[int]uint64{0: 0x0f}},
		{clos: "share-03", mask: map[int]uint64{0: 0xf0}},
		{clos: "dedicated", mask: map[int]uint64{0: 0x03}},
		{clos: "share-03", mask: map[int]uint64{0: 0x03}},
	}, manager.writes)
}

func TestCATPluginPartialFailureWithZeroDefaultRollsBackCapabilityBaseline(t *testing.T) {
	t.Parallel()
	manager := &fakeRDTManager{failClos: "share-03", failOnce: true}
	plugin := NewCATPluginWithManager(&qrmresctrl.ResctrlConfig{}, &fakeClosManager{
		clos: []qrmresctrlmanager.CPUListClos{{ID: "dedicated"}, {ID: "share-03"}},
	}, manager, fakeCapabilityProvider{capabilities: map[int]rdt.CATCapability{
		0: {CBMMask: 0xff, MinCBMBits: 1},
	}})

	_, err := plugin.reconcileConfig(context.Background(), qrmconfig.DynamicBulkheadRDTConfiguration{
		ClosCATWays: map[string]qrmconfig.CATWaysExpression{
			"dedicated": catExpr("4"),
			"share-03":  catExpr("4"),
		},
	})

	require.ErrorContains(t, err, "apply CAT")
	require.Equal(t, []catWrite{
		{clos: "dedicated", mask: map[int]uint64{0: 0x0f}},
		{clos: "share-03", mask: map[int]uint64{0: 0xf0}},
		{clos: "dedicated", mask: map[int]uint64{0: 0xff}},
		{clos: "share-03", mask: map[int]uint64{0: 0xff}},
	}, manager.writes)
}

func TestCATPluginRestartedDisabledStateIdempotentlyRollsBack(t *testing.T) {
	t.Parallel()
	manager := &fakeRDTManager{}
	plugin := NewCATPluginWithManager(&qrmresctrl.ResctrlConfig{}, &fakeClosManager{
		clos: []qrmresctrlmanager.CPUListClos{{ID: "dedicated"}},
	}, manager, fakeCapabilityProvider{capabilities: map[int]rdt.CATCapability{
		0: {CBMMask: 0xff, MinCBMBits: 1},
	}})
	conf := dynamicconfig.NewConfiguration()
	conf.AdminQoSConfiguration.CPUPluginConfiguration.BulkheadConfig.BulkheadRDTConfig.DefaultCATWays = catExpr("2")

	require.NoError(t, plugin.PeriodicalHandler(context.Background(), periodicalContext(conf)))
	require.NoError(t, plugin.PeriodicalHandler(context.Background(), periodicalContext(conf)))

	require.Equal(t, []catWrite{
		{clos: "dedicated", mask: map[int]uint64{0: 0x03}},
		{clos: "dedicated", mask: map[int]uint64{0: 0x03}},
	}, manager.writes)
}

func TestCATPluginRestartedDisabledWithZeroDefaultRestoresCapabilityBaseline(t *testing.T) {
	t.Parallel()
	manager := &fakeRDTManager{}
	plugin := NewCATPluginWithManager(&qrmresctrl.ResctrlConfig{}, &fakeClosManager{
		clos: []qrmresctrlmanager.CPUListClos{{ID: "dedicated"}},
	}, manager, fakeCapabilityProvider{capabilities: map[int]rdt.CATCapability{
		0: {CBMMask: 0xf0, MinCBMBits: 2},
		1: {CBMMask: 0x0f, MinCBMBits: 2},
	}})
	conf := dynamicconfig.NewConfiguration()

	require.NoError(t, plugin.PeriodicalHandler(context.Background(), periodicalContext(conf)))
	require.Equal(t, []catWrite{
		{clos: "dedicated", mask: map[int]uint64{0: 0xf0, 1: 0x0f}},
	}, manager.writes)
}

func TestCATPluginDisabledByTopLevelDisableRDT(t *testing.T) {
	t.Parallel()
	manager := &fakeRDTManager{}
	plugin := NewCATPluginWithManager(&qrmresctrl.ResctrlConfig{}, &fakeClosManager{
		clos: []qrmresctrlmanager.CPUListClos{{ID: "dedicated"}},
	}, manager,
		fakeCapabilityProvider{capabilities: map[int]rdt.CATCapability{
			0: {CBMMask: 0xff, MinCBMBits: 1},
		}})
	conf := dynamicconfig.NewConfiguration()
	conf.AdminQoSConfiguration.CPUPluginConfiguration.BulkheadConfig.BulkheadRDTConfig.EnableCAT = true
	conf.AdminQoSConfiguration.QRMPluginConfiguration.RDTConfig.DisableRDT = true

	require.NoError(t, plugin.PeriodicalHandler(context.Background(), periodicalContext(conf)))
	require.Empty(t, manager.writes)
}

func TestCATPluginRejectsInvalidCATConfigurationBeforeWriting(t *testing.T) {
	t.Parallel()
	manager := &fakeRDTManager{}
	plugin := NewCATPluginWithManager(&qrmresctrl.ResctrlConfig{}, &fakeClosManager{
		clos: []qrmresctrlmanager.CPUListClos{{ID: "dedicated"}},
	}, manager, fakeCapabilityProvider{capabilities: map[int]rdt.CATCapability{
		0: {CBMMask: 0xff, MinCBMBits: 1},
	}})
	conf := dynamicconfig.NewConfiguration()
	conf.AdminQoSConfiguration.CPUPluginConfiguration.BulkheadConfig.BulkheadRDTConfig.EnableCAT = true
	conf.AdminQoSConfiguration.CPUPluginConfiguration.BulkheadConfig.BulkheadRDTConfig.CATConfigError = "invalid cat ways for clos \"share-00\""

	err := plugin.PeriodicalHandler(context.Background(), periodicalContext(conf))

	require.ErrorContains(t, err, "invalid cat configuration")
	require.Empty(t, manager.writes)
}

func TestCATPluginDisabledWithoutPriorTakeoverIsNoop(t *testing.T) {
	t.Parallel()
	manager := &fakeRDTManager{}
	plugin := NewCATPluginWithManager(&qrmresctrl.ResctrlConfig{}, &fakeClosManager{}, manager,
		fakeCapabilityProvider{err: rdt.ErrCATUnsupported})

	conf := dynamicconfig.NewConfiguration()

	require.NoError(t, plugin.PeriodicalHandler(context.Background(), periodicalContext(conf)))
	require.Empty(t, manager.writes)
}

func TestCATPluginValidatesEveryManagedTargetBeforeApplyingAny(t *testing.T) {
	t.Parallel()
	manager := &fakeRDTManager{}
	plugin := NewCATPluginWithManager(&qrmresctrl.ResctrlConfig{
		CPUSetPoolToSharedSubgroup: map[string]int{"batch": 3},
	}, &fakeClosManager{clos: []qrmresctrlmanager.CPUListClos{
		{ID: "dedicated"}, {ID: "share-03"},
	}}, manager, fakeCapabilityProvider{capabilities: map[int]rdt.CATCapability{
		0: {CBMMask: 0x0f, MinCBMBits: 1},
	}})

	_, err := plugin.reconcileConfig(context.Background(), qrmconfig.DynamicBulkheadRDTConfiguration{
		DefaultCATWays: catExpr("2"),
		ClosCATWays: map[string]qrmconfig.CATWaysExpression{
			"share-03": catExpr("5"),
		},
	})

	require.Error(t, err)
	require.Empty(t, manager.writes)
}

func TestCATPluginBuildsZeroTargetForZeroMinimum(t *testing.T) {
	t.Parallel()
	manager := &fakeRDTManager{}
	plugin := NewCATPluginWithManager(&qrmresctrl.ResctrlConfig{}, &fakeClosManager{
		clos: []qrmresctrlmanager.CPUListClos{{ID: "clos-a"}},
	}, manager, fakeCapabilityProvider{capabilities: map[int]rdt.CATCapability{
		0: {CBMMask: 0xffff, MinCBMBits: 0},
		1: {CBMMask: 0xffff, MinCBMBits: 0},
	}})

	_, err := plugin.reconcileConfig(context.Background(), qrmconfig.DynamicBulkheadRDTConfiguration{
		DefaultCATWays: catExpr("MaxCATWays-MinCATWays"),
		ClosCATWays: map[string]qrmconfig.CATWaysExpression{
			"clos-a": catExpr("MinCATWays"),
		},
	})

	require.NoError(t, err)
	require.Equal(t, []catWrite{{
		clos: "clos-a",
		mask: map[int]uint64{0: 0, 1: 0},
	}}, manager.writes)
}

func TestCATPluginRejectsZeroTargetForPositiveMinimum(t *testing.T) {
	t.Parallel()
	_, err := targetForAvailable(
		0,
		rdt.CATCapability{CBMMask: 0x3, MinCBMBits: 2},
		catExpr("MaxCATWays-MinCATWays"),
		qrmconfig.CATAllocationDirectionLow,
		0x3,
	)

	require.ErrorContains(t, err, "domain 0 does not support zero CAT ways")
}

func TestCATPluginRollbackValidatesEveryTargetBeforeRestoringRoot(t *testing.T) {
	t.Parallel()
	manager := &fakeRDTManager{}
	plugin := NewCATPluginWithManager(&qrmresctrl.ResctrlConfig{}, &fakeClosManager{
		clos: []qrmresctrlmanager.CPUListClos{{ID: "dedicated"}},
	}, manager, fakeCapabilityProvider{capabilities: map[int]rdt.CATCapability{
		0: {CBMMask: 0x0f, MinCBMBits: 1},
	}})
	enabled := dynamicconfig.NewConfiguration()
	enabled.AdminQoSConfiguration.CPUPluginConfiguration.BulkheadConfig.BulkheadRDTConfig.EnableCAT = true
	enabled.AdminQoSConfiguration.CPUPluginConfiguration.BulkheadConfig.BulkheadRDTConfig.DefaultCATWays = catExpr("2")
	require.NoError(t, plugin.PeriodicalHandler(context.Background(), periodicalContext(enabled)))
	manager.writes = nil

	disabled := dynamicconfig.NewConfiguration()
	disabled.AdminQoSConfiguration.CPUPluginConfiguration.BulkheadConfig.BulkheadRDTConfig.DefaultCATWays = catExpr("5")
	err := plugin.PeriodicalHandler(context.Background(), periodicalContext(disabled))

	require.Error(t, err)
	require.Empty(t, manager.writes)
}

func TestCATPluginKeepsPhysicalClosIDsDistinct(t *testing.T) {
	t.Parallel()
	manager := &fakeRDTManager{}
	plugin := NewCATPluginWithManager(&qrmresctrl.ResctrlConfig{
		CPUSetPoolToSharedSubgroup: map[string]int{"batch": 3},
	}, &fakeClosManager{clos: []qrmresctrlmanager.CPUListClos{
		{ID: "share-03"}, {ID: "shared-foreign"},
	}}, manager, fakeCapabilityProvider{capabilities: map[int]rdt.CATCapability{
		0: {CBMMask: 0x0f, MinCBMBits: 1},
	}})

	_, err := plugin.reconcileConfig(context.Background(), qrmconfig.DynamicBulkheadRDTConfiguration{
		DefaultCATWays: catExpr("2"),
	})
	require.NoError(t, err)
	require.Equal(t, []catWrite{
		{clos: "share-03", mask: map[int]uint64{0: 0x03}},
		{clos: "shared-foreign", mask: map[int]uint64{0: 0x0c}},
	}, manager.writes)
}

func TestCATPluginNormalizesSharedExpressionOverride(t *testing.T) {
	t.Parallel()
	plugin := NewCATPluginWithManager(&qrmresctrl.ResctrlConfig{
		CPUSetPoolToSharedSubgroup: map[string]int{"batch": 3},
	}, &fakeClosManager{}, &fakeRDTManager{}, fakeCapabilityProvider{})

	resolved, err := plugin.resolveExpressionOverrides(map[string]qrmconfig.CATWaysExpression{
		"batch":     catExpr("3"),
		"shared-03": catExpr("4"),
	})

	require.NoError(t, err)
	require.Equal(t, map[string]qrmconfig.CATWaysExpression{"share-03": catExpr("4")}, resolved)
}

func TestCATPluginKeepsDefaultShareAliasDistinctFromExplicitSubgroup(t *testing.T) {
	t.Parallel()
	plugin := NewCATPluginWithManager(&qrmresctrl.ResctrlConfig{}, &fakeClosManager{}, &fakeRDTManager{}, fakeCapabilityProvider{})

	resolved, err := plugin.resolveExpressionOverrides(map[string]qrmconfig.CATWaysExpression{
		"share":     catExpr("4"),
		"shared-50": catExpr("4"),
		"share-50":  catExpr("2"),
	})

	require.NoError(t, err)
	require.Equal(t, map[string]qrmconfig.CATWaysExpression{
		"shared-50": catExpr("4"),
		"share-50":  catExpr("2"),
	}, resolved)
}

func TestCATPluginRejectsConflictingDefaultShareAliases(t *testing.T) {
	t.Parallel()
	plugin := NewCATPluginWithManager(&qrmresctrl.ResctrlConfig{
		CPUSetPoolToSharedSubgroup: map[string]int{"share": 30},
	}, &fakeClosManager{}, &fakeRDTManager{}, fakeCapabilityProvider{})

	_, err := plugin.resolveExpressionOverrides(map[string]qrmconfig.CATWaysExpression{
		"share":     catExpr("4"),
		"shared-50": catExpr("2"),
	})

	require.EqualError(t, err, `conflicting CAT way configuration for CLOS "shared-50"`)
}

func TestCATPluginTreatsUnsupportedCapabilityAsNoop(t *testing.T) {
	t.Parallel()
	manager := &fakeRDTManager{}
	plugin := NewCATPluginWithManager(&qrmresctrl.ResctrlConfig{}, &fakeClosManager{
		clos: []qrmresctrlmanager.CPUListClos{{ID: "dedicated"}},
	}, manager, fakeCapabilityProvider{err: rdt.ErrCATUnsupported})
	conf := dynamicconfig.NewConfiguration()
	conf.AdminQoSConfiguration.CPUPluginConfiguration.BulkheadConfig.BulkheadRDTConfig.EnableCAT = true
	conf.AdminQoSConfiguration.CPUPluginConfiguration.BulkheadConfig.BulkheadRDTConfig.DefaultCATWays = catExpr("2")

	require.NoError(t, plugin.PeriodicalHandler(context.Background(), periodicalContext(conf)))
	conf.AdminQoSConfiguration.CPUPluginConfiguration.BulkheadConfig.BulkheadRDTConfig.EnableCAT = false
	require.NoError(t, plugin.PeriodicalHandler(context.Background(), periodicalContext(conf)))
	require.Empty(t, manager.writes)
	require.Empty(t, manager.invalidated)
}

func mustWays(t *testing.T, raw string) qrmconfig.CATWaysExpression {
	t.Helper()
	expr, err := qrmconfig.ParseCATWaysExpression(raw)
	if err != nil {
		t.Fatalf("parse ways expression %q: %v", raw, err)
	}
	return expr
}

func maskByID(t *testing.T, clos []qrmresctrlmanager.CPUListClos, def string, overrides map[string]qrmconfig.CATWaysExpression, policy qrmconfig.CATPolicy) map[string]uint64 {
	t.Helper()
	caps := map[int]rdt.CATCapability{0: {CBMMask: 0xff, MinCBMBits: 0}}
	targets, err := buildTargets(caps, clos, mustWays(t, def), overrides, policy)
	if err != nil {
		t.Fatalf("buildTargets: %v", err)
	}
	out := make(map[string]uint64, len(targets))
	for _, tg := range targets {
		out[tg.clos] = tg.mask[0]
	}
	return out
}

// Regression: two non-exclusive CLOSes must pack into disjoint cache ways.
// Before the fix both independently claimed the lowest free slot and the masks
// overlapped completely, defeating RDT cache isolation.
func TestBuildTargetsNonExclusivePacksDisjoint(t *testing.T) {
	t.Parallel()
	clos := []qrmresctrlmanager.CPUListClos{{ID: "shared"}, {ID: "reclaim"}}
	masks := maskByID(t, clos, "2", nil, qrmconfig.CATPolicy{})
	if masks["shared"] != 0x3 {
		t.Errorf("shared mask = %#x, want 0x3", masks["shared"])
	}
	if masks["reclaim"] != 0xc {
		t.Errorf("reclaim mask = %#x, want 0xc", masks["reclaim"])
	}
	if masks["shared"]&masks["reclaim"] != 0 {
		t.Errorf("non-exclusive masks overlap: shared=%#x reclaim=%#x", masks["shared"], masks["reclaim"])
	}
}

// An exclusive CLOS reserves the high ways; the non-exclusive CLOS must fit in
// the remaining low ways without overlapping the exclusive reservation.
func TestBuildTargetsExclusiveThenNonExclusiveDisjoint(t *testing.T) {
	t.Parallel()
	clos := []qrmresctrlmanager.CPUListClos{{ID: "dedicated"}, {ID: "shared"}}
	policy := qrmconfig.CATPolicy{
		ExclusiveClosIDs: []string{"dedicated"},
		ClosPlacements: map[string]qrmconfig.CATPlacementPolicy{
			"dedicated": {Direction: qrmconfig.CATAllocationDirectionHigh},
		},
	}
	overrides := map[string]qrmconfig.CATWaysExpression{"dedicated": mustWays(t, "4")}
	masks := maskByID(t, clos, "2", overrides, policy)
	if masks["dedicated"] != 0xf0 {
		t.Errorf("dedicated mask = %#x, want 0xf0", masks["dedicated"])
	}
	if masks["shared"] != 0x3 {
		t.Errorf("shared mask = %#x, want 0x3", masks["shared"])
	}
	if masks["dedicated"]&masks["shared"] != 0 {
		t.Errorf("exclusive and non-exclusive masks overlap: dedicated=%#x shared=%#x", masks["dedicated"], masks["shared"])
	}
}
