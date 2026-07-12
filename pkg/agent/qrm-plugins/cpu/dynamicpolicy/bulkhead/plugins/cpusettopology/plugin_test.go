package cpusettopology

import (
	"testing"

	bulkheadconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/qrm/bulkhead"
)

func TestCPUSetTopologyPluginIsConfiguredReclaimNUMARel(t *testing.T) {
	p := &CPUSetTopologyPlugin{
		cfg: bulkheadconfig.BulkheadConfiguration{
			BulkheadReclaimNumaPrefixes: []string{"reclaimed/reclaimed-", "/foo/bar-"},
		},
	}

	for _, rel := range []string{
		"reclaimed/reclaimed-0",
		"/reclaimed/reclaimed-1",
		"foo/bar-2",
	} {
		if !p.isConfiguredReclaimNUMARel(rel) {
			t.Fatalf("expected %q to be recognized as reclaim NUMA rel", rel)
		}
	}

	for _, rel := range []string{
		"reclaimed/reclaimed",
		"reclaimed/reclaimed-a",
		"reclaimed/reclaimed-0-extra",
		"foo/bar",
		"other/bar-0",
	} {
		if p.isConfiguredReclaimNUMARel(rel) {
			t.Fatalf("expected %q not to be recognized as reclaim NUMA rel", rel)
		}
	}
}
