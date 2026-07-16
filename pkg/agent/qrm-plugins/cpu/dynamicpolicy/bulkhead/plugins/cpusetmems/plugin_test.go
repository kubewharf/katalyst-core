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

package cpusetmems

import (
	"context"
	"errors"
	"os"
	"reflect"
	"sort"
	"syscall"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	bulkheadapi "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/api"
	cpusetutil "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/util"
	dynamicconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	adminqosconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/adminqos"
	qrmconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/adminqos/qrm"
	bulkheadconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/qrm/bulkhead"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	cgroupclient "github.com/kubewharf/katalyst-core/pkg/util/cgroup/client"
	cgcommon "github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

type fakeCgroupClient struct {
	cgroupclient.FakeCgroupClient

	version        cgroupclient.CgroupVersion
	existing       map[string]bool
	children       map[string][]string
	cgroupFiles    map[string]map[string][]byte
	applyErr       map[string]error
	applyErrSeq    map[string][]error
	cpusetWrites   map[string]cgcommon.CPUSetData
	writeOrder     []string
	applyCallCount int
	listCallCount  int
	onApplyError   func(rel string, err error)
}

func (f *fakeCgroupClient) Version(context.Context) cgroupclient.CgroupVersion {
	if f.version != "" {
		return f.version
	}
	return cgroupclient.CgroupVersionV1
}

func (f *fakeCgroupClient) StatDir(_ context.Context, rel string) (time.Time, error) {
	if f.existing[rel] {
		return time.Time{}, nil
	}
	return time.Time{}, os.ErrNotExist
}

func (f *fakeCgroupClient) ApplyCPUSet(_ context.Context, rel string, data *cgcommon.CPUSetData) error {
	f.applyCallCount++
	f.writeOrder = append(f.writeOrder, rel)
	if seq := f.applyErrSeq[rel]; len(seq) > 0 {
		err := seq[0]
		f.applyErrSeq[rel] = seq[1:]
		if err != nil {
			if f.onApplyError != nil {
				f.onApplyError(rel, err)
			}
			return err
		}
	}
	if err := f.applyErr[rel]; err != nil {
		if f.onApplyError != nil {
			f.onApplyError(rel, err)
		}
		return err
	}
	if f.cpusetWrites == nil {
		f.cpusetWrites = map[string]cgcommon.CPUSetData{}
	}
	f.cpusetWrites[rel] = *data
	return nil
}

func (f *fakeCgroupClient) ListChildren(_ context.Context, rel string) ([]string, error) {
	f.listCallCount++
	children := append([]string(nil), f.children[rel]...)
	sort.Strings(children)
	return children, nil
}

func (f *fakeCgroupClient) ReadCgroupFile(_ context.Context, rel, file string) ([]byte, error) {
	if files := f.cgroupFiles[rel]; files != nil {
		if data, ok := files[file]; ok {
			return append([]byte(nil), data...), nil
		}
	}
	return nil, os.ErrNotExist
}

func TestCPUSetMemsPluginEnable(t *testing.T) {
	t.Parallel()

	p := &CPUSetMemsPlugin{}
	if p.Enable(handlerCtx(true)) != true {
		t.Fatalf("Enable() = false, want true")
	}
	if p.Enable(handlerCtx(false)) != false {
		t.Fatalf("Enable() = true, want false")
	}
}

func TestCPUSetMemsPluginAdmissionHandlersNoop(t *testing.T) {
	t.Parallel()

	cg := &fakeCgroupClient{
		applyErr: map[string]error{
			"reclaim/reclaim-0": &os.PathError{Op: "write", Path: "cpuset.mems", Err: syscall.EBUSY},
		},
	}
	p := &CPUSetMemsPlugin{cfg: testBulkheadConfig(), cgroup: cg}
	ctx := context.Background()

	if err := p.CPUSetAdjustmentHandler(ctx, bulkheadapi.HandlerContext{}); err != nil {
		t.Fatalf("CPUSetAdjustmentHandler: %v", err)
	}
	if err := p.CPUSetAdjustmentDisabledHandler(ctx, bulkheadapi.HandlerContext{}); err != nil {
		t.Fatalf("CPUSetAdjustmentDisabledHandler: %v", err)
	}
	if cg.applyCallCount != 0 {
		t.Fatalf("admission handlers wrote cpuset.mems %d times, want 0", cg.applyCallCount)
	}
}

func TestCPUSetMemsPluginPeriodicalReconcileWritesMemsOnly(t *testing.T) {
	t.Parallel()

	for _, version := range []cgroupclient.CgroupVersion{
		cgroupclient.CgroupVersionV1,
		cgroupclient.CgroupVersionV2,
	} {
		version := version
		t.Run(string(version), func(t *testing.T) {
			t.Parallel()

			cg := &fakeCgroupClient{
				version: version,
				existing: map[string]bool{
					"reclaim/reclaim-0": true,
					"reclaim/reclaim-1": true,
				},
			}
			p := &CPUSetMemsPlugin{cfg: testBulkheadConfig(), cgroup: cg}

			if err := p.PeriodicalHandler(context.Background(), periodicalCtx(true)); err != nil {
				t.Fatalf("PeriodicalHandler: %v", err)
			}

			for rel, wantMems := range map[string]string{
				"reclaim/reclaim-0": "0",
				"reclaim/reclaim-1": "1",
			} {
				write := cg.cpusetWrites[rel]
				if write.CPUs != "" {
					t.Fatalf("%s wrote cpuset.cpus on %s = %q, want empty", rel, version, write.CPUs)
				}
				if write.Mems != wantMems {
					t.Fatalf("%s cpuset.mems on %s = %q, want %q", rel, version, write.Mems, wantMems)
				}
				if write.Migrate != "" {
					t.Fatalf("%s migrate on %s = %q, want empty", rel, version, write.Migrate)
				}
			}
		})
	}
}

func TestCPUSetMemsPluginPeriodicalReconcileSkipsRecursiveWhenRootMemsMatches(t *testing.T) {
	t.Parallel()

	cg := &fakeCgroupClient{
		version: cgroupclient.CgroupVersionV1,
		existing: map[string]bool{
			"reclaim/reclaim-0": true,
		},
		children: map[string][]string{
			"reclaim/reclaim-0": {"pod-a"},
		},
		cgroupFiles: map[string]map[string][]byte{
			"reclaim/reclaim-0": {
				"cpuset.mems": []byte("0\n"),
			},
		},
	}
	p := &CPUSetMemsPlugin{cfg: bulkheadconfig.BulkheadConfiguration{
		BulkheadReclaimRelPaths:     []string{"reclaim"},
		BulkheadReclaimNumaPrefixes: []string{"reclaim/reclaim-"},
	}, cgroup: cg}

	if err := p.PeriodicalHandler(context.Background(), periodicalCtx(true)); err != nil {
		t.Fatalf("PeriodicalHandler: %v", err)
	}
	if cg.listCallCount != 0 {
		t.Fatalf("ListChildren calls = %d, want 0 when root cpuset.mems already matches", cg.listCallCount)
	}
	if cg.applyCallCount != 0 {
		t.Fatalf("ApplyCPUSet calls = %d, want 0 when root cpuset.mems already matches", cg.applyCallCount)
	}
}

func TestCPUSetMemsPluginPeriodicalRollbackByCgroupVersion(t *testing.T) {
	t.Parallel()

	for _, version := range []cgroupclient.CgroupVersion{
		cgroupclient.CgroupVersionV1,
		cgroupclient.CgroupVersionV2,
	} {
		version := version
		t.Run(string(version), func(t *testing.T) {
			t.Parallel()

			cg := &fakeCgroupClient{
				version: version,
				existing: map[string]bool{
					"reclaim/reclaim-0": true,
					"reclaim/reclaim-1": true,
				},
			}
			p := &CPUSetMemsPlugin{cfg: testBulkheadConfig(), cgroup: cg}

			if err := p.PeriodicalHandler(context.Background(), periodicalCtx(false)); err != nil {
				t.Fatalf("PeriodicalHandler: %v", err)
			}

			for _, rel := range []string{"reclaim/reclaim-0", "reclaim/reclaim-1"} {
				write := cg.cpusetWrites[rel]
				if version == cgroupclient.CgroupVersionV1 {
					if write.Mems != "0,1" || write.WriteEmptyMems {
						t.Fatalf("v1 rollback %s write = %+v, want mems 0,1 without WriteEmptyMems", rel, write)
					}
				} else {
					if write.Mems != "" || !write.WriteEmptyMems {
						t.Fatalf("v2 rollback %s write = %+v, want empty mems with WriteEmptyMems", rel, write)
					}
				}
				if write.CPUs != "" || write.Migrate != "" {
					t.Fatalf("rollback %s write = %+v, want no CPUs/Migrate", rel, write)
				}
			}
		})
	}
}

func TestCPUSetMemsPluginRecursiveWriteOrder(t *testing.T) {
	t.Parallel()

	cg := &fakeCgroupClient{
		version: cgroupclient.CgroupVersionV1,
		existing: map[string]bool{
			"reclaim/reclaim-0": true,
		},
		children: map[string][]string{
			"reclaim/reclaim-0":       {"pod-a"},
			"reclaim/reclaim-0/pod-a": {"container-a"},
		},
	}
	p := &CPUSetMemsPlugin{cfg: bulkheadconfig.BulkheadConfiguration{
		BulkheadReclaimRelPaths:     []string{"reclaim"},
		BulkheadReclaimNumaPrefixes: []string{"reclaim/reclaim-"},
	}, cgroup: cg}

	if err := p.PeriodicalHandler(context.Background(), periodicalCtx(true)); err != nil {
		t.Fatalf("PeriodicalHandler shrink: %v", err)
	}
	if diff := cmp.Diff([]string{
		"reclaim/reclaim-0/pod-a/container-a",
		"reclaim/reclaim-0/pod-a",
		"reclaim/reclaim-0",
	}, cg.writeOrder); diff != "" {
		t.Fatalf("shrink write order mismatch (-want +got):\n%s", diff)
	}

	cg.writeOrder = nil
	if err := p.PeriodicalHandler(context.Background(), periodicalCtx(false)); err != nil {
		t.Fatalf("PeriodicalHandler rollback: %v", err)
	}
	if diff := cmp.Diff([]string{
		"reclaim/reclaim-0",
		"reclaim/reclaim-0/pod-a",
		"reclaim/reclaim-0/pod-a/container-a",
	}, cg.writeOrder); diff != "" {
		t.Fatalf("rollback write order mismatch (-want +got):\n%s", diff)
	}
}

func TestCPUSetMemsPluginRetriesBusyRace(t *testing.T) {
	t.Parallel()

	busyErr := &os.PathError{Op: "write", Path: "cpuset.mems", Err: syscall.EBUSY}
	cg := &fakeCgroupClient{
		version: cgroupclient.CgroupVersionV1,
		existing: map[string]bool{
			"reclaim/reclaim-0": true,
		},
		children: map[string][]string{
			"reclaim/reclaim-0": nil,
		},
		applyErrSeq: map[string][]error{
			"reclaim/reclaim-0": {busyErr, nil},
		},
	}
	cg.onApplyError = func(rel string, err error) {
		if rel == "reclaim/reclaim-0" && errors.Is(err, syscall.EBUSY) {
			cg.children["reclaim/reclaim-0"] = []string{"pod-new"}
		}
	}
	p := &CPUSetMemsPlugin{cfg: bulkheadconfig.BulkheadConfiguration{
		BulkheadReclaimRelPaths:     []string{"reclaim"},
		BulkheadReclaimNumaPrefixes: []string{"reclaim/reclaim-"},
	}, cgroup: cg}

	if err := p.PeriodicalHandler(context.Background(), periodicalCtx(true)); err != nil {
		t.Fatalf("PeriodicalHandler retry shrink: %v", err)
	}
	if diff := cmp.Diff([]string{
		"reclaim/reclaim-0",
		"reclaim/reclaim-0/pod-new",
		"reclaim/reclaim-0",
	}, cg.writeOrder); diff != "" {
		t.Fatalf("retry write order mismatch (-want +got):\n%s", diff)
	}
}

func TestCPUSetMemsPluginBusyFailOpenAfterRetries(t *testing.T) {
	t.Parallel()

	busyErr := &os.PathError{Op: "write", Path: "cpuset.mems", Err: syscall.EBUSY}
	cg := &fakeCgroupClient{
		version: cgroupclient.CgroupVersionV1,
		existing: map[string]bool{
			"reclaim/reclaim-0": true,
		},
		applyErrSeq: map[string][]error{
			"reclaim/reclaim-0": {busyErr, busyErr, busyErr},
		},
	}
	p := &CPUSetMemsPlugin{cfg: bulkheadconfig.BulkheadConfiguration{
		BulkheadReclaimRelPaths:     []string{"reclaim"},
		BulkheadReclaimNumaPrefixes: []string{"reclaim/reclaim-"},
	}, cgroup: cg}

	if err := p.PeriodicalHandler(context.Background(), periodicalCtx(true)); err != nil {
		t.Fatalf("PeriodicalHandler exhausted busy retries = %v, want nil", err)
	}
	if got := countWrites(cg.writeOrder, "reclaim/reclaim-0"); got != memsApplyMaxAttempts {
		t.Fatalf("busy retry attempts = %d, want %d", got, memsApplyMaxAttempts)
	}
}

func TestCPUSetMemsPluginNonBusyErrorReturned(t *testing.T) {
	t.Parallel()

	cg := &fakeCgroupClient{
		version: cgroupclient.CgroupVersionV1,
		existing: map[string]bool{
			"reclaim/reclaim-0": true,
		},
		applyErr: map[string]error{
			"reclaim/reclaim-0": errors.New("permission denied"),
		},
	}
	p := &CPUSetMemsPlugin{cfg: bulkheadconfig.BulkheadConfiguration{
		BulkheadReclaimRelPaths:     []string{"reclaim"},
		BulkheadReclaimNumaPrefixes: []string{"reclaim/reclaim-"},
	}, cgroup: cg}

	if err := p.PeriodicalHandler(context.Background(), periodicalCtx(true)); err == nil {
		t.Fatalf("PeriodicalHandler returned nil for non-EBUSY error, want error")
	}
}

func TestCPUSetMemsPluginSkipsMissingRel(t *testing.T) {
	t.Parallel()

	cg := &fakeCgroupClient{
		version:  cgroupclient.CgroupVersionV1,
		existing: map[string]bool{},
	}
	p := &CPUSetMemsPlugin{cfg: testBulkheadConfig(), cgroup: cg}

	if err := p.PeriodicalHandler(context.Background(), periodicalCtx(true)); err != nil {
		t.Fatalf("PeriodicalHandler: %v", err)
	}
	if cg.applyCallCount != 0 {
		t.Fatalf("missing rel apply calls = %d, want 0", cg.applyCallCount)
	}
}

func TestCPUSetMemsPluginRequiresTopology(t *testing.T) {
	t.Parallel()

	p := &CPUSetMemsPlugin{cfg: testBulkheadConfig(), cgroup: &fakeCgroupClient{}}
	if err := p.PeriodicalHandler(context.Background(), bulkheadapi.PeriodicalHandlerContext{DynamicConf: dynamicConf(true)}); err == nil {
		t.Fatalf("PeriodicalHandler returned nil for missing topology, want error")
	}
}

func testBulkheadConfig() bulkheadconfig.BulkheadConfiguration {
	return bulkheadconfig.BulkheadConfiguration{
		BulkheadReclaimRelPaths:     []string{"reclaim"},
		BulkheadReclaimNumaPrefixes: []string{"reclaim/reclaim-"},
	}
}

func dynamicConf(enableMems bool) *dynamicconfig.Configuration {
	return &dynamicconfig.Configuration{
		AdminQoSConfiguration: &adminqosconfig.AdminQoSConfiguration{
			QRMPluginConfiguration: &qrmconfig.QRMPluginConfiguration{
				CPUPluginConfiguration: &qrmconfig.CPUPluginConfiguration{
					BulkheadConfig: qrmconfig.DynamicBulkheadConfiguration{
						EnableBulkheadCpusetMems: enableMems,
					},
				},
			},
		},
	}
}

func handlerCtx(enableMems bool) bulkheadapi.HandlerContext {
	return bulkheadapi.HandlerContext{
		CPUSetAdjustmentHandlerCtx: cpusetutil.CPUSetAdjustmentHandlerCtx{
			DynamicConf: dynamicConf(enableMems),
		},
	}
}

func periodicalCtx(enableMems bool) bulkheadapi.PeriodicalHandlerContext {
	return bulkheadapi.PeriodicalHandlerContext{
		DynamicConf: dynamicConf(enableMems),
		MetaServer: &metaserver.MetaServer{
			MetaAgent: &agent.MetaAgent{
				KatalystMachineInfo: &machine.KatalystMachineInfo{
					CPUTopology: &machine.CPUTopology{
						CPUDetails: machine.CPUDetails{
							0: {NUMANodeID: 0},
							1: {NUMANodeID: 1},
						},
					},
				},
			},
		},
	}
}

func countWrites(writes []string, rel string) int {
	count := 0
	for _, write := range writes {
		if write == rel {
			count++
		}
	}
	return count
}

func TestJoinNUMAIDs(t *testing.T) {
	t.Parallel()

	if got := joinNUMAIDs([]int{0, 1}); got != "0,1" {
		t.Fatalf("joinNUMAIDs = %q, want 0,1", got)
	}
}

func TestIsCPUSetMemsBusy(t *testing.T) {
	t.Parallel()

	for _, err := range []error{
		&os.PathError{Op: "write", Path: "cpuset.mems", Err: syscall.EBUSY},
		errors.New("write error: Device or resource busy"),
	} {
		if !isCPUSetMemsBusy(err) {
			t.Fatalf("isCPUSetMemsBusy(%v) = false, want true", err)
		}
	}
	if isCPUSetMemsBusy(errors.New("permission denied")) {
		t.Fatalf("permission denied reported as busy")
	}
}

func TestNUMAIDsFromMetaServerSorted(t *testing.T) {
	t.Parallel()

	ids, err := numaIDsFromMetaServer(periodicalCtx(true).MetaServer)
	if err != nil {
		t.Fatalf("numaIDsFromMetaServer: %v", err)
	}
	if !reflect.DeepEqual(ids, []int{0, 1}) {
		t.Fatalf("numaIDs = %v, want [0 1]", ids)
	}
}
