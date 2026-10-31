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
	"errors"
	"fmt"
	"testing"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/utils/topology/deadlock"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// TestDeadlockOwnerTypeCharacterization pins the compatibility contract the
// parent topology package relies on while the deadlock_compat shim exists and
// after it is removed: BuildPhasePlan's structural-deadlock outcome must keep
// satisfying errors.As against the owner type
// *deadlock.StructuralV1NonEmptyDeadlock, the probe must still report a
// complete two-atom proof with the owner ProbeComplete label, and the default
// probe budget must remain 4096. The shim only provided type/const/var
// aliases, so this test binds directly to the owner symbols that the shim
// re-exported.
func TestDeadlockOwnerTypeCharacterization(t *testing.T) {
	t.Parallel()
	input := primaryReclaimSwapInput(t, machine.NewCPUSet(0), machine.NewCPUSet(1))
	_, err := BuildPhasePlan(input)
	if err == nil {
		t.Fatal("expected a structural v1 non-empty deadlock error from BuildPhasePlan")
	}

	var owner *deadlock.StructuralV1NonEmptyDeadlock
	if !errors.As(err, &owner) {
		t.Fatalf("BuildPhasePlan error = %v, does not satisfy errors.As(*deadlock.StructuralV1NonEmptyDeadlock)", err)
	}
	if owner == nil || owner.Analysis.Completeness != deadlock.ProbeComplete {
		t.Fatalf("owner analysis = %+v, want complete probe", owner)
	}
	if len(owner.Analysis.Atoms) != 2 {
		t.Fatalf("owner analysis atoms = %d, want 2", len(owner.Analysis.Atoms))
	}

	if deadlock.DefaultDeadlockProbeBudget != 4096 {
		t.Fatalf("deadlock.DefaultDeadlockProbeBudget = %d, want 4096", deadlock.DefaultDeadlockProbeBudget)
	}

	// The witness sentinel must keep an error identity independent of the shim:
	// callers wrap it with %w and still recognize it through errors.Is.
	wrapped := fmt.Errorf("probe aborted: %w", deadlock.ErrIncompleteRequiredCoreReleaseWitness)
	if !errors.Is(wrapped, deadlock.ErrIncompleteRequiredCoreReleaseWitness) {
		t.Fatalf("wrapped sentinel = %v, does not errors.Is match deadlock.ErrIncompleteRequiredCoreReleaseWitness", wrapped)
	}
	if errors.Is(wrapped, errors.New("unrelated")) {
		t.Fatal("wrapped sentinel must not match an unrelated error")
	}
}
