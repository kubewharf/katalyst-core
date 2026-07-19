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
	cpustate "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	cpusetutil "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/util"
	dynamicconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
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

	version           cgroupclient.CgroupVersion
	existing          map[string]bool
	cpus              map[string]machine.CPUSet
	children          map[string][]string
	writes            map[string]string
	applyCounts       map[string]int
	cpusetWrites      map[string]cgcommon.CPUSetData
	pruned            map[string]struct{}
	schedLoadBalance  map[string]bool
	partitionWrites   map[string]cgcommon.CPUSetPartitionFlag
	partitionErrByRel map[string]error
	listErr           error
}

func (f *fakeCgroupClient) StatDir(_ context.Context, rel string) (time.Time, error) {
	if f.existing[rel] {
		return time.Time{}, nil
	}
	return time.Time{}, errors.New("missing")
}

func (f *fakeCgroupClient) Version(context.Context) cgroupclient.CgroupVersion {
	if f.version != "" {
		return f.version
	}
	return cgroupclient.CgroupVersionV1
}

func (f *fakeCgroupClient) ReadCPUSet(_ context.Context, rel string) (machine.CPUSet, error) {
	if cpus, ok := f.cpus[rel]; ok {
		return cpus.Clone(), nil
	}
	return machine.NewCPUSet(), nil
}

func (f *fakeCgroupClient) ApplyCPUSet(_ context.Context, rel string, data *cgcommon.CPUSetData) error {
	if f.writes == nil {
		f.writes = map[string]string{}
	}
	if f.cpusetWrites == nil {
		f.cpusetWrites = map[string]cgcommon.CPUSetData{}
	}
	if f.applyCounts == nil {
		f.applyCounts = map[string]int{}
	}
	f.writes[rel] = data.CPUs
	f.applyCounts[rel]++
	f.cpusetWrites[rel] = *data
	if f.cpus == nil {
		f.cpus = map[string]machine.CPUSet{}
	}
	if data.CPUs != "" || data.WriteEmptyCPUs {
		f.cpus[rel] = machine.MustParse(data.CPUs)
	}
	return nil
}

func (f *fakeCgroupClient) Prune(active map[string]struct{}) {
	f.pruned = active
}

func (f *fakeCgroupClient) ListChildren(_ context.Context, rel string) ([]string, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return append([]string(nil), f.children[rel]...), nil
}

func (f *fakeCgroupClient) ApplySchedLoadBalance(_ context.Context, rel string, enabled bool) error {
	if f.schedLoadBalance == nil {
		f.schedLoadBalance = map[string]bool{}
	}
	f.schedLoadBalance[rel] = enabled
	return nil
}

func (f *fakeCgroupClient) ApplyCPUSetPartition(_ context.Context, rel string, flag cgcommon.CPUSetPartitionFlag) error {
	if err := f.partitionErrByRel[rel]; err != nil {
		return err
	}
	if f.partitionWrites == nil {
		f.partitionWrites = map[string]cgcommon.CPUSetPartitionFlag{}
	}
	f.partitionWrites[rel] = flag
	return nil
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

func TestCPUSetTopologyPluginSkipsUnchangedApplyTarget(t *testing.T) {
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
	in := bulkheadapi.HandlerContext{
		View: &bulkheadutils.CPUSetPartitionView{
			NonReclaimPool:   machine.NewCPUSet(0, 1),
			ReclaimEffective: machine.NewCPUSet(2, 3),
		},
	}

	if err := p.CPUSetAdjustmentHandler(context.Background(), in); err != nil {
		t.Fatalf("first CPUSetAdjustmentHandler: %v", err)
	}
	firstApplyCount := totalApplyCount(cg.applyCounts)
	if firstApplyCount == 0 {
		t.Fatalf("first run should apply cpuset, writes=%v", cg.writes)
	}
	if err := p.CPUSetAdjustmentHandler(context.Background(), in); err != nil {
		t.Fatalf("second CPUSetAdjustmentHandler: %v", err)
	}
	if got := totalApplyCount(cg.applyCounts); got != firstApplyCount {
		t.Fatalf("unchanged apply target should not write again, got %d writes want %d", got, firstApplyCount)
	}
}

func TestCPUSetTopologyPluginReconcilesExternalCgroupDrift(t *testing.T) {
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
	in := bulkheadapi.HandlerContext{
		View: &bulkheadutils.CPUSetPartitionView{
			NonReclaimPool:   machine.NewCPUSet(0, 1),
			ReclaimEffective: machine.NewCPUSet(2, 3),
		},
	}

	if err := p.CPUSetAdjustmentHandler(context.Background(), in); err != nil {
		t.Fatalf("first CPUSetAdjustmentHandler: %v", err)
	}
	firstPrimaryApplyCount := cg.applyCounts["primary"]
	cg.cpus["primary"] = machine.NewCPUSet(0)

	if err := p.CPUSetAdjustmentHandler(context.Background(), in); err != nil {
		t.Fatalf("second CPUSetAdjustmentHandler: %v", err)
	}
	if got := cg.applyCounts["primary"]; got <= firstPrimaryApplyCount {
		t.Fatalf("external primary drift was not reconciled, primary apply count=%d want > %d", got, firstPrimaryApplyCount)
	}
	if got := cg.writes["primary"]; got != "0-1" {
		t.Fatalf("primary cpuset after drift = %q, want 0-1", got)
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

func TestCPUSetTopologyPluginDisabledTransitionUsesTopologySpecsAndDAGExpandV1(t *testing.T) {
	t.Parallel()

	p, cg, in, containerRel := newDisabledTransitionTestPlugin(
		t,
		cgroupclient.CgroupVersionV1,
		"bulkhead-disabled-v1-pod",
		"bulkhead-disabled-v1-container",
	)

	err := p.CPUSetAdjustmentDisabledHandler(context.Background(), in)
	if err != nil {
		t.Fatalf("CPUSetAdjustmentDisabledHandler: %v", err)
	}

	wantMachine := "0-3"
	for _, rel := range []string{
		"primary",
		"reclaim",
		"reclaim/reclaim-0",
		"sibling",
		"primary/burstable",
		"primary/burstable/pod-a",
	} {
		if got := cg.writes[rel]; got != wantMachine {
			t.Fatalf("cpuset @ %s = %q, want %q; writes=%#v", rel, got, wantMachine, cg.writes)
		}
	}
	if got := cg.writes[containerRel]; got != "0" {
		t.Fatalf("container cpuset = %q, want 0; writes=%#v", got, cg.writes)
	}
	if got := cg.cpusetWrites["reclaim/reclaim-0"].Mems; got != "" {
		t.Fatalf("cpuset_topology wrote reclaim NUMA cpuset.mems = %q, want empty", got)
	}
	if _, ok := cg.writes["partition"]; ok {
		t.Fatalf("partition should not receive cpuset.cpus write, writes=%#v", cg.writes)
	}
	if len(cg.schedLoadBalance) != 0 {
		t.Fatalf("disabled transition should not write sched_load_balance, got %#v", cg.schedLoadBalance)
	}
	if cg.pruned != nil {
		t.Fatalf("disabled transition should not prune, got %#v", cg.pruned)
	}
}

func TestCPUSetTopologyPluginDisabledTransitionUsesTopologySpecsAndDAGExpandV2ToEmpty(t *testing.T) {
	t.Parallel()

	p, cg, in, containerRel := newDisabledTransitionTestPlugin(
		t,
		cgroupclient.CgroupVersionV2,
		"bulkhead-disabled-v2-pod",
		"bulkhead-disabled-v2-container",
	)

	err := p.CPUSetAdjustmentDisabledHandler(context.Background(), in)
	if err != nil {
		t.Fatalf("CPUSetAdjustmentDisabledHandler: %v", err)
	}

	for _, rel := range []string{
		"primary",
		"reclaim",
		"reclaim/reclaim-0",
		"primary/burstable",
		"primary/burstable/pod-a",
	} {
		if got := cg.writes[rel]; got != "" {
			t.Fatalf("cpuset @ %s = %q, want empty; writes=%#v", rel, got, cg.writes)
		}
	}
	if got := cg.writes[containerRel]; got != "0" {
		t.Fatalf("container cpuset = %q, want 0; writes=%#v", got, cg.writes)
	}
	if got := cg.cpusetWrites["reclaim/reclaim-0"].Mems; got != "" {
		t.Fatalf("cpuset_topology wrote reclaim NUMA cpuset.mems = %q, want empty", got)
	}
	if write := cg.cpusetWrites["reclaim/reclaim-0"]; write.CPUs != "" || !write.WriteEmptyCPUs {
		t.Fatalf("v2 reclaim NUMA cpuset write = %+v, want empty cpus with WriteEmptyCPUs", write)
	}
	if _, ok := cg.writes["partition"]; ok {
		t.Fatalf("partition should not receive cpuset.cpus write, writes=%#v", cg.writes)
	}
	if len(cg.schedLoadBalance) != 0 {
		t.Fatalf("disabled transition should not write sched_load_balance, got %#v", cg.schedLoadBalance)
	}
	if cg.pruned != nil {
		t.Fatalf("disabled transition should not prune, got %#v", cg.pruned)
	}
}

func TestCPUSetTopologyPluginDisabledTransitionReturnsErrorForInvalidV1Target(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		topology *machine.CPUTopology
	}{
		{name: "nil topology"},
		{name: "empty machine cpuset", topology: &machine.CPUTopology{CPUDetails: machine.CPUDetails{}}},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			p, _, in, _ := newDisabledTransitionTestPlugin(
				t,
				cgroupclient.CgroupVersionV1,
				"bulkhead-disabled-invalid-"+tt.name,
				"bulkhead-disabled-invalid-container",
			)
			in.Topology = tt.topology

			err := p.CPUSetAdjustmentDisabledHandler(context.Background(), in)
			if err == nil {
				t.Fatalf("expected invalid v1 reset target error")
			}
		})
	}
}

func TestCPUSetTopologyPluginDisabledTransitionReturnsSiblingDiscoveryError(t *testing.T) {
	t.Parallel()

	p, _, in, _ := newDisabledTransitionTestPlugin(
		t,
		cgroupclient.CgroupVersionV1,
		"bulkhead-disabled-list-error-pod",
		"bulkhead-disabled-list-error-container",
	)
	p.cgroup.(*fakeCgroupClient).listErr = errors.New("list failed")

	err := p.CPUSetAdjustmentDisabledHandler(context.Background(), in)
	if err == nil {
		t.Fatalf("expected sibling discovery error")
	}
}

func newDisabledTransitionTestPlugin(
	t *testing.T,
	version cgroupclient.CgroupVersion,
	podUID string,
	containerID string,
) (*CPUSetTopologyPlugin, *fakeCgroupClient, bulkheadapi.HandlerContext, string) {
	t.Helper()

	containerRel := "primary/burstable/pod-a/container-a"
	cgcommon.RegisterRelativeCgroupPathHandler(cgcommon.RelativeCgroupPathHandler{
		Name: "bulkhead-disabled-" + podUID,
		Handler: func(gotPodUID, gotContainerID string) (string, error) {
			if gotPodUID == podUID && gotContainerID == containerID {
				return containerRel, nil
			}
			return "", errors.New("not a bulkhead disabled transition test container")
		},
	})

	cg := &fakeCgroupClient{
		version: version,
		existing: map[string]bool{
			"primary":           true,
			"reclaim":           true,
			"reclaim/reclaim-0": true,
			"sibling":           true,
			"partition":         true,
		},
		cpus: map[string]machine.CPUSet{
			"primary":                             machine.NewCPUSet(0, 1, 2, 3),
			"primary/burstable":                   machine.NewCPUSet(0, 1, 2, 3),
			"primary/burstable/pod-a":             machine.NewCPUSet(0, 1),
			"primary/burstable/pod-a/container-a": machine.NewCPUSet(0),
			"reclaim":                             machine.NewCPUSet(2, 3),
			"reclaim/reclaim-0":                   machine.NewCPUSet(2, 3),
			"sibling":                             machine.NewCPUSet(2, 3),
			"partition":                           machine.NewCPUSet(0, 1, 2, 3),
		},
		children: map[string][]string{
			"":                        {"primary", "reclaim", "sibling", "partition"},
			"primary":                 {"burstable"},
			"primary/burstable":       {"pod-a"},
			"primary/burstable/pod-a": {"container-a"},
			"reclaim":                 {"reclaim-0"},
		},
	}
	p := &CPUSetTopologyPlugin{
		cfg: bulkheadconfig.BulkheadConfiguration{
			BulkheadPrimaryRelPath:        "primary",
			BulkheadReclaimRelPaths:       []string{"reclaim"},
			BulkheadReclaimNumaPrefixes:   []string{"reclaim/reclaim-"},
			BulkheadPartitionRelPaths:     []string{"partition"},
			EnableBulkheadReclaimSiblings: true,
		},
		cgroup: cg,
	}
	in := bulkheadapi.HandlerContext{
		CPUSetAdjustmentHandlerCtx: cpusetutil.CPUSetAdjustmentHandlerCtx{
			MetaServer: &metaserver.MetaServer{
				MetaAgent: &agent.MetaAgent{
					PodFetcher: &metapod.PodFetcherStub{PodList: []*v1.Pod{{
						ObjectMeta: metav1.ObjectMeta{UID: types.UID(podUID)},
						Status: v1.PodStatus{ContainerStatuses: []v1.ContainerStatus{{
							Name:        "main",
							ContainerID: "containerd://" + containerID,
						}}},
					}}},
				},
			},
			Topology: &machine.CPUTopology{
				CPUDetails: machine.CPUDetails{
					0: {},
					1: {},
					2: {},
					3: {},
				},
			},
		},
		View: &bulkheadutils.CPUSetPartitionView{
			NonReclaimPool:   machine.NewCPUSet(0, 1),
			ReclaimEffective: machine.NewCPUSet(2, 3),
			ReclaimEffectivePerNUMA: map[int]machine.CPUSet{
				0: machine.NewCPUSet(2, 3),
			},
			ContainerCPUSetByPod: map[string]map[string]machine.CPUSet{
				podUID: {
					"main": machine.NewCPUSet(0),
				},
			},
		},
	}
	return p, cg, in, containerRel
}

func TestCPUSetTopologyPluginPeriodicalHandlerResetsSchedLoadBalanceWhenDisabledV1(t *testing.T) {
	t.Parallel()

	cg := &fakeCgroupClient{version: cgroupclient.CgroupVersionV1}
	p := &CPUSetTopologyPlugin{cgroup: cg}

	err := p.PeriodicalHandler(context.Background(), bulkheadapi.PeriodicalHandlerContext{})
	if err != nil {
		t.Fatalf("PeriodicalHandler: %v", err)
	}
	if got := cg.schedLoadBalance[""]; !got {
		t.Fatalf("root sched_load_balance = %t, want true", got)
	}
}

func TestCPUSetTopologyPluginPeriodicalHandlerAppliesSchedLoadBalanceFalseWhenEnabledV1(t *testing.T) {
	t.Parallel()

	cg := &fakeCgroupClient{version: cgroupclient.CgroupVersionV1}
	p := &CPUSetTopologyPlugin{cgroup: cg}

	err := p.PeriodicalHandler(context.Background(), bulkheadapi.PeriodicalHandlerContext{
		DynamicConf: enabledBulkheadCpusetTopologyDynamicConf(),
	})
	if err != nil {
		t.Fatalf("PeriodicalHandler: %v", err)
	}
	if got := cg.schedLoadBalance[""]; got {
		t.Fatalf("root sched_load_balance = %t, want false", got)
	}
}

func TestCPUSetTopologyPluginPeriodicalHandlerResetsPartitionWhenDisabledV2(t *testing.T) {
	t.Parallel()

	cg := &fakeCgroupClient{
		version:  cgroupclient.CgroupVersionV2,
		existing: map[string]bool{"partition": true},
	}
	p := &CPUSetTopologyPlugin{
		cfg: bulkheadconfig.BulkheadConfiguration{
			BulkheadPartitionRelPaths: []string{"partition"},
		},
		cgroup: cg,
	}

	err := p.PeriodicalHandler(context.Background(), bulkheadapi.PeriodicalHandlerContext{})
	if err != nil {
		t.Fatalf("PeriodicalHandler: %v", err)
	}
	if got := cg.partitionWrites["partition"]; got != cgcommon.CPUSetPartitionFlagMember {
		t.Fatalf("partition flag = %s, want %s", got, cgcommon.CPUSetPartitionFlagMember)
	}
}

func TestCPUSetTopologyPluginPeriodicalHandlerAppliesPartitionRootWhenEnabledV2(t *testing.T) {
	t.Parallel()

	cg := &fakeCgroupClient{
		version:  cgroupclient.CgroupVersionV2,
		existing: map[string]bool{"partition": true},
	}
	p := &CPUSetTopologyPlugin{
		cfg: bulkheadconfig.BulkheadConfiguration{
			BulkheadPartitionRelPaths: []string{"partition"},
		},
		cgroup: cg,
	}

	err := p.PeriodicalHandler(context.Background(), bulkheadapi.PeriodicalHandlerContext{
		DynamicConf: enabledBulkheadCpusetTopologyDynamicConf(),
	})
	if err != nil {
		t.Fatalf("PeriodicalHandler: %v", err)
	}
	if got := cg.partitionWrites["partition"]; got != cgcommon.CPUSetPartitionFlagRoot {
		t.Fatalf("partition flag = %s, want %s", got, cgcommon.CPUSetPartitionFlagRoot)
	}
}

func TestEnableBulkheadCpusetTopologyRequiresNonOverlapReclaimedCores(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                                       string
		enableBulkheadCpusetTopology               bool
		stateAllowSharedCoresOverlapReclaimedCores bool
		confAllowSharedCoresOverlapReclaimedCores  bool
		want                                       bool
	}{
		{
			name:                         "enabled and non overlap",
			enableBulkheadCpusetTopology: true,
			want:                         true,
		},
		{
			name:                         "enabled but overlap",
			enableBulkheadCpusetTopology: true,
			stateAllowSharedCoresOverlapReclaimedCores: true,
		},
		{
			name: "disabled and non overlap",
		},
		{
			name:                         "uses state overlap instead of dynamic config overlap",
			enableBulkheadCpusetTopology: true,
			confAllowSharedCoresOverlapReclaimedCores: true,
			want: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			conf := bulkheadCpusetTopologyDynamicConf(
				tt.enableBulkheadCpusetTopology,
				tt.confAllowSharedCoresOverlapReclaimedCores,
			)
			state := cpustate.NewCPUPluginState(nil)
			state.SetAllowSharedCoresOverlapReclaimedCores(tt.stateAllowSharedCoresOverlapReclaimedCores)
			if got := enableBulkheadCpusetTopology(bulkheadapi.HandlerContext{
				CPUSetAdjustmentHandlerCtx: cpusetutil.CPUSetAdjustmentHandlerCtx{
					DynamicConf: conf,
					State:       state,
				},
			}); got != tt.want {
				t.Fatalf("enableBulkheadCpusetTopology() = %t, want %t", got, tt.want)
			}
		})
	}
}

func enabledBulkheadCpusetTopologyDynamicConf() *dynamicconfig.Configuration {
	return bulkheadCpusetTopologyDynamicConf(true, false)
}

func bulkheadCpusetTopologyDynamicConf(enableBulkheadCpusetTopology, allowSharedCoresOverlapReclaimedCores bool) *dynamicconfig.Configuration {
	conf := dynamicconfig.NewConfiguration()
	conf.AdminQoSConfiguration.CPUPluginConfiguration.BulkheadConfig.EnableBulkheadCpusetTopology = enableBulkheadCpusetTopology
	conf.AdminQoSConfiguration.CPUProvisionConfiguration.AllowSharedCoresOverlapReclaimedCores = allowSharedCoresOverlapReclaimedCores
	return conf
}

func TestCPUSetTopologyPluginSkipsExpectedCPUSetForMissingPod(t *testing.T) {
	t.Parallel()

	p := &CPUSetTopologyPlugin{}
	view := &bulkheadutils.CPUSetPartitionView{
		ContainerCPUSetByPod: map[string]map[string]machine.CPUSet{
			"missing-pod": {
				"main": machine.NewCPUSet(0, 1),
			},
		},
	}
	metaServer := &metaserver.MetaServer{
		MetaAgent: &agent.MetaAgent{
			PodFetcher: &metapod.PodFetcherStub{},
		},
	}

	// A missing pod fails at the container-id stage, which is the admit-safe
	// pending case: no error, no expected leaf, but the allocation is recorded
	// as protected-pending so the writer keeps the parent a superset.
	res, err := p.buildExpectedCPUSetByRel(context.Background(), bulkheadapi.HandlerContext{
		CPUSetAdjustmentHandlerCtx: cpusetutil.CPUSetAdjustmentHandlerCtx{MetaServer: metaServer},
		View:                       view,
	})
	if err != nil {
		t.Fatalf("missing pod must not error (admit-safe pending), got %v", err)
	}
	if len(res.ExpectedByRel) != 0 {
		t.Fatalf("expected no resolved leaves, got %#v", res.ExpectedByRel)
	}
	if len(res.PendingByPod) != 1 {
		t.Fatalf("expected one protected-pending entry, got %#v", res.PendingByPod)
	}
	if got := res.PendingCPUSetUnion().String(); got != "0-1" {
		t.Fatalf("pending union = %s, want 0-1", got)
	}
}

func TestCPUSetTopologyPluginSkipsExpectedCPUSetForMissingContainer(t *testing.T) {
	t.Parallel()

	p := &CPUSetTopologyPlugin{}
	metaServer := &metaserver.MetaServer{
		MetaAgent: &agent.MetaAgent{
			PodFetcher: &metapod.PodFetcherStub{PodList: []*v1.Pod{{
				ObjectMeta: metav1.ObjectMeta{UID: types.UID("pod-1")},
			}}},
		},
	}
	view := &bulkheadutils.CPUSetPartitionView{
		ContainerCPUSetByPod: map[string]map[string]machine.CPUSet{
			"pod-1": {
				"missing-container": machine.NewCPUSet(0, 1),
			},
		},
	}

	// A container with no status yet also fails at the container-id stage:
	// admit-safe pending, not an error.
	res, err := p.buildExpectedCPUSetByRel(context.Background(), bulkheadapi.HandlerContext{
		CPUSetAdjustmentHandlerCtx: cpusetutil.CPUSetAdjustmentHandlerCtx{MetaServer: metaServer},
		View:                       view,
	})
	if err != nil {
		t.Fatalf("missing container must not error (admit-safe pending), got %v", err)
	}
	if len(res.ExpectedByRel) != 0 {
		t.Fatalf("expected no resolved leaves, got %#v", res.ExpectedByRel)
	}
	if len(res.PendingByPod) != 1 {
		t.Fatalf("expected one protected-pending entry, got %#v", res.PendingByPod)
	}
}

func TestCPUSetTopologyPluginFailsExpectedCPUSetForUnresolvedContainerRel(t *testing.T) {
	t.Parallel()

	p := &CPUSetTopologyPlugin{}
	metaServer := &metaserver.MetaServer{
		MetaAgent: &agent.MetaAgent{
			PodFetcher: &metapod.PodFetcherStub{PodList: []*v1.Pod{{
				ObjectMeta: metav1.ObjectMeta{UID: types.UID("pod-1")},
				Status: v1.PodStatus{ContainerStatuses: []v1.ContainerStatus{{
					Name:        "main",
					ContainerID: "invalid-container-id",
				}}},
			}}},
		},
	}
	view := &bulkheadutils.CPUSetPartitionView{
		ContainerCPUSetByPod: map[string]map[string]machine.CPUSet{
			"pod-1": {
				"main": machine.NewCPUSet(0, 1),
			},
		},
	}

	// The container id resolves, but the relative cgroup path cannot be resolved
	// (no handler / broken layout). This is a real error, NOT the admit window,
	// so the round must fail-closed rather than apply a partial topology.
	res, err := p.buildExpectedCPUSetByRel(context.Background(), bulkheadapi.HandlerContext{
		CPUSetAdjustmentHandlerCtx: cpusetutil.CPUSetAdjustmentHandlerCtx{MetaServer: metaServer},
		View:                       view,
	})
	if err == nil {
		t.Fatalf("unresolved container rel (id known) must fail-closed, got res=%#v", res)
	}
}

// TestCPUSetTopologyPluginBuildExpectedCPUSetByRelTrimsLeadingSlash is a
// regression guard: buildExpectedCPUSetByRel must produce map keys WITHOUT a
// leading "/", so they match the childRel format that
// utils/topology/writer.expandDescendants constructs via filepath.Join. If the
// key kept the leading "/" that GetKubernetesAnyExistRelativeCgroupPath
// prepends, per-container cpuset enforcement would silently degrade to
// inheriting the parent pool target inside ApplyDAGDiff.
func TestCPUSetTopologyPluginBuildExpectedCPUSetByRelTrimsLeadingSlash(t *testing.T) {
	t.Parallel()

	const (
		podUID      = "pod-build-expected-trim"
		containerID = "container-build-expected-trim"
		prefixedRel = "/kubepods/burstable/pod-a/container-a"
		expectedRel = "kubepods/burstable/pod-a/container-a"
		containerNm = "main"
	)

	cgcommon.RegisterRelativeCgroupPathHandler(cgcommon.RelativeCgroupPathHandler{
		Name: "bulkhead-build-expected-trim-" + podUID,
		Handler: func(gotPodUID, gotContainerID string) (string, error) {
			if gotPodUID == podUID && gotContainerID == containerID {
				return prefixedRel, nil
			}
			return "", errors.New("not a build-expected-trim test container")
		},
	})

	p := &CPUSetTopologyPlugin{}
	res, err := p.buildExpectedCPUSetByRel(context.Background(), bulkheadapi.HandlerContext{
		CPUSetAdjustmentHandlerCtx: cpusetutil.CPUSetAdjustmentHandlerCtx{
			MetaServer: &metaserver.MetaServer{
				MetaAgent: &agent.MetaAgent{
					PodFetcher: &metapod.PodFetcherStub{PodList: []*v1.Pod{{
						ObjectMeta: metav1.ObjectMeta{UID: types.UID(podUID)},
						Status: v1.PodStatus{ContainerStatuses: []v1.ContainerStatus{{
							Name:        containerNm,
							ContainerID: "containerd://" + containerID,
						}}},
					}}},
				},
			},
		},
		View: &bulkheadutils.CPUSetPartitionView{
			ContainerCPUSetByPod: map[string]map[string]machine.CPUSet{
				podUID: {
					containerNm: machine.NewCPUSet(0, 1),
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("buildExpectedCPUSetByRel: %v", err)
	}
	expected := res.ExpectedByRel
	if _, ok := expected[prefixedRel]; ok {
		t.Fatalf("map key still has leading '/': %q; keys=%v", prefixedRel, keysOf(expected))
	}
	cpus, ok := expected[expectedRel]
	if !ok {
		t.Fatalf("expected key %q not found; keys=%v", expectedRel, keysOf(expected))
	}
	if got := cpus.String(); got != "0-1" {
		t.Fatalf("cpuset @ %s = %q, want 0-1", expectedRel, got)
	}
}

func keysOf(m map[string]machine.CPUSet) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func totalApplyCount(counts map[string]int) int {
	total := 0
	for _, count := range counts {
		total += count
	}
	return total
}
