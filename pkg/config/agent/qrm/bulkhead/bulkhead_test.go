package bulkhead

import "testing"

func TestBulkheadReclaimPerNUMAUsesTrimmedPrefix(t *testing.T) {
	cfg := BulkheadConfiguration{BulkheadReclaimNumaPrefixes: []string{"/reclaimed/reclaimed-"}}

	if got := cfg.ReclaimPerNUMA(0, 2); got != "reclaimed/reclaimed-2" {
		t.Fatalf("unexpected rel: %q", got)
	}
	if got := cfg.ReclaimPerNUMA(1, 2); got != "" {
		t.Fatalf("out-of-range prefix should return empty rel, got %q", got)
	}
}
