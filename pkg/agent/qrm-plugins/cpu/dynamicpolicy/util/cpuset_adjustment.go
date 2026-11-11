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

package util

import (
	"context"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/config"
	dynamicconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

type CPUSetAdjustmentHandler func(context.Context, CPUSetAdjustmentHandlerCtx) error

type CPUSetAdjustmentMode string

const (
	CPUSetAdjustmentModeAdmission CPUSetAdjustmentMode = "admission"
	CPUSetAdjustmentModePeriodic  CPUSetAdjustmentMode = "periodic"
	CPUSetAdjustmentModeRetry     CPUSetAdjustmentMode = "retry"
)

type CPUSetAdjustmentRetryReason string

const (
	RetryReasonStaleState      CPUSetAdjustmentRetryReason = "stale_state"
	RetryReasonDeferredLeaf    CPUSetAdjustmentRetryReason = "deferred_leaf"
	RetryReasonOwnershipLost   CPUSetAdjustmentRetryReason = "ownership_lost"
	RetryReasonRestoreFailed   CPUSetAdjustmentRetryReason = "restore_failed"
	RetryReasonApplyFailed     CPUSetAdjustmentRetryReason = "apply_failed"
	RetryReasonPersistFailed   CPUSetAdjustmentRetryReason = "persist_failed"
	RetryReasonRecoveryCommit  CPUSetAdjustmentRetryReason = "recovery_commit"
	RetryReasonFixedPointStall CPUSetAdjustmentRetryReason = "fixed_point_stall"
)

func (m CPUSetAdjustmentMode) OrFullDefault() CPUSetAdjustmentMode {
	if m == "" {
		return CPUSetAdjustmentModePeriodic
	}
	return m
}

type CPUSetAdjustmentHandlerCtx struct {
	CoreConf                  *config.Configuration
	DynamicConf               *dynamicconfig.Configuration
	Emitter                   metrics.MetricEmitter
	MetaServer                *metaserver.MetaServer
	State                     state.ReadonlyState
	Topology                  *machine.CPUTopology
	ReservedCPUs              machine.CPUSet
	ReservedReclaimedCPUs     machine.CPUSet
	ReservedReclaimedCPUsSize int
	Generation                uint64
	CommitIfGenerationCurrent func(generation uint64, commit func()) bool
	// Mode is fail-safe: an unset mode is interpreted as Periodic/full
	// convergence by consumers and never enables admission-only relaxation.
	Mode              CPUSetAdjustmentMode
	ScheduleFullRetry func(CPUSetAdjustmentRetryReason)
	// CommitOverride carries topology-safe commit inputs produced by adjustment handlers.
	// Callers consume it before committing advisor state.
	CommitOverride *CPUSetAdjustmentCommitOverride
}

type CPUSetAdjustmentCommitOverride struct {
	ReclaimEffective machine.CPUSet
	Source           string
	// PendingSharedProjection is the transient per-container planning hint
	// (podUID -> containerName set) computed from the synchronous admission
	// snapshot. It mirrors the background adjustment hint kept on
	// HandlerContext.PendingSharedProjection and is intentionally NOT part of
	// DesiredView equality, revision, or checkpoint.
	PendingSharedProjection map[string]map[string]struct{}
}
