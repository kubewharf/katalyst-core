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

package systemservice

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"
	"time"

	apierrors "k8s.io/apimachinery/pkg/util/errors"

	bulkheadapi "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/api"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/model"
	cgroupclient "github.com/kubewharf/katalyst-core/pkg/util/cgroup/client"
	cgcommon "github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	procfscommon "github.com/kubewharf/katalyst-core/pkg/util/procfs/common"
)

const slowAttachThreshold = 200 * time.Millisecond

type migrationTargets struct {
	targetRel              string
	cpusetIdentityAttacher cgroupclient.IdentityBoundPIDAttacher
	cpusetProof            model.CgroupRelProof
	cpusetReady            bool
	cpuAttacher            cgroupclient.ControllerPIDAttacher
	cpuReady               bool
}

type migrationOutcome struct {
	errors          []error
	cpusetAttempted bool
	cpusetFailed    bool
}

func (o *migrationOutcome) addError(err error) {
	o.errors = append(o.errors, err)
}

func (o *migrationOutcome) merge(other migrationOutcome) {
	o.errors = append(o.errors, other.errors...)
	o.cpusetAttempted = o.cpusetAttempted || other.cpusetAttempted
	o.cpusetFailed = o.cpusetFailed || other.cpusetFailed
}

func migrateSweepLogLevel(elapsed time.Duration) int {
	if elapsed >= slowAttachThreshold {
		return 2
	}
	return 4
}

// runMigrate performs the enabled-path migration: read root cgroup PIDs,
// classify each via ReadProc, and attach eligible processes to the exact
// device/inode identity published for targetRel. Ineligible / racing PIDs are
// logged at V(4) and skipped without aborting the sweep.
func (p *SystemServicePlugin) runMigrate(ctx context.Context, in bulkheadapi.PeriodicalHandlerContext) error {
	migrateStart := time.Now()
	defer func() {
		migrateElapsed := time.Since(migrateStart)
		general.InfofV(migrateSweepLogLevel(migrateElapsed),
			"system_service: migrate sweep elapsed=%s", migrateElapsed)
	}()

	targetRel := strings.Trim(p.targetRel, "/")
	if targetRel == "" {
		general.InfofV(4, "system_service: migration skipped, empty target rel")
		emitBulkheadSystemServiceResult(in.Emitter, "migrate", "skipped", "empty_target_rel", "all")
		return nil
	}

	_, cpusetProof, cpusetReady, err := p.authorizedMigrationTarget(ctx, in)
	if err != nil {
		authorizeErr := operationError(cgcommon.CgroupSubsysCPUSet, "authorize_error",
			fmt.Errorf("authorize cpuset target %q: %w", targetRel, err))
		emitBulkheadSystemServiceFailures(in.Emitter, "migrate", []error{authorizeErr})
		return authorizeErr
	}
	if !cpusetReady {
		return nil
	}

	controllerAttacher, err := p.controllerAttacher()
	if err != nil {
		emitBulkheadSystemServiceFailures(in.Emitter, "migrate", []error{err})
		return err
	}
	sources, sourceErrors := p.controllerSources(ctx, controllerAttacher)
	candidates, listErrors := p.listRootMigrationCandidates(sources)
	targets, targetOutcome := p.prepareMigrationTargets(ctx, candidates, controllerAttacher, cpusetProof)
	outcome := migrationOutcome{}
	outcome.errors = append(sourceErrors, listErrors...)
	outcome.merge(targetOutcome)
	for _, candidate := range candidates {
		if ctx.Err() != nil {
			emitBulkheadSystemServiceResult(in.Emitter, "migrate", "failed", "context_canceled", "all")
			return fmt.Errorf("context canceled: %w", ctx.Err())
		}
		outcome.merge(p.migrateCandidate(ctx, candidate, targets))
	}
	if outcome.cpusetAttempted && !outcome.cpusetFailed {
		p.lastMigratedAppliedViewRevision = in.AppliedViewRevision
	}
	if len(outcome.errors) != 0 {
		emitBulkheadSystemServiceFailures(in.Emitter, "migrate", outcome.errors)
		return apierrors.NewAggregate(outcome.errors)
	}
	emitBulkheadSystemServiceResult(in.Emitter, "migrate", "success", "", "all")
	return nil
}

func (p *SystemServicePlugin) prepareMigrationTargets(
	ctx context.Context,
	candidates []migrationCandidate,
	cpuAttacher cgroupclient.ControllerPIDAttacher,
	cpusetProof model.CgroupRelProof,
) (migrationTargets, migrationOutcome) {
	targets := migrationTargets{
		targetRel:   strings.Trim(p.targetRel, "/"),
		cpusetProof: cpusetProof,
		cpusetReady: true,
		cpuAttacher: cpuAttacher,
	}
	var outcome migrationOutcome
	needsCPUSet, needsCPU := candidateControllerNeeds(candidates)

	if needsCPU {
		if err := cpuAttacher.EnsureControllerDir(ctx, cgcommon.CgroupSubsysCPU, targets.targetRel); err != nil {
			outcome.addError(operationError(cgcommon.CgroupSubsysCPU, "ensure_error",
				fmt.Errorf("ensure cpu target %q: %w", targets.targetRel, err)))
		} else {
			targets.cpuReady = true
		}
	}
	if needsCPUSet && targets.cpusetReady {
		var ok bool
		targets.cpusetIdentityAttacher, ok = p.cgroup.(cgroupclient.IdentityBoundPIDAttacher)
		if !ok {
			outcome.addError(operationError(cgcommon.CgroupSubsysCPUSet, "capability_error",
				fmt.Errorf("identity-bound cgroup attach capability is required for target %q", targets.targetRel)))
			targets.cpusetReady = false
		}
	}
	return targets, outcome
}

func (p *SystemServicePlugin) migrateCandidate(
	ctx context.Context,
	candidate migrationCandidate,
	targets migrationTargets,
) migrationOutcome {
	pid := candidate.pid
	pin, err := p.pinProcess(pid)
	if err != nil {
		if errors.Is(err, syscall.ESRCH) {
			general.InfofV(4, "system_service: migration skipped exited pid=%d err=%v", pid, err)
			return migrationOutcome{}
		}
		// Linux pidfd_open may reject a non-leader TID with EINVAL. A task-only
		// userspace thread is already covered by its leader from cgroup.procs;
		// all other pin failures remain fail-closed.
		if candidate.allTaskOnly() && errors.Is(err, syscall.EINVAL) {
			general.InfofV(4, "system_service: migration skipped task-only tid=%d rejected by pidfd_open err=%v", pid, err)
			return migrationOutcome{}
		}
		outcome := migrationOutcome{}
		outcome.addError(operationError("all", "attach_error",
			fmt.Errorf("pin pid %d before migration: %w", pid, err)))
		return outcome
	}
	info, err := p.proc.ReadProc(pid)
	if err != nil {
		// The pinned identity prevents this numeric PID from being reused. A
		// read failure therefore refers only to the listed process.
		_ = pin.Close()
		return migrationOutcome{}
	}
	if candidate.allTaskOnly() && !info.IsKThread {
		_ = pin.Close()
		return migrationOutcome{}
	}
	if !p.shouldMigrate(info) {
		_ = pin.Close()
		return migrationOutcome{}
	}

	var outcome migrationOutcome
	for _, controller := range candidate.controllers() {
		membership := candidate.memberships[controller]
		// A userspace TID listed only in tasks must not be written to this
		// controller's cgroup.procs. Whether the same numeric PID is a leader
		// in another controller is irrelevant: membership and task-only safety
		// are controller-local.
		if membership.taskOnly && !info.IsKThread {
			continue
		}
		attachStart := time.Now()
		var attachErr error
		switch controller {
		case cgcommon.CgroupSubsysCPUSet, unifiedControllerName:
			if !targets.cpusetReady {
				continue
			}
			outcome.cpusetAttempted = true
			// The identity-bound attach pins and verifies the exact cgroup
			// proved by cpuset_topology before writing cgroup.procs.
			attachErr = targets.cpusetIdentityAttacher.AttachPIDWithIdentity(ctx, targets.targetRel, cgroupclient.CgroupIdentity{
				Device: targets.cpusetProof.Device,
				Inode:  targets.cpusetProof.Inode,
			}, pid)
		case cgcommon.CgroupSubsysCPU:
			if !targets.cpuReady {
				continue
			}
			attachErr = targets.cpuAttacher.AttachPIDToController(ctx, cgcommon.CgroupSubsysCPU, targets.targetRel, pid)
		}
		attachElapsed := time.Since(attachStart)
		if attachElapsed >= slowAttachThreshold {
			general.InfofV(2, "system_service: slow cgroup attach, controller=%s pid=%d comm=%q kthread=%v elapsed=%s err=%v",
				controller, pid, info.Comm, info.IsKThread, attachElapsed, attachErr)
		}
		if attachErr == nil || errors.Is(attachErr, syscall.ESRCH) {
			continue
		}
		if controller == cgcommon.CgroupSubsysCPUSet || controller == unifiedControllerName {
			outcome.cpusetFailed = true
		}
		outcome.addError(operationError(controller, "attach_error",
			fmt.Errorf("attach pid %d to %q controller %s: %w", pid, targets.targetRel, controller, attachErr)))
	}
	closeErr := pin.Close()
	if len(outcome.errors) != 0 {
		return outcome
	}
	if closeErr != nil {
		outcome.addError(operationError("all", "attach_error",
			fmt.Errorf("close pid identity for %d after migration: %w", pid, closeErr)))
		return outcome
	}
	general.InfofV(2, "system_service: migrated process, pid=%d comm=%q kthread=%v",
		pid, info.Comm, info.IsKThread)
	return outcome
}

// authorizedMigrationTarget fail-closes the enabled migration path unless the
// manager supplies a newly-published AppliedView and the configured target
// cgroup's current cpuset matches a non-empty per-rel proof carrying a stable
// device/inode identity. This keeps system_service from resampling desired
// topology or trusting a static rel path that cpuset_topology did not authorize
// in the most recent converged publication.
func (p *SystemServicePlugin) authorizedMigrationTarget(
	ctx context.Context,
	in bulkheadapi.PeriodicalHandlerContext,
) (string, model.CgroupRelProof, bool, error) {
	if !in.AppliedViewValidForPeriodical || in.AppliedView == nil {
		general.InfofV(4, "system_service: migration skipped, missing applied view")
		emitBulkheadSystemServiceResult(in.Emitter, "migrate", "skipped", "missing_applied_view", cgcommon.CgroupSubsysCPUSet)
		return "", model.CgroupRelProof{}, false, nil
	}
	if in.AppliedViewRevision == 0 {
		general.InfofV(4, "system_service: migration skipped, invalid applied view revision=0")
		emitBulkheadSystemServiceResult(in.Emitter, "migrate", "skipped", "stale_applied_view", cgcommon.CgroupSubsysCPUSet)
		return "", model.CgroupRelProof{}, false, nil
	}

	targetRel := strings.Trim(p.targetRel, "/")
	if targetRel == "" {
		general.InfofV(4, "system_service: migration skipped, empty target rel")
		emitBulkheadSystemServiceResult(in.Emitter, "migrate", "skipped", "empty_target_rel", cgcommon.CgroupSubsysCPUSet)
		return "", model.CgroupRelProof{}, false, nil
	}
	proof, proved := in.AppliedView.RelProofByRel[targetRel]
	if !proved || proof.Device == 0 || proof.Inode == 0 || proof.CPUSet.IsEmpty() {
		general.InfofV(4, "system_service: migration skipped, target rel lacks non-empty identity-bound applied proof, rel=%q", targetRel)
		emitBulkheadSystemServiceResult(in.Emitter, "migrate", "skipped", "missing_target_rel_proof", cgcommon.CgroupSubsysCPUSet)
		return "", model.CgroupRelProof{}, false, nil
	}
	// Target cgroup not created yet — bail early, cpuset_topology owns
	// creation. Next tick will retry the same AppliedView revision.
	if _, err := p.cgroup.StatDir(ctx, targetRel); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return "", model.CgroupRelProof{}, false, fmt.Errorf("stat target cgroup %q: %w", targetRel, err)
		}
		general.InfofV(4, "system_service: target cgroup missing, skipping, rel=%q err=%v",
			targetRel, err)
		emitBulkheadSystemServiceResult(in.Emitter, "migrate", "skipped", "target_cgroup_missing", cgcommon.CgroupSubsysCPUSet)
		return "", model.CgroupRelProof{}, false, nil
	}

	targetCPUSet, err := p.readTargetCPUSet(ctx, targetRel)
	if err != nil {
		return "", model.CgroupRelProof{}, false, err
	}
	if targetCPUSet.IsEmpty() || !targetCPUSet.Equals(proof.CPUSet) {
		return "", model.CgroupRelProof{}, false, fmt.Errorf(
			"target cpuset %q differs from applied proof: got %s want %s",
			targetRel, targetCPUSet.String(), proof.CPUSet.String())
	}
	return targetRel, proof, true, nil
}

func (p *SystemServicePlugin) readTargetCPUSet(ctx context.Context, targetRel string) (machine.CPUSet, error) {
	raw, err := p.cgroup.ReadCgroupFile(ctx, targetRel, "cpuset.cpus.effective")
	if err == nil {
		cpuset, parseErr := machine.Parse(strings.TrimSpace(string(raw)))
		if parseErr != nil {
			return machine.NewCPUSet(), fmt.Errorf("parse target cpuset.cpus.effective %q @ %s: %w", strings.TrimSpace(string(raw)), targetRel, parseErr)
		}
		return cpuset, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return machine.NewCPUSet(), fmt.Errorf("read target cpuset.cpus.effective @ %s: %w", targetRel, err)
	}
	cpuset, err := p.cgroup.ReadCPUSet(ctx, targetRel)
	if err != nil {
		return machine.NewCPUSet(), fmt.Errorf("read target cpuset @ %s: %w", targetRel, err)
	}
	return cpuset, nil
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
