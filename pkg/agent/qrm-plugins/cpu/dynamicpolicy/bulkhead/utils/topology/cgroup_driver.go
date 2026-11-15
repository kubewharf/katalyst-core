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
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"golang.org/x/sys/unix"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

var (
	ErrEmptyCPUSetUnsupported      = errors.New("empty configured cpuset is unsupported")
	ErrFDBindingUnsupported        = errors.New("file-descriptor-bound cgroup write is unsupported")
	ErrCgroupCrossDevice           = errors.New("cgroup path crosses root device")
	ErrCgroupControllerUnavailable = errors.New("cgroup controller interface is unavailable")
)

const listChildrenBatchSize = 32

type cgroupFSDriver struct {
	rootPath     string
	rootFD       int
	rootIdentity CgroupIdentity
	roots        []string
	stable       bool
	policy       cgroupVersionPolicy
	mu           sync.RWMutex
	closed       bool

	openDirAt  func(int, string, int, uint32) (int, error)
	openFileAt func(int, string, int, uint32) (int, error)
	readFileAt func(int, string) ([]byte, error)
	fstat      func(int, *unix.Stat_t) error
}

// NewCgroupV1Driver opens an absolute rootPath component-by-component without
// following symlinks, then pins that root directory FD and identity for the
// driver's lifetime. Every later operation is derived from that FD.
// configuredRoots limits Roots to controlled reconciliation roots; when empty,
// direct hierarchy children are discovered from the pinned root.
func NewCgroupV1Driver(rootPath string, configuredRoots []string) (HierarchyDriver, error) {
	return newCgroupDriver(rootPath, configuredRoots, cgroupV1Policy)
}

// NewCgroupV2Driver shares the FD engine with v1; version differences come only from immutable policy.
func NewCgroupV2Driver(rootPath string, configuredRoots []string) (HierarchyDriver, error) {
	return newCgroupDriver(rootPath, configuredRoots, cgroupV2Policy)
}

func newCgroupDriver(rootPath string, configuredRoots []string, policy cgroupVersionPolicy) (HierarchyDriver, error) {
	driver := newCgroupFSDriver(rootPath, configuredRoots, policy, unix.Fstat, runtime.GOOS == "linux")
	driver.openDirAt = unix.Openat
	driver.openFileAt = unix.Openat
	driver.readFileAt = readFileAt
	if err := driver.pinRoot(); err != nil {
		return nil, err
	}
	return driver, nil
}

func newCgroupFSDriver(
	rootPath string,
	configuredRoots []string,
	policy cgroupVersionPolicy,
	fstat func(int, *unix.Stat_t) error,
	stable bool,
) *cgroupFSDriver {
	return &cgroupFSDriver{
		rootPath: filepath.Clean(rootPath),
		rootFD:   -1,
		roots:    append([]string(nil), configuredRoots...),
		fstat:    fstat,
		stable:   stable,
		policy:   policy,
	}
}

func (d *cgroupFSDriver) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return nil
	}
	d.closed = true
	if d.rootFD < 0 {
		return nil
	}
	err := unix.Close(d.rootFD)
	d.rootFD = -1
	return err
}

func (d *cgroupFSDriver) Roots(ctx context.Context) ([]RootRef, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	roots := append([]string(nil), d.roots...)
	if len(roots) == 0 {
		var err error
		roots, err = d.listDirectoryNames("")
		if err != nil {
			return nil, fmt.Errorf("list hierarchy roots: %w", err)
		}
	}
	sort.Strings(roots)
	out := make([]RootRef, 0, len(roots))
	for _, rel := range roots {
		identity, err := d.StatIdentity(ctx, rel)
		if err != nil {
			return nil, fmt.Errorf("stat hierarchy root %q: %w", rel, err)
		}
		out = append(out, RootRef{Rel: rel, Identity: identity})
	}
	return out, nil
}

func (d *cgroupFSDriver) StatIdentity(ctx context.Context, rel string) (CgroupIdentity, error) {
	if err := ctx.Err(); err != nil {
		return CgroupIdentity{}, err
	}
	dirFD, err := d.openDir(rel)
	if err != nil {
		return CgroupIdentity{}, err
	}
	defer unix.Close(dirFD)
	return d.identityFromFD(dirFD)
}

func (d *cgroupFSDriver) ReadEntry(ctx context.Context, rel string) (EntryState, error) {
	return d.readEntry(ctx, rel, CgroupIdentity{}, false, nil)
}

func (d *cgroupFSDriver) ReadEntryWithActivity(
	ctx context.Context,
	rel string,
	expected CgroupIdentity,
) (EntryState, error) {
	return d.readEntryWithActivityAndBudget(ctx, rel, expected, nil)
}

func (d *cgroupFSDriver) readEntryWithActivityAndBudget(
	ctx context.Context,
	rel string,
	expected CgroupIdentity,
	budget *BudgetTracker,
) (EntryState, error) {
	return d.readEntry(ctx, rel, expected, true, budget)
}

func (d *cgroupFSDriver) readEntry(
	ctx context.Context,
	rel string,
	expected CgroupIdentity,
	readActivity bool,
	budget *BudgetTracker,
) (EntryState, error) {
	if err := ctx.Err(); err != nil {
		return EntryState{}, err
	}
	dirFD, err := d.openDir(rel)
	if err != nil {
		return EntryState{}, fmt.Errorf("open cgroup directory %q: %w", rel, err)
	}
	defer unix.Close(dirFD)
	before, err := d.identityFromFD(dirFD)
	if err != nil {
		return EntryState{}, fmt.Errorf("fstat before read %q: %w", rel, err)
	}
	if expected != (CgroupIdentity{}) && before != expected {
		return EntryState{}, identityMismatchError(rel, expected, before)
	}
	var childrenBefore []ChildRef
	if readActivity {
		if d.policy != cgroupV1Policy {
			return EntryState{}, fmt.Errorf("cgroup activity proof is only supported for v1")
		}
		childrenBefore, err = d.listChildrenFromFD(ctx, dirFD, rel, before, budget)
		if err != nil {
			return EntryState{}, err
		}
	}
	activity := CgroupActivity{}
	if readActivity {
		rawTasks, readErr := d.readFileAt(dirFD, "tasks")
		if readErr != nil {
			return EntryState{}, d.wrapReadEntryFileError(rel, "tasks", readErr)
		}
		rawProcs, readErr := d.readFileAt(dirFD, "cgroup.procs")
		if readErr != nil {
			return EntryState{}, d.wrapReadEntryFileError(rel, "cgroup.procs", readErr)
		}
		activity.TasksEmpty, err = membershipFileEmpty(rawTasks)
		if err != nil {
			return EntryState{}, fmt.Errorf("parse tasks %q: %w", rel, err)
		}
		activity.CgroupProcsEmpty, err = membershipFileEmpty(rawProcs)
		if err != nil {
			return EntryState{}, fmt.Errorf("parse cgroup.procs %q: %w", rel, err)
		}
	}
	// A snapshot's effective/configured files must derive from this verified directory FD,
	// keeping the snapshot generation-safe if the path is replaced.
	rawCPUs, err := d.readFileAt(dirFD, d.policy.observedCPUsFile())
	if err != nil {
		return EntryState{}, d.wrapReadEntryFileError(rel, d.policy.observedCPUsFile(), err)
	}
	cpus, err := machine.Parse(strings.TrimSpace(string(rawCPUs)))
	if err != nil {
		return EntryState{}, fmt.Errorf("parse %s %q: %w", d.policy.observedCPUsFile(), rel, err)
	}
	rawMems, err := d.readFileAt(dirFD, d.policy.observedMemsFile())
	if err != nil {
		return EntryState{}, d.wrapReadEntryFileError(rel, d.policy.observedMemsFile(), err)
	}
	configuredCPUs := cpus.Clone()
	configuredMems := strings.TrimSpace(string(rawMems))
	if d.policy.configuredCPUsFile() != d.policy.observedCPUsFile() {
		rawConfiguredCPUs, readErr := d.readFileAt(dirFD, d.policy.configuredCPUsFile())
		if readErr != nil {
			return EntryState{}, d.wrapReadEntryFileError(rel, d.policy.configuredCPUsFile(), readErr)
		}
		configuredCPUs, err = machine.Parse(strings.TrimSpace(string(rawConfiguredCPUs)))
		if err != nil {
			return EntryState{}, fmt.Errorf("parse %s %q: %w", d.policy.configuredCPUsFile(), rel, err)
		}
	}
	if d.policy.configuredMemsFile() != d.policy.observedMemsFile() {
		rawConfiguredMems, readErr := d.readFileAt(dirFD, d.policy.configuredMemsFile())
		if readErr != nil {
			return EntryState{}, d.wrapReadEntryFileError(rel, d.policy.configuredMemsFile(), readErr)
		}
		configuredMems = strings.TrimSpace(string(rawConfiguredMems))
	}
	if readActivity {
		childrenAfter, listErr := d.listChildrenFromFD(ctx, dirFD, rel, before, budget)
		if listErr != nil {
			return EntryState{}, listErr
		}
		if ChildrenFingerprint(childrenBefore) != ChildrenFingerprint(childrenAfter) {
			return EntryState{}, fmt.Errorf("%w: rel=%q child identity set changed during activity read",
				ErrCgroupIdentityChanged, rel)
		}
		activity.Childless = len(childrenBefore) == 0
	}
	after, err := d.identityFromFD(dirFD)
	if err != nil {
		return EntryState{}, fmt.Errorf("fstat after read %q: %w", rel, err)
	}
	if before != after {
		return EntryState{}, identityMismatchError(rel, before, after)
	}
	return EntryState{
		Rel:      rel,
		Identity: before,
		CPUs:     cpus,
		Mems:     strings.TrimSpace(string(rawMems)),
		// In v2, empty configured means inheritance; effective is the runtime state visible to the planner.
		ConfiguredCPUs: configuredCPUs,
		ConfiguredMems: configuredMems,
		Activity:       activity,
	}, nil
}

func membershipFileEmpty(raw []byte) (bool, error) {
	fields := strings.Fields(string(raw))
	for _, field := range fields {
		pid, err := strconv.ParseUint(field, 10, 64)
		if err != nil || pid == 0 {
			return false, fmt.Errorf("invalid pid %q", field)
		}
	}
	return len(fields) == 0, nil
}

func (d *cgroupFSDriver) wrapReadEntryFileError(rel, file string, err error) error {
	if d.policy == cgroupV2Policy && errors.Is(err, syscall.ENOENT) && strings.HasPrefix(file, "cpuset.") {
		return fmt.Errorf("read %s %q: %w: %v", file, rel, ErrCgroupControllerUnavailable, err)
	}
	return fmt.Errorf("read %s %q: %w", file, rel, err)
}

func (d *cgroupFSDriver) ListChildren(ctx context.Context, rel string) ([]ChildRef, error) {
	return d.listChildrenWithBudget(ctx, rel, nil)
}

func (d *cgroupFSDriver) listChildrenWithBudget(ctx context.Context, rel string, budget *BudgetTracker) ([]ChildRef, error) {
	checkWork := func() error {
		if budget != nil {
			return budget.checkContextDeadline(ctx)
		}
		return ctx.Err()
	}
	if err := checkWork(); err != nil {
		return nil, err
	}
	dirFD, err := d.openDir(rel)
	if err != nil {
		return nil, fmt.Errorf("open cgroup directory %q: %w", rel, err)
	}
	defer unix.Close(dirFD)
	before, err := d.identityFromFD(dirFD)
	if err != nil {
		return nil, fmt.Errorf("fstat before list %q: %w", rel, err)
	}
	return d.listChildrenFromFD(ctx, dirFD, rel, before, budget)
}

func (d *cgroupFSDriver) listChildrenFromFD(
	ctx context.Context,
	dirFD int,
	rel string,
	expectedParent CgroupIdentity,
	budget *BudgetTracker,
) ([]ChildRef, error) {
	checkWork := func() error {
		if budget != nil {
			return budget.checkContextDeadline(ctx)
		}
		return ctx.Err()
	}
	before, err := d.identityFromFD(dirFD)
	if err != nil {
		return nil, fmt.Errorf("fstat before list %q: %w", rel, err)
	}
	if before != expectedParent {
		return nil, identityMismatchError(rel, expectedParent, before)
	}
	openDirAt := d.openDirAt
	if openDirAt == nil {
		openDirAt = unix.Openat
	}
	enumerationFD, err := openDirAt(dirFD, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("open pinned directory fd for list %q: %w", rel, err)
	}
	file := os.NewFile(uintptr(enumerationFD), rel)
	if file == nil {
		_ = unix.Close(enumerationFD)
		return nil, fmt.Errorf("wrap directory fd")
	}
	defer file.Close()
	children := make([]ChildRef, 0, listChildrenBatchSize)
	for {
		if err := checkWork(); err != nil {
			return nil, err
		}
		entries, readErr := file.ReadDir(listChildrenBatchSize)
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return nil, fmt.Errorf("list children %q: %w", rel, readErr)
		}
		for _, entry := range entries {
			if err := checkWork(); err != nil {
				return nil, err
			}
			childRel := filepath.Join(rel, entry.Name())
			if entry.Type()&os.ModeSymlink != 0 {
				return nil, fmt.Errorf("list child %q: symlink is not allowed", childRel)
			}
			if !entry.IsDir() {
				continue
			}
			childFD, identity, err := d.openChildDirWithIdentity(dirFD, before.Device, childRel, entry.Name())
			if err != nil {
				if isCgroupPathAbsent(err) {
					continue
				}
				return nil, fmt.Errorf("open child %q: %w", childRel, err)
			}
			closeErr := unix.Close(childFD)
			if closeErr != nil {
				return nil, fmt.Errorf("close child %q: %w", childRel, closeErr)
			}
			if budget != nil {
				if err := budget.VisitNode(childRel, identity, childDepth(rel)); err != nil {
					return nil, err
				}
			}
			children = append(children, ChildRef{Name: entry.Name(), Identity: identity})
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
	}
	sort.Slice(children, func(i, j int) bool { return children[i].Name < children[j].Name })
	after, err := d.identityFromFD(dirFD)
	if err != nil {
		return nil, fmt.Errorf("fstat after list %q: %w", rel, err)
	}
	if after != expectedParent {
		return nil, identityMismatchError(rel, expectedParent, after)
	}
	return children, nil
}

func (d *cgroupFSDriver) openChildDirWithIdentity(parentFD int, rootDevice uint64, rel, name string) (int, CgroupIdentity, error) {
	openDirAt := d.openDirAt
	if openDirAt == nil {
		openDirAt = unix.Openat
	}
	fd, err := openDirAt(parentFD, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return -1, CgroupIdentity{}, err
	}
	identity, err := d.identityFromFD(fd)
	if err != nil {
		_ = unix.Close(fd)
		return -1, CgroupIdentity{}, err
	}
	if identity.Device != rootDevice {
		_ = unix.Close(fd)
		return -1, CgroupIdentity{}, fmt.Errorf("%w: rel=%q root-device=%d actual-device=%d", ErrCgroupCrossDevice, rel, rootDevice, identity.Device)
	}
	return fd, identity, nil
}

func (d *cgroupFSDriver) WriteCPUs(ctx context.Context, rel string, expected CgroupIdentity, cpus machine.CPUSet) error {
	if err := d.policy.validateConfiguredCPUs(cpus); err != nil {
		return fmt.Errorf("%w: rel=%q", err, rel)
	}
	if d.openFileAt == nil {
		return fmt.Errorf("%w: rel=%q file=%s", ErrFDBindingUnsupported, rel, d.policy.configuredCPUsFile())
	}
	value := cpus.String()
	if cpus.IsEmpty() {
		// In v2, empty configured means inheritance, but a zero-length write(2) does not invoke kernel write handling;
		// a newline trims to empty while ensuring a nonzero-length write on the same FD.
		value = "\n"
	}
	return d.writeFileAt(ctx, rel, expected, d.policy.configuredCPUsFile(), value)
}

func (d *cgroupFSDriver) WriteMems(ctx context.Context, rel string, expected CgroupIdentity, mems string) error {
	mems = strings.TrimSpace(mems)
	if d.openFileAt == nil {
		return fmt.Errorf("%w: rel=%q file=%s", ErrFDBindingUnsupported, rel, d.policy.configuredMemsFile())
	}
	if mems == "" && d.policy == cgroupV2Policy {
		// V2 allows empty configured mems to mean inheritance; because a zero-length write(2) does not invoke kernel
		// write handling, send a newline that trims to empty. V1 retains its existing zero-length call semantics.
		mems = "\n"
	}
	return d.writeFileAt(ctx, rel, expected, d.policy.configuredMemsFile(), mems)
}

func (d *cgroupFSDriver) writeFileAt(
	ctx context.Context,
	rel string,
	expected CgroupIdentity,
	name, value string,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// Identity validation and the final openat/write must use the same directory FD,
	// making the write generation-safe against pathname replacement after validation.
	dirFD, err := d.openDir(rel)
	if err != nil {
		return fmt.Errorf("open cgroup directory %q: %w", rel, err)
	}
	defer unix.Close(dirFD)

	actual, err := d.identityFromFD(dirFD)
	if err != nil {
		return fmt.Errorf("fstat before write %q: %w", rel, err)
	}
	if actual != expected {
		return identityMismatchError(rel, expected, actual)
	}

	fileFD, err := d.openFileAt(dirFD, name, unix.O_WRONLY|unix.O_TRUNC|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return fmt.Errorf("openat %s %q: %w", name, rel, err)
	}
	defer unix.Close(fileFD)

	data := []byte(value)
	n, err := unix.Write(fileFD, data)
	if err != nil {
		return fmt.Errorf("write %s %q: %w", name, rel, err)
	}
	if n != len(data) {
		return fmt.Errorf("write %s %q: short write %d/%d", name, rel, n, len(data))
	}
	return nil
}

func (d *cgroupFSDriver) Classify(err error, _ HierarchyOperation) HierarchyErrorClass {
	switch {
	case err == nil:
		return HierarchyErrorNone
	case errors.Is(err, ErrCgroupControllerUnavailable),
		errors.Is(err, ErrCgroupIdentityChanged),
		errors.Is(err, syscall.ENOENT),
		errors.Is(err, syscall.ENOTDIR),
		errors.Is(err, syscall.ENODEV),
		errors.Is(err, syscall.EBUSY):
		return HierarchyErrorStale
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return HierarchyErrorBudget
	default:
		return HierarchyErrorInvalid
	}
}

func (d *cgroupFSDriver) Capabilities() HierarchyCapabilities {
	return d.policy.capabilities(d.stable)
}

func (d *cgroupFSDriver) openDir(rel string) (int, error) {
	rootFD, rootIdentity, err := d.acquireRootFD()
	if err != nil {
		return -1, err
	}
	if strings.TrimSpace(rel) == "" {
		return rootFD, nil
	}
	clean, err := cleanHierarchyRel(rel)
	if err != nil {
		_ = unix.Close(rootFD)
		return -1, err
	}
	currentFD := rootFD
	for _, component := range strings.Split(clean, string(filepath.Separator)) {
		nextFD, openErr := d.openChildDir(currentFD, rootIdentity.Device, clean, component)
		_ = unix.Close(currentFD)
		if openErr != nil {
			return -1, openErr
		}
		currentFD = nextFD
	}
	return currentFD, nil
}

func (d *cgroupFSDriver) pinRoot() error {
	clean := filepath.Clean(d.rootPath)
	if !filepath.IsAbs(clean) {
		return fmt.Errorf("hierarchy root path must be absolute: %q", d.rootPath)
	}
	currentFD, err := unix.Open(string(filepath.Separator), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return fmt.Errorf("open hierarchy filesystem root: %w", err)
	}
	for _, component := range strings.Split(strings.TrimPrefix(clean, string(filepath.Separator)), string(filepath.Separator)) {
		if component == "" {
			continue
		}
		nextFD, openErr := unix.Openat(currentFD, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		_ = unix.Close(currentFD)
		if openErr != nil {
			return fmt.Errorf("open hierarchy root component %q: %w", component, openErr)
		}
		currentFD = nextFD
	}
	identity, err := d.identityFromFD(currentFD)
	if err != nil {
		_ = unix.Close(currentFD)
		return fmt.Errorf("fstat hierarchy root: %w", err)
	}
	d.rootFD = currentFD
	d.rootIdentity = identity
	return nil
}

func (d *cgroupFSDriver) acquireRootFD() (int, CgroupIdentity, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.closed || d.rootFD < 0 {
		return -1, CgroupIdentity{}, syscall.EBADF
	}
	fd, err := unix.FcntlInt(uintptr(d.rootFD), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return -1, CgroupIdentity{}, err
	}
	identity, err := d.identityFromFD(fd)
	if err != nil {
		_ = unix.Close(fd)
		return -1, CgroupIdentity{}, fmt.Errorf("fstat pinned hierarchy root: %w", err)
	}
	if identity != d.rootIdentity {
		_ = unix.Close(fd)
		return -1, CgroupIdentity{}, identityMismatchError("", d.rootIdentity, identity)
	}
	return fd, d.rootIdentity, nil
}

func (d *cgroupFSDriver) openChildDir(parentFD int, rootDevice uint64, rel, name string) (int, error) {
	fd, _, err := d.openChildDirWithIdentity(parentFD, rootDevice, rel, name)
	return fd, err
}

func (d *cgroupFSDriver) identityFromFD(fd int) (CgroupIdentity, error) {
	if d.fstat == nil {
		return CgroupIdentity{}, ErrCgroupIdentityUnsupported
	}
	var stat unix.Stat_t
	if err := d.fstat(fd, &stat); err != nil {
		return CgroupIdentity{}, err
	}
	return CgroupIdentity{Device: uint64(stat.Dev), Inode: stat.Ino}, nil //nolint:unconvert // Stat_t.Dev is int32 on darwin
}

func (d *cgroupFSDriver) listDirectoryNames(rel string) ([]string, error) {
	dirFD, err := d.openDir(rel)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(dirFD), rel)
	if file == nil {
		_ = unix.Close(dirFD)
		return nil, fmt.Errorf("wrap directory fd")
	}
	defer file.Close()
	entries, err := file.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("list child %q: symlink is not allowed", filepath.Join(rel, entry.Name()))
		}
		if entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	return names, nil
}

func cleanHierarchyRel(rel string) (string, error) {
	clean := filepath.Clean(rel)
	if clean == "." || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("invalid hierarchy relative path %q", rel)
	}
	return clean, nil
}

func readFileAt(dirFD int, name string) ([]byte, error) {
	fd, err := unix.Openat(dirFD, name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), name)
	if file == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("wrap file fd")
	}
	defer file.Close()
	return io.ReadAll(file)
}

func identityMismatchError(rel string, expected, actual CgroupIdentity) error {
	return fmt.Errorf("%w: rel=%q expected=%v actual=%v", ErrCgroupIdentityChanged, rel, expected, actual)
}
