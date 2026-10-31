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
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	cgroupclient "github.com/kubewharf/katalyst-core/pkg/util/cgroup/client"
	cgcommon "github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
)

type controllerSource struct {
	name  string
	mount cgcommon.ControllerMount
}

func (s controllerSource) subsystem() string {
	if s.name == unifiedControllerName {
		return cgcommon.CgroupSubsysCPUSet
	}
	return s.name
}

type candidateMembership struct {
	controller string
	taskOnly   bool
}

type migrationCandidate struct {
	pid         int
	memberships map[string]candidateMembership
}

func (c migrationCandidate) controllers() []string {
	controllers := make([]string, 0, len(c.memberships))
	for controller := range c.memberships {
		controllers = append(controllers, controller)
	}
	// Keep cpuset ahead of cpu: the cpuset write is identity-bound by the
	// applied view, while a failed cpu write remains discoverable from the cpu
	// root and is retried independently on the next sweep.
	sort.Slice(controllers, func(i, j int) bool {
		order := map[string]int{
			cgcommon.CgroupSubsysCPUSet: 0,
			cgcommon.CgroupSubsysCPU:    1,
			unifiedControllerName:       0,
		}
		return order[controllers[i]] < order[controllers[j]]
	})
	return controllers
}

func (c migrationCandidate) allTaskOnly() bool {
	for _, membership := range c.memberships {
		if !membership.taskOnly {
			return false
		}
	}
	return true
}

func (p *SystemServicePlugin) controllerAttacher() (cgroupclient.ControllerPIDAttacher, error) {
	attacher, ok := p.cgroup.(cgroupclient.ControllerPIDAttacher)
	if !ok {
		return nil, operationError("all", "capability_error",
			errors.New("controller-aware cgroup attach capability is required"))
	}
	return attacher, nil
}

func (p *SystemServicePlugin) controllerSources(
	ctx context.Context,
	attacher cgroupclient.ControllerPIDAttacher,
) ([]controllerSource, []error) {
	if p.cgroup.Version(ctx) == cgroupclient.CgroupVersionV2 {
		cpusetMount, err := attacher.ControllerMount(ctx, cgcommon.CgroupSubsysCPUSet)
		if err != nil {
			return nil, []error{operationError(unifiedControllerName, "mount_error",
				fmt.Errorf("resolve cpuset controller mount: %w", err))}
		}
		// A v2 cgroup has one membership hierarchy even though it exposes both
		// cpu and cpuset controller semantics; scanning it twice would duplicate
		// classification and cgroup.procs writes.
		return []controllerSource{{name: unifiedControllerName, mount: cpusetMount}}, nil
	}

	var sources []controllerSource
	var errs []error
	for _, controller := range []string{cgcommon.CgroupSubsysCPUSet, cgcommon.CgroupSubsysCPU} {
		mount, err := attacher.ControllerMount(ctx, controller)
		if err != nil {
			if !errors.Is(err, cgcommon.ErrControllerMountUnavailable) {
				errs = append(errs, operationError(controller, "mount_error",
					fmt.Errorf("resolve %s controller mount: %w", controller, err)))
			}
			continue
		}
		sources = append(sources, controllerSource{name: controller, mount: mount})
	}
	return sources, errs
}

// listRootMigrationCandidates merges controller-local leader/task membership
// sources by PID. A source's taskOnly state is never inferred from another
// controller, so a leader in one hierarchy cannot weaken a task-only source in
// another hierarchy.
func (p *SystemServicePlugin) listRootMigrationCandidates(sources []controllerSource) ([]migrationCandidate, []error) {
	byPID := make(map[int]migrationCandidate)
	var errs []error
	for _, source := range sources {
		procsPath := filepath.Join(source.mount.Root, "cgroup.procs")
		data, err := p.fs.ReadFile(procsPath)
		if err != nil {
			errs = append(errs, operationError(source.name, "list_error",
				fmt.Errorf("read %s: %w", procsPath, err)))
			continue
		}
		for _, pid := range stableUniquePIDs(parsePIDList(data)) {
			candidate := byPID[pid]
			if candidate.memberships == nil {
				candidate = migrationCandidate{pid: pid, memberships: map[string]candidateMembership{}}
			}
			candidate.memberships[source.name] = candidateMembership{controller: source.name}
			byPID[pid] = candidate
		}
		tasks, err := p.fs.ReadFile(rootCgroupTasksPath(procsPath))
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				errs = append(errs, operationError(source.name, "list_error",
					fmt.Errorf("read %s: %w", rootCgroupTasksPath(procsPath), err)))
			}
			continue
		}
		for _, tid := range stableUniquePIDs(parsePIDList(tasks)) {
			candidate := byPID[tid]
			if candidate.memberships == nil {
				candidate = migrationCandidate{pid: tid, memberships: map[string]candidateMembership{}}
			}
			if _, exists := candidate.memberships[source.name]; !exists {
				candidate.memberships[source.name] = candidateMembership{controller: source.name, taskOnly: true}
			}
			byPID[tid] = candidate
		}
	}
	return sortedCandidates(byPID), errs
}

func candidateControllerNeeds(candidates []migrationCandidate) (needsCPUSet, needsCPU bool) {
	for _, candidate := range candidates {
		for controller := range candidate.memberships {
			switch controller {
			case cgcommon.CgroupSubsysCPUSet, unifiedControllerName:
				needsCPUSet = true
			case cgcommon.CgroupSubsysCPU:
				needsCPU = true
			}
		}
	}
	return needsCPUSet, needsCPU
}

func (p *SystemServicePlugin) pinProcess(pid int) (io.Closer, error) {
	pinPID := p.pinPID
	if pinPID == nil {
		pinPID = openPIDIdentity
	}
	return pinPID(pid)
}

// listTargetCgroupCandidates merges controller-local target memberships. All
// target reads use the descriptor-relative controller client so cpuset, cpu,
// and unified paths share the same traversal and symlink-safety contract.
func (p *SystemServicePlugin) listTargetCgroupCandidates(
	ctx context.Context,
	sources []controllerSource,
	attacher cgroupclient.ControllerPIDAttacher,
) ([]migrationCandidate, []error) {
	byPID := make(map[int]migrationCandidate)
	var errs []error
	for _, source := range sources {
		subsystem := source.subsystem()
		data, err := attacher.ReadControllerFile(ctx, subsystem, p.targetRel, "cgroup.procs")
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			errs = append(errs, operationError(source.name, "list_error",
				fmt.Errorf("read %s target cgroup.procs @ %s: %w", source.name, p.targetRel, err)))
			continue
		}
		for _, pid := range stableUniquePIDs(parsePIDList(data)) {
			candidate := byPID[pid]
			if candidate.memberships == nil {
				candidate = migrationCandidate{pid: pid, memberships: map[string]candidateMembership{}}
			}
			candidate.memberships[source.name] = candidateMembership{controller: source.name}
			byPID[pid] = candidate
		}
		tasks, tasksErr := attacher.ReadControllerFile(ctx, subsystem, p.targetRel, "tasks")
		if tasksErr != nil {
			if !errors.Is(tasksErr, os.ErrNotExist) {
				errs = append(errs, operationError(source.name, "list_error",
					fmt.Errorf("read %s target tasks @ %s: %w", source.name, p.targetRel, tasksErr)))
			}
			continue
		}
		for _, tid := range stableUniquePIDs(parsePIDList(tasks)) {
			candidate := byPID[tid]
			if candidate.memberships == nil {
				candidate = migrationCandidate{pid: tid, memberships: map[string]candidateMembership{}}
			}
			if _, exists := candidate.memberships[source.name]; !exists {
				candidate.memberships[source.name] = candidateMembership{controller: source.name, taskOnly: true}
			}
			byPID[tid] = candidate
		}
	}
	return sortedCandidates(byPID), errs
}

func sortedCandidates(byPID map[int]migrationCandidate) []migrationCandidate {
	pids := make([]int, 0, len(byPID))
	for pid := range byPID {
		pids = append(pids, pid)
	}
	sort.Ints(pids)
	out := make([]migrationCandidate, 0, len(pids))
	for _, pid := range pids {
		out = append(out, byPID[pid])
	}
	return out
}

func rootCgroupTasksPath(cgroupProcsPath string) string {
	return strings.TrimSuffix(cgroupProcsPath, "cgroup.procs") + "tasks"
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

func stableUniquePIDs(pids []int) []int {
	if len(pids) == 0 {
		return nil
	}
	sort.Ints(pids)
	out := pids[:0]
	last := 0
	for i, pid := range pids {
		if i > 0 && pid == last {
			continue
		}
		out = append(out, pid)
		last = pid
	}
	return out
}
