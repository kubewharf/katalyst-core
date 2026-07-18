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

package utils

import (
	"testing"

	bulkheadconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/qrm/bulkhead"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func TestCollectActiveRelsIncludesRootPartitionsSiblingsAndPerNUMA(t *testing.T) {
	t.Parallel()

	cfg := bulkheadconfig.BulkheadConfiguration{
		BulkheadPrimaryRelPath:      "kubepods",
		BulkheadReclaimRelPaths:     []string{"reclaimed"},
		BulkheadReclaimNumaPrefixes: []string{"reclaimed/reclaimed-"},
		BulkheadPartitionRelPaths:   []string{"kubepods", "burstable"},
	}
	view := &CPUSetPartitionView{
		ReclaimEffectivePerNUMA: map[int]machine.CPUSet{
			1: machine.NewCPUSet(4, 5),
		},
	}

	got := CollectActiveRels(cfg, view, nil, []string{"besteffort"}, nil)
	for _, rel := range []string{"", "kubepods", "reclaimed", "reclaimed/reclaimed-1", "burstable", "besteffort"} {
		if _, ok := got[rel]; !ok {
			t.Fatalf("expected active rel %q in %#v", rel, got)
		}
	}
}

func TestBuildTopologyNodeSpecsFromViewUsesBulkheadConfig(t *testing.T) {
	t.Parallel()

	cfg := bulkheadconfig.BulkheadConfiguration{
		BulkheadPrimaryRelPath:      "kubepods",
		BulkheadReclaimRelPaths:     []string{"reclaimed"},
		BulkheadReclaimNumaPrefixes: []string{"reclaimed/reclaimed-"},
	}
	view := &CPUSetPartitionView{
		NonReclaimPool:          machine.NewCPUSet(0, 1),
		ReclaimEffective:        machine.NewCPUSet(2, 3),
		ReclaimEffectivePerNUMA: map[int]machine.CPUSet{0: machine.NewCPUSet(2)},
	}

	specs, err := BuildTopologyNodeSpecsFromView(cfg, view, []string{"sibling"}, nil)
	if err != nil {
		t.Fatalf("BuildTopologyNodeSpecsFromView: %v", err)
	}
	rels := map[string]struct{}{}
	for _, spec := range specs {
		rels[spec.Rel] = struct{}{}
	}
	for _, rel := range []string{"kubepods", "reclaimed", "reclaimed/reclaimed-0", "sibling"} {
		if _, ok := rels[rel]; !ok {
			t.Fatalf("expected rel %q in specs %#v", rel, specs)
		}
	}
}
