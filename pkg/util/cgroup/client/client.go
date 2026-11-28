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

// Package client provides a small cgroup manager facade with write-if-change caching.
package client

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"k8s.io/klog/v2"

	cgcommon "github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	cgmanager "github.com/kubewharf/katalyst-core/pkg/util/cgroup/manager"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// CgroupVersion tags the cgroup hierarchy layout the current host is booted into.
type CgroupVersion string

const (
	// CgroupVersionV1 indicates the host is using cgroup v1 hierarchies.
	CgroupVersionV1 CgroupVersion = "v1"
	// CgroupVersionV2 indicates the host is using cgroup v2 unified hierarchy.
	CgroupVersionV2 CgroupVersion = "v2"
)

// CgroupClient is the narrow cgroup interface consumed by reconciliation code.
type CgroupClient interface {
	// ApplyCPUSet writes cpuset.cpus and cpuset.mems to rel.
	ApplyCPUSet(ctx context.Context, rel string, data *cgcommon.CPUSetData) error
	// ApplyCPUSetPartition writes cpuset.cpus.partition to rel.
	ApplyCPUSetPartition(ctx context.Context, rel string, flag cgcommon.CPUSetPartitionFlag) error
	// ApplyCPU writes cpu controller data to rel.
	ApplyCPU(ctx context.Context, rel string, data *cgcommon.CPUData) error
	// ApplySchedLoadBalance toggles cpuset.sched_load_balance at rel.
	ApplySchedLoadBalance(ctx context.Context, rel string, enabled bool) error
	// AttachPID writes pid into cgroup.procs at rel.
	AttachPID(ctx context.Context, rel string, pid int) error
	// Version reports whether the host is running cgroup v1 or v2.
	Version(ctx context.Context) CgroupVersion
	// ReadCPUSet parses cpuset.cpus at rel.
	ReadCPUSet(ctx context.Context, rel string) (machine.CPUSet, error)
	// StatCPUSet returns metadata for cpuset.cpus at rel.
	StatCPUSet(ctx context.Context, rel string) (mtime time.Time, size int64, err error)
	// StatCgroupFile returns metadata for file under rel's cpuset cgroup directory.
	StatCgroupFile(ctx context.Context, rel, file string) (mtime time.Time, size int64, err error)
	// ReadCgroupFile returns raw content for file under rel's cgroup directory.
	ReadCgroupFile(ctx context.Context, rel, file string) ([]byte, error)
	// ReadCPUSetPartition reads cpuset.cpus.partition at rel.
	ReadCPUSetPartition(ctx context.Context, rel string) (cgcommon.CPUSetPartitionFlag, error)
	// ListRootChildren returns direct child directory names under the cpuset cgroup root.
	ListRootChildren(ctx context.Context) ([]string, error)
	// ListChildren returns direct child directory names under rel.
	ListChildren(ctx context.Context, rel string) ([]string, error)
	// EnsureDir creates the cpuset cgroup directory at rel when it is missing.
	EnsureDir(ctx context.Context, rel string) error
	// StatDir returns the mtime of the cgroup directory at rel.
	StatDir(ctx context.Context, rel string) (mtime time.Time, err error)
	// Prune drops cached state whose rel is not active.
	Prune(activeRels map[string]struct{})
}

// CgroupIdentity identifies a cgroup directory independently of its path.
type CgroupIdentity struct {
	Device uint64
	Inode  uint64
}

// ErrCgroupIdentityMismatch indicates that a rel was recreated after proof publication.
var ErrCgroupIdentityMismatch = errors.New("cgroup identity does not match proof")

// IdentityBoundPIDAttacher is an optional capability for attaching a PID only
// when rel still names the cgroup directory identified by the supplied proof.
type IdentityBoundPIDAttacher interface {
	AttachPIDWithIdentity(ctx context.Context, rel string, identity CgroupIdentity, pid int) error
}

// ControllerPIDAttacher is an optional capability for controller-local cgroup
// discovery, reads, directory creation, and PID/TID attachment.
type ControllerPIDAttacher interface {
	ControllerMount(ctx context.Context, subsys string) (cgcommon.ControllerMount, error)
	EnsureControllerDir(ctx context.Context, subsys, rel string) error
	ReadControllerFile(ctx context.Context, subsys, rel, file string) ([]byte, error)
	AttachPIDToController(ctx context.Context, subsys, rel string, pid int) error
	AttachTIDToController(ctx context.Context, subsys, rel string, tid int) error
}

type controllerMountResolver func(subsys string) (cgcommon.ControllerMount, error)

type coreCgroupClient struct {
	resolveControllerMount controllerMountResolver
}

const slowAttachPIDThreshold = 200 * time.Millisecond

func isSlowAttachPID(elapsed time.Duration) bool {
	return elapsed >= slowAttachPIDThreshold
}

// NewCgroupClient returns a core cgroup client wrapped by a write-if-change cache.
func NewCgroupClient() CgroupClient {
	return NewCachedCgroupClient(coreCgroupClient{})
}

// CgroupClientFactory constructs a CgroupClient. Production code uses
// DefaultCgroupClientFactory; tests inject a fake factory so cgroup-reading
// plugins can run without monkey patching NewCgroupClient.
type CgroupClientFactory func() CgroupClient

// DefaultCgroupClientFactory returns the production cgroup client factory.
func DefaultCgroupClientFactory() CgroupClientFactory { return NewCgroupClient }

func (coreCgroupClient) ApplyCPUSet(_ context.Context, rel string, data *cgcommon.CPUSetData) error {
	if data == nil {
		return fmt.Errorf("ApplyCPUSet: nil data")
	}
	return cgmanager.ApplyCPUSetWithRelativePath(rel, data)
}

func (coreCgroupClient) ApplyCPUSetPartition(_ context.Context, rel string, flag cgcommon.CPUSetPartitionFlag) error {
	return cgmanager.ApplyCPUSetPartitionWithRelativePath(rel, flag)
}

func (coreCgroupClient) ApplyCPU(_ context.Context, rel string, data *cgcommon.CPUData) error {
	if data == nil {
		return fmt.Errorf("ApplyCPU: nil data")
	}
	return cgmanager.ApplyCPUWithRelativePath(rel, data)
}

func (coreCgroupClient) ApplySchedLoadBalance(_ context.Context, rel string, enabled bool) error {
	err := cgmanager.ApplySchedLoadBalanceWithRelativePath(rel, enabled)
	if err != nil && errors.Is(err, cgcommon.ErrNotSupported) {
		return fmt.Errorf("sched_load_balance @ %s: %w", rel, err)
	}
	return err
}

func (coreCgroupClient) AttachPID(_ context.Context, rel string, pid int) error {
	if pid <= 0 {
		return fmt.Errorf("AttachPID: invalid pid %d", pid)
	}
	abs := cgcommon.GetKubernetesAbsCgroupPath(cgcommon.CgroupSubsysCPUSet, rel)
	f, err := os.OpenFile(filepath.Join(abs, "cgroup.procs"), os.O_WRONLY, 0)
	if err != nil {
		return fmt.Errorf("open cgroup.procs @ %s: %w", rel, err)
	}
	defer func() { _ = f.Close() }()
	writeStart := time.Now()
	defer func() {
		if elapsed := time.Since(writeStart); isSlowAttachPID(elapsed) {
			klog.V(2).Infof("AttachPID slow cgroup.procs write: rel=%q pid=%d elapsed=%s", rel, pid, elapsed)
		}
	}()
	if _, err := fmt.Fprintf(f, "%d", pid); err != nil {
		return fmt.Errorf("write pid %d @ %s: %w", pid, rel, err)
	}
	return nil
}

func (coreCgroupClient) Version(context.Context) CgroupVersion {
	if cgcommon.CheckCgroup2UnifiedMode() {
		return CgroupVersionV2
	}
	return CgroupVersionV1
}

func (coreCgroupClient) ReadCPUSet(_ context.Context, rel string) (machine.CPUSet, error) {
	stats, err := cgmanager.GetCPUSetWithRelativePath(rel)
	if err != nil {
		return machine.NewCPUSet(), fmt.Errorf("read cpuset @ %s: %w", rel, err)
	}
	if stats == nil {
		return machine.NewCPUSet(), nil
	}
	cs, err := machine.Parse(stats.CPUs)
	if err != nil {
		return machine.NewCPUSet(), fmt.Errorf("parse cpuset %q @ %s: %w", stats.CPUs, rel, err)
	}
	return cs, nil
}

func (coreCgroupClient) StatCPUSet(_ context.Context, rel string) (time.Time, int64, error) {
	abs := cgcommon.GetKubernetesAbsCgroupPath(cgcommon.CgroupSubsysCPUSet, rel)
	info, err := os.Stat(filepath.Join(abs, "cpuset.cpus"))
	if err != nil {
		return time.Time{}, 0, err
	}
	return info.ModTime(), info.Size(), nil
}

func (coreCgroupClient) StatCgroupFile(_ context.Context, rel, file string) (time.Time, int64, error) {
	abs := cgcommon.GetKubernetesAbsCgroupPath(cgroupSubsysForFile(file), rel)
	info, err := os.Stat(filepath.Join(abs, file))
	if err != nil {
		return time.Time{}, 0, err
	}
	return info.ModTime(), info.Size(), nil
}

func (coreCgroupClient) ReadCgroupFile(_ context.Context, rel, file string) ([]byte, error) {
	abs := cgcommon.GetKubernetesAbsCgroupPath(cgroupSubsysForFile(file), rel)
	raw, err := os.ReadFile(filepath.Join(abs, file))
	if err != nil {
		return nil, err
	}
	return raw, nil
}

func cgroupSubsysForFile(file string) string {
	if strings.HasPrefix(file, "cpu.") {
		return cgcommon.CgroupSubsysCPU
	}
	return cgcommon.CgroupSubsysCPUSet
}

func (coreCgroupClient) ReadCPUSetPartition(_ context.Context, rel string) (cgcommon.CPUSetPartitionFlag, error) {
	abs := cgcommon.GetKubernetesAbsCgroupPath(cgcommon.CgroupSubsysCPUSet, rel)
	raw, err := os.ReadFile(filepath.Join(abs, "cpuset.cpus.partition"))
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("read cpuset.cpus.partition @ %s: %w", rel, cgcommon.ErrNotSupported)
		}
		return "", fmt.Errorf("read cpuset.cpus.partition @ %s: %w", rel, err)
	}
	val := strings.TrimSpace(string(raw))
	switch cgcommon.CPUSetPartitionFlag(val) {
	case cgcommon.CPUSetPartitionFlagRoot, cgcommon.CPUSetPartitionFlagMember, cgcommon.CPUSetPartitionFlagIsolated:
		return cgcommon.CPUSetPartitionFlag(val), nil
	default:
		if idx := strings.IndexAny(val, " \t"); idx > 0 {
			return cgcommon.CPUSetPartitionFlag(val[:idx]), nil
		}
		return cgcommon.CPUSetPartitionFlag(val), nil
	}
}

func (c coreCgroupClient) ListRootChildren(ctx context.Context) ([]string, error) {
	return c.ListChildren(ctx, "")
}

func (coreCgroupClient) ListChildren(_ context.Context, rel string) ([]string, error) {
	var abs string
	if strings.TrimSpace(rel) == "" {
		abs = cgcommon.GetCgroupRootPath(cgcommon.CgroupSubsysCPUSet)
	} else {
		abs = cgcommon.GetKubernetesAbsCgroupPath(cgcommon.CgroupSubsysCPUSet, rel)
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		return nil, fmt.Errorf("readdir %s: %w", abs, err)
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out, nil
}

func (coreCgroupClient) EnsureDir(_ context.Context, rel string) error {
	abs := cgcommon.GetKubernetesAbsCgroupPath(cgcommon.CgroupSubsysCPUSet, rel)
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return fmt.Errorf("mkdir cpuset cgroup @ %s: %w", rel, err)
	}
	if err := initializeCPUSetMemsFromParent(abs, rel); err != nil {
		return err
	}
	return nil
}

func initializeCPUSetMemsFromParent(abs, rel string) error {
	memsPath := filepath.Join(abs, "cpuset.mems")
	current, err := os.ReadFile(memsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read cpuset.mems @ %s: %w", rel, err)
	}
	if strings.TrimSpace(string(current)) != "" {
		return nil
	}
	parent := filepath.Dir(abs)
	if parent == abs || parent == "." || parent == string(filepath.Separator) {
		return nil
	}
	parentMems, err := os.ReadFile(filepath.Join(parent, "cpuset.mems"))
	if err != nil {
		return fmt.Errorf("read parent cpuset.mems @ %s: %w", rel, err)
	}
	value := strings.TrimSpace(string(parentMems))
	if value == "" {
		return nil
	}
	if err := os.WriteFile(memsPath, []byte(value), 0o644); err != nil {
		return fmt.Errorf("initialize cpuset.mems @ %s: %w", rel, err)
	}
	return nil
}

func (coreCgroupClient) StatDir(_ context.Context, rel string) (time.Time, error) {
	abs := cgcommon.GetKubernetesAbsCgroupPath(cgcommon.CgroupSubsysCPUSet, rel)
	info, err := os.Stat(abs)
	if err != nil {
		return time.Time{}, err
	}
	return info.ModTime(), nil
}

func (coreCgroupClient) Prune(map[string]struct{}) {}
