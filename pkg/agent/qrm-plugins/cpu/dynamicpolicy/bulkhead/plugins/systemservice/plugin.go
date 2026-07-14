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
// intent is to keep the latency-critical partition free of surprise CPU contention
// while leaving safety-critical services (kworker, migration, ksoftirqd,
// audit, systemd itself) untouched.
//
// All movement goes through an explicit whitelist / substring filter, and we
// log every migration at V(2) so operators can audit.
//
// Migration strategy (split across the two core bulkhead handler paths):
//   - Kernel threads CANNOT be moved via cgroup.procs — the kernel
//     rejects the write with EPERM/EINVAL because kthreads live outside
//     the normal cgroup hierarchy. Instead we bound them via the
//     sched_setaffinity(2) syscall, pinning them to the tick-scoped
//     reclaim CPU set (View.ReclaimEffective). Because that set is only
//     available on the CPUSetAdjustment path (which carries View), kthread
//     pinning runs in CPUSetAdjustmentHandler.
//   - Userspace daemons (systemd services, daemonset PIDs) are moved by
//     writing the decimal PID into the target cgroup's cgroup.procs via
//     CgroupClient.AttachPID. Their target cgroup is filesystem-resident and
//     does not depend on View, so userspace migration runs in
//     PeriodicalHandler.
package systemservice

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	bulkheadapi "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/api"
	"github.com/kubewharf/katalyst-core/pkg/config"
	dynamicconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	bulkheadconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/qrm/bulkhead"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	cgroupclient "github.com/kubewharf/katalyst-core/pkg/util/cgroup/client"
	cgcommon "github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	utilfs "github.com/kubewharf/katalyst-core/pkg/util/fs"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	procfscommon "github.com/kubewharf/katalyst-core/pkg/util/procfs/common"
)

const SystemServicePluginName = "system_service"

const defaultProcfsPath = "/proc"

var _ bulkheadapi.Plugin = (*SystemServicePlugin)(nil)

type SystemServicePlugin struct {
	cfg    bulkheadconfig.BulkheadConfiguration
	fs     utilfs.FS
	proc   procfscommon.ProcReader
	cgroup cgroupclient.CgroupClient

	// targetRel is the cgroup-relative path of the system-reclaim target
	// (e.g. "system"). We use the CgroupClient AttachPID interface to migrate
	// userspace processes rather than writing cgroup.procs directly, so only
	// the rel path is needed.
	targetRel string

	// rootCgroupProcsPath is the cgroup ROOT's cgroup.procs file. We only
	// classify processes that live directly in the cgroup root (i.e. tasks
	// kubelet / the container runtime never placed in a managed sub-cgroup,
	// which is exactly the set of host-level systemd services and movable
	// kthreads we may steer). Reading this one file is far cheaper and far
	// more precise than walking every PID in /proc.
	rootCgroupProcsPath string
}

func NewSystemServicePlugin(conf *config.Configuration) bulkheadapi.Plugin {
	var cfg bulkheadconfig.BulkheadConfiguration
	if conf != nil && conf.CPUQRMPluginConfig != nil && conf.CPUQRMPluginConfig.BulkheadConfiguration != nil {
		cfg = *conf.CPUQRMPluginConfig.BulkheadConfiguration
	}

	// The factory signature cannot return an error, so a missing procfs path
	// falls back to a safe default instead of failing construction. Runtime
	// behaviour is still gated by Enable / the dynamic switch.
	procfsPath := cfg.BulkheadSystemServiceProcfsPath
	if procfsPath == "" {
		procfsPath = defaultProcfsPath
	}

	fs := utilfs.NewOSFS()
	return &SystemServicePlugin{
		cfg:    cfg,
		fs:     fs,
		proc:   procfscommon.NewProcReader(fs, procfsPath),
		cgroup: cgroupclient.NewCgroupClient(),
		// The cpuset cgroup ROOT (mount point on v2, <mount>/cpuset on v1) is
		// the only place we scan for candidate PIDs: processes still sitting in
		// the cpuset root are the host-level services / kthreads not yet claimed
		// by any managed sub-cgroup.
		targetRel:           cfg.BulkheadSystemRelPath,
		rootCgroupProcsPath: cgcommon.GetCgroupRootPath(cgcommon.CgroupSubsysCPUSet) + "/cgroup.procs",
	}
}

func (p *SystemServicePlugin) Name() string { return SystemServicePluginName }

func (p *SystemServicePlugin) Enable(in bulkheadapi.HandlerContext) bool {
	return enableBulkheadSystemService(in.DynamicConf)
}

// CPUSetAdjustmentHandler pins matching kernel threads to the tick-scoped
// reclaim CPU set. Kthreads cannot be migrated via cgroup.procs, so
// sched_setaffinity(2) is used. This path carries View, which is the only
// source of the current reclaim partition.
func (p *SystemServicePlugin) CPUSetAdjustmentHandler(_ context.Context, in bulkheadapi.HandlerContext) error {
	if in.View == nil {
		// No reclaim ground truth this tick — skip rather than clear masks.
		return nil
	}
	sysCPUs := in.View.ReclaimEffective.Clone().ToSliceInt()
	if len(sysCPUs) == 0 {
		// An empty affinity mask would pin the kthread to no CPUs — an
		// unrecoverable state on many kernels. Defer to a future tick.
		return nil
	}

	pids, err := p.listRootCgroupPIDs()
	if err != nil {
		emitBulkheadSystemServiceResult(in.Emitter, "kthread_pin", "skipped", "list_root_cgroup_pids")
		return fmt.Errorf("list root cgroup pids: %w", err)
	}

	pinned := 0
	for _, pid := range pids {
		info, err := p.proc.ReadProc(pid)
		if err != nil {
			// PID likely exited between listing and read — normal race.
			continue
		}
		if !info.IsKThread || !p.shouldMigrate(info) {
			continue
		}
		if err := p.proc.SchedSetaffinity(pid, sysCPUs); err != nil {
			// PID may have exited, or the kernel may refuse to migrate a
			// per-CPU thread. Log and move on — retries next tick.
			general.InfofV(4, "system_service: sched_setaffinity failed, pid=%d comm=%q err=%v",
				pid, info.Comm, err)
			continue
		}
		general.InfofV(2, "system_service: pinned kernel thread, pid=%d comm=%q cpus=%v",
			pid, info.Comm, sysCPUs)
		pinned++
	}
	emitBulkheadSystemServiceResult(in.Emitter, "kthread_pin", "success", "")
	general.InfofV(4, "system_service: kthread pin complete, scanned=%d pinned=%d", len(pids), pinned)
	return nil
}

// CPUSetAdjustmentDisabledHandler is a no-op: when bulkhead is disabled we do
// not proactively revert kthread affinity (the adapter source has no undo
// action either).
func (p *SystemServicePlugin) CPUSetAdjustmentDisabledHandler(context.Context, bulkheadapi.HandlerContext) error {
	return nil
}

// PeriodicalHandler migrates matching userspace daemons into the target
// cgroup via CgroupClient.AttachPID. This path has no View, but the target
// cgroup is filesystem-resident and does not depend on it.
func (p *SystemServicePlugin) PeriodicalHandler(ctx context.Context, in bulkheadapi.PeriodicalHandlerContext) error {
	// RunPeriodicalHandlers is only gated by the top-level bulkhead switch, so
	// re-check this plugin's own dynamic switch here.
	if !enableBulkheadSystemService(in.DynamicConf) {
		return nil
	}

	// Target cgroup not created yet — bail early, cpuset_topology owns
	// creation. Next tick will retry.
	if _, err := p.cgroup.StatDir(ctx, p.targetRel); err != nil {
		general.InfofV(4, "system_service: target cgroup missing, skipping, rel=%q err=%v",
			p.targetRel, err)
		emitBulkheadSystemServiceResult(in.Emitter, "userspace_migrate", "skipped", "target_cgroup_missing")
		return nil
	}

	pids, err := p.listRootCgroupPIDs()
	if err != nil {
		emitBulkheadSystemServiceResult(in.Emitter, "userspace_migrate", "skipped", "list_root_cgroup_pids")
		return fmt.Errorf("list root cgroup pids: %w", err)
	}

	moved := 0
	for _, pid := range pids {
		if ctx.Err() != nil {
			emitBulkheadSystemServiceResult(in.Emitter, "userspace_migrate", "failed", "context_canceled")
			return fmt.Errorf("context canceled: %w", ctx.Err())
		}
		info, err := p.proc.ReadProc(pid)
		if err != nil {
			// PID likely exited between listing and read — normal race.
			continue
		}
		if info.IsKThread || !p.shouldMigrate(info) {
			continue
		}
		if err := p.cgroup.AttachPID(ctx, p.targetRel, pid); err != nil {
			general.InfofV(4, "system_service: cgroup migration failed, pid=%d comm=%q err=%v",
				pid, info.Comm, err)
			continue
		}
		general.InfofV(2, "system_service: migrated systemd process, pid=%d comm=%q",
			pid, info.Comm)
		moved++
	}
	emitBulkheadSystemServiceResult(in.Emitter, "userspace_migrate", "success", "")
	general.InfofV(4, "system_service: userspace migrate complete, scanned=%d moved=%d", len(pids), moved)
	return nil
}

// shouldMigrate returns true if the process described by info is eligible for
// migration. The decision is defensive by design:
//   - Kernel threads: only migrate when comm matches one of the configured
//     substrings; skip otherwise (never touch scheduler-critical kthreads).
//   - Userspace: only migrate when comm is in BulkheadSystemdCommWhitelist.
func (p *SystemServicePlugin) shouldMigrate(info procfscommon.ProcInfo) bool {
	if info.IsKThread {
		for _, sub := range p.cfg.BulkheadSystemKThreadCommSubstrs {
			if sub == "" {
				continue
			}
			if strings.Contains(info.Comm, sub) {
				return true
			}
		}
		return false
	}
	for _, w := range p.cfg.BulkheadSystemdCommWhitelist {
		if w != "" && info.Comm == w {
			return true
		}
	}
	return false
}

// listRootCgroupPIDs reads the cgroup ROOT's cgroup.procs and returns the
// PIDs sitting directly in it. This restricts the candidate set to the
// host-level tasks that no managed sub-cgroup has claimed — the only
// processes this plugin is ever allowed to steer. Malformed / non-numeric
// lines are skipped defensively; a read error is surfaced to the caller.
func (p *SystemServicePlugin) listRootCgroupPIDs() ([]int, error) {
	data, err := p.fs.ReadFile(p.rootCgroupProcsPath)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", p.rootCgroupProcsPath, err)
	}
	lines := strings.Fields(string(data))
	out := make([]int, 0, len(lines))
	for _, line := range lines {
		pid, err := strconv.Atoi(strings.TrimSpace(line))
		if err != nil || pid <= 0 {
			continue
		}
		out = append(out, pid)
	}
	return out, nil
}

func enableBulkheadSystemService(conf *dynamicconfig.Configuration) bool {
	if conf == nil || conf.AdminQoSConfiguration == nil || conf.AdminQoSConfiguration.CPUPluginConfiguration == nil {
		return false
	}
	return conf.AdminQoSConfiguration.CPUPluginConfiguration.BulkheadConfig.EnableBulkheadSystemService
}

const metricBulkheadSystemServiceResult = "bulkhead_system_service_result"

func emitBulkheadSystemServiceResult(emitter metrics.MetricEmitter, phase, status, reason string) {
	if emitter == nil {
		return
	}
	_ = emitter.StoreInt64(metricBulkheadSystemServiceResult, 1, metrics.MetricTypeNameCount,
		metrics.MetricTag{Key: "phase", Val: phase},
		metrics.MetricTag{Key: "status", Val: status},
		metrics.MetricTag{Key: "reason", Val: reason},
	)
}
