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
