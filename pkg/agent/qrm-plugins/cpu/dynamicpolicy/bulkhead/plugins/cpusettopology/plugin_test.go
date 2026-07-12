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
	"context"
	"errors"
	"testing"
	"time"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	bulkheadapi "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/api"
	bulkheadutils "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/utils"
	bypassutil "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/util"
	bulkheadconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/qrm/bulkhead"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	metapod "github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	cgroupclient "github.com/kubewharf/katalyst-core/pkg/util/cgroup/client"
	cgcommon "github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func TestCPUSetTopologyPluginIsConfiguredReclaimNUMARel(t *testing.T) {
	t.Parallel()

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

type fakeCgroupClient struct {
	cgroupclient.FakeCgroupClient

	existing map[string]bool
	writes   map[string]string
	pruned   map[string]struct{}
	listErr  error
}

func (f *fakeCgroupClient) StatDir(_ context.Context, rel string) (time.Time, error) {
	if f.existing[rel] {
		return time.Time{}, nil
	}
	return time.Time{}, errors.New("missing")
}

func (f *fakeCgroupClient) Version(context.Context) cgroupclient.CgroupVersion {
	return cgroupclient.CgroupVersionV1
}

func (f *fakeCgroupClient) ApplyCPUSet(_ context.Context, rel string, data *cgcommon.CPUSetData) error {
	if f.writes == nil {
		f.writes = map[string]string{}
	}
	f.writes[rel] = data.CPUs
	return nil
}

func (f *fakeCgroupClient) Prune(active map[string]struct{}) {
	f.pruned = active
}

func (f *fakeCgroupClient) ListChildren(context.Context, string) ([]string, error) {
	return nil, f.listErr
}

func TestCPUSetTopologyPluginReconcilesPrimaryWhenReclaimEmpty(t *testing.T) {
	t.Parallel()

	cg := &fakeCgroupClient{existing: map[string]bool{
		"primary": true,
		"reclaim": true,
	}}
	p := &CPUSetTopologyPlugin{
		cfg: bulkheadconfig.BulkheadConfiguration{
			BulkheadPrimaryRelPath:  "primary",
			BulkheadReclaimRelPaths: []string{"reclaim"},
		},
		cgroup: cg,
	}

	err := p.CPUSetAdjustmentHandler(context.Background(), bulkheadapi.HandlerContext{
		View: &bulkheadutils.CPUSetPartitionView{
			NonReclaimPool:   machine.NewCPUSet(0, 1, 2, 3),
			ReclaimEffective: machine.NewCPUSet(),
		},
	})
	if err != nil {
		t.Fatalf("CPUSetAdjustmentHandler: %v", err)
	}
	if got := cg.writes["primary"]; got != "0-3" {
		t.Fatalf("primary cpuset = %q, want 0-3; writes=%v", got, cg.writes)
	}
	if _, ok := cg.pruned["primary"]; !ok {
		t.Fatalf("primary rel not pruned as active: %#v", cg.pruned)
	}
}

func TestCPUSetTopologyPluginReturnsSiblingDiscoveryError(t *testing.T) {
	t.Parallel()

	cg := &fakeCgroupClient{
		existing: map[string]bool{"primary": true, "reclaim": true},
		listErr:  errors.New("list failed"),
	}
	p := &CPUSetTopologyPlugin{
		cfg: bulkheadconfig.BulkheadConfiguration{
			BulkheadPrimaryRelPath:        "primary",
			BulkheadReclaimRelPaths:       []string{"reclaim"},
			EnableBulkheadReclaimSiblings: true,
		},
		cgroup: cg,
	}

	err := p.CPUSetAdjustmentHandler(context.Background(), bulkheadapi.HandlerContext{
		View: &bulkheadutils.CPUSetPartitionView{
			NonReclaimPool:   machine.NewCPUSet(0, 1),
			ReclaimEffective: machine.NewCPUSet(2, 3),
		},
	})
	if err == nil {
		t.Fatalf("expected sibling discovery error")
	}
}

func TestCPUSetTopologyPluginSkipsExpectedCPUSetForMissingPod(t *testing.T) {
	t.Parallel()

	p := &CPUSetTopologyPlugin{}
	expected, err := p.buildExpectedCPUSetByRel(context.Background(), bulkheadapi.HandlerContext{
		BypassCPUSetAdjustmentHandlerCtx: bypassutil.BypassCPUSetAdjustmentHandlerCtx{
			MetaServer: &metaserver.MetaServer{
				MetaAgent: &agent.MetaAgent{
					PodFetcher: &metapod.PodFetcherStub{},
				},
			},
		},
		View: &bulkheadutils.CPUSetPartitionView{
			ContainerCPUSetByPod: map[string]map[string]machine.CPUSet{
				"missing-pod": {
					"main": machine.NewCPUSet(0, 1),
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("missing pod should be skipped, got error: %v", err)
	}
	if expected != nil {
		t.Fatalf("expected nil map for missing pod, got %#v", expected)
	}
}

func TestCPUSetTopologyPluginSkipsExpectedCPUSetForMissingContainer(t *testing.T) {
	t.Parallel()

	p := &CPUSetTopologyPlugin{}
	expected, err := p.buildExpectedCPUSetByRel(context.Background(), bulkheadapi.HandlerContext{
		BypassCPUSetAdjustmentHandlerCtx: bypassutil.BypassCPUSetAdjustmentHandlerCtx{
			MetaServer: &metaserver.MetaServer{
				MetaAgent: &agent.MetaAgent{
					PodFetcher: &metapod.PodFetcherStub{PodList: []*v1.Pod{{
						ObjectMeta: metav1.ObjectMeta{UID: types.UID("pod-1")},
					}}},
				},
			},
		},
		View: &bulkheadutils.CPUSetPartitionView{
			ContainerCPUSetByPod: map[string]map[string]machine.CPUSet{
				"pod-1": {
					"missing-container": machine.NewCPUSet(0, 1),
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("missing container should be skipped, got error: %v", err)
	}
	if expected != nil {
		t.Fatalf("expected nil map for missing container, got %#v", expected)
	}
}
