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
