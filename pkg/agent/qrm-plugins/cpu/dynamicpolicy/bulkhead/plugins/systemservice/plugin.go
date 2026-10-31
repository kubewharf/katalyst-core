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

// Package systemservice migrates non-critical systemd services, matching
// daemonset-pod PIDs, and (optionally) certain movable kernel threads out
// of the latency-critical cgroup and into a dedicated "system" cpuset. The
// intent is to keep the latency-critical partition free of surprise CPU
// contention while leaving safety-critical services (kworker, migration,
// ksoftirqd, audit, systemd itself) untouched.
//
// Migration strategy:
//   - CPU and cpuset memberships are discovered from their own controller
//     roots, merged by PID for one classification pass, then migrated
//     independently. Cpuset attaches are identity-bound to the exact
//     device/inode published by cpuset_topology; CPU attaches use the CPU
//     controller's target path. These operations change cgroup membership only
//     and do not set or otherwise guarantee a PID's scheduler affinity.
//   - Userspace leaders come from each controller root's cgroup.procs, with
//     kthread-only TID supplementation from that root's tasks. Classification
//     uses /proc/<pid>/stat (see procfscommon.ProcInfo.IsKThread, which reads
//     PF_KTHREAD).
//   - Kernel threads (info.IsKThread == true) are migrated only when their
//     comm contains one of the whitelisted substrings
//     (BulkheadSystemKThreadCommSubstrs). This is a positive-list to guard
//     against touching scheduler-critical kthreads (per-CPU migration/N,
//     ksoftirqd/N, kworker/N — none of which are safely movable).
//   - Userspace daemons (info.IsKThread == false) are migrated UNLESS their
//     comm appears in BulkheadSystemdCommBlacklist. Latency-critical daemons
//     (systemd, kubelet, containerd, ...) should be listed there.
//
// When the plugin's dynamic switch transitions from enabled to disabled
// (or the first PeriodicalHandler tick after restart observes disabled),
// a one-shot inverse migration reads every PID/TID currently listed in each
// controller's targetRel/cgroup.procs or targetRel/tasks and reattaches each
// membership to that controller's own root. It does not recurse into child
// cgroups or filter PIDs by managed status. Subsequent ticks while disabled are
// no-ops.
//
// Every migration is logged at V(2) so operators can audit.
package systemservice

import (
	"io"

	bulkheadapi "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/api"
	"github.com/kubewharf/katalyst-core/pkg/config"
	bulkheadconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/qrm/bulkhead"
	cgroupclient "github.com/kubewharf/katalyst-core/pkg/util/cgroup/client"
	utilfs "github.com/kubewharf/katalyst-core/pkg/util/fs"
	procfscommon "github.com/kubewharf/katalyst-core/pkg/util/procfs/common"
)

const (
	SystemServicePluginName = "system_service"
	unifiedControllerName   = "unified"
)

const defaultProcfsPath = "/proc"

var (
	_ bulkheadapi.Plugin            = (*SystemServicePlugin)(nil)
	_ bulkheadapi.PeriodicalCapable = (*SystemServicePlugin)(nil)
)

type SystemServicePlugin struct {
	cfg    bulkheadconfig.BulkheadConfiguration
	fs     utilfs.FS
	proc   procfscommon.ProcReader
	cgroup cgroupclient.CgroupClient
	// pinPID opens a process identity handle. Tests replace it to exercise
	// PID-reuse races without depending on host pidfd support.
	pinPID func(pid int) (io.Closer, error)

	// targetRel is the cgroup-relative path of the system-reclaim target
	// (e.g. "system"). We use the CgroupClient AttachPID interface to migrate
	// processes rather than writing cgroup.procs directly, so only the rel
	// path is needed.
	targetRel string

	// lastPeriodicalEnabled tracks the enable state observed by the previous
	// PeriodicalHandler tick. A nil value means "no prior tick observed"
	// (fresh process); when the first tick observes disabled we must run the
	// one-shot reset to converge state after a restart. Read/written only
	// from PeriodicalHandler, which the bulkhead Manager invokes under
	// Manager.mu — no plugin-local lock is required.
	lastPeriodicalEnabled *bool

	// lastMigratedAppliedViewRevision records the most recent applied-view
	// revision used by a successful enabled migration sweep. It is diagnostic
	// state only: a stable revision remains valid for later sweeps because new
	// processes may enter the root cgroup at any time.
	lastMigratedAppliedViewRevision uint64
}

func NewSystemServicePlugin(conf *config.Configuration) bulkheadapi.Plugin {
	return NewSystemServicePluginWithCgroupClient(conf, nil)
}

// NewSystemServicePluginWithCgroupClient injects the cgroup client factory.
// A nil factory defaults to cgroupclient.NewCgroupClient.
func NewSystemServicePluginWithCgroupClient(conf *config.Configuration, factory cgroupclient.CgroupClientFactory) bulkheadapi.Plugin {
	var cfg bulkheadconfig.BulkheadConfiguration
	if conf != nil && conf.CPUQRMPluginConfig != nil && conf.CPUQRMPluginConfig.BulkheadConfiguration != nil {
		cfg = *conf.CPUQRMPluginConfig.BulkheadConfiguration
	}

	// The factory signature cannot return an error, so a missing procfs path
	// falls back to a safe default instead of failing construction. Runtime
	// behavior is still gated by Enable / the dynamic switch.
	procfsPath := cfg.BulkheadSystemServiceProcfsPath
	if procfsPath == "" {
		procfsPath = defaultProcfsPath
	}

	if factory == nil {
		factory = cgroupclient.DefaultCgroupClientFactory()
	}

	fs := utilfs.NewOSFS()
	return &SystemServicePlugin{
		cfg:       cfg,
		fs:        fs,
		proc:      procfscommon.NewProcReader(fs, procfsPath),
		cgroup:    factory(),
		pinPID:    openPIDIdentity,
		targetRel: cfg.BulkheadSystemRelPath,
	}
}

func (p *SystemServicePlugin) Name() string { return SystemServicePluginName }
