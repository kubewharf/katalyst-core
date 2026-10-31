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

package topology

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func TestCgroupVersionPolicyContracts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		policy           cgroupVersionPolicy
		observedCPUs     string
		observedMems     string
		configuredCPUs   string
		configuredMems   string
		acceptsEmptyCPUs bool
		effectiveCPUSet  bool
		partitionRoots   bool
	}{
		{
			name:         "v1",
			policy:       cgroupV1Policy,
			observedCPUs: "cpuset.cpus", observedMems: "cpuset.mems",
			configuredCPUs: "cpuset.cpus", configuredMems: "cpuset.mems",
		},
		{
			name:         "v2",
			policy:       cgroupV2Policy,
			observedCPUs: "cpuset.cpus.effective", observedMems: "cpuset.mems.effective",
			configuredCPUs: "cpuset.cpus", configuredMems: "cpuset.mems",
			acceptsEmptyCPUs: true, effectiveCPUSet: true, partitionRoots: false,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := tc.policy.observedCPUsFile(); got != tc.observedCPUs {
				t.Fatalf("observedCPUsFile() = %q, want %q", got, tc.observedCPUs)
			}
			if got := tc.policy.observedMemsFile(); got != tc.observedMems {
				t.Fatalf("observedMemsFile() = %q, want %q", got, tc.observedMems)
			}
			if got := tc.policy.configuredCPUsFile(); got != tc.configuredCPUs {
				t.Fatalf("configuredCPUsFile() = %q, want %q", got, tc.configuredCPUs)
			}
			if got := tc.policy.configuredMemsFile(); got != tc.configuredMems {
				t.Fatalf("configuredMemsFile() = %q, want %q", got, tc.configuredMems)
			}

			err := tc.policy.validateConfiguredCPUs(machine.NewCPUSet())
			if tc.acceptsEmptyCPUs && err != nil {
				t.Fatalf("validateConfiguredCPUs(empty) error = %v, want nil", err)
			}
			if !tc.acceptsEmptyCPUs && !errors.Is(err, ErrEmptyCPUSetUnsupported) {
				t.Fatalf("validateConfiguredCPUs(empty) error = %v, want ErrEmptyCPUSetUnsupported", err)
			}

			caps := tc.policy.capabilities(true)
			if !caps.StableIdentity || !caps.KernelParentContainment {
				t.Fatalf("capabilities = %+v, want stable identity and kernel containment", caps)
			}
			if caps.EmptyConfiguredCPUSet != tc.acceptsEmptyCPUs ||
				caps.EffectiveCPUSet != tc.effectiveCPUSet ||
				caps.PartitionRoots != tc.partitionRoots {
				t.Fatalf("capabilities = %+v, want empty=%t effective=%t partition=%t",
					caps, tc.acceptsEmptyCPUs, tc.effectiveCPUSet, tc.partitionRoots)
			}
		})
	}
}

func TestCgroupDriverConstructorsSelectPolicyAndPinRoot(t *testing.T) {
	t.Parallel()
	root := resolvedPath(t, t.TempDir())
	tests := []struct {
		name string
		new  func(string, []string) (HierarchyDriver, error)
		want cgroupVersionPolicy
	}{
		{name: "v1", new: NewCgroupV1Driver, want: cgroupV1Policy},
		{name: "v2", new: NewCgroupV2Driver, want: cgroupV2Policy},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			driver, err := tc.new(root, nil)
			if err != nil {
				t.Fatalf("constructor error = %v", err)
			}
			concrete, ok := driver.(*cgroupFSDriver)
			if !ok {
				t.Fatalf("driver type = %T, want *cgroupFSDriver", driver)
			}
			if concrete.policy != tc.want {
				t.Fatalf("policy = %v, want %v", concrete.policy, tc.want)
			}
			if concrete.rootFD < 0 {
				t.Fatalf("rootFD = %d, want pinned descriptor", concrete.rootFD)
			}
			wantCapabilities := tc.want.capabilities(runtime.GOOS == "linux")
			if got := driver.Capabilities(); got != wantCapabilities {
				t.Fatalf("capabilities = %+v, want %+v", got, wantCapabilities)
			}
			fd := concrete.rootFD
			if err := driver.Close(); err != nil {
				t.Fatalf("Close() error = %v", err)
			}
			if err := unix.Fstat(fd, &unix.Stat_t{}); !errors.Is(err, syscall.EBADF) {
				t.Fatalf("Fstat(closed root fd) error = %v, want EBADF", err)
			}
		})
	}
}

func TestCgroupV2DriverReadsConfiguredAndEffectiveStateFromOneDirectoryFD(t *testing.T) {
	t.Parallel()
	root := resolvedPath(t, t.TempDir())
	writeTestCgroupV2Directory(t, filepath.Join(root, "pod"), "0-3", "0", "1-2", "1")

	hierarchyDriver, err := NewCgroupV2Driver(root, nil)
	if err != nil {
		t.Fatalf("NewCgroupV2Driver() error = %v", err)
	}
	defer hierarchyDriver.Close()
	driver := hierarchyDriver.(*cgroupFSDriver)
	originalRead := driver.readFileAt
	var readFDs []int
	driver.readFileAt = func(dirFD int, name string) ([]byte, error) {
		readFDs = append(readFDs, dirFD)
		return originalRead(dirFD, name)
	}

	entry, err := driver.ReadEntry(context.Background(), "pod")
	if err != nil {
		t.Fatalf("ReadEntry() error = %v", err)
	}
	if got := entry.CPUs.String(); got != "0-3" || entry.Mems != "0" {
		t.Fatalf("effective state = %q/%q, want 0-3/0", got, entry.Mems)
	}
	if got := entry.ConfiguredCPUs.String(); got != "1-2" || entry.ConfiguredMems != "1" {
		t.Fatalf("configured state = %q/%q, want 1-2/1", got, entry.ConfiguredMems)
	}
	if len(readFDs) != 4 {
		t.Fatalf("read calls = %d, want 4", len(readFDs))
	}
	for _, fd := range readFDs[1:] {
		if fd != readFDs[0] {
			t.Fatalf("read FDs = %v, want one opened directory FD", readFDs)
		}
	}
}

func TestCgroupV2DriverClassifiesMissingCpusetControllerInterface(t *testing.T) {
	t.Parallel()
	root := resolvedPath(t, t.TempDir())
	podDir := filepath.Join(root, "pod")
	if err := os.MkdirAll(podDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(podDir, "cpuset.cpus"), []byte("0-3"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(podDir, "cpuset.mems"), []byte("0"), 0o644); err != nil {
		t.Fatal(err)
	}

	hierarchyDriver, err := NewCgroupV2Driver(root, nil)
	if err != nil {
		t.Fatalf("NewCgroupV2Driver() error = %v", err)
	}
	defer hierarchyDriver.Close()

	_, err = hierarchyDriver.ReadEntry(context.Background(), "pod")
	if !errors.Is(err, ErrCgroupControllerUnavailable) {
		t.Fatalf("ReadEntry() error = %v, want ErrCgroupControllerUnavailable", err)
	}
	if got := hierarchyDriver.Classify(err, HierarchyOperationRead); got != HierarchyErrorStale {
		t.Fatalf("Classify() = %s, want %s", got, HierarchyErrorStale)
	}
}

func TestCgroupV2DriverEmptyWriteStaysBoundToOpenedGeneration(t *testing.T) {
	t.Parallel()
	parent := resolvedPath(t, t.TempDir())
	root := filepath.Join(parent, "root")
	writeTestCgroupV2Directory(t, filepath.Join(root, "pod"), "0-3", "0", "1-2", "0")

	hierarchyDriver, err := NewCgroupV2Driver(root, nil)
	if err != nil {
		t.Fatalf("NewCgroupV2Driver() error = %v", err)
	}
	defer hierarchyDriver.Close()
	driver := hierarchyDriver.(*cgroupFSDriver)
	identity, err := driver.StatIdentity(context.Background(), "pod")
	if err != nil {
		t.Fatal(err)
	}

	originalOpen := driver.openFileAt
	movedPod := filepath.Join(parent, "moved-pod")
	var openedIdentity CgroupIdentity
	driver.openFileAt = func(dirFD int, name string, flags int, mode uint32) (int, error) {
		if err := os.Rename(filepath.Join(root, "pod"), movedPod); err != nil {
			return -1, err
		}
		writeTestCgroupV2Directory(t, filepath.Join(root, "pod"), "4-7", "1", "4-7", "1")
		var stat unix.Stat_t
		if err := unix.Fstat(dirFD, &stat); err != nil {
			return -1, err
		}
		openedIdentity = CgroupIdentity{Device: uint64(stat.Dev), Inode: stat.Ino} //nolint:unconvert // Stat_t.Dev is int32 on darwin
		driver.openFileAt = originalOpen
		return originalOpen(dirFD, name, flags, mode)
	}

	if err := driver.WriteCPUs(context.Background(), "pod", identity, machine.NewCPUSet()); err != nil {
		t.Fatalf("WriteCPUs(empty) error = %v", err)
	}
	if openedIdentity != identity {
		t.Fatalf("openFileAt directory identity = %v, want validated generation %v", openedIdentity, identity)
	}
	// Empty configured state in v2 requires a nonzero-length payload; a regular file verifies the newline-only payload,
	// while the replacement generation remains unaffected by writes through the old directory FD.
	assertFileContent(t, filepath.Join(movedPod, "cpuset.cpus"), "\n")
	assertFileContent(t, filepath.Join(root, "pod", "cpuset.cpus"), "4-7")
}

func TestCgroupDriverEmptyMemsWritePreservesVersionSemantics(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name        string
		newDriver   func(string, []string) (HierarchyDriver, error)
		prepare     func(*testing.T, string)
		wantPayload string
	}{
		{
			name:      "v2 uses nonzero newline payload",
			newDriver: NewCgroupV2Driver,
			prepare: func(t *testing.T, root string) {
				writeTestCgroupV2Directory(t, filepath.Join(root, "pod"), "0-3", "0", "0-3", "0")
			},
			wantPayload: "\n",
		},
		{
			name:      "v1 keeps zero-length payload",
			newDriver: NewCgroupV1Driver,
			prepare: func(t *testing.T, root string) {
				dir := filepath.Join(root, "pod")
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "cpuset.cpus"), []byte("0-3"), 0o644); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "cpuset.mems"), []byte("0"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			wantPayload: "",
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := resolvedPath(t, t.TempDir())
			tc.prepare(t, root)
			driver, err := tc.newDriver(root, nil)
			if err != nil {
				t.Fatalf("new driver error = %v", err)
			}
			defer driver.Close()
			identity, err := driver.StatIdentity(context.Background(), "pod")
			if err != nil {
				t.Fatal(err)
			}
			if err := driver.WriteMems(context.Background(), "pod", identity, ""); err != nil {
				t.Fatalf("WriteMems(empty) error = %v", err)
			}
			assertFileContent(t, filepath.Join(root, "pod", "cpuset.mems"), tc.wantPayload)
		})
	}
}

func TestCgroupV1DriverUsesNonMountedRootPathForAllHierarchyIO(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for _, rel := range []string{"pod", "pod/child"} {
		dir := filepath.Join(root, rel)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "cpuset.cpus"), []byte("0-3"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "cpuset.mems"), []byte("0"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	driver := newTestCgroupV1Driver(t, root, nil)
	ctx := context.Background()

	roots, err := driver.Roots(ctx)
	if err != nil {
		t.Fatalf("Roots() error = %v", err)
	}
	if len(roots) != 1 || roots[0].Rel != "pod" {
		t.Fatalf("Roots() = %+v, want only pod", roots)
	}
	wantIdentity := identityFromDirectory(t, filepath.Join(root, "pod"))
	if roots[0].Identity != wantIdentity {
		t.Fatalf("root identity = %v, want %v", roots[0].Identity, wantIdentity)
	}
	if got, err := driver.StatIdentity(ctx, "pod"); err != nil || got != wantIdentity {
		t.Fatalf("StatIdentity() = (%v, %v), want (%v, nil)", got, err, wantIdentity)
	}
	entry, err := driver.ReadEntry(ctx, "pod")
	if err != nil {
		t.Fatalf("ReadEntry() error = %v", err)
	}
	if got := entry.CPUs.String(); got != "0-3" || entry.Mems != "0" {
		t.Fatalf("ReadEntry() = cpus:%q mems:%q, want 0-3/0", got, entry.Mems)
	}
	children, err := driver.ListChildren(ctx, "pod")
	if err != nil {
		t.Fatalf("ListChildren() error = %v", err)
	}
	if len(children) != 1 || children[0].Name != "child" {
		t.Fatalf("ListChildren() = %+v, want only child", children)
	}
	if err := driver.WriteCPUs(ctx, "pod", wantIdentity, machine.MustParse("1-2")); err != nil {
		t.Fatalf("WriteCPUs() error = %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "pod", "cpuset.cpus"))
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "1-2" {
		t.Fatalf("cpuset.cpus = %q, want 1-2", raw)
	}
}

func TestCgroupFSDriverListChildrenSkipsVanishedChild(t *testing.T) {
	t.Parallel()
	root := resolvedPath(t, t.TempDir())
	parent := filepath.Join(root, "parent")
	survivor := filepath.Join(parent, "survivor")
	vanished := filepath.Join(parent, "vanished")
	for _, path := range []string{survivor, vanished} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	driver := newTestCgroupV1Driver(t, root, nil)
	wantIdentity := identityFromDirectory(t, survivor)
	originalOpen := driver.openDirAt
	driver.openDirAt = func(dirFD int, name string, flags int, mode uint32) (int, error) {
		if name == "vanished" {
			if err := os.Remove(vanished); err != nil && !errors.Is(err, os.ErrNotExist) {
				return -1, err
			}
		}
		return originalOpen(dirFD, name, flags, mode)
	}

	children, err := driver.ListChildren(context.Background(), "parent")
	if err != nil {
		t.Fatalf("ListChildren() error = %v", err)
	}
	if len(children) != 1 || children[0] != (ChildRef{Name: "survivor", Identity: wantIdentity}) {
		t.Fatalf("ListChildren() = %+v, want surviving child with identity %v", children, wantIdentity)
	}
}

func TestCgroupFSDriverListChildrenReportsRequestedParentAbsence(t *testing.T) {
	t.Parallel()
	root := resolvedPath(t, t.TempDir())
	parent := filepath.Join(root, "parent")
	if err := os.MkdirAll(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	driver := newTestCgroupV1Driver(t, root, nil)
	if err := os.Remove(parent); err != nil {
		t.Fatal(err)
	}

	children, err := driver.ListChildren(context.Background(), "parent")

	if children != nil {
		t.Fatalf("ListChildren() children = %+v, want nil", children)
	}
	if !isCgroupPathAbsent(err) {
		t.Fatalf("ListChildren() error = %v, want typed requested-parent absence", err)
	}
}

func TestCgroupFSDriverListChildrenDoesNotSkipPermissionError(t *testing.T) {
	t.Parallel()
	assertListChildrenDoesNotSkipOpenError(t, syscall.EACCES)
}

func TestCgroupFSDriverListChildrenDoesNotSkipCrossDeviceError(t *testing.T) {
	t.Parallel()
	assertListChildrenDoesNotSkipOpenError(t, ErrCgroupCrossDevice)
}

func TestCgroupFSDriverListChildrenDoesNotSkipIdentityError(t *testing.T) {
	t.Parallel()
	assertListChildrenDoesNotSkipOpenError(t, ErrCgroupIdentityChanged)
}

func TestCgroupFSDriverListChildrenDoesNotSkipSymlinkError(t *testing.T) {
	t.Parallel()
	root := resolvedPath(t, t.TempDir())
	parent := filepath.Join(root, "parent")
	if err := os.MkdirAll(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(parent, "child")); err != nil {
		t.Fatal(err)
	}
	driver := newTestCgroupV1Driver(t, root, nil)

	_, err := driver.ListChildren(context.Background(), "parent")

	if err == nil || !strings.Contains(err.Error(), "symlink is not allowed") {
		t.Fatalf("ListChildren() error = %v, want symlink rejection", err)
	}
}

func assertListChildrenDoesNotSkipOpenError(t *testing.T, injected error) {
	t.Helper()
	root := resolvedPath(t, t.TempDir())
	if err := os.MkdirAll(filepath.Join(root, "parent", "child"), 0o755); err != nil {
		t.Fatal(err)
	}
	driver := newTestCgroupV1Driver(t, root, nil)
	originalOpen := driver.openDirAt
	driver.openDirAt = func(dirFD int, name string, flags int, mode uint32) (int, error) {
		if name == "child" {
			return -1, injected
		}
		return originalOpen(dirFD, name, flags, mode)
	}

	_, err := driver.ListChildren(context.Background(), "parent")

	if !errors.Is(err, injected) {
		t.Fatalf("ListChildren() error = %v, want %v", err, injected)
	}
}

func TestCgroupV1DriverReadsAndValidatesMembershipFromPinnedDirectory(t *testing.T) {
	t.Parallel()
	root := resolvedPath(t, t.TempDir())
	holderDir := filepath.Join(root, "holder")
	if err := os.MkdirAll(holderDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{
		"cpuset.cpus":  "0-3",
		"cpuset.mems":  "0",
		"tasks":        "",
		"cgroup.procs": "",
	} {
		if err := os.WriteFile(filepath.Join(holderDir, name), []byte(value), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	driver := newTestCgroupV1Driver(t, root, nil)
	identity, err := driver.StatIdentity(context.Background(), "holder")
	if err != nil {
		t.Fatal(err)
	}

	entry, err := driver.ReadEntryWithActivity(context.Background(), "holder", identity)
	if err != nil {
		t.Fatalf("ReadEntryWithActivity() error = %v", err)
	}
	if !entry.Activity.Inactive() {
		t.Fatalf("activity = %+v, want both membership files proved empty", entry.Activity)
	}

	if err := os.WriteFile(filepath.Join(holderDir, "tasks"), []byte("not-a-pid\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := driver.ReadEntryWithActivity(context.Background(), "holder", identity); err == nil {
		t.Fatal("ReadEntryWithActivity() accepted malformed tasks content")
	}
}

func TestBudgetedCgroupV1ActivityReadChargesPinnedChildScan(t *testing.T) {
	t.Parallel()
	root := resolvedPath(t, t.TempDir())
	holderDir := filepath.Join(root, "holder")
	if err := os.MkdirAll(filepath.Join(holderDir, "child"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{
		"cpuset.cpus": "0-3", "cpuset.mems": "0", "tasks": "", "cgroup.procs": "",
	} {
		if err := os.WriteFile(filepath.Join(holderDir, name), []byte(value), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	base := newTestCgroupV1Driver(t, root, nil)
	identity, err := base.StatIdentity(context.Background(), "holder")
	if err != nil {
		t.Fatal(err)
	}
	budget := NewBudgetTracker(ConvergenceBudget{MaxSnapshotNodes: 1})
	if err := budget.VisitNode("holder", identity, 1); err != nil {
		t.Fatal(err)
	}
	driver := NewBudgetedHierarchyDriver(base, budget)
	reader := driver.(hierarchyActivityReader)

	if _, err := reader.ReadEntryWithActivity(context.Background(), "holder", identity); !errors.Is(err, ErrNodeBudgetExceeded) {
		t.Fatalf("ReadEntryWithActivity() error = %v, want node budget from pinned child scan", err)
	}
}

func TestBudgetedCgroupV1ActivityReadHonorsTrackerDeadlineDuringPinnedChildScan(t *testing.T) {
	t.Parallel()
	root := resolvedPath(t, t.TempDir())
	holderDir := filepath.Join(root, "holder")
	if err := os.MkdirAll(filepath.Join(holderDir, "child-a"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(holderDir, "child-b"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{
		"cpuset.cpus": "0-3", "cpuset.mems": "0", "tasks": "", "cgroup.procs": "",
	} {
		if err := os.WriteFile(filepath.Join(holderDir, name), []byte(value), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	base := newTestCgroupV1Driver(t, root, nil)
	identity, err := base.StatIdentity(context.Background(), "holder")
	if err != nil {
		t.Fatal(err)
	}
	budget := NewBudgetTracker(ConvergenceBudget{})
	openAt := base.openDirAt
	childOpens := 0
	base.openDirAt = func(dirFD int, name string, flags int, mode uint32) (int, error) {
		if strings.HasPrefix(name, "child-") {
			childOpens++
			if childOpens == 1 {
				budget.mu.Lock()
				budget.limit.Deadline = time.Now().Add(-time.Second)
				budget.mu.Unlock()
			}
		}
		return openAt(dirFD, name, flags, mode)
	}
	driver := NewBudgetedHierarchyDriver(base, budget)
	reader := driver.(hierarchyActivityReader)

	if _, err := reader.ReadEntryWithActivity(context.Background(), "holder", identity); !errors.Is(err, ErrConvergenceDeadlineExceeded) {
		t.Fatalf("ReadEntryWithActivity() error = %v, want tracker deadline", err)
	}
	if childOpens != 1 {
		t.Fatalf("opened children = %d, want scan to stop after tracker deadline", childOpens)
	}
}

func TestCgroupV1DriverActivityReadRejectsChildCreationInsideAtomicWindow(t *testing.T) {
	t.Parallel()
	root := resolvedPath(t, t.TempDir())
	holderDir := filepath.Join(root, "holder")
	if err := os.MkdirAll(holderDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{
		"cpuset.cpus": "0-3", "cpuset.mems": "0", "tasks": "", "cgroup.procs": "",
	} {
		if err := os.WriteFile(filepath.Join(holderDir, name), []byte(value), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	driver := newTestCgroupV1Driver(t, root, nil)
	identity, err := driver.StatIdentity(context.Background(), "holder")
	if err != nil {
		t.Fatal(err)
	}
	originalRead := driver.readFileAt
	driver.readFileAt = func(dirFD int, name string) ([]byte, error) {
		raw, readErr := originalRead(dirFD, name)
		if name == "tasks" {
			if mkdirErr := os.Mkdir(filepath.Join(holderDir, "new-child"), 0o755); mkdirErr != nil {
				t.Fatal(mkdirErr)
			}
		}
		return raw, readErr
	}

	if _, err := driver.ReadEntryWithActivity(context.Background(), "holder", identity); !errors.Is(err, ErrCgroupIdentityChanged) {
		t.Fatalf("ReadEntryWithActivity() error = %v, want child-closure stale error", err)
	}
}

func TestCgroupV1DriverActivityReadUsesOnePinnedFDAndRejectsExpectedParentABA(t *testing.T) {
	t.Parallel()
	root := resolvedPath(t, t.TempDir())
	holderDir := filepath.Join(root, "holder")
	if err := os.MkdirAll(holderDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{
		"cpuset.cpus": "0-3", "cpuset.mems": "0", "tasks": "", "cgroup.procs": "",
	} {
		if err := os.WriteFile(filepath.Join(holderDir, name), []byte(value), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	driver := newTestCgroupV1Driver(t, root, nil)
	identity, err := driver.StatIdentity(context.Background(), "holder")
	if err != nil {
		t.Fatal(err)
	}
	originalRead := driver.readFileAt
	var readFDs []int
	driver.readFileAt = func(dirFD int, name string) ([]byte, error) {
		readFDs = append(readFDs, dirFD)
		return originalRead(dirFD, name)
	}

	entry, err := driver.ReadEntryWithActivity(context.Background(), "holder", identity)
	if err != nil {
		t.Fatalf("ReadEntryWithActivity() error = %v", err)
	}
	if !entry.Activity.Inactive() {
		t.Fatalf("activity = %+v, want childless inactive leaf", entry.Activity)
	}
	for _, fd := range readFDs[1:] {
		if fd != readFDs[0] {
			t.Fatalf("activity read FDs = %v, want one pinned directory FD", readFDs)
		}
	}

	wrong := identity
	wrong.Inode++
	if _, err := driver.ReadEntryWithActivity(context.Background(), "holder", wrong); !errors.Is(err, ErrCgroupIdentityChanged) {
		t.Fatalf("wrong expected parent identity error = %v, want ErrCgroupIdentityChanged", err)
	}
}

func TestCgroupV1DriverRejectsPathOutsideRoot(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	root := filepath.Join(parent, "root")
	outside := filepath.Join(parent, "outside")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	driver := newTestCgroupV1Driver(t, root, nil)

	if _, err := driver.StatIdentity(context.Background(), "../outside"); err == nil {
		t.Fatal("StatIdentity() accepted a relative path outside rootPath")
	}
}

func TestCgroupV1DriverRejectsConfiguredRootSymlink(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	root := filepath.Join(parent, "root")
	outside := filepath.Join(parent, "outside")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "configured")); err != nil {
		t.Fatal(err)
	}

	driver := newTestCgroupV1Driver(t, root, []string{"configured"})
	if _, err := driver.Roots(context.Background()); err == nil {
		t.Fatal("Roots() accepted a configured root symlink")
	}
}

func TestCgroupV1DriverConstructorRejectsAncestorSymlink(t *testing.T) {
	t.Parallel()
	parent := resolvedPath(t, t.TempDir())
	realParent := filepath.Join(parent, "real")
	root := filepath.Join(realParent, "root")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(parent, "link")
	if err := os.Symlink(realParent, link); err != nil {
		t.Fatal(err)
	}

	driver, err := NewCgroupV1Driver(filepath.Join(link, "root"), nil)
	if err == nil {
		_ = driver.Close()
		t.Fatal("NewCgroupV1Driver() accepted a symlink in rootPath")
	}
}

func TestCgroupV1DriverPinsRootFDAndIdentityAcrossPathReplacement(t *testing.T) {
	t.Parallel()
	parent := resolvedPath(t, t.TempDir())
	root := filepath.Join(parent, "root")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestCgroupDirectory(t, filepath.Join(root, "old"))

	driver, err := NewCgroupV1Driver(resolvedPath(t, root), nil)
	if err != nil {
		t.Fatalf("NewCgroupV1Driver() error = %v", err)
	}
	defer driver.Close()

	moved := filepath.Join(parent, "moved")
	if err := os.Rename(root, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestCgroupDirectory(t, filepath.Join(root, "new"))

	roots, err := driver.Roots(context.Background())
	if err != nil {
		t.Fatalf("Roots() error = %v", err)
	}
	if len(roots) != 1 || roots[0].Rel != "old" {
		t.Fatalf("Roots() = %+v, want pinned generation child old", roots)
	}
	if _, err := driver.StatIdentity(context.Background(), "new"); !errors.Is(err, syscall.ENOENT) {
		t.Fatalf("StatIdentity(new) error = %v, want ENOENT from pinned root", err)
	}
}

func TestCgroupV1DriverRootsBelongToPinnedRootGeneration(t *testing.T) {
	t.Parallel()
	parent := resolvedPath(t, t.TempDir())
	root := filepath.Join(parent, "root")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestCgroupDirectory(t, filepath.Join(root, "first"))

	driver, err := NewCgroupV1Driver(root, nil)
	if err != nil {
		t.Fatalf("NewCgroupV1Driver() error = %v", err)
	}
	defer driver.Close()
	pinnedIdentity, err := driver.StatIdentity(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}

	moved := filepath.Join(parent, "moved")
	if err := os.Rename(root, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestCgroupDirectory(t, filepath.Join(root, "second"))

	roots, err := driver.Roots(context.Background())
	if err != nil {
		t.Fatalf("Roots() error = %v", err)
	}
	if len(roots) != 1 || roots[0].Rel != "first" {
		t.Fatalf("Roots() = %+v, want first from pinned generation", roots)
	}
	if got, err := driver.StatIdentity(context.Background(), ""); err != nil || got != pinnedIdentity {
		t.Fatalf("root identity after replacement = (%v, %v), want (%v, nil)", got, err, pinnedIdentity)
	}
}

func TestCgroupV1DriverCloseReleasesPinnedRootFD(t *testing.T) {
	t.Parallel()
	root := resolvedPath(t, t.TempDir())
	driver, err := NewCgroupV1Driver(resolvedPath(t, root), nil)
	if err != nil {
		t.Fatalf("NewCgroupV1Driver() error = %v", err)
	}
	concrete := driver.(*cgroupFSDriver)
	fd := concrete.rootFD

	if err := driver.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if _, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0); !errors.Is(err, syscall.EBADF) {
		t.Fatalf("fcntl on closed root fd error = %v, want EBADF", err)
	}
	if _, err := driver.Roots(context.Background()); !errors.Is(err, syscall.EBADF) {
		t.Fatalf("Roots() after Close error = %v, want EBADF", err)
	}
	if err := driver.Close(); err != nil {
		t.Fatalf("second Close() error = %v, want idempotent nil", err)
	}
}

func TestCgroupV1DriverRejectsIntermediateSymlinkEscapeForAllIO(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	root := filepath.Join(parent, "root")
	outside := filepath.Join(parent, "outside")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestCgroupDirectory(t, filepath.Join(outside, "pod"))
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	driver := newTestCgroupV1Driver(t, root, nil)
	ctx := context.Background()
	rel := filepath.Join("escape", "pod")
	expected := identityFromDirectory(t, filepath.Join(outside, "pod"))

	if _, err := driver.StatIdentity(ctx, rel); err == nil {
		t.Error("StatIdentity() accepted an intermediate symlink")
	}
	if _, err := driver.ReadEntry(ctx, rel); err == nil {
		t.Error("ReadEntry() accepted an intermediate symlink")
	}
	if _, err := driver.ListChildren(ctx, rel); err == nil {
		t.Error("ListChildren() accepted an intermediate symlink")
	}
	if err := driver.WriteCPUs(ctx, rel, expected, machine.MustParse("2-3")); err == nil {
		t.Error("WriteCPUs() accepted an intermediate symlink")
	}
	assertFileContent(t, filepath.Join(outside, "pod", "cpuset.cpus"), "0-1")
}

func TestCgroupV1DriverRejectsDirectChildSymlinkEscapeForAllIO(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	root := filepath.Join(parent, "root")
	outside := filepath.Join(parent, "outside")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestCgroupDirectory(t, outside)
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	driver := newTestCgroupV1Driver(t, root, nil)
	ctx := context.Background()
	expected := identityFromDirectory(t, outside)

	if _, err := driver.Roots(ctx); err == nil {
		t.Error("Roots() accepted a direct child symlink")
	}
	if _, err := driver.StatIdentity(ctx, "escape"); err == nil {
		t.Error("StatIdentity() accepted a direct child symlink")
	}
	if _, err := driver.ReadEntry(ctx, "escape"); err == nil {
		t.Error("ReadEntry() accepted a direct child symlink")
	}
	if _, err := driver.ListChildren(ctx, "escape"); err == nil {
		t.Error("ListChildren() accepted a direct child symlink")
	}
	if err := driver.WriteCPUs(ctx, "escape", expected, machine.MustParse("2-3")); err == nil {
		t.Error("WriteCPUs() accepted a direct child symlink")
	}
	assertFileContent(t, filepath.Join(outside, "cpuset.cpus"), "0-1")
}

func TestCgroupV1DriverRejectsCrossDeviceComponentWithInjectedFstat(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "pod"), 0o755); err != nil {
		t.Fatal(err)
	}
	fstatCalls := 0
	driver := newCgroupV1Driver(resolvedPath(t, root), nil, func(fd int, stat *unix.Stat_t) error {
		if err := unix.Fstat(fd, stat); err != nil {
			return err
		}
		fstatCalls++
		if fstatCalls == 3 {
			stat.Dev++
		}
		return nil
	}, true)
	if err := driver.pinRoot(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = driver.Close() })

	if _, err := driver.StatIdentity(context.Background(), "pod"); !errors.Is(err, ErrCgroupCrossDevice) {
		t.Fatalf("StatIdentity() error = %v, want cross-device rejection", err)
	}
}

func TestCgroupV1DriverReadEntryChecksIdentityBeforeAndAfter(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	rel := "pod"
	dir := filepath.Join(root, rel)
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cpuset.cpus"), []byte("0-1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cpuset.mems"), []byte("0"), 0o644); err != nil {
		t.Fatal(err)
	}
	mutateIdentity := false
	driver := newCgroupV1Driver(resolvedPath(t, root), nil, func(fd int, stat *unix.Stat_t) error {
		if err := unix.Fstat(fd, stat); err != nil {
			return err
		}
		if mutateIdentity {
			stat.Ino++
		}
		return nil
	}, true)
	if err := driver.pinRoot(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = driver.Close() })
	driver.readFileAt = func(dirFD int, name string) ([]byte, error) {
		raw, err := readFileAt(dirFD, name)
		if name == "cpuset.mems" {
			mutateIdentity = true
		}
		return raw, err
	}
	entry, err := driver.ReadEntry(context.Background(), rel)
	if !errors.Is(err, ErrCgroupIdentityChanged) {
		t.Fatalf("ReadEntry() error = %v, want identity changed", err)
	}
	if entry.Rel != "" || entry.Identity != (CgroupIdentity{}) || entry.CPUs.Initialed || entry.Mems != "" {
		t.Fatalf("ReadEntry() returned partial entry: %+v", entry)
	}
}

func TestCgroupV1DriverWriteCPUsRejectsExpectedIdentityMismatch(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	rel := "pod"
	if err := os.Mkdir(filepath.Join(root, rel), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, rel, "cpuset.cpus"), []byte("0-1"), 0o644); err != nil {
		t.Fatal(err)
	}
	driver, err := NewCgroupV1Driver(resolvedPath(t, root), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer driver.Close()
	oldIdentity := identityFromDirectory(t, filepath.Join(root, rel))
	oldIdentity.Inode++

	err = driver.WriteCPUs(context.Background(), rel, oldIdentity, machine.MustParse("0-1"))
	if !errors.Is(err, ErrCgroupIdentityChanged) {
		t.Fatalf("WriteCPUs() error = %v, want identity changed", err)
	}
}

func TestCgroupV1DriverWriteFailsClosedWithoutFDBinding(t *testing.T) {
	t.Parallel()
	driver := newCgroupV1Driver(resolvedPath(t, t.TempDir()), nil, unix.Fstat, true)
	if err := driver.pinRoot(); err != nil {
		t.Fatal(err)
	}
	defer driver.Close()

	err := driver.WriteCPUs(context.Background(), "pod", CgroupIdentity{Device: 1, Inode: 1}, machine.MustParse("0-1"))
	if !errors.Is(err, ErrFDBindingUnsupported) {
		t.Fatalf("WriteCPUs() error = %v, want FD binding unsupported", err)
	}
}

func TestCgroupV1DriverWriteCPUsStaysBoundToExpectedDirectoryFD(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	rel := "pod"
	currentDir := filepath.Join(root, rel)
	oldDir := filepath.Join(root, "old-pod")
	if err := os.Mkdir(currentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(currentDir, "cpuset.cpus"), []byte("0-1"), 0o644); err != nil {
		t.Fatal(err)
	}
	dirFD, err := unix.Open(currentDir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	var stat unix.Stat_t
	if err := unix.Fstat(dirFD, &stat); err != nil {
		_ = unix.Close(dirFD)
		t.Fatal(err)
	}
	if err := unix.Close(dirFD); err != nil {
		t.Fatal(err)
	}
	expected := CgroupIdentity{Device: uint64(stat.Dev), Inode: stat.Ino} //nolint:unconvert // Stat_t.Dev is int32 on darwin

	hierarchyDriver, err := NewCgroupV1Driver(resolvedPath(t, root), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer hierarchyDriver.Close()
	driver := hierarchyDriver.(*cgroupFSDriver)
	openFileAt := driver.openFileAt
	driver.openFileAt = func(dirFD int, name string, flags int, perm uint32) (int, error) {
		if err := os.Rename(currentDir, oldDir); err != nil {
			return -1, err
		}
		if err := os.Mkdir(currentDir, 0o755); err != nil {
			return -1, err
		}
		if err := os.WriteFile(filepath.Join(currentDir, name), []byte("replacement"), 0o644); err != nil {
			return -1, err
		}
		return openFileAt(dirFD, name, flags, perm)
	}

	if err := driver.WriteCPUs(context.Background(), rel, expected, machine.MustParse("2-3")); err != nil {
		t.Fatalf("WriteCPUs() error = %v", err)
	}
	oldContent, err := os.ReadFile(filepath.Join(oldDir, "cpuset.cpus"))
	if err != nil {
		t.Fatal(err)
	}
	replacementContent, err := os.ReadFile(filepath.Join(currentDir, "cpuset.cpus"))
	if err != nil {
		t.Fatal(err)
	}
	if string(oldContent) != "2-3" {
		t.Fatalf("old object content = %q, want %q", oldContent, "2-3")
	}
	if string(replacementContent) != "replacement" {
		t.Fatalf("replacement object content = %q, want unchanged", replacementContent)
	}
}

func TestCgroupV1DriverRejectsEmptyConfiguredCPUSet(t *testing.T) {
	t.Parallel()
	identity := CgroupIdentity{Device: 1, Inode: 1}
	driver := newCgroupV1Driver(resolvedPath(t, t.TempDir()), nil, unix.Fstat, true)
	if err := driver.pinRoot(); err != nil {
		t.Fatal(err)
	}
	defer driver.Close()

	err := driver.WriteCPUs(context.Background(), "pod", identity, machine.NewCPUSet())
	if !errors.Is(err, ErrEmptyCPUSetUnsupported) {
		t.Fatalf("WriteCPUs() error = %v, want empty cpuset unsupported", err)
	}
}

func TestCgroupV1DriverCapabilitiesAndErrorClassification(t *testing.T) {
	t.Parallel()
	driver := newCgroupV1Driver(resolvedPath(t, t.TempDir()), nil, unix.Fstat, true)
	if err := driver.pinRoot(); err != nil {
		t.Fatal(err)
	}
	defer driver.Close()
	want := HierarchyCapabilities{
		StableIdentity:          true,
		EmptyConfiguredCPUSet:   false,
		EffectiveCPUSet:         false,
		KernelParentContainment: true,
		PartitionRoots:          false,
	}
	if got := driver.Capabilities(); got != want {
		t.Fatalf("Capabilities() = %+v, want %+v", got, want)
	}

	tests := []struct {
		err  error
		op   HierarchyOperation
		want HierarchyErrorClass
	}{
		{syscall.ENOENT, HierarchyOperationRead, HierarchyErrorStale},
		{syscall.ENOTDIR, HierarchyOperationList, HierarchyErrorStale},
		{syscall.ENODEV, HierarchyOperationWriteCPUs, HierarchyErrorStale},
		{syscall.EBUSY, HierarchyOperationWriteCPUs, HierarchyErrorStale},
		{syscall.EACCES, HierarchyOperationWriteCPUs, HierarchyErrorInvalid},
		{context.DeadlineExceeded, HierarchyOperationRead, HierarchyErrorBudget},
		{errors.New("parse"), HierarchyOperationRead, HierarchyErrorInvalid},
	}
	for _, tt := range tests {
		if got := driver.Classify(tt.err, tt.op); got != tt.want {
			t.Errorf("Classify(%v, %s) = %s, want %s", tt.err, tt.op, got, tt.want)
		}
	}
}

func TestCgroupFSDriverReadEntryTrimsMemsAndAvoidsNoopPlan(t *testing.T) {
	t.Parallel()
	const rel = "root"
	root := resolvedPath(t, t.TempDir())
	dir := filepath.Join(root, rel)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cpuset.cpus"), []byte("0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cpuset.mems"), []byte("0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	driver := newTestCgroupV1Driver(t, root, nil)

	entry, err := driver.ReadEntry(context.Background(), rel)
	if err != nil {
		t.Fatalf("ReadEntry() error = %v", err)
	}
	if got := entry.CPUs.String(); got != "0" {
		t.Fatalf("ReadEntry().CPUs = %q, want 0", got)
	}
	if entry.Mems != "0" {
		t.Fatalf("ReadEntry().Mems = %q, want trimmed 0", entry.Mems)
	}

	dag := mustPlanDAG(t, []NodeSpec{{
		Rel: rel, Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(0), Mems: "0",
	}})
	snapshot := planSnapshot(map[string]EntryState{rel: entry}, map[DomainID]machine.CPUSet{DomainPrimary: machine.NewCPUSet(0)})
	plan, err := BuildPhasePlan(PhasePlanInput{
		Kind:             PhaseExpand,
		DAG:              dag,
		Snapshot:         snapshot,
		DesiredByRel:     map[string]machine.CPUSet{rel: machine.NewCPUSet(0)},
		DesiredMemsByRel: map[string]string{rel: "0"},
		AllowedCPUs:      machine.NewCPUSet(0),
		Budget:           NewBudgetTracker(ConvergenceBudget{}),
	})
	if err != nil {
		t.Fatalf("BuildPhasePlan() error = %v", err)
	}
	if len(plan.Operations) != 0 {
		t.Fatalf("plan operations = %+v, want none for matching cpus/mems", plan.Operations)
	}
}

func TestCgroupFSDriverReadEntryUsesV2EffectiveCPUSet(t *testing.T) {
	t.Parallel()
	const rel = "root"
	root := resolvedPath(t, t.TempDir())
	writeTestCgroupV2Directory(t, filepath.Join(root, rel), "0-3\n", "0\n", "", "0\n")
	hierarchyDriver, err := NewCgroupV2Driver(root, nil)
	if err != nil {
		t.Fatalf("NewCgroupV2Driver() error = %v", err)
	}
	defer hierarchyDriver.Close()

	entry, err := hierarchyDriver.ReadEntry(context.Background(), rel)
	if err != nil {
		t.Fatalf("ReadEntry() error = %v", err)
	}
	if got := entry.CPUs.String(); got != "0-3" {
		t.Fatalf("ReadEntry().CPUs = %q, want v2 effective cpuset 0-3", got)
	}
}

func TestCgroupFSDriverClassifiesENOTDIRAsStale(t *testing.T) {
	t.Parallel()

	driver := cgroupFSDriver{}
	if got := driver.Classify(syscall.ENOTDIR, HierarchyOperationRead); got != HierarchyErrorStale {
		t.Fatalf("Classify(ENOTDIR) = %s, want %s", got, HierarchyErrorStale)
	}
}

func TestBudgetedDriverCountsLogicalHierarchyIOOperations(t *testing.T) {
	t.Parallel()
	base := newFakeHierarchyDriver()
	base.add("root", CgroupIdentity{Device: 1, Inode: 1}, "0-3", "0")
	budget := NewBudgetTracker(ConvergenceBudget{
		MaxHierarchyIOOperations: 5,
		MaxSnapshotNodes:         1,
		MaxSnapshotDepth:         2,
	})
	driver := NewBudgetedHierarchyDriver(base, budget)
	ctx := context.Background()

	identity, err := driver.StatIdentity(ctx, "root")
	if err != nil {
		t.Fatal(err)
	}
	if err := budget.VisitNode("root", identity, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := driver.ReadEntry(ctx, "root"); err != nil {
		t.Fatal(err)
	}
	if _, err := driver.ListChildren(ctx, "root"); err != nil {
		t.Fatal(err)
	}
	if err := driver.WriteCPUs(ctx, "root", identity, machine.MustParse("0-2")); err != nil {
		t.Fatal(err)
	}
	if _, err := driver.ReadEntry(ctx, "root"); err != nil {
		t.Fatal(err)
	}
	if got := budget.Usage(); got.HierarchyIOOperations != 5 || got.Nodes != 1 || got.MaxDepth != 1 {
		t.Fatalf("Usage() = %+v, want hierarchyIOOperations=5 nodes=1 maxDepth=1", got)
	}
	if _, err := driver.StatIdentity(ctx, "root"); !errors.Is(err, ErrHierarchyIOOperationBudgetExceeded) {
		t.Fatalf("sixth call error = %v, want hierarchy I/O operation budget", err)
	}

	base.bumpIdentity("root")
	if err := budget.VisitNode("root", base.nodes["root"].identity, 1); !errors.Is(err, ErrNodeBudgetExceeded) {
		t.Fatalf("recreated node error = %v, want node budget", err)
	}
	if err := budget.CheckDepth(3); !errors.Is(err, ErrHierarchyDepthBudget) {
		t.Fatalf("depth error = %v, want depth budget", err)
	}
}

func TestBudgetedDriverChecksDeadlineBeforeUnderlyingCall(t *testing.T) {
	t.Parallel()
	base := newFakeHierarchyDriver()
	base.add("root", CgroupIdentity{Device: 1, Inode: 1}, "0-3", "0")
	budget := NewBudgetTracker(ConvergenceBudget{
		MaxHierarchyIOOperations: 10,
		Deadline:                 time.Now().Add(-time.Second),
	})
	driver := NewBudgetedHierarchyDriver(base, budget)

	_, err := driver.StatIdentity(context.Background(), "root")
	if !errors.Is(err, ErrConvergenceDeadlineExceeded) {
		t.Fatalf("StatIdentity() error = %v, want deadline budget", err)
	}
	if base.calls != 0 {
		t.Fatalf("underlying calls = %d, want zero", base.calls)
	}
	if got := budget.Usage().HierarchyIOOperations; got != 0 {
		t.Fatalf("charged hierarchy I/O operations = %d, want zero", got)
	}
}

func TestBudgetWithInvocationDeadlinePreservesEarlierAbsoluteDeadline(t *testing.T) {
	t.Parallel()
	now := time.Unix(100, 0)
	explicit := now.Add(10 * time.Millisecond)
	ctxDeadline := now.Add(time.Second)
	ctx, cancel := context.WithDeadline(context.Background(), ctxDeadline)
	defer cancel()

	got := BudgetWithInvocationDeadline(ctx, ConvergenceBudget{
		Deadline:         explicit,
		DeadlineDuration: 500 * time.Millisecond,
	}, now)

	if !got.Deadline.Equal(explicit) {
		t.Fatalf("Deadline = %s, want explicit earlier absolute deadline %s", got.Deadline, explicit)
	}
}

func TestBudgetedCgroupV1DriverChargesReadEntryOnce(t *testing.T) {
	t.Parallel()
	const rel = "pod"
	root := t.TempDir()
	dir := filepath.Join(root, rel)
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cpuset.cpus"), []byte("0-1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cpuset.mems"), []byte("0"), 0o644); err != nil {
		t.Fatal(err)
	}
	base := newTestCgroupV1Driver(t, root, nil)
	budget := NewBudgetTracker(ConvergenceBudget{MaxHierarchyIOOperations: 1})
	driver := NewBudgetedHierarchyDriver(base, budget)

	if _, err := driver.ReadEntry(context.Background(), rel); err != nil {
		t.Fatalf("first ReadEntry() error = %v", err)
	}
	if _, err := driver.ReadEntry(context.Background(), rel); !errors.Is(err, ErrHierarchyIOOperationBudgetExceeded) {
		t.Fatalf("second ReadEntry() error = %v, want hierarchy I/O operation budget", err)
	}
	if got := budget.Usage().HierarchyIOOperations; got != 1 {
		t.Fatalf("charged hierarchy I/O operations = %d, want 1", got)
	}
}

func TestBudgetedCgroupV1DriverChargesListChildrenOnce(t *testing.T) {
	t.Parallel()
	const rel = "root"
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, rel, "child"), 0o755); err != nil {
		t.Fatal(err)
	}
	base := newTestCgroupV1Driver(t, root, nil)
	budget := NewBudgetTracker(ConvergenceBudget{MaxHierarchyIOOperations: 1})
	driver := NewBudgetedHierarchyDriver(base, budget)

	if _, err := driver.ListChildren(context.Background(), rel); err != nil {
		t.Fatalf("first ListChildren() error = %v", err)
	}
	if _, err := driver.ListChildren(context.Background(), rel); !errors.Is(err, ErrHierarchyIOOperationBudgetExceeded) {
		t.Fatalf("second ListChildren() error = %v, want hierarchy I/O operation budget", err)
	}
	if got := budget.Usage().HierarchyIOOperations; got != 1 {
		t.Fatalf("charged hierarchy I/O operations = %d, want 1", got)
	}
}

func TestBudgetedCgroupV1DriverStopsLargeFanOutAtNodeBudget(t *testing.T) {
	t.Parallel()
	const (
		rel      = "root"
		childNum = 128
		nodeMax  = 4
	)
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, rel), 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < childNum; i++ {
		if err := os.Mkdir(filepath.Join(root, rel, fmt.Sprintf("child-%03d", i)), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	base := newTestCgroupV1Driver(t, root, nil)
	openAt := base.openDirAt
	childOpens := 0
	base.openDirAt = func(dirFD int, name string, flags int, mode uint32) (int, error) {
		if flags&unix.O_DIRECTORY != 0 && strings.HasPrefix(name, "child-") {
			childOpens++
		}
		return openAt(dirFD, name, flags, mode)
	}
	budget := NewBudgetTracker(ConvergenceBudget{MaxSnapshotNodes: nodeMax})
	parentIdentity, err := base.StatIdentity(context.Background(), rel)
	if err != nil {
		t.Fatal(err)
	}
	if err := budget.VisitNode(rel, parentIdentity, 1); err != nil {
		t.Fatal(err)
	}
	driver := NewBudgetedHierarchyDriver(base, budget)

	if _, err := driver.ListChildren(context.Background(), rel); !errors.Is(err, ErrNodeBudgetExceeded) {
		t.Fatalf("ListChildren() error = %v, want node budget", err)
	}
	if got := budget.Usage().Nodes; got != nodeMax {
		t.Fatalf("visited nodes = %d, want %d", got, nodeMax)
	}
	if childOpens != nodeMax {
		t.Fatalf("opened children = %d, want %d (accepted children plus first over-budget child)", childOpens, nodeMax)
	}
}

func TestBudgetedCgroupV1DriverChecksContextDuringEnumeration(t *testing.T) {
	t.Parallel()
	const (
		rel      = "root"
		childNum = 128
		cancelAt = 5
	)
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, rel), 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < childNum; i++ {
		if err := os.Mkdir(filepath.Join(root, rel, fmt.Sprintf("child-%03d", i)), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	base := newTestCgroupV1Driver(t, root, nil)
	openAt := base.openDirAt
	childOpens := 0
	base.openDirAt = func(dirFD int, name string, flags int, mode uint32) (int, error) {
		if flags&unix.O_DIRECTORY != 0 && strings.HasPrefix(name, "child-") {
			childOpens++
			if childOpens == cancelAt {
				cancel()
			}
		}
		return openAt(dirFD, name, flags, mode)
	}
	driver := NewBudgetedHierarchyDriver(base, NewBudgetTracker(ConvergenceBudget{}))

	if _, err := driver.ListChildren(ctx, rel); !errors.Is(err, context.Canceled) {
		t.Fatalf("ListChildren() error = %v, want context canceled", err)
	}
	if childOpens != cancelAt {
		t.Fatalf("opened children = %d, want enumeration to stop at %d", childOpens, cancelAt)
	}
}

func TestBudgetedCgroupV1DriverListChildrenVisitIsDeduplicated(t *testing.T) {
	t.Parallel()
	const rel = "root"
	root := t.TempDir()
	for _, child := range []string{"a", "b", "c"} {
		if err := os.MkdirAll(filepath.Join(root, rel, child), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	budget := NewBudgetTracker(ConvergenceBudget{MaxSnapshotNodes: 3})
	driver := NewBudgetedHierarchyDriver(newTestCgroupV1Driver(t, root, nil), budget)

	children, err := driver.ListChildren(context.Background(), rel)
	if err != nil {
		t.Fatalf("ListChildren() error = %v", err)
	}
	if got := budget.Usage().Nodes; got != len(children) {
		t.Fatalf("visited nodes after list = %d, want %d", got, len(children))
	}
	for _, child := range children {
		if err := budget.VisitNode(filepath.Join(rel, child.Name), child.Identity, 2); err != nil {
			t.Fatalf("snapshot repeat VisitNode(%q) error = %v", child.Name, err)
		}
	}
	if got := budget.Usage().Nodes; got != len(children) {
		t.Fatalf("visited nodes after repeated snapshot visits = %d, want %d", got, len(children))
	}
}

func TestBudgetedCgroupV1DriverChargesWriteOnce(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	rel := "pod"
	dir := filepath.Join(root, rel)
	file := filepath.Join(dir, "cpuset.cpus")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("0-1"), 0o644); err != nil {
		t.Fatal(err)
	}
	expected := identityFromDirectory(t, dir)
	budget := NewBudgetTracker(ConvergenceBudget{MaxHierarchyIOOperations: 1})
	driver := NewBudgetedHierarchyDriver(newTestCgroupV1Driver(t, root, nil), budget)
	if err := driver.WriteCPUs(context.Background(), rel, expected, machine.MustParse("2-3")); err != nil {
		t.Fatalf("WriteCPUs() error = %v", err)
	}
	if got := budget.Usage().HierarchyIOOperations; got != 1 {
		t.Fatalf("successful write charged hierarchy I/O operations = %d, want 1", got)
	}
	if err := driver.WriteCPUs(context.Background(), rel, expected, machine.MustParse("0-1")); !errors.Is(err, ErrHierarchyIOOperationBudgetExceeded) {
		t.Fatalf("second WriteCPUs() error = %v, want hierarchy I/O operation budget", err)
	}
}

func TestBudgetTrackerReserveHierarchyIOOperationsCompatibility(t *testing.T) {
	t.Parallel()
	budget := NewBudgetTracker(ConvergenceBudget{MaxHierarchyIOOperations: 1})
	if err := budget.ReserveHierarchyIOOperations(1); err != nil {
		t.Fatalf("ReserveHierarchyIOOperations() error = %v", err)
	}
	if got := budget.Usage().HierarchyIOOperations; got != 1 {
		t.Fatalf("reserved hierarchy I/O operations = %d, want 1", got)
	}
}

func TestBudgetTrackerDepthBoundaryZeroValueAndCanceledContext(t *testing.T) {
	t.Parallel()
	t.Run("depth-at-limit", func(t *testing.T) {
		t.Parallel()
		budget := NewBudgetTracker(ConvergenceBudget{MaxSnapshotDepth: 2})
		if err := budget.CheckDepth(2); err != nil {
			t.Fatalf("CheckDepth(limit) = %v", err)
		}
		if got := budget.Usage().MaxDepth; got != 2 {
			t.Fatalf("MaxDepth = %d, want 2", got)
		}
	})

	t.Run("zero-is-unlimited", func(t *testing.T) {
		t.Parallel()
		budget := NewBudgetTracker(ConvergenceBudget{})
		if err := budget.CheckDepth(1 << 20); err != nil {
			t.Fatalf("unlimited depth failed: %v", err)
		}
		for i := 0; i < 100; i++ {
			if err := budget.beforeHierarchyIOOperation(context.Background()); err != nil {
				t.Fatalf("unlimited hierarchy I/O operation %d failed: %v", i, err)
			}
		}
		if got := budget.Usage(); got.MaxDepth != 1<<20 || got.HierarchyIOOperations != 100 {
			t.Fatalf("Usage() = %+v, want maxDepth=%d hierarchyIOOperations=100", got, 1<<20)
		}
	})

	t.Run("context-canceled", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		budget := NewBudgetTracker(ConvergenceBudget{MaxHierarchyIOOperations: 1})
		driver := NewBudgetedHierarchyDriver(newFakeHierarchyDriver(), budget)
		if _, err := driver.Roots(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("Roots() error = %v, want context canceled", err)
		}
		if got := budget.Usage().HierarchyIOOperations; got != 0 {
			t.Fatalf("canceled call charged %d hierarchy I/O operations, want zero", got)
		}
	})
}

func TestBudgetTrackerRoundDomainEdgeOperationBoundaries(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		limit    ConvergenceBudget
		consume  func(*BudgetTracker, int) error
		sentinel error
		used     func(BudgetUsage) int
	}{
		{
			name:     "round",
			limit:    ConvergenceBudget{MaxRounds: 2},
			consume:  func(b *BudgetTracker, _ int) error { return b.ConsumeRound() },
			sentinel: ErrRoundBudgetExceeded,
			used:     func(u BudgetUsage) int { return u.Rounds },
		},
		{
			name:     "domain",
			limit:    ConvergenceBudget{MaxDomains: 2},
			consume:  (*BudgetTracker).ConsumeDomains,
			sentinel: ErrDomainBudgetExceeded,
			used:     func(u BudgetUsage) int { return u.Domains },
		},
		{
			name:     "edge",
			limit:    ConvergenceBudget{MaxTransferEdges: 2},
			consume:  (*BudgetTracker).ConsumeTransferEdges,
			sentinel: ErrTransferEdgeBudgetExceeded,
			used:     func(u BudgetUsage) int { return u.Edges },
		},
		{
			name:     "operation",
			limit:    ConvergenceBudget{MaxPlanOperations: 2},
			consume:  (*BudgetTracker).ConsumePlanOperations,
			sentinel: ErrPlanOperationBudgetExceeded,
			used:     func(u BudgetUsage) int { return u.Operations },
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			budget := NewBudgetTracker(tt.limit)
			if err := tt.consume(budget, 1); err != nil {
				t.Fatalf("consume limit-1: %v", err)
			}
			if err := tt.consume(budget, 1); err != nil {
				t.Fatalf("consume at limit: %v", err)
			}
			if err := tt.consume(budget, 1); !errors.Is(err, tt.sentinel) {
				t.Fatalf("consume limit+1 error = %v, want %v", err, tt.sentinel)
			}
			if got := tt.used(budget.Usage()); got != 2 {
				t.Fatalf("usage after rejected charge = %d, want 2", got)
			}
		})
	}
}

func TestBudgetTrackerRejectsOverflowingCumulativeCharge(t *testing.T) {
	t.Parallel()

	maxInt := int(^uint(0) >> 1)
	budget := NewBudgetTracker(ConvergenceBudget{MaxPlanOperations: maxInt})
	budget.usage.Operations = maxInt - 1

	err := budget.ConsumePlanOperations(2)
	if !errors.Is(err, ErrPlanOperationBudgetExceeded) {
		t.Fatalf("overflowing charge error = %v, want %v", err, ErrPlanOperationBudgetExceeded)
	}
	if got := budget.Usage().Operations; got != maxInt-1 {
		t.Fatalf("usage after rejected overflowing charge = %d, want %d", got, maxInt-1)
	}
}

func TestBudgetTrackerReserveHierarchyIOOperationsPrefersCanceledContext(t *testing.T) {
	t.Parallel()

	budget := NewBudgetTracker(ConvergenceBudget{MaxHierarchyIOOperations: 1})
	if err := budget.ReserveHierarchyIOOperationsWithContext(context.Background(), 1); err != nil {
		t.Fatalf("fill hierarchy I/O budget: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := budget.ReserveHierarchyIOOperationsWithContext(ctx, 1)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("reservation error = %v, want context.Canceled", err)
	}
	if errors.Is(err, ErrHierarchyIOOperationBudgetExceeded) {
		t.Fatalf("reservation error = %v, must prefer context cancellation over budget exhaustion", err)
	}
	if got := budget.Usage().HierarchyIOOperations; got != 1 {
		t.Fatalf("usage after canceled reservation = %d, want 1", got)
	}
}

func TestBudgetTrackerReserveHierarchyIOOperationsPrefersExpiredAbsoluteDeadline(t *testing.T) {
	t.Parallel()

	budget := NewBudgetTracker(ConvergenceBudget{
		MaxHierarchyIOOperations: 1,
		Deadline:                 time.Now().Add(-time.Second),
	})
	budget.usage.HierarchyIOOperations = 1

	err := budget.ReserveHierarchyIOOperationsWithContext(context.Background(), 1)
	if !errors.Is(err, ErrConvergenceDeadlineExceeded) {
		t.Fatalf("reservation error = %v, want ErrConvergenceDeadlineExceeded", err)
	}
	if errors.Is(err, ErrHierarchyIOOperationBudgetExceeded) {
		t.Fatalf("reservation error = %v, must prefer absolute deadline over budget exhaustion", err)
	}
	if got := budget.Usage().HierarchyIOOperations; got != 1 {
		t.Fatalf("usage after expired-deadline reservation = %d, want 1", got)
	}
}

func TestBudgetTrackerConcurrentHierarchyIOReservations(t *testing.T) {
	t.Parallel()

	const (
		limit    = 64
		attempts = 128
	)
	budget := NewBudgetTracker(ConvergenceBudget{MaxHierarchyIOOperations: limit})
	start := make(chan struct{})
	errs := make(chan error, attempts)
	var wg sync.WaitGroup
	wg.Add(attempts)
	for i := 0; i < attempts; i++ {
		go func() {
			defer wg.Done()
			<-start
			errs <- budget.ReserveHierarchyIOOperationsWithContext(context.Background(), 1)
		}()
	}
	close(start)
	wg.Wait()
	close(errs)

	var succeeded, exhausted int
	for err := range errs {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, ErrHierarchyIOOperationBudgetExceeded):
			exhausted++
		default:
			t.Fatalf("reservation error = %v, want nil or hierarchy I/O budget error", err)
		}
	}
	if succeeded != limit || exhausted != attempts-limit {
		t.Fatalf("reservation results: succeeded=%d exhausted=%d, want %d and %d",
			succeeded, exhausted, limit, attempts-limit)
	}
	if got := budget.Usage().HierarchyIOOperations; got != limit {
		t.Fatalf("charged hierarchy I/O = %d, want %d", got, limit)
	}
}

func TestReservedBudgetedHierarchyDriverConcurrentPrepaidUse(t *testing.T) {
	t.Parallel()

	const operations = 64
	budget := NewBudgetTracker(ConvergenceBudget{MaxHierarchyIOOperations: operations})
	driver, err := newReservedBudgetedHierarchyDriver(context.Background(), newFakeHierarchyDriver(), budget, operations)
	if err != nil {
		t.Fatalf("reserve hierarchy I/O: %v", err)
	}
	reserved := driver.(*budgetedHierarchyDriver)

	start := make(chan struct{})
	errs := make(chan error, operations)
	var wg sync.WaitGroup
	wg.Add(operations)
	for i := 0; i < operations; i++ {
		go func() {
			defer wg.Done()
			<-start
			_, callErr := reserved.context(context.Background())
			errs <- callErr
		}()
	}
	close(start)
	wg.Wait()
	close(errs)

	for callErr := range errs {
		if callErr != nil {
			t.Fatalf("prepaid context failed: %v", callErr)
		}
	}
	if got := budget.Usage().HierarchyIOOperations; got != operations {
		t.Fatalf("charged hierarchy I/O = %d, want reserved %d", got, operations)
	}
}

func identityFromDirectory(t *testing.T, path string) CgroupIdentity {
	t.Helper()
	dirFD, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(dirFD)
	var stat unix.Stat_t
	if err := unix.Fstat(dirFD, &stat); err != nil {
		t.Fatal(err)
	}
	return CgroupIdentity{Device: uint64(stat.Dev), Inode: stat.Ino} //nolint:unconvert // Stat_t.Dev is int32 on darwin
}

func writeTestCgroupDirectory(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "cpuset.cpus"), []byte("0-1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "cpuset.mems"), []byte("0"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeTestCgroupV2Directory(t *testing.T, path, effectiveCPUs, effectiveMems, configuredCPUs, configuredMems string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"cpuset.cpus.effective": effectiveCPUs,
		"cpuset.mems.effective": effectiveMems,
		"cpuset.cpus":           configuredCPUs,
		"cpuset.mems":           configuredMems,
	}
	for name, value := range files {
		if err := os.WriteFile(filepath.Join(path, name), []byte(value), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func assertFileContent(t *testing.T, path, want string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(raw); got != want {
		t.Fatalf("%s = %q, want %q", path, got, want)
	}
}

func newTestCgroupV1Driver(t *testing.T, root string, configuredRoots []string) *cgroupFSDriver {
	t.Helper()
	driver := newCgroupV1Driver(resolvedPath(t, root), configuredRoots, unix.Fstat, true)
	driver.openDirAt = unix.Openat
	driver.openFileAt = unix.Openat
	driver.readFileAt = readFileAt
	if err := driver.pinRoot(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = driver.Close() })
	return driver
}

func resolvedPath(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}
