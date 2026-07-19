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
//   - Everything goes through a single unified path: PID lookup from the
//     cgroup ROOT's cgroup.procs, per-PID classification via /proc/<pid>/stat
//     (see procfscommon.ProcInfo.IsKThread which reads PF_KTHREAD), and
//     CgroupClient.AttachPID into the target "system" cgroup.
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
// a one-shot inverse migration reads targetRel/cgroup.procs and reattaches
// every PID into the cpuset root, so state converges without operator
// intervention. Subsequent ticks while disabled are no-ops.
//
// Every migration is logged at V(2) so operators can audit.
package systemservice

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/util/errors"

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
	// processes rather than writing cgroup.procs directly, so only the rel
	// path is needed.
	targetRel string

	// rootCgroupProcsPath is the cgroup ROOT's cgroup.procs file. We only
	// classify processes that live directly in the cgroup root (i.e. tasks
	// kubelet / the container runtime never placed in a managed sub-cgroup,
	// which is exactly the set of host-level systemd services and movable
	// kthreads we may steer). Reading this one file is far cheaper and far
	// more precise than walking every PID in /proc.
	rootCgroupProcsPath string

	// lastPeriodicalEnabled tracks the enable state observed by the previous
	// PeriodicalHandler tick. A nil value means "no prior tick observed"
	// (fresh process); when the first tick observes disabled we must run the
	// one-shot reset to converge state after a restart. Read/written only
	// from PeriodicalHandler, which the bulkhead Manager invokes under
	// Manager.mu — no plugin-local lock is required.
	lastPeriodicalEnabled *bool
}

func NewSystemServicePlugin(conf *config.Configuration) bulkheadapi.Plugin {
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

// CPUSetAdjustmentHandler is intentionally a no-op: all migration runs in
// PeriodicalHandler via cgroup.procs (AttachPID).
func (p *SystemServicePlugin) CPUSetAdjustmentHandler(context.Context, bulkheadapi.HandlerContext) error {
	return nil
}

// CPUSetAdjustmentDisabledHandler is a no-op: when bulkhead is disabled we do
// not proactively revert cgroup placement (there is no safe global undo).
func (p *SystemServicePlugin) CPUSetAdjustmentDisabledHandler(context.Context, bulkheadapi.HandlerContext) error {
	return nil
}

// PeriodicalHandler migrates every eligible root-cgroup PID into the target
// cgroup via CgroupClient.AttachPID when the plugin's dynamic switch is
// enabled. When the switch transitions from enabled to disabled (or the
// first tick after restart observes disabled), it runs a one-shot reset
// that reattaches every PID under targetRel back into the cpuset root.
// Subsequent ticks while disabled are no-ops.
func (p *SystemServicePlugin) PeriodicalHandler(ctx context.Context, in bulkheadapi.PeriodicalHandlerContext) error {
	enabled := enableBulkheadSystemService(in.DynamicConf)

	if !enabled {
		// Trigger a reset on enabled → disabled transition, or on the first
		// tick after restart if that first observation is disabled. Steady
		// disabled state is a no-op.
		needsReset := p.lastPeriodicalEnabled == nil || *p.lastPeriodicalEnabled
		if !needsReset {
			return nil
		}
		err := p.resetTargetToRoot(ctx, in)
		if err == nil {
			f := false
			p.lastPeriodicalEnabled = &f
		}
		return err
	}

	err := p.runMigrate(ctx, in)
	// Any observed enabled tick — including early returns from missing target
	// or listing errors — updates the tracker to true so a subsequent real
	// disable transition triggers reset.
	t := true
	p.lastPeriodicalEnabled = &t
	return err
}

// runMigrate performs the enabled-path migration: read root cgroup PIDs,
// classify each via ReadProc, and AttachPID into targetRel for the eligible
// subset. Ineligible / racing PIDs are logged at V(4) and skipped without
// aborting the sweep.
func (p *SystemServicePlugin) runMigrate(ctx context.Context, in bulkheadapi.PeriodicalHandlerContext) error {
	// Target cgroup not created yet — bail early, cpuset_topology owns
	// creation. Next tick will retry.
	if _, err := p.cgroup.StatDir(ctx, p.targetRel); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("stat target cgroup %q: %w", p.targetRel, err)
		}
		general.InfofV(4, "system_service: target cgroup missing, skipping, rel=%q err=%v",
			p.targetRel, err)
		emitBulkheadSystemServiceResult(in.Emitter, "migrate", "skipped", "target_cgroup_missing")
		return nil
	}

	pids, err := p.listRootCgroupPIDs()
	if err != nil {
		emitBulkheadSystemServiceResult(in.Emitter, "migrate", "skipped", "list_root_cgroup_pids")
		return fmt.Errorf("list root cgroup pids: %w", err)
	}

	moved := 0
	for _, pid := range pids {
		if ctx.Err() != nil {
			emitBulkheadSystemServiceResult(in.Emitter, "migrate", "failed", "context_canceled")
			return fmt.Errorf("context canceled: %w", ctx.Err())
		}
		info, err := p.proc.ReadProc(pid)
		if err != nil {
			// PID likely exited between listing and read — normal race.
			continue
		}
		if !p.shouldMigrate(info) {
			continue
		}
		if err := p.cgroup.AttachPID(ctx, p.targetRel, pid); err != nil {
			// PID may have exited, or the kernel may refuse to migrate a
			// per-CPU / non-movable kthread. Log and move on — retries next tick.
			general.InfofV(4, "system_service: cgroup migration failed, pid=%d comm=%q kthread=%v err=%v",
				pid, info.Comm, info.IsKThread, err)
			continue
		}
		general.InfofV(2, "system_service: migrated process, pid=%d comm=%q kthread=%v",
			pid, info.Comm, info.IsKThread)
		moved++
	}
	emitBulkheadSystemServiceResult(in.Emitter, "migrate", "success", "")
	general.InfofV(4, "system_service: migrate complete, scanned=%d moved=%d", len(pids), moved)
	return nil
}

// resetTargetToRoot performs the one-shot inverse migration when the plugin's
// dynamic switch transitions from enabled to disabled (or the first tick
// after restart observes disabled). It reads every PID currently in
// targetRel/cgroup.procs and re-attaches it into the cpuset root (rel="")
// via CgroupClient.AttachPID. Any per-PID failure is returned so the disabled
// transition remains pending and a later tick retries the incomplete reset.
func (p *SystemServicePlugin) resetTargetToRoot(ctx context.Context, in bulkheadapi.PeriodicalHandlerContext) error {
	if _, err := p.cgroup.StatDir(ctx, p.targetRel); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("stat target cgroup %q for reset: %w", p.targetRel, err)
		}
		general.InfofV(4, "system_service: reset skipped, target missing, rel=%q err=%v",
			p.targetRel, err)
		emitBulkheadSystemServiceResult(in.Emitter, "reset", "skipped", "target_cgroup_missing")
		return nil
	}

	pids, err := p.listTargetCgroupPIDs(ctx)
	if err != nil {
		emitBulkheadSystemServiceResult(in.Emitter, "reset", "skipped", "list_target_cgroup_pids")
		return fmt.Errorf("list target cgroup pids: %w", err)
	}

	moved := 0
	var errs []error
	for _, pid := range pids {
		if ctx.Err() != nil {
			emitBulkheadSystemServiceResult(in.Emitter, "reset", "failed", "context_canceled")
			return fmt.Errorf("context canceled: %w", ctx.Err())
		}
		if err := p.cgroup.AttachPID(ctx, "", pid); err != nil {
			general.InfofV(4, "system_service: reset attach failed, pid=%d err=%v", pid, err)
			errs = append(errs, fmt.Errorf("attach pid %d to root: %w", pid, err))
			continue
		}
		general.InfofV(2, "system_service: reset migrated pid=%d back to root cgroup", pid)
		moved++
	}
	if len(errs) != 0 {
		emitBulkheadSystemServiceResult(in.Emitter, "reset", "failed", "attach_error")
		return apierrors.NewAggregate(errs)
	}
	emitBulkheadSystemServiceResult(in.Emitter, "reset", "success", "")
	general.InfofV(4, "system_service: reset complete, scanned=%d moved=%d", len(pids), moved)
	return nil
}

// shouldMigrate returns true if the process described by info is eligible for
// migration. The decision is defensive by design:
//   - Kernel threads: only migrate when comm contains one of the configured
//     substrings (positive whitelist); skip otherwise so scheduler-critical
//     kthreads are never touched.
//   - Userspace: migrate unless comm exactly matches one of the configured
//     blacklist entries (negative list of latency-critical daemons).
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
	for _, b := range p.cfg.BulkheadSystemdCommBlacklist {
		if b != "" && info.Comm == b {
			return false
		}
	}
	return true
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
	return parsePIDList(data), nil
}

// listTargetCgroupPIDs reads targetRel's cgroup.procs via CgroupClient and
// returns the PID list. Malformed / non-numeric tokens are skipped
// defensively; read errors are surfaced to the caller.
func (p *SystemServicePlugin) listTargetCgroupPIDs(ctx context.Context) ([]int, error) {
	data, err := p.cgroup.ReadCgroupFile(ctx, p.targetRel, "cgroup.procs")
	if err != nil {
		return nil, fmt.Errorf("read target cgroup.procs @ %s: %w", p.targetRel, err)
	}
	return parsePIDList(data), nil
}

// parsePIDList parses a whitespace-separated cgroup.procs payload into a PID
// slice, skipping malformed / non-positive tokens defensively.
func parsePIDList(data []byte) []int {
	lines := strings.Fields(string(data))
	out := make([]int, 0, len(lines))
	for _, line := range lines {
		pid, err := strconv.Atoi(strings.TrimSpace(line))
		if err != nil || pid <= 0 {
			continue
		}
		out = append(out, pid)
	}
	return out
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
