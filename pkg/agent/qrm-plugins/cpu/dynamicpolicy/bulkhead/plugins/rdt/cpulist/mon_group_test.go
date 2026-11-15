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

	bulkheadapi "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/api"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/model"
	qrmresctrlmanager "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/resctrl"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	resctrlutil "github.com/kubewharf/katalyst-core/pkg/util/resctrl"
)

type fakeMonCPUListManager struct {
	*fakeCPUListManager
	groups  []qrmresctrlmanager.MonGroupCPUList
	targets map[string]machine.CPUSet
	err     error
}

func (m *fakeMonCPUListManager) ListMonGroupCPULists(context.Context, string) ([]qrmresctrlmanager.MonGroupCPUList, error) {
	return m.groups, nil
}

func (m *fakeMonCPUListManager) ApplyMonGroupCPULists(_ context.Context, _ string, targets map[string]machine.CPUSet) error {
	m.targets = targets
	return m.err
}

func TestMonGroupCPUListProjectsPodContainerUnion(t *testing.T) {
	manager := &fakeMonCPUListManager{
		fakeCPUListManager: &fakeCPUListManager{clos: []Clos{{ID: "dedicated"}}},
		groups: []qrmresctrlmanager.MonGroupCPUList{
			{Name: "poda", CPUs: machine.NewCPUSet()},
			{Name: "podb", CPUs: machine.NewCPUSet()},
		},
	}
	plugin := newTestPlugin(manager.fakeCPUListManager)
	plugin.manager = manager
	view := &model.CPUSetPartitionView{
		Dedicated: machine.NewCPUSet(1, 2, 3, 4),
		ContainerCPUSetByPod: map[string]map[string]machine.CPUSet{
			"a": {"main": machine.NewCPUSet(1, 2), "sidecar": machine.NewCPUSet(2, 3)},
			"b": {"main": machine.NewCPUSet(4)},
		},
	}
	require.NoError(t, plugin.CPUSetAdjustmentHandler(context.Background(), handlerContext(view)))
	require.Equal(t, map[string]machine.CPUSet{"poda": machine.NewCPUSet(1, 2, 3), "podb": machine.NewCPUSet(4)}, manager.targets)
}

func TestMonGroupCPUListRejectsWholeOverlapChain(t *testing.T) {
	manager := &fakeMonCPUListManager{fakeCPUListManager: &fakeCPUListManager{clos: []Clos{{ID: "shared-50"}}}}
	view := &model.CPUSetPartitionView{
		SharePoolMap: map[string]machine.CPUSet{"share": machine.NewCPUSet(1, 2, 3, 4)},
		ContainerCPUSetByPod: map[string]map[string]machine.CPUSet{
			"a": {"main": machine.NewCPUSet(1, 2)},
			"b": {"main": machine.NewCPUSet(2, 3)},
			"c": {"main": machine.NewCPUSet(3)},
			"d": {"main": machine.NewCPUSet(4)},
		},
	}
	for _, uid := range []string{"a", "b", "c", "d"} {
		manager.groups = append(manager.groups, qrmresctrlmanager.MonGroupCPUList{Name: resctrlutil.PodMonGroupName(uid), CPUs: machine.NewCPUSet()})
	}
	plugin := newTestPlugin(manager.fakeCPUListManager)
	plugin.manager = manager
	require.NoError(t, plugin.CPUSetAdjustmentHandler(context.Background(), handlerContext(view)))
	for _, name := range []string{"poda", "podb", "podc"} {
		require.True(t, manager.targets[name].IsEmpty(), name)
	}
	require.Equal(t, machine.NewCPUSet(4), manager.targets["podd"])
}

func TestMonGroupCPUListPreservesUnknownOwnersAndParentBounds(t *testing.T) {
	manager := &fakeMonCPUListManager{
		fakeCPUListManager: &fakeCPUListManager{clos: []Clos{{ID: "dedicated"}}},
		groups: []qrmresctrlmanager.MonGroupCPUList{
			{Name: "poda", CPUs: machine.NewCPUSet()},
			{Name: "podb", CPUs: machine.NewCPUSet()},
			{Name: "podunknown", CPUs: machine.NewCPUSet(1)},
			{Name: "external", CPUs: machine.NewCPUSet(2)},
		},
	}
	plugin := newTestPlugin(manager.fakeCPUListManager)
	plugin.manager = manager
	view := &model.CPUSetPartitionView{
		Dedicated: machine.NewCPUSet(1, 2, 3),
		ContainerCPUSetByPod: map[string]map[string]machine.CPUSet{
			"a": {"main": machine.NewCPUSet(1, 2)},
			"b": {"main": machine.NewCPUSet(3, 4)},
		},
	}
	require.NoError(t, plugin.CPUSetAdjustmentHandler(context.Background(), handlerContext(view)))
	require.Equal(t, map[string]machine.CPUSet{"poda": machine.NewCPUSet(), "podb": machine.NewCPUSet()}, manager.targets)
}

func TestMonGroupCPUListCountsOverlapBeforeParentFiltering(t *testing.T) {
	manager := &fakeMonCPUListManager{
		fakeCPUListManager: &fakeCPUListManager{clos: []Clos{{ID: "dedicated"}}},
		groups: []qrmresctrlmanager.MonGroupCPUList{
			{Name: "poda", CPUs: machine.NewCPUSet()},
			{Name: "podb", CPUs: machine.NewCPUSet()},
		},
	}
	plugin := newTestPlugin(manager.fakeCPUListManager)
	plugin.manager = manager
	view := &model.CPUSetPartitionView{
		Dedicated: machine.NewCPUSet(1, 2),
		ContainerCPUSetByPod: map[string]map[string]machine.CPUSet{
			"a": {"main": machine.NewCPUSet(1, 3)},
			"b": {"main": machine.NewCPUSet(1, 2)},
		},
	}
	require.NoError(t, plugin.CPUSetAdjustmentHandler(context.Background(), handlerContext(view)))
	require.Equal(t, map[string]machine.CPUSet{"poda": machine.NewCPUSet(), "podb": machine.NewCPUSet()}, manager.targets)
}

func TestMonGroupCPUListIgnoresPodWithoutSiblingMon(t *testing.T) {
	manager := &fakeMonCPUListManager{
		fakeCPUListManager: &fakeCPUListManager{clos: []Clos{{ID: "shared-50"}}},
		groups:             []qrmresctrlmanager.MonGroupCPUList{{Name: "poda", CPUs: machine.NewCPUSet()}},
	}
	plugin := newTestPlugin(manager.fakeCPUListManager)
	plugin.manager = manager
	view := &model.CPUSetPartitionView{
		SharePoolMap: map[string]machine.CPUSet{"share": machine.NewCPUSet(1, 2)},
		ContainerCPUSetByPod: map[string]map[string]machine.CPUSet{
			"a": {"main": machine.NewCPUSet(1, 2)},
			"b": {"main": machine.NewCPUSet(1, 2)},
		},
	}
	require.NoError(t, plugin.CPUSetAdjustmentHandler(context.Background(), handlerContext(view)))
	require.Equal(t, machine.NewCPUSet(1, 2), manager.targets["poda"])
}

func TestMonGroupCPUListWriteFailureIsReturned(t *testing.T) {
	manager := &fakeMonCPUListManager{
		fakeCPUListManager: &fakeCPUListManager{clos: []Clos{{ID: "dedicated"}}},
		groups:             []qrmresctrlmanager.MonGroupCPUList{{Name: "poda", CPUs: machine.NewCPUSet()}},
		err:                errors.New("write denied"),
	}
	plugin := newTestPlugin(manager.fakeCPUListManager)
	plugin.manager = manager
	view := &model.CPUSetPartitionView{
		Dedicated:            machine.NewCPUSet(1),
		ContainerCPUSetByPod: map[string]map[string]machine.CPUSet{"a": {"main": machine.NewCPUSet(1)}},
	}
	require.ErrorContains(t, plugin.CPUSetAdjustmentHandler(context.Background(), handlerContext(view)), "write denied")
	manager.err = nil
	require.NoError(t, plugin.CPUSetAdjustmentHandler(context.Background(), handlerContext(view)))
}

func TestMonGroupCPUListDisabledUsesParentReset(t *testing.T) {
	manager := &fakeMonCPUListManager{
		fakeCPUListManager: &fakeCPUListManager{clos: []Clos{{ID: "dedicated"}}},
		groups: []qrmresctrlmanager.MonGroupCPUList{
			{Name: "poda", CPUs: machine.NewCPUSet(1)},
			{Name: "external", CPUs: machine.NewCPUSet(2)},
		},
	}
	plugin := newTestPlugin(manager.fakeCPUListManager)
	plugin.manager = manager
	require.NoError(t, plugin.CPUSetAdjustmentDisabledHandler(context.Background(), bulkheadapi.HandlerContext{}))
	require.Nil(t, manager.targets)
	require.Equal(t, []cpuListWrite{{closID: "dedicated", target: ""}}, manager.writes)
}
