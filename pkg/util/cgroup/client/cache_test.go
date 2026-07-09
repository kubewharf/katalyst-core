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

package client

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	cgcommon "github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

type fakeCgroupClient struct{}

var _ CgroupClient = (*fakeCgroupClient)(nil)

func (fakeCgroupClient) ApplyCPUSet(context.Context, string, *cgcommon.CPUSetData) error {
	return nil
}

func (fakeCgroupClient) ApplyCPUSetPartition(context.Context, string, cgcommon.CPUSetPartitionFlag) error {
	return nil
}

func (fakeCgroupClient) ApplyCPU(context.Context, string, *cgcommon.CPUData) error {
	return nil
}

func (fakeCgroupClient) ApplySchedLoadBalance(context.Context, string, bool) error {
	return nil
}

func (fakeCgroupClient) AttachPID(context.Context, string, int) error {
	return nil
}

func (fakeCgroupClient) Version(context.Context) CgroupVersion {
	return CgroupVersionV2
}

func (fakeCgroupClient) ReadCPUSet(context.Context, string) (machine.CPUSet, error) {
	return machine.NewCPUSet(), nil
}

func (fakeCgroupClient) StatCPUSet(context.Context, string) (time.Time, int64, error) {
	return time.Time{}, 0, nil
}

func (fakeCgroupClient) StatCgroupFile(context.Context, string, string) (time.Time, int64, error) {
	return time.Time{}, 0, nil
}

func (fakeCgroupClient) ReadCPUSetPartition(context.Context, string) (cgcommon.CPUSetPartitionFlag, error) {
	return cgcommon.CPUSetPartitionFlagMember, nil
}

func (fakeCgroupClient) ListRootChildren(context.Context) ([]string, error) {
	return nil, nil
}

func (fakeCgroupClient) ListChildren(context.Context, string) ([]string, error) {
	return nil, nil
}

func (fakeCgroupClient) StatDir(context.Context, string) (time.Time, error) {
	return time.Time{}, nil
}

func (fakeCgroupClient) Prune(map[string]struct{}) {}

type countingFake struct {
	fakeCgroupClient

	applyCPUSetN          int64
	applyCPUSetPartitionN int64
	applyCPUN             int64
	applySchedLBN         int64
	attachPIDN            int64
	statErr               error
	mtime                 int64
	size                  int64
}

func newCountingFake() *countingFake {
	f := &countingFake{}
	atomic.StoreInt64(&f.mtime, 1)
	atomic.StoreInt64(&f.size, 4)
	return f
}

func (f *countingFake) bumpMTime() {
	atomic.AddInt64(&f.mtime, 1)
}

func (f *countingFake) statNow() (time.Time, int64, error) {
	if f.statErr != nil {
		return time.Time{}, 0, f.statErr
	}
	return time.Unix(0, atomic.LoadInt64(&f.mtime)), atomic.LoadInt64(&f.size), nil
}

func (f *countingFake) StatCPUSet(context.Context, string) (time.Time, int64, error) {
	return f.statNow()
}

func (f *countingFake) StatCgroupFile(context.Context, string, string) (time.Time, int64, error) {
	return f.statNow()
}

func (f *countingFake) StatDir(context.Context, string) (time.Time, error) {
	t, _, err := f.statNow()
	return t, err
}

func (f *countingFake) ApplyCPUSet(context.Context, string, *cgcommon.CPUSetData) error {
	atomic.AddInt64(&f.applyCPUSetN, 1)
	f.bumpMTime()
	return nil
}

func (f *countingFake) ApplyCPUSetPartition(context.Context, string, cgcommon.CPUSetPartitionFlag) error {
	atomic.AddInt64(&f.applyCPUSetPartitionN, 1)
	f.bumpMTime()
	return nil
}

func (f *countingFake) ApplyCPU(context.Context, string, *cgcommon.CPUData) error {
	atomic.AddInt64(&f.applyCPUN, 1)
	f.bumpMTime()
	return nil
}

func (f *countingFake) ApplySchedLoadBalance(context.Context, string, bool) error {
	atomic.AddInt64(&f.applySchedLBN, 1)
	f.bumpMTime()
	return nil
}

func (f *countingFake) AttachPID(context.Context, string, int) error {
	atomic.AddInt64(&f.attachPIDN, 1)
	return nil
}

func TestCachedCgroupClient_ApplyCPU_ShortCircuitsAndInvalidates(t *testing.T) {
	t.Parallel()

	inner := newCountingFake()
	c := NewCachedCgroupClient(inner)
	ctx := context.Background()
	idleOn := true
	idleOff := false

	if err := c.ApplyCPU(ctx, "kubepods", &cgcommon.CPUData{CpuIdlePtr: &idleOn}); err != nil {
		t.Fatalf("first ApplyCPU: %v", err)
	}
	if err := c.ApplyCPU(ctx, "kubepods", &cgcommon.CPUData{CpuIdlePtr: &idleOn}); err != nil {
		t.Fatalf("second ApplyCPU: %v", err)
	}
	if got := atomic.LoadInt64(&inner.applyCPUN); got != 1 {
		t.Fatalf("inner ApplyCPU calls after idempotent write = %d, want 1", got)
	}

	if err := c.ApplyCPU(ctx, "kubepods", &cgcommon.CPUData{CpuIdlePtr: &idleOff}); err != nil {
		t.Fatalf("third ApplyCPU: %v", err)
	}
	if got := atomic.LoadInt64(&inner.applyCPUN); got != 2 {
		t.Fatalf("inner ApplyCPU calls after value change = %d, want 2", got)
	}

	inner.bumpMTime()
	if err := c.ApplyCPU(ctx, "kubepods", &cgcommon.CPUData{CpuIdlePtr: &idleOff}); err != nil {
		t.Fatalf("fourth ApplyCPU: %v", err)
	}
	if got := atomic.LoadInt64(&inner.applyCPUN); got != 3 {
		t.Fatalf("inner ApplyCPU calls after external drift = %d, want 3", got)
	}
}

func TestCachedCgroupClient_ApplyCPUSet_PerFileCache(t *testing.T) {
	t.Parallel()

	inner := newCountingFake()
	c := NewCachedCgroupClient(inner)
	ctx := context.Background()

	if err := c.ApplyCPUSet(ctx, "kubepods", &cgcommon.CPUSetData{CPUs: "0-1", Mems: "0"}); err != nil {
		t.Fatalf("first ApplyCPUSet: %v", err)
	}
	if err := c.ApplyCPUSet(ctx, "kubepods", &cgcommon.CPUSetData{CPUs: "0-1", Mems: "0"}); err != nil {
		t.Fatalf("second ApplyCPUSet: %v", err)
	}
	if got := atomic.LoadInt64(&inner.applyCPUSetN); got != 1 {
		t.Fatalf("inner ApplyCPUSet calls after idempotent write = %d, want 1", got)
	}
	if err := c.ApplyCPUSet(ctx, "kubepods", &cgcommon.CPUSetData{CPUs: "0-1", Mems: "1"}); err != nil {
		t.Fatalf("third ApplyCPUSet: %v", err)
	}
	if got := atomic.LoadInt64(&inner.applyCPUSetN); got != 2 {
		t.Fatalf("inner ApplyCPUSet calls after mems change = %d, want 2", got)
	}
}

func TestCachedCgroupClient_ApplyCPUSetPartitionAndSchedLoadBalance(t *testing.T) {
	t.Parallel()

	inner := newCountingFake()
	c := NewCachedCgroupClient(inner)
	ctx := context.Background()

	if err := c.ApplyCPUSetPartition(ctx, "kubepods", cgcommon.CPUSetPartitionFlagRoot); err != nil {
		t.Fatalf("first partition: %v", err)
	}
	if err := c.ApplyCPUSetPartition(ctx, "kubepods", cgcommon.CPUSetPartitionFlagRoot); err != nil {
		t.Fatalf("second partition: %v", err)
	}
	if got := atomic.LoadInt64(&inner.applyCPUSetPartitionN); got != 1 {
		t.Fatalf("partition writes = %d, want 1", got)
	}

	if err := c.ApplySchedLoadBalance(ctx, "kubepods", true); err != nil {
		t.Fatalf("first sched_load_balance: %v", err)
	}
	if err := c.ApplySchedLoadBalance(ctx, "kubepods", true); err != nil {
		t.Fatalf("second sched_load_balance: %v", err)
	}
	if got := atomic.LoadInt64(&inner.applySchedLBN); got != 1 {
		t.Fatalf("sched_load_balance writes = %d, want 1", got)
	}
}

func TestCachedCgroupClient_StatFailureInvalidates(t *testing.T) {
	t.Parallel()

	inner := newCountingFake()
	c := NewCachedCgroupClient(inner)
	ctx := context.Background()
	idleOn := true

	if err := c.ApplyCPU(ctx, "kubepods", &cgcommon.CPUData{CpuIdlePtr: &idleOn}); err != nil {
		t.Fatalf("first ApplyCPU: %v", err)
	}
	inner.statErr = errors.New("stat failed")
	if err := c.ApplyCPU(ctx, "kubepods", &cgcommon.CPUData{CpuIdlePtr: &idleOn}); err != nil {
		t.Fatalf("second ApplyCPU after stat failure: %v", err)
	}
	if got := atomic.LoadInt64(&inner.applyCPUN); got != 2 {
		t.Fatalf("ApplyCPU calls after stat failure = %d, want 2", got)
	}
}

type overlappingApplyFake struct {
	fakeCgroupClient

	calls    int64
	inFlight int64
	entered  chan struct{}
	release  chan struct{}
	overlap  chan struct{}
	mtime    int64
}

func newOverlappingApplyFake() *overlappingApplyFake {
	return &overlappingApplyFake{
		entered: make(chan struct{}),
		release: make(chan struct{}),
		overlap: make(chan struct{}),
		mtime:   1,
	}
}

func (f *overlappingApplyFake) StatDir(context.Context, string) (time.Time, error) {
	return time.Unix(0, atomic.LoadInt64(&f.mtime)), nil
}

func (f *overlappingApplyFake) ApplyCPU(context.Context, string, *cgcommon.CPUData) error {
	call := atomic.AddInt64(&f.calls, 1)
	if atomic.AddInt64(&f.inFlight, 1) > 1 {
		select {
		case <-f.overlap:
		default:
			close(f.overlap)
		}
	}
	if call == 1 {
		close(f.entered)
		<-f.release
	}
	atomic.AddInt64(&f.inFlight, -1)
	return nil
}

func TestCachedCgroupClient_ApplyCPU_SerializesSameKeyCheckWriteRecord(t *testing.T) {
	inner := newOverlappingApplyFake()
	c := NewCachedCgroupClient(inner)
	ctx := context.Background()
	idleOn := true
	idleOff := false

	firstDone := make(chan error, 1)
	go func() {
		firstDone <- c.ApplyCPU(ctx, "kubepods", &cgcommon.CPUData{CpuIdlePtr: &idleOn})
	}()
	<-inner.entered

	secondDone := make(chan error, 1)
	go func() {
		secondDone <- c.ApplyCPU(ctx, "kubepods", &cgcommon.CPUData{CpuIdlePtr: &idleOff})
	}()

	select {
	case <-inner.overlap:
		close(inner.release)
		t.Fatalf("same-key ApplyCPU entered inner apply concurrently")
	case <-time.After(20 * time.Millisecond):
	}

	close(inner.release)
	if err := <-firstDone; err != nil {
		t.Fatalf("first ApplyCPU: %v", err)
	}
	if err := <-secondDone; err != nil {
		t.Fatalf("second ApplyCPU: %v", err)
	}
}

func TestCachedCgroupClient_AttachPIDNeverCaches(t *testing.T) {
	t.Parallel()

	inner := newCountingFake()
	c := NewCachedCgroupClient(inner)
	ctx := context.Background()

	if err := c.AttachPID(ctx, "kubepods", 1234); err != nil {
		t.Fatalf("first AttachPID: %v", err)
	}
	if err := c.AttachPID(ctx, "kubepods", 1234); err != nil {
		t.Fatalf("second AttachPID: %v", err)
	}
	if got := atomic.LoadInt64(&inner.attachPIDN); got != 2 {
		t.Fatalf("AttachPID calls = %d, want 2", got)
	}
}

type statFileFake struct {
	fakeCgroupClient

	statCPUSetCalls int
	statCgroupFile  []string
	statDirCalls    int
	mtimeCPUSet     time.Time
	mtimeCgroupFile time.Time
	mtimeDir        time.Time
}

func (f *statFileFake) StatCPUSet(context.Context, string) (time.Time, int64, error) {
	f.statCPUSetCalls++
	return f.mtimeCPUSet, 0, nil
}

func (f *statFileFake) StatCgroupFile(_ context.Context, _ string, file string) (time.Time, int64, error) {
	f.statCgroupFile = append(f.statCgroupFile, file)
	return f.mtimeCgroupFile, 0, nil
}

func (f *statFileFake) StatDir(context.Context, string) (time.Time, error) {
	f.statDirCalls++
	return f.mtimeDir, nil
}

func TestCachedCgroupClient_statFile_PerFileRouting(t *testing.T) {
	t.Parallel()

	f := &statFileFake{
		mtimeCPUSet:     time.Unix(1, 0),
		mtimeCgroupFile: time.Unix(2, 0),
		mtimeDir:        time.Unix(3, 0),
	}
	c := NewCachedCgroupClient(f).(*cachedCgroupClient)
	ctx := context.Background()

	mt, _, err := c.statFile(ctx, "kubepods", "cpuset.cpus")
	if err != nil || !mt.Equal(f.mtimeCPUSet) {
		t.Fatalf("statFile(cpuset.cpus) = (%v,%v), want %v", mt, err, f.mtimeCPUSet)
	}
	if f.statCPUSetCalls != 1 || len(f.statCgroupFile) != 0 || f.statDirCalls != 0 {
		t.Fatalf("cpuset.cpus routing: statCPUSet=%d statCgroupFile=%v statDir=%d",
			f.statCPUSetCalls, f.statCgroupFile, f.statDirCalls)
	}

	perFile := []string{"cpuset.mems", "cpuset.cpus.partition", "cpuset.sched_load_balance"}
	for _, name := range perFile {
		if _, _, err := c.statFile(ctx, "kubepods", name); err != nil {
			t.Fatalf("statFile(%s): %v", name, err)
		}
	}
	for i, name := range perFile {
		if f.statCgroupFile[i] != name {
			t.Fatalf("statCgroupFile[%d] = %q, want %q", i, f.statCgroupFile[i], name)
		}
	}

	mt, size, err := c.statFile(ctx, "kubepods", "cpu.max")
	if err != nil || !mt.Equal(f.mtimeDir) || size != 0 {
		t.Fatalf("statFile(cpu.max) = (%v,%d,%v), want (%v,0,nil)", mt, size, err, f.mtimeDir)
	}
	if f.statDirCalls != 1 {
		t.Fatalf("StatDir calls = %d, want 1", f.statDirCalls)
	}
}

func TestCachedCgroupClient_Prune(t *testing.T) {
	t.Parallel()

	c := NewCachedCgroupClient(&fakeCgroupClient{}).(*cachedCgroupClient)
	c.cache[cacheKey{rel: "rel-a", file: "cpuset.cpus"}] = cacheEntry{value: "0-1"}
	c.cache[cacheKey{rel: "rel-a", file: "cpuset.mems"}] = cacheEntry{value: "0"}
	c.cache[cacheKey{rel: "rel-b", file: "cpuset.cpus"}] = cacheEntry{value: "2-3"}

	c.Prune(map[string]struct{}{"rel-a": {}})

	if len(c.cache) != 2 {
		t.Fatalf("cache size after Prune = %d, want 2", len(c.cache))
	}
	if _, ok := c.cache[cacheKey{rel: "rel-b", file: "cpuset.cpus"}]; ok {
		t.Fatalf("rel-b/cpuset.cpus should be dropped")
	}

	c.Prune(nil)
	if len(c.cache) != 0 {
		t.Fatalf("Prune(nil) left %d entries, want 0", len(c.cache))
	}
}
