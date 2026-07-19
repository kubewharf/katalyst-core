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
	"testing"

	cgcommon "github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
)

// TestFakeCgroupClientSatisfiesInterface fails at compile time if
// FakeCgroupClient drifts from CgroupClient.
func TestFakeCgroupClientSatisfiesInterface(t *testing.T) {
	t.Parallel()

	var _ CgroupClient = FakeCgroupClient{}
}

// TestFakeCgroupClientDefaults documents the safe-default policy of the
// zero-value FakeCgroupClient.
func TestFakeCgroupClientDefaults(t *testing.T) {
	t.Parallel()

	f := FakeCgroupClient{}
	ctx := context.Background()

	if got := f.Version(ctx); got != CgroupVersionV2 {
		t.Errorf("Version default = %v, want %v", got, CgroupVersionV2)
	}
	if err := f.ApplyCPUSet(ctx, "kubepods", &cgcommon.CPUSetData{CPUs: "0-1"}); err != nil {
		t.Errorf("ApplyCPUSet default returned err=%v, want nil", err)
	}
	if err := f.ApplyCPUSetPartition(ctx, "kubepods", cgcommon.CPUSetPartitionFlagRoot); err != nil {
		t.Errorf("ApplyCPUSetPartition default returned err=%v, want nil", err)
	}
	if err := f.ApplyCPU(ctx, "kubepods", &cgcommon.CPUData{}); err != nil {
		t.Errorf("ApplyCPU default returned err=%v, want nil", err)
	}
	if err := f.ApplySchedLoadBalance(ctx, "kubepods", false); err != nil {
		t.Errorf("ApplySchedLoadBalance default returned err=%v, want nil", err)
	}
	if err := f.AttachPID(ctx, "kubepods", 1); err != nil {
		t.Errorf("AttachPID default returned err=%v, want nil", err)
	}

	cs, err := f.ReadCPUSet(ctx, "kubepods")
	if err != nil {
		t.Errorf("ReadCPUSet default returned err=%v, want nil", err)
	}
	if !cs.IsEmpty() {
		t.Errorf("ReadCPUSet default = %v, want empty", cs.String())
	}

	if mt, sz, err := f.StatCPUSet(ctx, "kubepods"); err != nil || !mt.IsZero() || sz != 0 {
		t.Errorf("StatCPUSet default = (%v,%d,%v), want (zero,0,nil)", mt, sz, err)
	}
	if mt, sz, err := f.StatCgroupFile(ctx, "kubepods", "cpuset.cpus"); err != nil || !mt.IsZero() || sz != 0 {
		t.Errorf("StatCgroupFile default = (%v,%d,%v), want (zero,0,nil)", mt, sz, err)
	}
	if flag, err := f.ReadCPUSetPartition(ctx, "kubepods"); err != nil || flag != cgcommon.CPUSetPartitionFlagMember {
		t.Errorf("ReadCPUSetPartition default = (%v,%v), want (member,nil)", flag, err)
	}
	if kids, err := f.ListRootChildren(ctx); err != nil || len(kids) != 0 {
		t.Errorf("ListRootChildren default = (%v,%v), want (nil,nil)", kids, err)
	}
	if kids, err := f.ListChildren(ctx, "kubepods"); err != nil || len(kids) != 0 {
		t.Errorf("ListChildren default = (%v,%v), want (nil,nil)", kids, err)
	}
	if mt, err := f.StatDir(ctx, "kubepods"); err != nil || !mt.IsZero() {
		t.Errorf("StatDir default = (%v,%v), want (zero,nil)", mt, err)
	}
}

// TestFakeCgroupClientOverride verifies that embedding fakes can override a
// subset of methods and inherit the rest from FakeCgroupClient defaults.
func TestFakeCgroupClientOverride(t *testing.T) {
	t.Parallel()

	f := &attachRecordingFake{}
	if err := f.AttachPID(context.Background(), "kubepods", 1234); err != nil {
		t.Fatalf("AttachPID: %v", err)
	}
	if f.gotPID != 1234 {
		t.Errorf("gotPID = %d, want 1234", f.gotPID)
	}
	if err := f.ApplyCPUSet(context.Background(), "kubepods", &cgcommon.CPUSetData{}); err != nil {
		t.Errorf("inherited ApplyCPUSet returned err=%v, want nil", err)
	}
}

type attachRecordingFake struct {
	FakeCgroupClient
	gotPID int
}

func (f *attachRecordingFake) AttachPID(_ context.Context, _ string, pid int) error {
	f.gotPID = pid
	return nil
}
