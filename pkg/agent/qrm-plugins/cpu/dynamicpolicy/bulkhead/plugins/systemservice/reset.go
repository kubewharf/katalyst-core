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
	"syscall"

	apierrors "k8s.io/apimachinery/pkg/util/errors"

	bulkheadapi "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/api"
	cgcommon "github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

// resetTargetToRoot performs the one-shot inverse migration when the plugin's
// dynamic switch transitions from enabled to disabled (or the first tick
// after restart observes disabled). It independently reads each controller's
// target membership and re-attaches it to that controller's root. Any per-PID
// failure is returned so the disabled transition remains pending and a later
// tick retries the incomplete reset. Returning PIDs currently in the target
// cgroup to root before topology-disabled state converges avoids leaving stale
// system-cgroup ownership behind after the feature is disabled.
func (p *SystemServicePlugin) resetTargetToRoot(ctx context.Context, in bulkheadapi.PeriodicalHandlerContext) error {
	controllerAttacher, err := p.controllerAttacher()
	if err != nil {
		emitBulkheadSystemServiceFailures(in.Emitter, "reset", []error{err})
		return err
	}
	sources, sourceErrors := p.controllerSources(ctx, controllerAttacher)
	candidates, listErrors := p.listTargetCgroupCandidates(ctx, sources, controllerAttacher)

	moved := 0
	errs := append(sourceErrors, listErrors...)
	for _, candidate := range candidates {
		pid := candidate.pid
		if ctx.Err() != nil {
			emitBulkheadSystemServiceResult(in.Emitter, "reset", "failed", "context_canceled", "all")
			return fmt.Errorf("context canceled: %w", ctx.Err())
		}
		pin, pinErr := p.pinProcess(pid)
		if pinErr != nil {
			if errors.Is(pinErr, syscall.ESRCH) {
				general.InfofV(4, "system_service: reset skipped exited pid=%d err=%v", pid, pinErr)
				continue
			}
			// A task-only non-leader TID is moved with the userspace leader
			// listed in cgroup.procs, so EINVAL is safe only for this case.
			if candidate.allTaskOnly() && errors.Is(pinErr, syscall.EINVAL) {
				general.InfofV(4, "system_service: reset skipped task-only tid=%d rejected by pidfd_open err=%v", pid, pinErr)
				continue
			}
			errs = append(errs, operationError("all", "attach_error",
				fmt.Errorf("pin pid %d before reset: %w", pid, pinErr)))
			continue
		}
		var candidateErrors []error
		for _, controller := range candidate.controllers() {
			membership := candidate.memberships[controller]
			controllerSubsys := controller
			if controllerSubsys == unifiedControllerName {
				controllerSubsys = cgcommon.CgroupSubsysCPUSet
			}
			var attachErr error
			if membership.taskOnly {
				attachErr = controllerAttacher.AttachTIDToController(ctx, controllerSubsys, "", pid)
			} else {
				switch controller {
				case cgcommon.CgroupSubsysCPUSet, unifiedControllerName:
					attachErr = p.cgroup.AttachPID(ctx, "", pid)
				case cgcommon.CgroupSubsysCPU:
					attachErr = controllerAttacher.AttachPIDToController(ctx, cgcommon.CgroupSubsysCPU, "", pid)
				}
			}
			if attachErr != nil && !errors.Is(attachErr, syscall.ESRCH) {
				candidateErrors = append(candidateErrors, operationError(controller, "attach_error",
					fmt.Errorf("attach pid %d to root controller %s: %w", pid, controller, attachErr)))
			}
		}
		closeErr := pin.Close()
		if len(candidateErrors) != 0 {
			errs = append(errs, candidateErrors...)
			continue
		}
		if closeErr != nil {
			errs = append(errs, operationError("all", "attach_error",
				fmt.Errorf("close pid identity for %d after reset: %w", pid, closeErr)))
			continue
		}
		general.InfofV(2, "system_service: reset migrated pid=%d back to root cgroup", pid)
		moved++
	}
	if len(errs) != 0 {
		emitBulkheadSystemServiceFailures(in.Emitter, "reset", errs)
		return apierrors.NewAggregate(errs)
	}
	emitBulkheadSystemServiceResult(in.Emitter, "reset", "success", "", "all")
	general.InfofV(4, "system_service: reset complete, scanned=%d moved=%d", len(candidates), moved)
	return nil
}
