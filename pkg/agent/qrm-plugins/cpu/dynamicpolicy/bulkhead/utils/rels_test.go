package utils

import (
	"testing"

	bulkheadconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/qrm/bulkhead"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func TestCollectActiveRelsIncludesRootPartitionsSiblingsAndPerNUMA(t *testing.T) {
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

	specs := BuildTopologyNodeSpecsFromView(cfg, view, []string{"sibling"}, nil)
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
