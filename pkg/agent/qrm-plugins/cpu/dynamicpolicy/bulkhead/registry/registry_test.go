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

package registry

import (
	"reflect"
	"sync/atomic"
	"testing"

	cgroupclient "github.com/kubewharf/katalyst-core/pkg/util/cgroup/client"
)

func TestNewDefaultPluginsPreservesOrder(t *testing.T) {
	t.Parallel()

	plugins, err := NewDefaultPlugins(nil)
	if err != nil {
		t.Fatalf("NewDefaultPlugins failed: %v", err)
	}
	got := make([]string, 0, len(plugins))
	cpuMetricsCount := 0
	for _, plugin := range plugins {
		name := plugin.Name()
		got = append(got, name)
		if name == "cpu_metrics" {
			cpuMetricsCount++
		}
	}
	want := []string{"cpuset_topology", "cpuset_mems", "workqueue", "system_service", "rdt_cpulist", "rdt_cat", "cpu_metrics"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected plugin order, got %v want %v", got, want)
	}
	if cpuMetricsCount != 1 {
		t.Fatalf("cpu_metrics registration count = %d, want exactly 1", cpuMetricsCount)
	}
}

// TestNewDefaultPluginsWithCgroupClientInjectsFactory verifies the cgroup client
// factory is invoked when building the default plugin set, and that a nil
// factory still produces the default (production) client path.
func TestNewDefaultPluginsWithCgroupClientInjectsFactory(t *testing.T) {
	t.Parallel()

	var calls int32
	injected := &recordingCgroupClient{}
	plugins, err := NewDefaultPluginsWithCgroupClient(nil, func() cgroupclient.CgroupClient {
		atomic.AddInt32(&calls, 1)
		return injected
	})
	if err != nil {
		t.Fatalf("NewDefaultPluginsWithCgroupClient failed: %v", err)
	}
	if len(plugins) == 0 {
		t.Fatal("expected non-empty plugin set")
	}
	// The topology plugin, mems plugin, and systemservice plugin all build a
	// cgroup client, so the factory must be invoked at least once.
	if atomic.LoadInt32(&calls) == 0 {
		t.Fatal("injected cgroup client factory was never called")
	}

	// A nil factory must fall back to the production default without error.
	if _, err := NewDefaultPluginsWithCgroupClient(nil, nil); err != nil {
		t.Fatalf("nil factory should default to production client: %v", err)
	}
}

// recordingCgroupClient is a minimal CgroupClient used only to confirm the
// injected factory returns the same instance the topology plugin stores.
type recordingCgroupClient struct {
	cgroupclient.FakeCgroupClient
}
