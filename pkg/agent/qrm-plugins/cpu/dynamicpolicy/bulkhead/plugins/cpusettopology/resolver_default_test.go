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

	"github.com/kubewharf/katalyst-core/pkg/util/cgroup/client"
)

// These tests lock the boundary around the test-only resolver override. The
// production constructor must retain the nil-override fast path; only synthetic
// hierarchy tests may opt into explicit resolution.

// TestNewCPUSetTopologyPluginUsesDefaultResolverWithoutOverride proves normal
// production construction stores no test override and safely falls back to the
// host cgroup resolver.
func TestNewCPUSetTopologyPluginUsesDefaultResolverWithoutOverride(t *testing.T) {
	t.Parallel()
	p := NewCPUSetTopologyPluginWithCgroupClient(nil, nil).(*CPUSetTopologyPlugin)
	if p.resolveContainerRel != nil {
		t.Fatal("production constructor unexpectedly stored a resolver override")
	}
	// The nil override path must fall back without panicking.
	_, _ = p.resolveContainerRelSafe("pod", "cid")
}

// mockCgroupClient only supplies cgroup I/O for this constructor test. Resolver
// behavior is injected as a separate dependency and is intentionally not part of
// the cgroup-client interface.
type mockCgroupClient struct {
	client.FakeCgroupClient
}

// TestNewCPUSetTopologyPluginUsesExplicitResolverOverride proves the test-only
// constructor uses exactly the explicitly supplied synthetic resolver.
func TestNewCPUSetTopologyPluginUsesExplicitResolverOverride(t *testing.T) {
	t.Parallel()
	resolver := func(string, string) (string, error) { return "mock-rel", nil }
	p := NewCPUSetTopologyPluginWithDependencies(nil, func() client.CgroupClient {
		return &mockCgroupClient{}
	}, resolver).(*CPUSetTopologyPlugin)

	rel, err := p.resolveContainerRelSafe("pod", "cid")
	if err != nil || rel != "mock-rel" {
		t.Fatalf("explicit resolver returned rel=%q err=%v, want rel=mock-rel, err=nil", rel, err)
	}
}

// TestZeroValuePluginFallsBackToDefaultResolver protects direct literal
// construction in package-local tests from a nil-function panic.
func TestZeroValuePluginFallsBackToDefaultResolver(t *testing.T) {
	t.Parallel()
	p := &CPUSetTopologyPlugin{}
	_, _ = p.resolveContainerRelSafe("pod", "cid")
}
