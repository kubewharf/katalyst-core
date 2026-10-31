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

package workqueue

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	bulkheadapi "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/api"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/model"
	bulkheadconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/qrm/bulkhead"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

type fakeFS struct {
	files      map[string][]byte
	writeCount map[string]int
}

func newFakeFS() *fakeFS {
	return &fakeFS{
		files:      map[string][]byte{},
		writeCount: map[string]int{},
	}
}

func (f *fakeFS) ReadFile(path string) ([]byte, error) {
	raw, ok := f.files[path]
	if !ok {
		return nil, os.ErrNotExist
	}
	return append([]byte(nil), raw...), nil
}

func (f *fakeFS) WriteFile(path string, raw []byte, _ os.FileMode) error {
	f.files[path] = append([]byte(nil), raw...)
	f.writeCount[path]++
	return nil
}

func (f *fakeFS) Exists(path string) bool {
	_, ok := f.files[path]
	return ok
}

func (f *fakeFS) ReadDir(string) ([]fs.DirEntry, error) {
	return nil, os.ErrInvalid
}

func TestWorkqueuePluginResetsMasksWhenReclaimBecomesEmpty(t *testing.T) {
	t.Parallel()

	sysfs := "/sys/devices/virtual/workqueue"
	f := newFakeFS()
	f.files[filepath.Join(sysfs, "wq0", "cpumask")] = []byte("stale")
	p := &WorkqueuePlugin{
		cfg: bulkheadconfig.BulkheadConfiguration{
			BulkheadWorkqueueSysfsDir: sysfs,
			BulkheadWorkqueueNames:    []string{"wq0"},
		},
		fs: f,
	}
	topology := &machine.CPUTopology{CPUDetails: machine.CPUDetails{
		0: {}, 1: {}, 2: {}, 3: {},
	}}
	reclaimMask, err := general.ConvertIntSliceToBitmapString([]int64{0, 1})
	if err != nil {
		t.Fatalf("convert reclaim mask: %v", err)
	}
	fallbackMask, err := general.ConvertIntSliceToBitmapString([]int64{0, 1, 2, 3})
	if err != nil {
		t.Fatalf("convert fallback mask: %v", err)
	}

	ctx := context.Background()
	if err := p.CPUSetAdjustmentHandler(ctx, bulkheadapi.HandlerContext{
		AppliedView: &model.AppliedView{CPUSetPartitionView: model.CPUSetPartitionView{
			ReclaimEffective: machine.NewCPUSet(0, 1),
		}},
	}); err != nil {
		t.Fatalf("reclaim handler: %v", err)
	}
	if got := string(f.files[filepath.Join(sysfs, "cpumask")]); got != reclaimMask {
		t.Fatalf("global reclaim mask = %q, want %q", got, reclaimMask)
	}

	if err := p.CPUSetAdjustmentHandler(ctx, bulkheadapi.HandlerContext{
		AppliedView: &model.AppliedView{CPUSetPartitionView: model.CPUSetPartitionView{ReclaimEffective: machine.NewCPUSet()}},
	}); err != nil {
		t.Fatalf("empty reclaim handler without topology: %v", err)
	}

	in := bulkheadapi.HandlerContext{}
	in.Topology = topology
	in.AppliedView = &model.AppliedView{CPUSetPartitionView: model.CPUSetPartitionView{ReclaimEffective: machine.NewCPUSet()}}
	if err := p.CPUSetAdjustmentHandler(ctx, in); err != nil {
		t.Fatalf("empty reclaim reset handler: %v", err)
	}
	if got := string(f.files[filepath.Join(sysfs, "cpumask")]); got != fallbackMask {
		t.Fatalf("global fallback mask = %q, want %q", got, fallbackMask)
	}
	if got := string(f.files[filepath.Join(sysfs, "wq0", "cpumask")]); got != fallbackMask {
		t.Fatalf("per-workqueue fallback mask = %q, want %q", got, fallbackMask)
	}
}

func TestWorkqueuePluginDisabledTransitionResetsMasks(t *testing.T) {
	t.Parallel()

	sysfs := "/sys/devices/virtual/workqueue"
	f := newFakeFS()
	f.files[filepath.Join(sysfs, "wq0", "cpumask")] = []byte("stale")
	p := &WorkqueuePlugin{
		cfg: bulkheadconfig.BulkheadConfiguration{
			BulkheadWorkqueueSysfsDir: sysfs,
			BulkheadWorkqueueNames:    []string{"wq0"},
		},
		fs: f,
	}
	topology := &machine.CPUTopology{CPUDetails: machine.CPUDetails{
		0: {}, 1: {}, 2: {}, 3: {},
	}}
	fallbackMask, err := general.ConvertIntSliceToBitmapString([]int64{0, 1, 2, 3})
	if err != nil {
		t.Fatalf("convert fallback mask: %v", err)
	}

	in := bulkheadapi.HandlerContext{}
	in.Topology = topology
	if err := p.CPUSetAdjustmentDisabledHandler(context.Background(), in); err != nil {
		t.Fatalf("disabled transition handler: %v", err)
	}
	if got := string(f.files[filepath.Join(sysfs, "cpumask")]); got != fallbackMask {
		t.Fatalf("global disabled mask = %q, want %q", got, fallbackMask)
	}
	if got := string(f.files[filepath.Join(sysfs, "wq0", "cpumask")]); got != fallbackMask {
		t.Fatalf("per-workqueue disabled mask = %q, want %q", got, fallbackMask)
	}
}

func TestWorkqueuePluginSkipsUnchangedMasks(t *testing.T) {
	t.Parallel()

	sysfs := "/sys/devices/virtual/workqueue"
	f := newFakeFS()
	p := &WorkqueuePlugin{
		cfg: bulkheadconfig.BulkheadConfiguration{
			BulkheadWorkqueueSysfsDir: sysfs,
		},
		fs: f,
	}
	in := bulkheadapi.HandlerContext{
		AppliedView: &model.AppliedView{CPUSetPartitionView: model.CPUSetPartitionView{
			ReclaimEffective: machine.NewCPUSet(0, 1),
		}},
	}
	ctx := context.Background()
	if err := p.CPUSetAdjustmentHandler(ctx, in); err != nil {
		t.Fatalf("first handler: %v", err)
	}
	global := filepath.Join(sysfs, "cpumask")
	firstWrites := f.writeCount[global]
	if firstWrites != 1 {
		t.Fatalf("first run writes = %d, want 1", firstWrites)
	}
	if err := p.CPUSetAdjustmentHandler(ctx, in); err != nil {
		t.Fatalf("second handler: %v", err)
	}
	if got := f.writeCount[global]; got != firstWrites {
		t.Fatalf("unchanged target should not rewrite mask, got %d writes want %d", got, firstWrites)
	}
}

// failingWorkqueueFS wraps fakeFS and forces WriteFile to fail for exactly one
// designated path, while reads and every other write pass through. It models a
// single persistently broken per-workqueue cpumask file.
type failingWorkqueueFS struct {
	*fakeFS
	failPath string
}

func (f *failingWorkqueueFS) WriteFile(path string, raw []byte, mode os.FileMode) error {
	if path == f.failPath {
		return fmt.Errorf("injected write failure for %s", path)
	}
	return f.fakeFS.WriteFile(path, raw, mode)
}

// TestWorkqueuePluginContinuesPastFailedPerWorkqueueWrite pins de-starvation: a
// persistently failing per-workqueue cpumask must surface an error but must not
// stop the loop from writing the workqueues that come after it in config order.
// On the old fail-fast code the later workqueue is never reached and keeps its
// stale mask.
func TestWorkqueuePluginContinuesPastFailedPerWorkqueueWrite(t *testing.T) {
	t.Parallel()

	sysfs := "/sys/devices/virtual/workqueue"
	base := newFakeFS()
	globalPath := filepath.Join(sysfs, "cpumask")
	badPath := filepath.Join(sysfs, "wqBad", "cpumask")
	goodPath := filepath.Join(sysfs, "wqGood", "cpumask")
	// Pre-seed global and both per-workqueue files with a stale mask so every
	// write below is a real content change (never skipped as unchanged).
	base.files[globalPath] = []byte("0")
	base.files[badPath] = []byte("0")
	base.files[goodPath] = []byte("0")

	fs := &failingWorkqueueFS{fakeFS: base, failPath: badPath}
	p := &WorkqueuePlugin{
		cfg: bulkheadconfig.BulkheadConfiguration{
			BulkheadWorkqueueSysfsDir: sysfs,
			BulkheadWorkqueueNames:    []string{"wqBad", "wqGood"},
		},
		fs: fs,
	}
	mask, err := general.ConvertIntSliceToBitmapString([]int64{0, 1})
	if err != nil {
		t.Fatalf("convert mask: %v", err)
	}

	in := bulkheadapi.HandlerContext{
		AppliedView: &model.AppliedView{CPUSetPartitionView: model.CPUSetPartitionView{
			ReclaimEffective: machine.NewCPUSet(0, 1),
		}},
	}
	if err := p.CPUSetAdjustmentHandler(context.Background(), in); err == nil {
		t.Fatalf("expected the persistently failing wqBad to surface an error, got nil")
	}

	// Global write is unaffected by the per-workqueue failure.
	if got := string(base.files[globalPath]); got != mask {
		t.Fatalf("global mask = %q, want %q", got, mask)
	}
	// Red on the old fail-fast code: the loop returned at wqBad and never wrote
	// wqGood. After the fix wqGood is still written despite wqBad failing.
	if got := string(base.files[goodPath]); got != mask {
		t.Fatalf("later workqueue mask = %q, want %q (an earlier workqueue write failure must not starve subsequent workqueues)", got, mask)
	}
}
