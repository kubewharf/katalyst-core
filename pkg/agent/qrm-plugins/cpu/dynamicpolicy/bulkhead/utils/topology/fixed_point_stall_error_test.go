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

package topology

import (
	"errors"
	"strings"
	"testing"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func TestFixedPointStallErrorDiagnostics(t *testing.T) {
	t.Parallel()
	snap := &CompleteSnapshot{
		Entries: map[string]EntryState{
			"reclaim-root":  {Rel: "reclaim-root", CPUs: machine.MustParse("0-2,64-66")},
			"kubepods/pod1": {Rel: "kubepods/pod1", CPUs: machine.MustParse("3-63")},
		},
		DomainUnion: map[DomainID]machine.CPUSet{
			"reclaim": machine.MustParse("0-2,64-66"),
			"primary": machine.MustParse("3-63,67-127"),
		},
	}
	protected := machine.MustParse("0,5-15,64,69-79")
	targets := map[string]machine.CPUSet{
		"reclaim-root":  machine.MustParse("1-2,65-66"),
		"kubepods/pod1": machine.MustParse("3-63"),
	}
	err := error(&FixedPointStallError{
		ProtectedPending:      protected,
		ProtectedInCurrentDom: protectedPendingOverlapByDomain(snap, protected),
		UnmetTargets:          unmetTargetRels(snap, targets),
	})
	if !errors.Is(err, ErrNoProgress) {
		t.Fatalf("stall must unwrap to ErrNoProgress: %v", err)
	}
	msg := err.Error()
	for _, want := range []string{"reclaim=0,64", "unmet_targets=[reclaim-root]", "fixed-point compilation made no progress"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("missing %q in %q", want, msg)
		}
	}
	if strings.Contains(msg, "kubepods/pod1") {
		t.Fatalf("met target listed as unmet: %q", msg)
	}
}
