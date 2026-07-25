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

package topology

import (
	"testing"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func TestDomainGateBlocksGrowUntilSourceReleased(t *testing.T) {
	t.Parallel()

	snapshot := domainSnapshot{
		observedPrimaryDomain: machine.NewCPUSet(0),
		targetPrimaryDomain:   machine.NewCPUSet(0, 1),
		observedReclaimDomain: machine.NewCPUSet(1, 2),
		targetReclaimDomain:   machine.NewCPUSet(2),
		safeUnownedToPrimary:  machine.NewCPUSet(),
		safeUnownedToReclaim:  machine.NewCPUSet(),
	}
	gate := newDomainGate(snapshot)
	if got, want := gate.pendingToPrimary, machine.NewCPUSet(1); !got.Equals(want) {
		t.Fatalf("pendingToPrimary = %s, want %s", got.String(), want.String())
	}
	if got, want := gate.allowedGrowTarget(cpusetDomainPrimary, snapshot.targetPrimaryDomain, snapshot.observedPrimaryDomain), machine.NewCPUSet(0); !got.Equals(want) {
		t.Fatalf("allowed primary grow before release = %s, want %s", got.String(), want.String())
	}

	refreshed := snapshot
	refreshed.observedReclaimDomain = machine.NewCPUSet(2)
	released := gate.publishReleased(cpusetDomainReclaim, machine.NewCPUSet(1), refreshed)
	if got, want := released, machine.NewCPUSet(1); !got.Equals(want) {
		t.Fatalf("actuallyReleased = %s, want %s", got.String(), want.String())
	}
	if got, want := gate.releasedToPrimary, machine.NewCPUSet(1); !got.Equals(want) {
		t.Fatalf("releasedToPrimary = %s, want %s", got.String(), want.String())
	}
	if got, want := gate.allowedGrowTarget(cpusetDomainPrimary, snapshot.targetPrimaryDomain, snapshot.observedPrimaryDomain), machine.NewCPUSet(0, 1); !got.Equals(want) {
		t.Fatalf("allowed primary grow after release = %s, want %s", got.String(), want.String())
	}
}

func TestDomainGateDoesNotPublishCleanupAsReleased(t *testing.T) {
	t.Parallel()

	snapshot := domainSnapshot{
		observedPrimaryDomain: machine.NewCPUSet(0),
		targetPrimaryDomain:   machine.NewCPUSet(0),
		observedReclaimDomain: machine.NewCPUSet(1, 2),
		targetReclaimDomain:   machine.NewCPUSet(1),
		safeUnownedToPrimary:  machine.NewCPUSet(),
		safeUnownedToReclaim:  machine.NewCPUSet(),
	}
	gate := newDomainGate(snapshot)
	if got, want := gate.cleanupPendingReclaim, machine.NewCPUSet(2); !got.Equals(want) {
		t.Fatalf("cleanupPendingReclaim = %s, want %s", got.String(), want.String())
	}

	refreshed := snapshot
	refreshed.observedReclaimDomain = machine.NewCPUSet(1)
	released := gate.publishReleased(cpusetDomainReclaim, machine.NewCPUSet(2), refreshed)
	if got, want := released, machine.NewCPUSet(2); !got.Equals(want) {
		t.Fatalf("actuallyReleased cleanup = %s, want %s", got.String(), want.String())
	}
	if !gate.releasedToPrimary.IsEmpty() {
		t.Fatalf("releasedToPrimary = %s, want empty for cleanup CPU", gate.releasedToPrimary.String())
	}
	if got, want := gate.allowedGrowTarget(cpusetDomainPrimary, machine.NewCPUSet(0, 2), snapshot.observedPrimaryDomain), machine.NewCPUSet(0); !got.Equals(want) {
		t.Fatalf("allowed primary grow with cleanup CPU = %s, want %s", got.String(), want.String())
	}
}
