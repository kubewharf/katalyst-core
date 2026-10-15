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

package dynamicpolicy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/gogo/protobuf/proto"

	cpuconsts "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/consts"
	checkpointutils "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/utils"
	advisorapi "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/cpuadvisor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	cpusetutil "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/util"
	"github.com/kubewharf/katalyst-core/pkg/config"
	dynamicconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	bulkheadconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/qrm/bulkhead"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

const (
	cpuSetAdjustmentRetryMaxAttempts    = 4
	cpuSetAdjustmentRetryInitialBackoff = 10 * time.Millisecond
	cpuSetAdjustmentRetryMaxBackoff     = 200 * time.Millisecond
	advisorPostCommitCheckpointName     = "cpu_advisor_post_commit_target"
	advisorPostCommitStagingName        = advisorPostCommitCheckpointName + ".staging"
	advisorPostCommitCheckpointVersion  = 2
	advisorPostCommitWALV2Magic         = "\x00KATALYST_CPU_ADVISOR_WAL_V2\x00"
	// advisorPostCommitWaitReevalInterval bounds how long a caller blocked behind
	// a pending post-commit fence may sleep without re-checking whether the
	// target aged into the safe writer-fence handoff. It must stay well below
	// advisorPostCommitStuckThreshold so the handoff is observed promptly rather
	// than only on the next driver-emitted change notification.
	advisorPostCommitWaitReevalInterval = 500 * time.Millisecond
)

type advisorPostCommitPhase string

const (
	advisorPostCommitPhasePrepared           advisorPostCommitPhase = "prepared"
	advisorPostCommitPhasePublished          advisorPostCommitPhase = "published"
	advisorPostCommitPhasePhysicalApply      advisorPostCommitPhase = "physical_apply"
	advisorPostCommitPhaseCanonicalReconcile advisorPostCommitPhase = "canonical_reconcile"
	advisorPostCommitPhaseAppliedMarker      advisorPostCommitPhase = "applied_marker"
	advisorPostCommitPhaseCleanup            advisorPostCommitPhase = "cleanup"
)

type advisorPostCommitTarget struct {
	preCommitRevision             uint64
	prepared                      bool
	abortPending                  bool
	publicationPending            bool
	applyMarkerPending            bool
	cleanupPending                bool
	writerFenceReleased           bool
	previousTarget                *advisorPostCommitTarget
	applied                       bool
	checkpointVersion             int
	revision                      uint64
	response                      *advisorapi.ListAndWatchResponse
	migrationCheckpointTransition steadyFakeNUMAMigrationCheckpointTransition
	phase                         advisorPostCommitPhase
	createdAt                     time.Time
	lastProgressAt                time.Time
	progressGeneration            uint64
}

type advisorPostCommitProgress struct {
	target         *advisorPostCommitTarget
	revision       uint64
	phase          advisorPostCommitPhase
	createdAt      time.Time
	lastProgressAt time.Time
	generation     uint64
	changed        <-chan struct{}
}

type advisorPostCommitTargetContextKey struct{}

func cpuSetAdjustmentExecutionLeaseFromContext(
	ctx context.Context,
	p *DynamicPolicy,
) *cpuSetAdjustmentExecutionLease {
	lease, _ := ctx.Value(cpuSetAdjustmentExecutionLeaseContextKey{}).(*cpuSetAdjustmentExecutionLease)
	if !lease.isActiveFor(p, p.adjustment.cpuSetAdjustmentExecution) {
		return nil
	}
	return lease
}

type advisorPostCommitCheckpoint struct {
	Version                       int                                      `json:"version,omitempty"`
	PreCommitRevision             *uint64                                  `json:"pre_commit_revision,omitempty"`
	Revision                      uint64                                   `json:"revision"`
	Response                      []byte                                   `json:"response"`
	MigrationCheckpointTransition *advisorMigrationCheckpointTransitionWAL `json:"migration_checkpoint_transition,omitempty"`
	Applied                       bool                                     `json:"applied,omitempty"`
	Checksum                      string                                   `json:"checksum,omitempty"`
}

type advisorMigrationCheckpointTransitionWAL struct {
	Kind             steadyFakeNUMAMigrationCheckpointTransitionKind `json:"kind"`
	ConstraintDigest string                                          `json:"constraint_digest,omitempty"`
	TargetCPUs       []int                                           `json:"target_cpus,omitempty"`
}

func advisorMigrationCheckpointTransitionToWAL(
	transition steadyFakeNUMAMigrationCheckpointTransition,
	topology *machine.CPUTopology,
) (*advisorMigrationCheckpointTransitionWAL, error) {
	switch transition.kind {
	case steadyFakeNUMAMigrationCheckpointKeep:
		return nil, nil
	case steadyFakeNUMAMigrationCheckpointRemove:
		return &advisorMigrationCheckpointTransitionWAL{
			Kind: steadyFakeNUMAMigrationCheckpointRemove,
		}, nil
	case steadyFakeNUMAMigrationCheckpointReplace:
		if transition.target == nil || transition.target.constraintDigest == "" {
			return nil, fmt.Errorf("invalid empty advisor migration checkpoint replacement")
		}
		if err := validateAdvisorMigrationCheckpointTarget(
			transition.target.target, topology); err != nil {
			return nil, err
		}
		return &advisorMigrationCheckpointTransitionWAL{
			Kind:             steadyFakeNUMAMigrationCheckpointReplace,
			ConstraintDigest: transition.target.constraintDigest,
			TargetCPUs:       transition.target.target.ToSliceInt(),
		}, nil
	default:
		return nil, fmt.Errorf(
			"invalid advisor migration checkpoint transition %d", transition.kind)
	}
}

func advisorMigrationCheckpointTransitionFromWAL(
	transition *advisorMigrationCheckpointTransitionWAL,
	topology *machine.CPUTopology,
) (steadyFakeNUMAMigrationCheckpointTransition, error) {
	keep := steadyFakeNUMAMigrationCheckpointTransition{
		kind: steadyFakeNUMAMigrationCheckpointKeep,
	}
	if transition == nil {
		return keep, nil
	}
	switch transition.Kind {
	case steadyFakeNUMAMigrationCheckpointRemove:
		return steadyFakeNUMAMigrationCheckpointTransition{
			kind: steadyFakeNUMAMigrationCheckpointRemove,
		}, nil
	case steadyFakeNUMAMigrationCheckpointReplace:
		if transition.ConstraintDigest == "" {
			return keep, fmt.Errorf(
				"advisor migration checkpoint replacement has empty constraint digest")
		}
		target := machine.NewCPUSet(transition.TargetCPUs...)
		if target.Size() != len(transition.TargetCPUs) {
			return keep, fmt.Errorf(
				"advisor migration checkpoint replacement contains duplicate CPUs")
		}
		if err := validateAdvisorMigrationCheckpointTarget(target, topology); err != nil {
			return keep, err
		}
		return steadyFakeNUMAMigrationCheckpointTransition{
			kind: steadyFakeNUMAMigrationCheckpointReplace,
			target: &steadyFakeNUMAMigrationIntent{
				constraintDigest: transition.ConstraintDigest,
				target:           target,
			},
		}, nil
	default:
		return keep, fmt.Errorf(
			"invalid advisor migration checkpoint transition %d", transition.Kind)
	}
}

func validateAdvisorMigrationCheckpointTarget(
	target machine.CPUSet,
	topology *machine.CPUTopology,
) error {
	if topology == nil {
		return fmt.Errorf("advisor migration checkpoint validation requires CPU topology")
	}
	if outside := target.Difference(topology.CPUDetails.CPUs()); !outside.IsEmpty() {
		return fmt.Errorf(
			"advisor migration checkpoint target contains CPUs outside topology: %s",
			outside.String())
	}
	if err := assertCoreAligned(target, topology); err != nil {
		return fmt.Errorf("advisor migration checkpoint target is not core aligned: %w", err)
	}
	return nil
}

func advisorPostCommitCheckpointChecksum(
	version int,
	preCommitRevision *uint64,
	revision uint64,
	response []byte,
	transition *advisorMigrationCheckpointTransitionWAL,
	applied ...bool,
) string {
	hash := sha256.New()
	_, _ = fmt.Fprintf(hash, "%d\n%d\n", version, revision)
	if preCommitRevision != nil {
		_, _ = fmt.Fprintf(hash, "%d\n", *preCommitRevision)
	}
	_, _ = hash.Write(response)
	_, _ = hash.Write([]byte{'\n'})
	transitionBytes, _ := json.Marshal(transition)
	_, _ = hash.Write(transitionBytes)
	if len(applied) > 0 && applied[0] {
		_, _ = hash.Write([]byte("\napplied"))
	}
	return hex.EncodeToString(hash.Sum(nil))
}

// advisorPostCommitCheckpointCodec adapts the advisor post-commit write-ahead
// log record to the generic checkpointutils.CheckpointCodec contract. It keeps
// the exact historical JSON envelope, WAL v2 magic, sha256 checksum digest, and
// the v0 fallback decoding so checkpoints written by the legacy hand-rolled
// writer remain readable (and vice versa) after the file I/O was replaced by the
// shared FileCheckpointStore. Payload semantics (version gating, checksum
// verification, topology-bounded transition validation, revision-CAS) stay here;
// the store owns only the crash-safe file protocol.
type advisorPostCommitCheckpointCodec struct {
	version           int
	preCommitRevision *uint64
	revision          uint64
	responseBytes     []byte // raw protobuf, WITHOUT the WAL v2 magic (the checksum input)
	response          *advisorapi.ListAndWatchResponse
	transition        *advisorMigrationCheckpointTransitionWAL
	domainTransition  steadyFakeNUMAMigrationCheckpointTransition
	applied           bool
	checksum          string

	// topology is transient: never serialized, but required to re-validate the
	// migration checkpoint target domain bounds on Unmarshal, exactly as the
	// legacy restore did.
	topology *machine.CPUTopology
}

func (c *advisorPostCommitCheckpointCodec) Marshal() ([]byte, error) {
	c.version = advisorPostCommitCheckpointVersion
	record := advisorPostCommitCheckpoint{
		Version:                       c.version,
		PreCommitRevision:             c.preCommitRevision,
		Revision:                      c.revision,
		Response:                      append([]byte(advisorPostCommitWALV2Magic), c.responseBytes...),
		MigrationCheckpointTransition: c.transition,
		Applied:                       c.applied,
	}
	record.Checksum = advisorPostCommitCheckpointChecksum(
		c.version, c.preCommitRevision, c.revision, c.responseBytes, c.transition, c.applied)
	data, err := json.Marshal(record)
	if err != nil {
		return nil, fmt.Errorf("marshal advisor checkpoint: %w", err)
	}
	return data, nil
}

func (c *advisorPostCommitCheckpointCodec) Unmarshal(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var record advisorPostCommitCheckpoint
	if err := decoder.Decode(&record); err != nil {
		return fmt.Errorf("decode advisor checkpoint: %w", err)
	}
	if err := ensureSteadyFakeNUMACheckpointEOF(decoder); err != nil {
		return fmt.Errorf("advisor checkpoint trailing data: %w", err)
	}
	responseBytes := record.Response
	switch record.Version {
	case 0:
		if record.Checksum != "" || record.MigrationCheckpointTransition != nil ||
			record.Applied ||
			bytes.HasPrefix(responseBytes, []byte(advisorPostCommitWALV2Magic)) {
			return fmt.Errorf("advisor checkpoint version is missing")
		}
	case advisorPostCommitCheckpointVersion:
		if record.Checksum == "" {
			return fmt.Errorf("advisor checkpoint checksum is missing")
		}
		if record.PreCommitRevision == nil {
			return fmt.Errorf("advisor checkpoint pre-commit revision is missing")
		}
		if !bytes.HasPrefix(responseBytes, []byte(advisorPostCommitWALV2Magic)) {
			return fmt.Errorf("advisor WAL V2 magic is missing")
		}
		responseBytes = responseBytes[len(advisorPostCommitWALV2Magic):]
		if record.Checksum != advisorPostCommitCheckpointChecksum(
			record.Version,
			record.PreCommitRevision,
			record.Revision,
			responseBytes,
			record.MigrationCheckpointTransition,
			record.Applied,
		) {
			return fmt.Errorf("advisor checkpoint checksum mismatch")
		}
	default:
		return fmt.Errorf(
			"unsupported advisor checkpoint version %d", record.Version)
	}
	response := &advisorapi.ListAndWatchResponse{}
	if err := proto.Unmarshal(responseBytes, response); err != nil {
		return err
	}
	transition, err := advisorMigrationCheckpointTransitionFromWAL(
		record.MigrationCheckpointTransition, c.topology)
	if err != nil {
		return err
	}
	if record.Version == advisorPostCommitCheckpointVersion {
		postCommitRevision, err := nextAdvisorRevision(*record.PreCommitRevision)
		if err != nil {
			return fmt.Errorf("invalid advisor checkpoint revision transition: %w", err)
		}
		if record.Revision != postCommitRevision {
			return fmt.Errorf(
				"invalid advisor checkpoint revision transition: pre=%d post=%d",
				*record.PreCommitRevision, record.Revision)
		}
	}
	c.version = record.Version
	c.preCommitRevision = record.PreCommitRevision
	c.revision = record.Revision
	c.responseBytes = responseBytes
	c.response = response
	c.transition = record.MigrationCheckpointTransition
	c.domainTransition = transition
	c.applied = record.Applied
	c.checksum = record.Checksum
	return nil
}

func (c *advisorPostCommitCheckpointCodec) New() checkpointutils.CheckpointCodec {
	return &advisorPostCommitCheckpointCodec{topology: c.topology}
}

// toTarget rebuilds the in-memory post-commit target from a decoded codec,
// mirroring the legacy loadAdvisorPostCommitTarget reconstruction.
func (c *advisorPostCommitCheckpointCodec) toTarget() *advisorPostCommitTarget {
	target := &advisorPostCommitTarget{
		checkpointVersion:             c.version,
		revision:                      c.revision,
		response:                      c.response,
		migrationCheckpointTransition: c.domainTransition,
		applied:                       c.applied,
	}
	phase := advisorPostCommitPhasePublished
	if c.applied {
		phase = advisorPostCommitPhaseCleanup
	}
	initializeAdvisorPostCommitProgress(target, phase)
	if c.preCommitRevision != nil {
		target.preCommitRevision = *c.preCommitRevision
	}
	return target
}

func cpuSetAdjustmentHandlerTimeout(conf *config.Configuration) time.Duration {
	if conf == nil || conf.CPUQRMPluginConfig == nil {
		return bulkheadconfig.TopologyHandlerTimeout(nil)
	}
	return bulkheadconfig.TopologyHandlerTimeout(conf.CPUQRMPluginConfig.BulkheadConfiguration)
}

func (p *DynamicPolicy) RegisterCPUSetAdjustmentHandler(name string, handler cpusetutil.CPUSetAdjustmentHandler) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("cpuset adjustment handler name is empty")
	}
	if handler == nil {
		return fmt.Errorf("cpuset adjustment handler %q is nil", name)
	}
	if p.adjustment.cpuSetAdjustmentHandlers == nil {
		p.adjustment.cpuSetAdjustmentHandlers = map[string]cpusetutil.CPUSetAdjustmentHandler{}
	}
	if _, ok := p.adjustment.cpuSetAdjustmentHandlers[name]; ok {
		return fmt.Errorf("cpuset adjustment handler %q already registered", name)
	}
	p.adjustment.cpuSetAdjustmentHandlers[name] = handler
	return nil
}

type cpuSetAdjustmentStateSnapshot struct {
	machineState     state.NUMANodeMap
	numaHeadroom     map[int]float64
	podEntries       state.PodEntries
	allowOverlap     bool
	disableDedicated bool
	// revision is the sole owner of snapshot freshness; state payloads are views, not identities.
	revision uint64
}

func newCPUSetAdjustmentStateSnapshot(source state.ReadonlyState) *cpuSetAdjustmentStateSnapshot {
	if source == nil {
		return nil
	}
	// PodEntries, machine state and revision MUST come from one atomic generation:
	// calling GetPodEntries/GetMachineState/GetRevision independently could mix
	// entries from one revision with machine state or a revision from another.
	// GetAdvisorStateSnapshot captures all three under the same read lock. Headroom
	// and the overlap flags are monotonic policy knobs without their own revision,
	// so they are read alongside the snapshot.
	podEntries, machineState, revision := source.GetAdvisorStateSnapshot()
	snapshot := &cpuSetAdjustmentStateSnapshot{
		machineState:     machineState,
		numaHeadroom:     source.GetNUMAHeadroom(),
		podEntries:       podEntries,
		allowOverlap:     source.GetAllowSharedCoresOverlapReclaimedCores(),
		disableDedicated: source.GetDisableDedicatedCoresOverlapReclaimedCores(),
		revision:         revision,
	}
	return snapshot
}

func (s *cpuSetAdjustmentStateSnapshot) matches(source state.ReadonlyState) bool {
	if s == nil || source == nil {
		return s == nil && source == nil
	}
	return s.revision == source.GetRevision()
}

func (s *cpuSetAdjustmentStateSnapshot) GetMachineState() state.NUMANodeMap {
	return s.machineState.Clone()
}

func (s *cpuSetAdjustmentStateSnapshot) GetNUMAHeadroom() map[int]float64 {
	out := make(map[int]float64, len(s.numaHeadroom))
	for numaID, headroom := range s.numaHeadroom {
		out[numaID] = headroom
	}
	return out
}

func (s *cpuSetAdjustmentStateSnapshot) GetPodEntries() state.PodEntries {
	return s.podEntries.Clone()
}

func (s *cpuSetAdjustmentStateSnapshot) GetAdvisorStateSnapshot() (state.PodEntries, state.NUMANodeMap, uint64) {
	return s.podEntries.Clone(), s.machineState.Clone(), s.revision
}

func (s *cpuSetAdjustmentStateSnapshot) GetAllocationInfo(podUID, containerName string) *state.AllocationInfo {
	if allocationInfo := s.podEntries[podUID][containerName]; allocationInfo != nil {
		return allocationInfo.Clone()
	}
	return nil
}

func (s *cpuSetAdjustmentStateSnapshot) GetAllowSharedCoresOverlapReclaimedCores() bool {
	return s.allowOverlap
}

func (s *cpuSetAdjustmentStateSnapshot) GetDisableDedicatedCoresOverlapReclaimedCores() bool {
	return s.disableDedicated
}

func (s *cpuSetAdjustmentStateSnapshot) GetRevision() uint64 {
	return s.revision
}

func (p *DynamicPolicy) runCPUSetAdjustmentHandlers(ctx context.Context, modes ...cpusetutil.CPUSetAdjustmentMode) error {
	if p.runCPUSetAdjustmentHandlersHook != nil {
		return p.runCPUSetAdjustmentHandlersHook(ctx, modes...)
	}
	if len(p.adjustment.cpuSetAdjustmentHandlers) == 0 {
		return nil
	}
	reconcileTarget, _ := ctx.Value(advisorPostCommitTargetContextKey{}).(*advisorPostCommitTarget)
	mode := cpusetutil.CPUSetAdjustmentModePeriodic
	if len(modes) > 0 {
		mode = modes[0].OrFullDefault()
	}

	// Serialize complete adjustment rounds without retaining the policy lock.
	// Waiting before taking the immutable snapshot ensures a queued round plans
	// from state left by the preceding round and its caller-side error handling.
	executionLease := cpuSetAdjustmentExecutionLeaseFromContext(ctx, p)
	if executionLease == nil {
		var err error
		executionLease, err = p.acquireCPUSetAdjustmentExecutionLocked(ctx)
		if err != nil {
			return err
		}
		defer executionLease.release()
		ctx = context.WithValue(ctx, cpuSetAdjustmentExecutionLeaseContextKey{}, executionLease)
	}

	for {
		var topology *machine.CPUTopology
		if p.machine.machineInfo != nil {
			topology = p.machine.machineInfo.CPUTopology
		}
		var dynamicConf *dynamicconfig.Configuration
		if p.config.dynamicConfig != nil {
			dynamicConf = p.config.dynamicConfig.GetDynamicConfiguration()
		}
		stateRevision := uint64(0)
		if p.state != nil {
			stateRevision = p.state.GetRevision()
		}
		stateSnapshot := newCPUSetAdjustmentStateSnapshot(p.state)
		commitOverride := &cpusetutil.CPUSetAdjustmentCommitOverride{}
		// Consume the synchronously staged admission projection hint exactly once
		// for this round. A fresh deep copy avoids aliasing the outliving staged
		// map; clearing it here means retry/periodic rounds never inherit a stale
		// hint and instead fall back to the manager live-state rebuild.
		p.adjustment.cpuSetAdjustmentRetryMu.Lock()
		commitOverride.PendingSharedProjection = checkpointutils.ClonePendingSharedProjection(p.adjustment.stagedPendingSharedProjection)
		p.adjustment.stagedPendingSharedProjection = nil
		p.adjustment.cpuSetAdjustmentRetryMu.Unlock()
		handlerCtx := cpusetutil.CPUSetAdjustmentHandlerCtx{
			CoreConf:                  p.config.conf,
			DynamicConf:               dynamicConf,
			Emitter:                   p.emitter.emitter,
			MetaServer:                p.meta.metaServer,
			State:                     stateSnapshot,
			Topology:                  topology,
			ReservedCPUs:              p.config.reservedCPUs.Clone(),
			ReservedReclaimedCPUs:     p.reclaim.reservedReclaimedCPUSet.Clone(),
			ReservedReclaimedCPUsSize: p.reclaim.reservedReclaimedCPUsSize,
			Mode:                      mode,
			ScheduleFullRetry: func(reason cpusetutil.CPUSetAdjustmentRetryReason) {
				p.scheduleCPUSetAdjustmentRetry(reason)
			},
			CommitOverride: commitOverride,
		}
		p.adjustment.cpuSetAdjustmentGeneration++
		handlerCtx.Generation = p.adjustment.cpuSetAdjustmentGeneration
		roundInvalidated := false
		handlerCtx.CommitIfGenerationCurrent = func(generation uint64, commit func()) bool {
			p.Lock()
			defer p.Unlock()
			var currentDynamicConf *dynamicconfig.Configuration
			if p.config.dynamicConfig != nil {
				currentDynamicConf = p.config.dynamicConfig.GetDynamicConfiguration()
			}
			if generation != p.adjustment.cpuSetAdjustmentGeneration ||
				dynamicConf != currentDynamicConf ||
				!stateSnapshot.matches(p.state) {
				roundInvalidated = true
				return false
			}
			commit()
			return true
		}

		names := make([]string, 0, len(p.adjustment.cpuSetAdjustmentHandlers))
		handlers := make(map[string]cpusetutil.CPUSetAdjustmentHandler, len(p.adjustment.cpuSetAdjustmentHandlers))
		for name := range p.adjustment.cpuSetAdjustmentHandlers {
			names = append(names, name)
			handlers[name] = p.adjustment.cpuSetAdjustmentHandlers[name]
		}
		sort.Strings(names)

		p.Unlock()
		var roundErr error
		for _, name := range names {
			if err := handlers[name](ctx, handlerCtx); err != nil {
				roundErr = fmt.Errorf("run cpuset adjustment handler %q: %w", name, err)
				break
			}
		}
		p.Lock()
		if roundInvalidated {
			if ctx.Err() == nil {
				continue
			}
			p.scheduleCPUSetAdjustmentRetry(cpusetutil.RetryReasonStaleState)
			if roundErr != nil {
				return fmt.Errorf("%v; scheduled latest cpuset adjustment after canceled stale round: %w", roundErr, ctx.Err())
			}
			return ctx.Err()
		}
		reclaimOverrideTrimmed := false
		if roundErr == nil && commitOverride.Source != "" {
			appliedReclaim := commitOverride.ReclaimEffective.Clone()
			alignedReclaim, err := p.coreAlignedReclaimOverride(
				commitOverride.ReclaimEffective,
				p.state.GetDisableDedicatedCoresOverlapReclaimedCores(),
			)
			if err != nil {
				roundErr = fmt.Errorf("align reclaim cpuset adjustment override: %w", err)
			} else {
				commitOverride.ReclaimEffective = alignedReclaim
				reclaimOverrideTrimmed = !alignedReclaim.Equals(appliedReclaim)
			}
		}
		if roundErr == nil && commitOverride.Source != "" {
			newEntries := p.state.GetPodEntries()
			if err := p.syncReclaimPoolWithAdjustmentCommitOverride(newEntries, commitOverride); err != nil {
				roundErr = fmt.Errorf("sync reclaim pool from cpuset adjustment override: %w", err)
			} else if isRampUpReclaimHardPartitionEnabledWithConfig(dynamicConf) && newEntries.HasActiveRampUp() {
				// The override may have moved the reclaim pool away from the reclaim
				// the ramp-up business was last laid out against: the bulkhead
				// verified reclaim is derived without the transient ramp-up
				// side-channel, so it can land on top of existing business. Re-lay
				// ramp-up shared business off THIS final reclaim pool before the
				// precommit mutual-exclusion check, mirroring the synchronous
				// advisor apply path. A reclaim that fully covers business fails
				// closed here; this never bypasses the validator.
				if err := p.rematerializeRampUpSharedAgainstFinalReclaim(newEntries); err != nil {
					roundErr = fmt.Errorf("rematerialize ramp-up shared against async reclaim override: %w", err)
				}
			}
			if roundErr == nil {
				_, _, err := p.commitPendingCPUPartitionForAdvisorTarget(pendingCPUPartition{
					expectedRevision: stateRevision,
					entries:          newEntries,
					allowOverlap:     p.state.GetAllowSharedCoresOverlapReclaimedCores(),
					disableDedicated: p.state.GetDisableDedicatedCoresOverlapReclaimedCores(),
					persist:          true,
					source:           "cpuset override",
					validate: func(entries state.PodEntries, ms state.NUMANodeMap, overlap, dedicated bool) error {
						return p.validatePendingAdvisorPartitionViewWithRampUpExclusionWithDynamicConfig(entries, ms, overlap, dedicated, dynamicConf)
					},
					dynamicConfig:             dynamicConf,
					requireCoreAlignedReclaim: p.state.GetDisableDedicatedCoresOverlapReclaimedCores(),
				}, reconcileTarget)
				if err != nil {
					if errors.Is(err, state.ErrStaleStateRevision) {
						p.scheduleCPUSetAdjustmentRetry(cpusetutil.RetryReasonStaleState)
					}
					roundErr = fmt.Errorf("commit cpuset adjustment override: %w", err)
				} else if reclaimOverrideTrimmed {
					p.scheduleCPUSetAdjustmentRetry(cpusetutil.RetryReasonRecoveryCommit)
				}
			}
		}
		if roundErr == nil && mode == cpusetutil.CPUSetAdjustmentModePeriodic &&
			!p.hasAnyPendingAdvisorPostCommitTarget() {
			p.adjustment.cpuSetAdjustmentRetryMu.Lock()
			if !p.adjustment.cpuSetAdjustmentRetryQueued && !p.adjustment.cpuSetAdjustmentRetryAgain {
				p.adjustment.cpuSetAdjustmentRetryDirty = false
				p.adjustment.cpuSetAdjustmentRetryReasons = nil
			}
			p.adjustment.cpuSetAdjustmentRetryMu.Unlock()
		}
		return roundErr
	}
}

func cloneAdvisorPostCommitTarget(
	resp *advisorapi.ListAndWatchResponse,
	revision uint64,
	transitions ...steadyFakeNUMAMigrationCheckpointTransition,
) *advisorPostCommitTarget {
	cloned := &advisorapi.ListAndWatchResponse{}
	if resp != nil {
		cloned = proto.Clone(resp).(*advisorapi.ListAndWatchResponse)
	}
	transition := steadyFakeNUMAMigrationCheckpointTransition{
		kind: steadyFakeNUMAMigrationCheckpointKeep,
	}
	if len(transitions) > 0 {
		transition = cloneSteadyFakeNUMAMigrationCheckpointTransition(transitions[0])
	}
	target := &advisorPostCommitTarget{
		checkpointVersion:             advisorPostCommitCheckpointVersion,
		revision:                      revision,
		response:                      cloned,
		migrationCheckpointTransition: transition,
	}
	initializeAdvisorPostCommitProgress(target, advisorPostCommitPhasePrepared)
	return target
}

func initializeAdvisorPostCommitProgress(target *advisorPostCommitTarget, phase advisorPostCommitPhase) {
	if target == nil {
		return
	}
	now := time.Now()
	target.phase = phase
	target.createdAt = now
	target.lastProgressAt = now
	target.progressGeneration = 1
}

func nextAdvisorRevision(revision uint64) (uint64, error) {
	if revision == math.MaxUint64 {
		return 0, fmt.Errorf("state revision overflow at %d", revision)
	}
	return revision + 1, nil
}

func (p *DynamicPolicy) beginPreparedAdvisorPostCommitTarget(
	target *advisorPostCommitTarget,
	preCommitRevision uint64,
) *advisorPostCommitTarget {
	p.adjustment.cpuSetAdjustmentRetryMu.Lock()
	defer p.adjustment.cpuSetAdjustmentRetryMu.Unlock()
	previous := p.adjustment.advisorPostCommitTarget
	target.preCommitRevision = preCommitRevision
	target.prepared = true
	p.setAdvisorPostCommitTargetLocked(target)
	p.recordAdvisorPostCommitProgressLocked(target, advisorPostCommitPhasePrepared)
	return previous
}

func (p *DynamicPolicy) finishPreparedAdvisorPostCommitTarget(target *advisorPostCommitTarget) {
	p.adjustment.cpuSetAdjustmentRetryMu.Lock()
	defer p.adjustment.cpuSetAdjustmentRetryMu.Unlock()
	if p.adjustment.advisorPostCommitTarget == target {
		target.prepared = false
	}
}

func (p *DynamicPolicy) rollbackPreparedAdvisorPostCommitTarget(
	target, previous *advisorPostCommitTarget,
) {
	p.adjustment.cpuSetAdjustmentRetryMu.Lock()
	defer p.adjustment.cpuSetAdjustmentRetryMu.Unlock()
	if p.adjustment.advisorPostCommitTarget == target {
		p.setAdvisorPostCommitTargetLocked(previous)
	}
	for permit, permitTarget := range p.adjustment.advisorStateWritePermits {
		if permitTarget == target {
			delete(p.adjustment.advisorStateWritePermits, permit)
		}
	}
}

func (p *DynamicPolicy) markAdvisorPostCommitAbortPending(
	target, previous *advisorPostCommitTarget,
) {
	p.adjustment.cpuSetAdjustmentRetryMu.Lock()
	if p.adjustment.advisorPostCommitTarget == target {
		target.abortPending = true
		target.previousTarget = previous
	}
	p.adjustment.cpuSetAdjustmentRetryMu.Unlock()
	p.scheduleCPUSetAdjustmentRetry(cpusetutil.RetryReasonApplyFailed)
}

func (p *DynamicPolicy) markAdvisorPostCommitPublicationPending(target *advisorPostCommitTarget) {
	p.adjustment.cpuSetAdjustmentRetryMu.Lock()
	if p.adjustment.advisorPostCommitTarget == target {
		target.publicationPending = true
	}
	p.adjustment.cpuSetAdjustmentRetryMu.Unlock()
	p.markAdvisorApplyFailed(target.revision)
}

func (p *DynamicPolicy) commitAdvisorResponseWithWriteAhead(
	resp *advisorapi.ListAndWatchResponse,
	preCommitRevision uint64,
	commitDesired func(*advisorPostCommitTarget) error,
) (*advisorPostCommitTarget, error) {
	return p.commitAdvisorResponseWithWriteAheadTransition(
		resp,
		steadyFakeNUMAMigrationCheckpointTransition{
			kind: steadyFakeNUMAMigrationCheckpointKeep,
		},
		preCommitRevision,
		commitDesired,
	)
}

// commitAdvisorResponseWithWriteAheadTransition installs the writer fence and
// durably stages the post-commit target before advancing canonical state. The
// revision-CAS commit is therefore recoverable from the WAL; the fence remains
// until publication, physical apply, and durable cleanup have completed.
func (p *DynamicPolicy) commitAdvisorResponseWithWriteAheadTransition(
	resp *advisorapi.ListAndWatchResponse,
	transition steadyFakeNUMAMigrationCheckpointTransition,
	preCommitRevision uint64,
	commitDesired func(*advisorPostCommitTarget) error,
) (*advisorPostCommitTarget, error) {
	postCommitRevision, err := nextAdvisorRevision(preCommitRevision)
	if err != nil {
		return nil, err
	}
	target := cloneAdvisorPostCommitTarget(resp, postCommitRevision, transition)
	p.installCPUStateWritePermit()
	previousTarget := p.beginPreparedAdvisorPostCommitTarget(target, preCommitRevision)
	if err := p.storeAdvisorPostCommitTarget(target, p.advisorPostCommitStagingPath()); err != nil {
		if removeErr := p.removeAdvisorPostCommitStaging(); removeErr != nil {
			p.markAdvisorPostCommitAbortPending(target, previousTarget)
			return nil, fmt.Errorf(
				"persist advisor post-commit target: %w; remove incomplete target while retaining writer fence: %v",
				err, removeErr)
		}
		p.rollbackPreparedAdvisorPostCommitTarget(target, previousTarget)
		return nil, fmt.Errorf("persist advisor post-commit target: %w", err)
	}
	if err := commitDesired(target); err != nil {
		if removeErr := p.removeAdvisorPostCommitStaging(); removeErr != nil {
			p.markAdvisorPostCommitAbortPending(target, previousTarget)
			return nil, fmt.Errorf("%w; remove uncommitted advisor target: %v", err, removeErr)
		}
		p.rollbackPreparedAdvisorPostCommitTarget(target, previousTarget)
		return nil, err
	}
	if p.state == nil || p.state.GetRevision() != postCommitRevision {
		actualRevision := uint64(0)
		if p.state != nil {
			actualRevision = p.state.GetRevision()
		}
		if removeErr := p.removeAdvisorPostCommitStaging(); removeErr != nil {
			p.markAdvisorPostCommitAbortPending(target, previousTarget)
			return nil, fmt.Errorf("advisor desired commit revision mismatch: expected=%d actual=%d; remove target: %v",
				postCommitRevision, actualRevision, removeErr)
		}
		p.rollbackPreparedAdvisorPostCommitTarget(target, previousTarget)
		return nil, fmt.Errorf("advisor desired commit revision mismatch: expected=%d actual=%d",
			postCommitRevision, actualRevision)
	}
	// The prepared target already fences every unrelated writer. Once the
	// canonical revision advances, switch it to post-commit reconciliation
	// before promoting the durable staging record.
	p.finishPreparedAdvisorPostCommitTarget(target)
	if err := p.promoteAdvisorPostCommitStaging(); err != nil {
		p.markAdvisorPostCommitPublicationPending(target)
		return nil, fmt.Errorf("promote advisor post-commit target: %w", err)
	}
	p.recordAdvisorPostCommitProgress(target, advisorPostCommitPhasePublished)
	return target, nil
}

func (p *DynamicPolicy) advisorPostCommitCheckpointPath() string {
	if p.advisorPostCommitCheckpointDir == "" {
		return ""
	}
	return filepath.Join(p.advisorPostCommitCheckpointDir, advisorPostCommitCheckpointName)
}

func (p *DynamicPolicy) advisorPostCommitStagingPath() string {
	path := p.advisorPostCommitCheckpointPath()
	if path == "" {
		return ""
	}
	return path + ".staging"
}

// advisorPostCommitCheckpointStore returns the CheckpointStore backing the
// advisor post-commit WAL. It shares the checkpoint directory with the steady
// fake-NUMA migration target, matching the historical file location.
func (p *DynamicPolicy) advisorPostCommitCheckpointStore() checkpointutils.CheckpointStore {
	return checkpointutils.NewFileCheckpointStore(p.advisorPostCommitCheckpointDir)
}

func (p *DynamicPolicy) advisorPostCommitTopology() *machine.CPUTopology {
	if p.machine.machineInfo == nil {
		return nil
	}
	return p.machine.machineInfo.CPUTopology
}

// recoverAdvisorPostCommitName reads and validates the checkpoint stored under
// name (active or staging) via the shared CheckpointStore. A missing checkpoint
// returns (nil, nil); a corrupt, wrong-version, or checksum-mismatched checkpoint
// returns an error that the caller surfaces as a corrupted-slot failure.
func (p *DynamicPolicy) recoverAdvisorPostCommitName(name string) (*advisorPostCommitTarget, error) {
	factory := &advisorPostCommitCheckpointCodec{topology: p.advisorPostCommitTopology()}
	recovered, err := p.advisorPostCommitCheckpointStore().Recover(name, factory)
	if err != nil {
		return nil, err
	}
	if recovered == nil {
		return nil, nil
	}
	codec, ok := recovered.(*advisorPostCommitCheckpointCodec)
	if !ok {
		return nil, fmt.Errorf("unexpected advisor post-commit checkpoint codec type %T", recovered)
	}
	return codec.toTarget(), nil
}

func (p *DynamicPolicy) storeAdvisorPostCommitTarget(target *advisorPostCommitTarget, path string) error {
	if path == "" || target == nil {
		return nil
	}
	response, err := proto.Marshal(target.response)
	if err != nil {
		return fmt.Errorf("marshal advisor response: %w", err)
	}
	topology := p.advisorPostCommitTopology()
	transition, err := advisorMigrationCheckpointTransitionToWAL(
		target.migrationCheckpointTransition, topology)
	if err != nil {
		return err
	}
	preCommitRevision := target.preCommitRevision
	codec := &advisorPostCommitCheckpointCodec{
		preCommitRevision: &preCommitRevision,
		revision:          target.revision,
		responseBytes:     response,
		transition:        transition,
		applied:           target.applied,
		topology:          topology,
	}
	// The temp-file -> fsync -> rename -> dir-fsync crash-safe write protocol
	// lives in FileCheckpointStore.Store. The checkpoint file name is derived
	// from path (active vs. staging), so staging-vs-active selection and the
	// on-disk location are unchanged.
	return p.advisorPostCommitCheckpointStore().Store(filepath.Base(path), codec)
}

func syncAdvisorPostCommitDirectory(path string) error {
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return fmt.Errorf("open advisor checkpoint directory: %w", err)
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil {
		return fmt.Errorf("sync advisor checkpoint directory: %w", err)
	}
	return nil
}

// removeAdvisorPostCommitPath deletes a post-commit WAL path. It is the production
// implementation referenced by the per-instance removePostCommitPathHook when a
// test has not overridden it.
func removeAdvisorPostCommitPath(path string) error {
	if path == "" {
		return nil
	}
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) || errors.Is(err, syscall.ENOTDIR) {
			return nil
		}
		return fmt.Errorf("remove advisor checkpoint: %w", err)
	}
	return syncAdvisorPostCommitDirectory(path)
}

func (p *DynamicPolicy) removeAdvisorPostCommitCheckpoint() error {
	fn := p.removePostCommitPathHook
	if fn == nil {
		fn = removeAdvisorPostCommitPath
	}
	return fn(p.advisorPostCommitCheckpointPath())
}

func (p *DynamicPolicy) removeAdvisorPostCommitStaging() error {
	fn := p.removePostCommitPathHook
	if fn == nil {
		fn = removeAdvisorPostCommitPath
	}
	return fn(p.advisorPostCommitStagingPath())
}

func (p *DynamicPolicy) removeAdvisorPostCommitCheckpoints() error {
	if err := p.removeAdvisorPostCommitStaging(); err != nil {
		return err
	}
	return p.removeAdvisorPostCommitCheckpoint()
}

func (p *DynamicPolicy) promoteAdvisorPostCommitStaging() error {
	if p.advisorPostCommitCheckpointDir == "" {
		return nil
	}
	// Atomic staging -> active rename plus directory fsync, owned by the shared
	// FileCheckpointStore so the crash-safe promotion protocol is identical to
	// the steady fake-NUMA checkpoint writer.
	return p.advisorPostCommitCheckpointStore().Promote(
		advisorPostCommitStagingName, advisorPostCommitCheckpointName)
}

func advisorPostCommitTargetsEqual(left, right *advisorPostCommitTarget) bool {
	if left == nil || right == nil {
		return left == right
	}
	return left.checkpointVersion == right.checkpointVersion &&
		left.preCommitRevision == right.preCommitRevision &&
		left.revision == right.revision &&
		proto.Equal(left.response, right.response) &&
		reflect.DeepEqual(left.migrationCheckpointTransition, right.migrationCheckpointTransition)
}

func (p *DynamicPolicy) ensureAdvisorPostCommitPublished(target *advisorPostCommitTarget) error {
	stagingPath := p.advisorPostCommitStagingPath()
	if stagingPath == "" {
		return nil
	}
	if _, err := os.Stat(stagingPath); err == nil {
		return p.promoteAdvisorPostCommitStaging()
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat advisor staging checkpoint: %w", err)
	}

	activePath := p.advisorPostCommitCheckpointPath()
	var topology *machine.CPUTopology
	if p.machine.machineInfo != nil {
		topology = p.machine.machineInfo.CPUTopology
	}
	active, err := loadAdvisorPostCommitTarget(activePath, topology)
	if err != nil {
		return fmt.Errorf("load promoted advisor checkpoint: %w", err)
	}
	if !advisorPostCommitTargetsEqual(active, target) {
		return fmt.Errorf("promoted advisor checkpoint does not match pending revision %d", target.revision)
	}
	return syncAdvisorPostCommitDirectory(activePath)
}

// loadAdvisorPostCommitTarget reads a WAL checkpoint from an explicit path and
// decodes it through advisorPostCommitCheckpointCodec. It preserves the legacy
// contract of returning the raw os.ReadFile error (so callers can distinguish an
// absent file from a decode failure) and the legacy error strings. The crash-safe
// dual-file restore path goes through CheckpointStore.Recover instead.
func loadAdvisorPostCommitTarget(
	path string,
	topology *machine.CPUTopology,
) (*advisorPostCommitTarget, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	codec := &advisorPostCommitCheckpointCodec{topology: topology}
	if err := codec.Unmarshal(data); err != nil {
		return nil, err
	}
	return codec.toTarget(), nil
}

type advisorPostCommitRecoveryState int

const (
	advisorPostCommitRecoveryCleanup advisorPostCommitRecoveryState = iota
	advisorPostCommitRecoveryReplay
	advisorPostCommitRecoveryBeforeCommit
	advisorPostCommitRecoverySuperseded
)

func advisorPostCommitRecoveryForRevision(
	target *advisorPostCommitTarget,
	currentRevision uint64,
) advisorPostCommitRecoveryState {
	if target == nil {
		return advisorPostCommitRecoveryCleanup
	}
	if target.checkpointVersion != advisorPostCommitCheckpointVersion {
		if currentRevision == target.revision {
			return advisorPostCommitRecoveryReplay
		}
		return advisorPostCommitRecoveryCleanup
	}
	if currentRevision == target.revision {
		return advisorPostCommitRecoveryReplay
	}
	if currentRevision == target.preCommitRevision {
		return advisorPostCommitRecoveryBeforeCommit
	}
	if currentRevision > target.revision {
		return advisorPostCommitRecoverySuperseded
	}
	return advisorPostCommitRecoveryCleanup
}

func (p *DynamicPolicy) restoreAdvisorPostCommitTarget() error {
	if p.advisorPostCommitCheckpointDir == "" {
		return nil
	}
	mainRevision := uint64(0)
	if p.state != nil {
		mainRevision = p.state.GetRevision()
	}
	// Read both slots through the shared CheckpointStore. Recover returns
	// (nil, nil) for a missing file, so a non-nil error now always means a
	// corrupt or unreadable slot (the legacy os.IsNotExist distinction is
	// absorbed by the store). The four-way recovery decision below is pure
	// revision-CAS business logic and is unchanged.
	active, activeErr := p.recoverAdvisorPostCommitName(advisorPostCommitCheckpointName)
	staging, stagingErr := p.recoverAdvisorPostCommitName(advisorPostCommitStagingName)
	activeRecovery := advisorPostCommitRecoveryForRevision(active, mainRevision)
	stagingRecovery := advisorPostCommitRecoveryForRevision(staging, mainRevision)

	var selected *advisorPostCommitTarget
	if stagingErr == nil && stagingRecovery == advisorPostCommitRecoveryReplay {
		selected = staging
		if err := p.promoteAdvisorPostCommitStaging(); err != nil {
			return err
		}
	} else if activeErr == nil && activeRecovery == advisorPostCommitRecoveryReplay {
		selected = active
		if err := p.removeAdvisorPostCommitStaging(); err != nil {
			return err
		}
	} else {
		if activeErr != nil {
			return fmt.Errorf("corrupted active advisor post-commit checkpoint: %w", activeErr)
		}
		if stagingErr != nil {
			return fmt.Errorf("corrupted staging advisor post-commit checkpoint: %w", stagingErr)
		}
		if err := p.removeAdvisorPostCommitCheckpoint(); err != nil {
			return err
		}
		if err := p.removeAdvisorPostCommitStaging(); err != nil {
			return err
		}
		if activeRecovery == advisorPostCommitRecoverySuperseded ||
			stagingRecovery == advisorPostCommitRecoverySuperseded {
			// A newer canonical revision proves that a writer advanced after
			// the old target released its fence. The old response is obsolete,
			// but latest-state cpuset reconciliation remains mandatory.
			p.markCPUSetAdjustmentDirty(cpusetutil.RetryReasonApplyFailed)
		}
		return nil
	}
	p.adjustment.cpuSetAdjustmentRetryMu.Lock()
	p.setAdvisorPostCommitTargetLocked(selected)
	p.adjustment.cpuSetAdjustmentRetryMu.Unlock()
	return nil
}

func (p *DynamicPolicy) prepareAdvisorPostCommitTargetOnStart() error {
	p.adjustment.cpuSetAdjustmentRetryMu.Lock()
	current := p.adjustment.advisorPostCommitTarget
	p.adjustment.cpuSetAdjustmentRetryMu.Unlock()
	if current == nil {
		if err := p.restoreAdvisorPostCommitTarget(); err != nil {
			return err
		}
	}

	stateRevision := uint64(0)
	if p.state != nil {
		stateRevision = p.state.GetRevision()
	}

	p.adjustment.cpuSetAdjustmentRetryMu.Lock()
	defer p.adjustment.cpuSetAdjustmentRetryMu.Unlock()
	if p.adjustment.advisorPostCommitTarget == nil {
		return nil
	}
	if p.state == nil || p.adjustment.advisorPostCommitTarget.revision != stateRevision {
		p.setAdvisorPostCommitTargetLocked(nil)
		return p.removeAdvisorPostCommitCheckpoints()
	}
	p.adjustment.cpuSetAdjustmentRetryDirty = true
	if p.adjustment.cpuSetAdjustmentRetryReasons == nil {
		p.adjustment.cpuSetAdjustmentRetryReasons = make(map[cpusetutil.CPUSetAdjustmentRetryReason]struct{})
	}
	p.adjustment.cpuSetAdjustmentRetryReasons[cpusetutil.RetryReasonApplyFailed] = struct{}{}
	return nil
}

func (p *DynamicPolicy) hasAnyPendingAdvisorPostCommitTarget() bool {
	p.adjustment.cpuSetAdjustmentRetryMu.Lock()
	defer p.adjustment.cpuSetAdjustmentRetryMu.Unlock()
	return p.adjustment.advisorPostCommitTarget != nil
}

func (p *DynamicPolicy) installCPUStateWritePermit() {
	p.state.SetWritePermit(func(revision uint64, operation string, permit *state.WritePermit) error {
		if permit == nil {
			return p.ensureCPUStateWriterAllowed(revision, "state."+operation, nil)
		}

		p.adjustment.cpuSetAdjustmentRetryMu.Lock()
		defer p.adjustment.cpuSetAdjustmentRetryMu.Unlock()
		target, ok := p.adjustment.advisorStateWritePermits[permit]
		delete(p.adjustment.advisorStateWritePermits, permit)
		if ok && target != nil && p.adjustment.advisorPostCommitTarget == target {
			if target.prepared && revision == target.preCommitRevision {
				return nil
			}
			if !target.prepared && revision == target.revision {
				return nil
			}
		}
		pendingRevision := uint64(0)
		if p.adjustment.advisorPostCommitTarget != nil {
			pendingRevision = p.adjustment.advisorPostCommitTarget.revision
		}
		return &advisorPostCommitPendingError{
			pendingRevision:   pendingRevision,
			attemptedRevision: revision,
			source:            "state." + operation,
		}
	})
}

func (p *DynamicPolicy) newAdvisorStateWritePermit(target *advisorPostCommitTarget) *state.WritePermit {
	permit := state.NewWritePermit()
	p.adjustment.cpuSetAdjustmentRetryMu.Lock()
	if p.adjustment.advisorStateWritePermits == nil {
		p.adjustment.advisorStateWritePermits = make(map[*state.WritePermit]*advisorPostCommitTarget)
	}
	p.adjustment.advisorStateWritePermits[permit] = target
	p.adjustment.cpuSetAdjustmentRetryMu.Unlock()
	return permit
}

func (p *DynamicPolicy) ensureCPUStateWriterAllowed(
	attemptedRevision uint64,
	source string,
	reconcileTarget *advisorPostCommitTarget,
) error {
	p.adjustment.cpuSetAdjustmentRetryMu.Lock()
	pendingTarget := p.adjustment.advisorPostCommitTarget
	if pendingTarget == nil || pendingTarget.writerFenceReleased {
		p.adjustment.cpuSetAdjustmentRetryMu.Unlock()
		return nil
	}
	pendingPrepared := pendingTarget.prepared
	pendingPreCommitRevision := pendingTarget.preCommitRevision
	pendingRevision := pendingTarget.revision
	p.adjustment.cpuSetAdjustmentRetryMu.Unlock()
	if reconcileTarget == pendingTarget {
		if pendingPrepared && attemptedRevision == pendingPreCommitRevision {
			return nil
		}
		if !pendingPrepared && attemptedRevision == pendingRevision {
			return nil
		}
	}
	return &advisorPostCommitPendingError{
		pendingRevision:   pendingRevision,
		attemptedRevision: attemptedRevision,
		source:            source,
	}
}

func (p *DynamicPolicy) currentAdvisorPostCommitTarget() *advisorPostCommitTarget {
	p.adjustment.cpuSetAdjustmentRetryMu.Lock()
	defer p.adjustment.cpuSetAdjustmentRetryMu.Unlock()
	return p.adjustment.advisorPostCommitTarget
}

func (p *DynamicPolicy) currentAdvisorPostCommitTargetAndChange() (*advisorPostCommitTarget, <-chan struct{}) {
	p.adjustment.cpuSetAdjustmentRetryMu.Lock()
	defer p.adjustment.cpuSetAdjustmentRetryMu.Unlock()
	if p.adjustment.advisorPostCommitTargetChange == nil {
		p.adjustment.advisorPostCommitTargetChange = make(chan struct{})
	}
	target := p.adjustment.advisorPostCommitTarget
	if target != nil && target.writerFenceReleased {
		target = nil
	}
	return target, p.adjustment.advisorPostCommitTargetChange
}

func (p *DynamicPolicy) currentAdvisorPostCommitProgress() advisorPostCommitProgress {
	p.adjustment.cpuSetAdjustmentRetryMu.Lock()
	defer p.adjustment.cpuSetAdjustmentRetryMu.Unlock()
	if p.adjustment.advisorPostCommitTargetChange == nil {
		p.adjustment.advisorPostCommitTargetChange = make(chan struct{})
	}
	target := p.adjustment.advisorPostCommitTarget
	if target == nil {
		return advisorPostCommitProgress{changed: p.adjustment.advisorPostCommitTargetChange}
	}
	return advisorPostCommitProgress{
		target:         target,
		revision:       target.revision,
		phase:          target.phase,
		createdAt:      target.createdAt,
		lastProgressAt: target.lastProgressAt,
		generation:     target.progressGeneration,
		changed:        p.adjustment.advisorPostCommitTargetChange,
	}
}

// advisorPostCommitProgressStuck reports whether a target has remained in its
// physical-apply phase past the stuck threshold. Other phases may be waiting on
// durable publication or cleanup and must retain their target-specific retry.
func (p *DynamicPolicy) advisorPostCommitProgressStuck(progress advisorPostCommitProgress) bool {
	return progress.target != nil &&
		progress.phase == advisorPostCommitPhasePhysicalApply &&
		time.Since(progress.lastProgressAt) >= advisorPostCommitStuckThreshold(p.config.conf)
}

func (p *DynamicPolicy) recordAdvisorPostCommitProgress(
	target *advisorPostCommitTarget,
	phase advisorPostCommitPhase,
) {
	p.adjustment.cpuSetAdjustmentRetryMu.Lock()
	defer p.adjustment.cpuSetAdjustmentRetryMu.Unlock()
	p.recordAdvisorPostCommitProgressLocked(target, phase)
}

// recordAdvisorPostCommitProgressLocked records runtime-only progress and wakes
// waiters. It deliberately does not alter the WAL or canonical state.
func (p *DynamicPolicy) recordAdvisorPostCommitProgressLocked(
	target *advisorPostCommitTarget,
	phase advisorPostCommitPhase,
) {
	if target == nil || p.adjustment.advisorPostCommitTarget != target {
		return
	}
	if target.phase == phase && target.progressGeneration > 0 {
		return
	}
	now := time.Now()
	if target.createdAt.IsZero() {
		target.createdAt = now
	}
	target.phase = phase
	target.lastProgressAt = now
	target.progressGeneration++
	if p.adjustment.advisorPostCommitTargetChange != nil {
		close(p.adjustment.advisorPostCommitTargetChange)
	}
	p.adjustment.advisorPostCommitTargetChange = make(chan struct{})
}

// setAdvisorPostCommitTargetLocked publishes a pointer transition and wakes all
// waiters that atomically observed the previous target and change channel.
// cpuSetAdjustmentRetryMu must be held by the caller.
func (p *DynamicPolicy) setAdvisorPostCommitTargetLocked(target *advisorPostCommitTarget) {
	if p.adjustment.advisorPostCommitTarget == target {
		return
	}
	if p.adjustment.advisorPostCommitTargetChange != nil {
		close(p.adjustment.advisorPostCommitTargetChange)
	}
	p.adjustment.advisorPostCommitTarget = target
	p.adjustment.advisorPostCommitTargetChange = make(chan struct{})
}

func (p *DynamicPolicy) retryAdvisorPostCommitAbort(target *advisorPostCommitTarget) error {
	if err := p.removeAdvisorPostCommitStaging(); err != nil {
		p.scheduleCPUSetAdjustmentRetry(cpusetutil.RetryReasonApplyFailed)
		return fmt.Errorf("remove aborted advisor post-commit staging: %w", err)
	}
	previous := target.previousTarget
	p.rollbackPreparedAdvisorPostCommitTarget(target, previous)
	p.adjustment.cpuSetAdjustmentRetryMu.Lock()
	delete(p.adjustment.cpuSetAdjustmentRetryReasons, cpusetutil.RetryReasonApplyFailed)
	if len(p.adjustment.cpuSetAdjustmentRetryReasons) == 0 && previous == nil {
		p.adjustment.cpuSetAdjustmentRetryDirty = false
		p.adjustment.cpuSetAdjustmentRetryReasons = nil
	}
	p.adjustment.cpuSetAdjustmentRetryMu.Unlock()
	return nil
}

func (p *DynamicPolicy) retryAdvisorPostCommitPublication(target *advisorPostCommitTarget) error {
	if err := p.ensureAdvisorPostCommitPublished(target); err != nil {
		p.scheduleCPUSetAdjustmentRetry(cpusetutil.RetryReasonApplyFailed)
		return fmt.Errorf("publish committed advisor post-commit target: %w", err)
	}
	p.adjustment.cpuSetAdjustmentRetryMu.Lock()
	if p.adjustment.advisorPostCommitTarget == target {
		target.publicationPending = false
		p.recordAdvisorPostCommitProgressLocked(target, advisorPostCommitPhasePublished)
	}
	p.adjustment.cpuSetAdjustmentRetryMu.Unlock()
	return nil
}

func (p *DynamicPolicy) persistAdvisorPostCommitApplied(target *advisorPostCommitTarget) error {
	if err := p.storeAdvisorPostCommitTarget(target, p.advisorPostCommitCheckpointPath()); err != nil {
		p.scheduleCPUSetAdjustmentRetry(cpusetutil.RetryReasonApplyFailed)
		return fmt.Errorf("persist applied advisor post-commit target: %w", err)
	}
	p.adjustment.cpuSetAdjustmentRetryMu.Lock()
	if p.adjustment.advisorPostCommitTarget == target {
		target.applyMarkerPending = false
		target.cleanupPending = true
		p.recordAdvisorPostCommitProgressLocked(target, advisorPostCommitPhaseCleanup)
	}
	p.adjustment.cpuSetAdjustmentRetryMu.Unlock()
	return nil
}

func (p *DynamicPolicy) completeAdvisorPostCommitCleanup(target *advisorPostCommitTarget) error {
	if err := p.removeAdvisorPostCommitCheckpoints(); err != nil {
		p.adjustment.cpuSetAdjustmentRetryMu.Lock()
		if p.adjustment.advisorPostCommitTarget == target {
			target.cleanupPending = true
		}
		p.adjustment.cpuSetAdjustmentRetryMu.Unlock()
		p.scheduleCPUSetAdjustmentRetry(cpusetutil.RetryReasonApplyFailed)
		return fmt.Errorf("remove reconciled advisor post-commit checkpoints: %w", err)
	}

	p.adjustment.cpuSetAdjustmentRetryMu.Lock()
	if p.releaseAdvisorPostCommitTargetFenceLocked(target) {
		p.completeAdvisorPostCommitRetryLocked()
	}
	p.adjustment.cpuSetAdjustmentRetryMu.Unlock()
	return nil
}

// releaseAdvisorPostCommitTargetFenceLocked drops only the target ownership and
// its writer permits. Retry state is completed separately because abandoning a
// stuck physical apply must leave canonical reconciliation dirty.
func (p *DynamicPolicy) releaseAdvisorPostCommitTargetFenceLocked(target *advisorPostCommitTarget) bool {
	if p.adjustment.advisorPostCommitTarget != target {
		return false
	}
	p.setAdvisorPostCommitTargetLocked(nil)
	for permit, permitTarget := range p.adjustment.advisorStateWritePermits {
		if permitTarget == target {
			delete(p.adjustment.advisorStateWritePermits, permit)
		}
	}
	return true
}

// releaseAdvisorPostCommitWriterFenceLocked lets unrelated writers advance
// while retaining target and its WAL for a complete physical replay. This is a
// runtime-only handoff: after a crash the durable target is fenced and replayed
// again, which is safer than losing an incomplete physical transaction.
func (p *DynamicPolicy) releaseAdvisorPostCommitWriterFenceLocked(target *advisorPostCommitTarget) bool {
	if p.adjustment.advisorPostCommitTarget != target {
		return false
	}
	for permit, permitTarget := range p.adjustment.advisorStateWritePermits {
		if permitTarget == target {
			delete(p.adjustment.advisorStateWritePermits, permit)
		}
	}
	target.writerFenceReleased = true
	p.recordAdvisorPostCommitProgressLocked(target, advisorPostCommitPhaseCanonicalReconcile)
	return true
}

// completeAdvisorPostCommitRetryLocked clears apply-failure retry state only
// after the target has completed physical apply and durable cleanup.
func (p *DynamicPolicy) completeAdvisorPostCommitRetryLocked() {
	delete(p.adjustment.cpuSetAdjustmentRetryReasons, cpusetutil.RetryReasonApplyFailed)
	if len(p.adjustment.cpuSetAdjustmentRetryReasons) == 0 {
		p.adjustment.cpuSetAdjustmentRetryDirty = false
		p.adjustment.cpuSetAdjustmentRetryReasons = nil
	}
}

// recoverStuckAdvisorPostCommitTargetLocked releases the writer fence of an
// aged physical apply only after all response-owned side effects have
// converged. The caller holds p.Lock; this method serializes the handoff with
// the same execution lease used by every physical adjustment round.
func (p *DynamicPolicy) recoverStuckAdvisorPostCommitTargetLocked(
	ctx context.Context,
	target *advisorPostCommitTarget,
) (bool, error) {
	if target == nil {
		return false, nil
	}
	executionLease := cpuSetAdjustmentExecutionLeaseFromContext(ctx, p)
	if executionLease == nil {
		var err error
		executionLease, err = p.acquireCPUSetAdjustmentExecutionLocked(ctx)
		if err != nil {
			return false, fmt.Errorf("acquire cpuset adjustment execution for stuck recovery: %w", err)
		}
		defer executionLease.release()
	}
	progress := p.currentAdvisorPostCommitProgress()
	if progress.target != target || !p.advisorPostCommitProgressStuck(progress) {
		return false, nil
	}
	if err := p.steadyFakeNUMAMigration.applyTransition(
		target.migrationCheckpointTransition); err != nil {
		return false, fmt.Errorf("replay stuck advisor migration checkpoint transition: %w", err)
	}
	headroomErr := p.applyHeadroom(target.response)
	cgroupErr := p.applyCgroupConfigs(target.response)
	if headroomErr != nil || cgroupErr != nil {
		var stageErrors []string
		if headroomErr != nil {
			stageErrors = append(stageErrors, fmt.Sprintf("applyHeadroom failed with error: %v", headroomErr))
		}
		if cgroupErr != nil {
			stageErrors = append(stageErrors, fmt.Sprintf("applyCgroupConfigs failed with error: %v", cgroupErr))
		}
		return false, fmt.Errorf("replay stuck advisor response-owned state: %s", strings.Join(stageErrors, "; "))
	}
	p.adjustment.cpuSetAdjustmentRetryMu.Lock()
	released := p.releaseAdvisorPostCommitWriterFenceLocked(target)
	if released && !p.adjustment.cpuSetAdjustmentRetryStopping {
		p.adjustment.cpuSetAdjustmentRetryDirty = true
		if p.adjustment.cpuSetAdjustmentRetryReasons == nil {
			p.adjustment.cpuSetAdjustmentRetryReasons = make(map[cpusetutil.CPUSetAdjustmentRetryReason]struct{})
		}
		p.adjustment.cpuSetAdjustmentRetryReasons[cpusetutil.RetryReasonApplyFailed] = struct{}{}
	}
	p.adjustment.cpuSetAdjustmentRetryMu.Unlock()
	return released, nil
}

func (p *DynamicPolicy) advisorPostCommitTargetSuperseded(target *advisorPostCommitTarget) bool {
	p.adjustment.cpuSetAdjustmentRetryMu.Lock()
	released := target != nil &&
		p.adjustment.advisorPostCommitTarget == target &&
		target.writerFenceReleased
	p.adjustment.cpuSetAdjustmentRetryMu.Unlock()
	return released && p.state != nil && p.state.GetRevision() != target.revision
}

// retireSupersededAdvisorPostCommitTarget removes a durable target only after a
// released writer has advanced canonical revision. Its response-owned effects
// were replayed before the fence release, so the old response is obsolete; the
// caller must still reconcile cpuset from the latest canonical state.
func (p *DynamicPolicy) retireSupersededAdvisorPostCommitTarget(target *advisorPostCommitTarget) error {
	if err := p.removeAdvisorPostCommitCheckpoints(); err != nil {
		return fmt.Errorf("remove superseded advisor post-commit checkpoints: %w", err)
	}
	p.adjustment.cpuSetAdjustmentRetryMu.Lock()
	p.releaseAdvisorPostCommitTargetFenceLocked(target)
	p.adjustment.cpuSetAdjustmentRetryMu.Unlock()
	return nil
}

func (p *DynamicPolicy) reconcileOrRetireAdvisorPostCommitTarget(
	ctx context.Context,
	target *advisorPostCommitTarget,
	mode cpusetutil.CPUSetAdjustmentMode,
) error {
	if p.advisorPostCommitTargetSuperseded(target) {
		if err := p.retireSupersededAdvisorPostCommitTarget(target); err != nil {
			return err
		}
		return p.runCPUSetAdjustmentHandlers(ctx, mode)
	}
	return p.reconcileAdvisorPostCommitTarget(ctx, target, mode)
}

// reconcileAdvisorPostCommitTarget is the sole owner of advancing a committed
// WAL target through publication, physical apply, applied-marker persistence,
// and cleanup. It defers checkpoint removal and writer-fence release until the
// exact target revision is durably marked applied.
func (p *DynamicPolicy) reconcileAdvisorPostCommitTarget(
	ctx context.Context,
	target *advisorPostCommitTarget,
	modes ...cpusetutil.CPUSetAdjustmentMode,
) error {
	if target == nil {
		return nil
	}
	executionLease := cpuSetAdjustmentExecutionLeaseFromContext(ctx, p)
	if executionLease == nil {
		var err error
		executionLease, err = p.acquireCPUSetAdjustmentExecutionLocked(ctx)
		if err != nil {
			return fmt.Errorf("reconcile advisor post-commit target waiting for cpuset adjustment execution: %w", err)
		}
		defer executionLease.release()
		ctx = context.WithValue(ctx, cpuSetAdjustmentExecutionLeaseContextKey{}, executionLease)
	}
	p.adjustment.cpuSetAdjustmentRetryMu.Lock()
	current := p.adjustment.advisorPostCommitTarget == target
	abortPending := target.abortPending
	publicationPending := target.publicationPending
	applyMarkerPending := target.applyMarkerPending
	cleanupPending := target.cleanupPending
	applied := target.applied
	p.adjustment.cpuSetAdjustmentRetryMu.Unlock()
	if !current {
		return nil
	}
	if abortPending {
		return p.retryAdvisorPostCommitAbort(target)
	}
	if publicationPending {
		if err := p.retryAdvisorPostCommitPublication(target); err != nil {
			return err
		}
	}
	if applyMarkerPending {
		if err := p.persistAdvisorPostCommitApplied(target); err != nil {
			return err
		}
		cleanupPending = true
	}
	if applied && !cleanupPending {
		p.adjustment.cpuSetAdjustmentRetryMu.Lock()
		if p.adjustment.advisorPostCommitTarget == target {
			target.cleanupPending = true
		}
		p.adjustment.cpuSetAdjustmentRetryMu.Unlock()
		cleanupPending = true
	}
	if cleanupPending || applied {
		return p.completeAdvisorPostCommitCleanup(target)
	}
	if current, err := p.advisorPostCommitTargetStatus(target); err != nil {
		return err
	} else if !current {
		return nil
	}

	p.recordAdvisorPostCommitProgress(target, advisorPostCommitPhasePhysicalApply)
	mode := cpusetutil.CPUSetAdjustmentModePeriodic
	if len(modes) > 0 {
		mode = modes[0].OrFullDefault()
	}
	if err := p.steadyFakeNUMAMigration.applyTransition(
		target.migrationCheckpointTransition); err != nil {
		if mode != cpusetutil.CPUSetAdjustmentModeRetry {
			p.markAdvisorApplyFailed(target.revision)
		}
		return fmt.Errorf("apply migration checkpoint transition failed: %w", err)
	}
	if current, err := p.advisorPostCommitTargetStatus(target); err != nil {
		return err
	} else if !current {
		return nil
	}
	headroomErr := p.applyHeadroom(target.response)
	if current, err := p.advisorPostCommitTargetStatus(target); err != nil {
		return err
	} else if !current {
		return nil
	}
	cgroupErr := p.applyCgroupConfigs(target.response)
	if current, err := p.advisorPostCommitTargetStatus(target); err != nil {
		return err
	} else if !current {
		return nil
	}
	adjustmentCtx := context.WithValue(ctx, advisorPostCommitTargetContextKey{}, target)
	adjustmentErr := p.runCPUSetAdjustmentHandlers(adjustmentCtx, mode)
	if headroomErr == nil && cgroupErr == nil && adjustmentErr == nil {
		p.adjustment.cpuSetAdjustmentRetryMu.Lock()
		if p.adjustment.advisorPostCommitTarget == target {
			target.applied = true
			target.applyMarkerPending = true
			p.recordAdvisorPostCommitProgressLocked(target, advisorPostCommitPhaseAppliedMarker)
		}
		p.adjustment.cpuSetAdjustmentRetryMu.Unlock()
		if err := p.persistAdvisorPostCommitApplied(target); err != nil {
			return err
		}
		return p.completeAdvisorPostCommitCleanup(target)
	}

	if mode != cpusetutil.CPUSetAdjustmentModeRetry {
		p.markAdvisorApplyFailed(target.revision)
	}
	var stageErrors []string
	if headroomErr != nil {
		stageErrors = append(stageErrors, fmt.Sprintf("applyHeadroom failed with error: %v", headroomErr))
	}
	if cgroupErr != nil {
		stageErrors = append(stageErrors, fmt.Sprintf("applyCgroupConfigs failed with error: %v", cgroupErr))
	}
	if adjustmentErr != nil {
		if len(stageErrors) == 0 {
			return fmt.Errorf("runCPUSetAdjustmentHandlers failed with error: %w", adjustmentErr)
		}
		return fmt.Errorf("%s; runCPUSetAdjustmentHandlers failed with error: %w",
			strings.Join(stageErrors, "; "), adjustmentErr)
	}
	return errors.New(strings.Join(stageErrors, "; "))
}

func (p *DynamicPolicy) advisorPostCommitTargetStatus(
	target *advisorPostCommitTarget,
) (bool, error) {
	p.adjustment.cpuSetAdjustmentRetryMu.Lock()
	current := p.adjustment.advisorPostCommitTarget
	p.adjustment.cpuSetAdjustmentRetryMu.Unlock()
	if current != target || target == nil || p.state == nil {
		return false, nil
	}
	actualRevision := p.state.GetRevision()
	if actualRevision != target.revision {
		return false, &advisorPostCommitPendingError{
			pendingRevision:   target.revision,
			attemptedRevision: actualRevision,
			source:            "advisor post-commit reconcile",
		}
	}
	return true, nil
}

func (p *DynamicPolicy) markAdvisorApplyFailed(revision uint64) {
	if p.markAdvisorApplyFailedHook != nil {
		p.markAdvisorApplyFailedHook(revision)
		return
	}
	general.Errorf("post-advisor-commit apply failed for state revision %d; scheduling latest-state retry", revision)
	p.scheduleCPUSetAdjustmentRetry(cpusetutil.RetryReasonApplyFailed)
}

func (p *DynamicPolicy) scheduleCPUSetAdjustmentPersistenceRetry() {
	p.markCPUSetAdjustmentPersistenceRequired()
	p.scheduleCPUSetAdjustmentRetry(cpusetutil.RetryReasonPersistFailed)
}

func (p *DynamicPolicy) markCPUSetAdjustmentPersistenceRequired() {
	p.adjustment.cpuSetAdjustmentRetryMu.Lock()
	p.adjustment.cpuSetAdjustmentRetryPersist = true
	p.adjustment.cpuSetAdjustmentRetryMu.Unlock()
}

func (p *DynamicPolicy) persistCPUSetAdjustmentStateIfNeeded() error {
	p.adjustment.cpuSetAdjustmentRetryMu.Lock()
	pending := p.adjustment.cpuSetAdjustmentRetryPersist
	p.adjustment.cpuSetAdjustmentRetryMu.Unlock()
	if !pending {
		return nil
	}
	if err := p.state.StoreState(); err != nil {
		return fmt.Errorf("persist restored CPU state: %w", err)
	}
	p.adjustment.cpuSetAdjustmentRetryMu.Lock()
	p.adjustment.cpuSetAdjustmentRetryPersist = false
	delete(p.adjustment.cpuSetAdjustmentRetryReasons, cpusetutil.RetryReasonPersistFailed)
	p.adjustment.cpuSetAdjustmentRetryMu.Unlock()
	return nil
}

func (p *DynamicPolicy) retryLatestCPUSetAdjustment(
	ctx context.Context,
	mode cpusetutil.CPUSetAdjustmentMode,
) error {
	// The caller holds the policy lock, so persist a restored in-memory state
	// before any fallible cgroup reconciliation. Otherwise a persistent
	// adjustment failure can indefinitely leave the failed candidate on disk.
	persistErr := p.persistCPUSetAdjustmentStateIfNeeded()

	var adjustmentErr error
	if target := p.currentAdvisorPostCommitTarget(); target != nil {
		adjustmentErr = p.reconcileOrRetireAdvisorPostCommitTarget(ctx, target, mode)
	} else {
		adjustmentErr = p.runCPUSetAdjustmentHandlers(ctx, mode)
	}
	if adjustmentErr != nil && persistErr != nil {
		return fmt.Errorf("%v; cpuset adjustment failed: %w", persistErr, adjustmentErr)
	}
	if adjustmentErr != nil {
		return adjustmentErr
	}
	return persistErr
}

func (p *DynamicPolicy) markCPUSetAdjustmentDirty(reason cpusetutil.CPUSetAdjustmentRetryReason) {
	p.adjustment.cpuSetAdjustmentRetryMu.Lock()
	defer p.adjustment.cpuSetAdjustmentRetryMu.Unlock()
	if p.adjustment.cpuSetAdjustmentRetryStopping {
		return
	}
	p.adjustment.cpuSetAdjustmentRetryDirty = true
	if p.adjustment.cpuSetAdjustmentRetryReasons == nil {
		p.adjustment.cpuSetAdjustmentRetryReasons = make(map[cpusetutil.CPUSetAdjustmentRetryReason]struct{})
	}
	p.adjustment.cpuSetAdjustmentRetryReasons[reason] = struct{}{}
}

func (p *DynamicPolicy) scheduleCPUSetAdjustmentRetry(reason cpusetutil.CPUSetAdjustmentRetryReason) {
	p.adjustment.cpuSetAdjustmentRetryMu.Lock()
	if p.adjustment.cpuSetAdjustmentRetryStopping {
		p.adjustment.cpuSetAdjustmentRetryMu.Unlock()
		return
	}
	p.adjustment.cpuSetAdjustmentRetryDirty = true
	if p.adjustment.cpuSetAdjustmentRetryReasons == nil {
		p.adjustment.cpuSetAdjustmentRetryReasons = make(map[cpusetutil.CPUSetAdjustmentRetryReason]struct{})
	}
	p.adjustment.cpuSetAdjustmentRetryReasons[reason] = struct{}{}
	if p.adjustment.cpuSetAdjustmentRetryQueued {
		p.adjustment.cpuSetAdjustmentRetryAgain = true
		p.adjustment.cpuSetAdjustmentRetryMu.Unlock()
		return
	}
	p.adjustment.cpuSetAdjustmentRetryQueued = true
	stopCh := p.adjustment.cpuSetAdjustmentRetryStopCh
	p.adjustment.cpuSetAdjustmentRetryWG.Add(1)
	p.adjustment.cpuSetAdjustmentRetryMu.Unlock()
	go func() {
		defer p.adjustment.cpuSetAdjustmentRetryWG.Done()
		finishStopped := func() {
			p.adjustment.cpuSetAdjustmentRetryMu.Lock()
			p.adjustment.cpuSetAdjustmentRetryQueued = false
			p.adjustment.cpuSetAdjustmentRetryAgain = false
			p.adjustment.cpuSetAdjustmentRetryMu.Unlock()
		}
		attempt := 0
		for {
			select {
			case <-stopCh:
				finishStopped()
				return
			default:
			}
			p.Lock()
			ctx, cancel := context.WithTimeout(context.Background(), cpuSetAdjustmentHandlerTimeout(p.config.conf))
			if stopCh != nil {
				go func() {
					select {
					case <-stopCh:
						cancel()
					case <-ctx.Done():
					}
				}()
			}
			err := p.retryLatestCPUSetAdjustment(ctx, cpusetutil.CPUSetAdjustmentModeRetry)
			cancel()
			p.Unlock()
			attempt++
			if err != nil {
				general.Errorf("retry latest cpuset adjustment failed, reason=%s: %v", reason, err)
			}

			p.adjustment.cpuSetAdjustmentRetryMu.Lock()
			if p.adjustment.cpuSetAdjustmentRetryStopping {
				p.adjustment.cpuSetAdjustmentRetryQueued = false
				p.adjustment.cpuSetAdjustmentRetryAgain = false
				p.adjustment.cpuSetAdjustmentRetryMu.Unlock()
				return
			}
			retryAgain := p.adjustment.cpuSetAdjustmentRetryAgain
			if retryAgain {
				p.adjustment.cpuSetAdjustmentRetryAgain = false
			}
			if (err != nil || retryAgain) && attempt < cpuSetAdjustmentRetryMaxAttempts {
				p.adjustment.cpuSetAdjustmentRetryMu.Unlock()
				timer := time.NewTimer(cpuSetAdjustmentRetryBackoff(attempt))
				select {
				case <-timer.C:
				case <-stopCh:
					if !timer.Stop() {
						<-timer.C
					}
					finishStopped()
					return
				}
				continue
			}
			if err == nil && !retryAgain && p.adjustment.advisorPostCommitTarget == nil && !p.adjustment.cpuSetAdjustmentRetryPersist {
				p.adjustment.cpuSetAdjustmentRetryDirty = false
				p.adjustment.cpuSetAdjustmentRetryReasons = nil
			} else {
				p.adjustment.cpuSetAdjustmentRetryDirty = true
			}
			p.adjustment.cpuSetAdjustmentRetryQueued = false
			p.adjustment.cpuSetAdjustmentRetryMu.Unlock()
			return
		}
	}()
}

func (p *DynamicPolicy) handleCgroupCreateEvent() {
	p.adjustment.cpuSetAdjustmentRetryMu.Lock()
	_, deferredLeaf := p.adjustment.cpuSetAdjustmentRetryReasons[cpusetutil.RetryReasonDeferredLeaf]
	shouldRetry := p.adjustment.cpuSetAdjustmentRetryDirty && deferredLeaf
	p.adjustment.cpuSetAdjustmentRetryMu.Unlock()
	if shouldRetry {
		p.scheduleCPUSetAdjustmentRetry(cpusetutil.RetryReasonDeferredLeaf)
	}
}

func (p *DynamicPolicy) reconcileDirtyCPUSetAdjustment() error {
	p.adjustment.cpuSetAdjustmentRetryMu.Lock()
	dirty := p.adjustment.cpuSetAdjustmentRetryDirty && !p.adjustment.cpuSetAdjustmentRetryQueued
	p.adjustment.cpuSetAdjustmentRetryMu.Unlock()
	if !dirty {
		return nil
	}

	p.Lock()
	ctx, cancel := context.WithTimeout(context.Background(), cpuSetAdjustmentHandlerTimeout(p.config.conf))
	err := p.retryLatestCPUSetAdjustment(ctx, cpusetutil.CPUSetAdjustmentModePeriodic)
	cancel()
	p.Unlock()
	if err != nil {
		general.Errorf("periodic latest-state cpuset adjustment reconcile failed: %v", err)
	} else {
		p.adjustment.cpuSetAdjustmentRetryMu.Lock()
		if p.adjustment.advisorPostCommitTarget == nil && !p.adjustment.cpuSetAdjustmentRetryPersist &&
			!p.adjustment.cpuSetAdjustmentRetryQueued && !p.adjustment.cpuSetAdjustmentRetryAgain {
			p.adjustment.cpuSetAdjustmentRetryDirty = false
			p.adjustment.cpuSetAdjustmentRetryReasons = nil
		}
		p.adjustment.cpuSetAdjustmentRetryMu.Unlock()
	}
	return err
}

func (p *DynamicPolicy) runBulkheadPeriodicalHandlers(
	coreConf *config.Configuration,
	extraConf interface{},
	dynamicConf *dynamicconfig.DynamicAgentConfiguration,
	emitter metrics.MetricEmitter,
	metaServer *metaserver.MetaServer,
) {
	reconcileErr := p.reconcileDirtyCPUSetAdjustment()
	if p.bulkhead.bulkheadManager != nil {
		p.bulkhead.bulkheadManager.RunPeriodicalHandlers(coreConf, extraConf, dynamicConf, emitter, metaServer)
	}
	if reconcileErr != nil {
		_ = general.UpdateHealthzStateByError(cpuconsts.SyncBulkhead, reconcileErr)
	}
}

func cpuSetAdjustmentRetryBackoff(failedAttempts int) time.Duration {
	if failedAttempts <= 1 {
		return cpuSetAdjustmentRetryInitialBackoff
	}
	backoff := cpuSetAdjustmentRetryInitialBackoff
	for i := 1; i < failedAttempts && backoff < cpuSetAdjustmentRetryMaxBackoff; i++ {
		backoff *= 2
	}
	if backoff > cpuSetAdjustmentRetryMaxBackoff {
		return cpuSetAdjustmentRetryMaxBackoff
	}
	return backoff
}
