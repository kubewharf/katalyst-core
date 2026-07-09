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
	"time"

	cgcommon "github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// FakeCgroupClient is a no-op implementation of CgroupClient meant to be
// embedded by test doubles. It exists so that widening CgroupClient does not
// force every downstream fake to grow N new stub methods — tests only need to
// override the specific methods they exercise.
//
// It is intentionally located in the production package instead of a _test.go
// file because Go's build system does not export _test.go symbols across
// packages; downstream package tests may need to import this stub directly.
//
// Zero value is usable. All methods return safe defaults: nil errors, empty
// slices, empty CPUSet, member cpuset partition, and v2 cgroup version. Embed
// this stub and override only the methods a test cares about:
//
//	type myFake struct {
//	    client.FakeCgroupClient
//	    calls []someCall
//	}
//
//	func (f *myFake) ApplyCPUSet(...) error { ... }
//
// The rest of the interface stays satisfied by FakeCgroupClient's stubs.
type FakeCgroupClient struct{}

var _ CgroupClient = (*FakeCgroupClient)(nil)

func (FakeCgroupClient) ApplyCPUSet(context.Context, string, *cgcommon.CPUSetData) error {
	return nil
}

func (FakeCgroupClient) ApplyCPUSetPartition(context.Context, string, cgcommon.CPUSetPartitionFlag) error {
	return nil
}

func (FakeCgroupClient) ApplyCPU(context.Context, string, *cgcommon.CPUData) error {
	return nil
}

func (FakeCgroupClient) ApplySchedLoadBalance(context.Context, string, bool) error {
	return nil
}

func (FakeCgroupClient) AttachPID(context.Context, string, int) error {
	return nil
}

func (FakeCgroupClient) Version(context.Context) CgroupVersion {
	return CgroupVersionV2
}

func (FakeCgroupClient) ReadCPUSet(context.Context, string) (machine.CPUSet, error) {
	return machine.NewCPUSet(), nil
}

func (FakeCgroupClient) StatCPUSet(context.Context, string) (time.Time, int64, error) {
	return time.Time{}, 0, nil
}

func (FakeCgroupClient) StatCgroupFile(context.Context, string, string) (time.Time, int64, error) {
	return time.Time{}, 0, nil
}

func (FakeCgroupClient) ReadCPUSetPartition(context.Context, string) (cgcommon.CPUSetPartitionFlag, error) {
	return cgcommon.CPUSetPartitionFlagMember, nil
}

func (FakeCgroupClient) ListRootChildren(context.Context) ([]string, error) {
	return nil, nil
}

func (FakeCgroupClient) ListChildren(context.Context, string) ([]string, error) {
	return nil, nil
}

func (FakeCgroupClient) StatDir(context.Context, string) (time.Time, error) {
	return time.Time{}, nil
}

func (FakeCgroupClient) Prune(map[string]struct{}) {}
