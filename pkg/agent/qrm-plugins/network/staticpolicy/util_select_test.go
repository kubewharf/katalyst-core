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

package staticpolicy

import (
	"testing"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/network/state"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func nicList(names ...string) []machine.InterfaceInfo {
	nics := make([]machine.InterfaceInfo, 0, len(names))
	for _, name := range names {
		nics = append(nics, machine.InterfaceInfo{Name: name})
	}
	return nics
}

// fakeState only implements GetMachineState so that selectOneNIC can consult
// the per-NIC pod counts for the balance policy; all other state.State methods
// are embedded from the nil interface and never called by these tests.
type fakeState struct {
	state.State
	machineState state.NICMap
}

func (f *fakeState) GetMachineState() state.NICMap { return f.machineState }

func nicMapWithPodCounts(counts map[string]int) state.NICMap {
	m := make(state.NICMap, len(counts))
	for name, count := range counts {
		podEntries := make(state.PodEntries, count)
		for i := 0; i < count; i++ {
			podEntries[string(rune('p'+i))] = nil
		}
		m[name] = &state.NICState{PodEntries: podEntries}
	}
	return m
}

func TestSelectOneNICBalance(t *testing.T) {
	t.Parallel()

	p := &StaticPolicy{
		nicSelectionPolicy: BalanceOne,
		state:              &fakeState{machineState: nicMapWithPodCounts(map[string]int{"eth0": 3, "eth1": 0, "eth2": 2})},
	}

	got := p.selectOneNIC(nicList("eth0", "eth1", "eth2"))
	if got.Name != "eth1" {
		t.Fatalf("balance policy expected to pick the NIC with the least pods (eth1), got %q", got.Name)
	}
}

func TestSelectOneNICBalanceTieBreaksByOrder(t *testing.T) {
	t.Parallel()

	p := &StaticPolicy{
		nicSelectionPolicy: BalanceOne,
		state:              &fakeState{machineState: nicMapWithPodCounts(map[string]int{"eth0": 3, "eth1": 0, "eth2": 0})},
	}

	got := p.selectOneNIC(nicList("eth0", "eth1", "eth2"))
	if got.Name != "eth1" {
		t.Fatalf("balance tie should pick the first candidate, expected eth1, got %q", got.Name)
	}
}

func TestSelectOneNICBalanceMissingCountTreatedAsZero(t *testing.T) {
	t.Parallel()

	p := &StaticPolicy{
		nicSelectionPolicy: BalanceOne,
		state:              &fakeState{machineState: nicMapWithPodCounts(map[string]int{"eth0": 5})},
	}

	got := p.selectOneNIC(nicList("eth0", "eth1"))
	if got.Name != "eth1" {
		t.Fatalf("balance should treat a NIC missing from state as 0 pods, expected eth1, got %q", got.Name)
	}
}

func TestSelectOneNICPolicies(t *testing.T) {
	t.Parallel()

	nics := nicList("eth0", "eth1", "eth2")

	if got := (&StaticPolicy{nicSelectionPolicy: FirstOne}).selectOneNIC(nics); got.Name != "eth0" {
		t.Fatalf("first policy expected eth0, got %q", got.Name)
	}
	if got := (&StaticPolicy{nicSelectionPolicy: LastOne}).selectOneNIC(nics); got.Name != "eth2" {
		t.Fatalf("last policy expected eth2, got %q", got.Name)
	}
	// unknown policy falls back to the last candidate.
	if got := (&StaticPolicy{nicSelectionPolicy: "unknown"}).selectOneNIC(nics); got.Name != "eth2" {
		t.Fatalf("unknown policy should fallback to last, got %q", got.Name)
	}
}

func TestSelectOneNICEmpty(t *testing.T) {
	t.Parallel()

	got := (&StaticPolicy{nicSelectionPolicy: BalanceOne}).selectOneNIC(nil)
	if got.Name != "" {
		t.Fatalf("empty NIC list should return zero InterfaceInfo, got %q", got.Name)
	}
}
