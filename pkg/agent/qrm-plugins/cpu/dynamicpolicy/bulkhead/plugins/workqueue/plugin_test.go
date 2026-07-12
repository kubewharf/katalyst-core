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
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	bulkheadapi "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/api"
	bulkheadutils "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/utils"
	bulkheadconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/qrm/bulkhead"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

type fakeFS struct {
	files map[string][]byte
}

func newFakeFS() *fakeFS {
	return &fakeFS{files: map[string][]byte{}}
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

	if err := p.CPUSetAdjustmentHandler(nil, bulkheadapi.HandlerContext{
		View: &bulkheadutils.CPUSetPartitionView{
			ReclaimEffective: machine.NewCPUSet(0, 1),
		},
	}); err != nil {
		t.Fatalf("reclaim handler: %v", err)
	}
	if got := string(f.files[filepath.Join(sysfs, "cpumask")]); got != reclaimMask {
		t.Fatalf("global reclaim mask = %q, want %q", got, reclaimMask)
	}

	if err := p.CPUSetAdjustmentHandler(nil, bulkheadapi.HandlerContext{
		View: &bulkheadutils.CPUSetPartitionView{ReclaimEffective: machine.NewCPUSet()},
	}); err != nil {
		t.Fatalf("empty reclaim handler without topology: %v", err)
	}

	in := bulkheadapi.HandlerContext{}
	in.Topology = topology
	in.View = &bulkheadutils.CPUSetPartitionView{ReclaimEffective: machine.NewCPUSet()}
	if err := p.CPUSetAdjustmentHandler(nil, in); err != nil {
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
	if err := p.CPUSetAdjustmentDisabledHandler(nil, in); err != nil {
		t.Fatalf("disabled transition handler: %v", err)
	}
	if got := string(f.files[filepath.Join(sysfs, "cpumask")]); got != fallbackMask {
		t.Fatalf("global disabled mask = %q, want %q", got, fallbackMask)
	}
	if got := string(f.files[filepath.Join(sysfs, "wq0", "cpumask")]); got != fallbackMask {
		t.Fatalf("per-workqueue disabled mask = %q, want %q", got, fallbackMask)
	}
}
