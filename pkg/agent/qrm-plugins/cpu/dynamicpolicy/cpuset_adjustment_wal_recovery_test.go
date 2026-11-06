/*
Copyright 2026 The Katalyst Authors.

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
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/gogo/protobuf/proto"
	"github.com/stretchr/testify/require"

	advisorsvc "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/advisorsvc"
	advisorapi "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/cpuadvisor"
)

// The WAL recovery matrix distinguishes authoritative data from crash residue.
// A valid slot whose revision exactly matches canonical state is authoritative
// and may replace/remove a corrupt sibling. Corrupt, unreadable, or unknown-ahead
// records still fail closed when no authoritative sibling exists.

// TestWALRecoveryReplayableStagingReplacesCorruptActive proves that a valid
// staging record matching canonical state completes the interrupted promotion
// even when the obsolete active slot is corrupt.
func TestWALRecoveryReplayableStagingReplacesCorruptActive(t *testing.T) {
	t.Parallel()

	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()
	dir := t.TempDir()
	p.advisorPostCommitCheckpointDir = dir
	pre := p.state.GetRevision()
	post, err := nextAdvisorRevision(pre)
	require.NoError(t, err)

	resp := &advisorapi.ListAndWatchResponse{
		ExtraEntries: []*advisorsvc.CalculationInfo{{CgroupPath: "/replayable-staging"}},
	}
	require.NoError(t, os.WriteFile(
		p.advisorPostCommitStagingPath(),
		legacyV2CheckpointBytes(t, pre, post, resp, nil, false),
		0o600))
	require.NoError(t, os.WriteFile(
		p.advisorPostCommitCheckpointPath(), []byte("{corrupt-active-not-json"), 0o600))
	commitStateToNext(t, p)

	require.NoError(t, p.restoreAdvisorPostCommitTarget())
	restored := p.currentAdvisorPostCommitTarget()
	require.NotNil(t, restored)
	require.Equal(t, post, restored.revision)
	require.True(t, proto.Equal(resp, restored.response))
	require.FileExists(t, p.advisorPostCommitCheckpointPath())
	require.NoFileExists(t, p.advisorPostCommitStagingPath())
}

// TestWALRecoveryReplayableActiveRemovesCorruptStaging proves that a valid
// active record matching canonical state is replayed and stale staging residue
// does not block startup.
func TestWALRecoveryReplayableActiveRemovesCorruptStaging(t *testing.T) {
	t.Parallel()

	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()
	p.advisorPostCommitCheckpointDir = t.TempDir()
	pre := p.state.GetRevision()
	post, err := nextAdvisorRevision(pre)
	require.NoError(t, err)

	resp := &advisorapi.ListAndWatchResponse{
		ExtraEntries: []*advisorsvc.CalculationInfo{{CgroupPath: "/replayable-active"}},
	}
	commitStateToNext(t, p)
	require.NoError(t, os.WriteFile(
		p.advisorPostCommitCheckpointPath(),
		legacyV2CheckpointBytes(t, pre, post, resp, nil, false),
		0o600))
	require.NoError(t, os.WriteFile(
		p.advisorPostCommitStagingPath(), []byte("{corrupt-staging-not-json"), 0o600))

	require.NoError(t, p.restoreAdvisorPostCommitTarget())
	restored := p.currentAdvisorPostCommitTarget()
	require.NotNil(t, restored)
	require.Equal(t, post, restored.revision)
	require.True(t, proto.Equal(resp, restored.response))
	require.FileExists(t, p.advisorPostCommitCheckpointPath())
	require.NoFileExists(t, p.advisorPostCommitStagingPath())
}

// TestWALRecoveryTruncatedEnvelopeFailsClosedAndRetained proves a file cut
// mid-write (truncated JSON envelope) blocks startup and is left on disk.
func TestWALRecoveryTruncatedEnvelopeFailsClosedAndRetained(t *testing.T) {
	t.Parallel()

	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()
	p.advisorPostCommitCheckpointDir = t.TempDir()
	pre := p.state.GetRevision()
	post, err := nextAdvisorRevision(pre)
	require.NoError(t, err)

	full := legacyV2CheckpointBytes(t, pre, post,
		&advisorapi.ListAndWatchResponse{
			ExtraEntries: []*advisorsvc.CalculationInfo{{CgroupPath: "/truncated"}},
		}, nil, false)
	truncated := full[:len(full)/2]
	require.NoError(t, os.WriteFile(p.advisorPostCommitCheckpointPath(), truncated, 0o600))

	err = p.restoreAdvisorPostCommitTarget()
	require.Error(t, err)
	require.Nil(t, p.currentAdvisorPostCommitTarget())
	require.FileExists(t, p.advisorPostCommitCheckpointPath(),
		"truncated record must be retained for operator recovery")
}

// TestWALRecoveryChecksumMismatchStagingRetained proves a well-formed staging
// JSON envelope whose digest no longer matches (bit rot / tamper) fails closed
// and is retained rather than discarded.
func TestWALRecoveryChecksumMismatchStagingRetained(t *testing.T) {
	t.Parallel()

	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()
	p.advisorPostCommitCheckpointDir = t.TempDir()
	pre := p.state.GetRevision()
	post, err := nextAdvisorRevision(pre)
	require.NoError(t, err)

	respBytes := mustMarshalProto(t, &advisorapi.ListAndWatchResponse{
		ExtraEntries: []*advisorsvc.CalculationInfo{{CgroupPath: "/checksum-mismatch"}},
	})
	record := advisorPostCommitCheckpoint{
		Version:           advisorPostCommitCheckpointVersion,
		PreCommitRevision: &pre,
		Revision:          post,
		Response:          append([]byte(advisorPostCommitWALV2Magic), respBytes...),
	}
	record.Checksum = advisorPostCommitCheckpointChecksum(
		record.Version, &pre, post, respBytes, nil, false)
	data, err := json.Marshal(record)
	require.NoError(t, err)
	// Flip one response byte after the magic prefix so the embedded checksum no
	// longer recomputes; the envelope itself stays valid JSON.
	magicLen := len(advisorPostCommitWALV2Magic)
	idx := len(data) - magicLen - 2
	data[idx] ^= 0xff
	require.NoError(t, os.WriteFile(p.advisorPostCommitStagingPath(), data, 0o600))

	err = p.restoreAdvisorPostCommitTarget()
	require.ErrorContains(t, err, "checksum mismatch")
	require.Nil(t, p.currentAdvisorPostCommitTarget())
	require.FileExists(t, p.advisorPostCommitStagingPath(),
		"checksum-mismatched staging must be retained")
}

// TestWALRecoveryUnknownVersionStagingBlocked proves a future/unknown envelope
// version is rejected in the staging slot and retained, never silently read as
// a legacy record.
func TestWALRecoveryUnknownVersionStagingBlocked(t *testing.T) {
	t.Parallel()

	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()
	p.advisorPostCommitCheckpointDir = t.TempDir()
	pre := p.state.GetRevision()
	post, err := nextAdvisorRevision(pre)
	require.NoError(t, err)

	respBytes := mustMarshalProto(t, &advisorapi.ListAndWatchResponse{})
	record := advisorPostCommitCheckpoint{
		Version:           99,
		PreCommitRevision: &pre,
		Revision:          post,
		Response:          append([]byte(advisorPostCommitWALV2Magic), respBytes...),
	}
	record.Checksum = advisorPostCommitCheckpointChecksum(
		99, &pre, post, respBytes, nil, false)
	data, err := json.Marshal(record)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(p.advisorPostCommitStagingPath(), data, 0o600))

	err = p.restoreAdvisorPostCommitTarget()
	require.ErrorContains(t, err, "unsupported advisor checkpoint version 99")
	require.Nil(t, p.currentAdvisorPostCommitTarget())
	require.FileExists(t, p.advisorPostCommitStagingPath())
}

// TestWALRecoverySupersededRecordIsCleanedWithoutReplay proves a durable record
// whose revision the canonical state already advanced past (stale CAS) is
// cleaned up on restart and is never replayed over newer state.
func TestWALRecoverySupersededRecordIsCleanedWithoutReplay(t *testing.T) {
	t.Parallel()

	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()
	p.advisorPostCommitCheckpointDir = t.TempDir()
	pre := p.state.GetRevision()
	oldPost, err := nextAdvisorRevision(pre)
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(
		p.advisorPostCommitCheckpointPath(),
		legacyV2CheckpointBytes(t, pre, oldPost,
			&advisorapi.ListAndWatchResponse{
				ExtraEntries: []*advisorsvc.CalculationInfo{{CgroupPath: "/superseded"}},
			}, nil, false),
		0o600))
	// Advance canonical two revisions past the stale record.
	commitStateToNext(t, p)
	commitStateToNext(t, p)
	require.Greater(t, p.state.GetRevision(), oldPost)

	require.NoError(t, p.restoreAdvisorPostCommitTarget())
	require.Nil(t, p.currentAdvisorPostCommitTarget())
	require.NoFileExists(t, p.advisorPostCommitCheckpointPath())
	require.NoFileExists(t, p.advisorPostCommitStagingPath())
}

// TestWALRecoveryUnknownAheadRecordBlockedAndRetained proves a validly-formed
// record parked ahead of the canonical revision (current < preCommitRevision)
// cannot be proven to be our own uncommitted intent. Unlike a known
// BeforeCommit staging record, it must fail closed and be retained, never
// arbitrarily cleaned up as if it were obsolete.
func TestWALRecoveryUnknownAheadRecordBlockedAndRetained(t *testing.T) {
	t.Parallel()

	for _, slot := range []string{"active", "staging"} {
		slot := slot
		t.Run(slot, func(t *testing.T) {
			t.Parallel()
			p, cleanup := newReclaimReuseTestPolicy(t)
			defer cleanup()
			dir := t.TempDir()
			p.advisorPostCommitCheckpointDir = dir
			pre := p.state.GetRevision()
			futurePre := pre + 4
			futurePost, err := nextAdvisorRevision(futurePre)
			require.NoError(t, err)

			bytes := legacyV2CheckpointBytes(t, futurePre, futurePost,
				&advisorapi.ListAndWatchResponse{
					ExtraEntries: []*advisorsvc.CalculationInfo{{CgroupPath: "/unknown-ahead"}},
				}, nil, false)
			path := p.advisorPostCommitCheckpointPath()
			if slot == "staging" {
				path = p.advisorPostCommitStagingPath()
			}
			require.NoError(t, os.WriteFile(path, bytes, 0o600))
			// Canonical remains at pre; the unknown ahead record must not replay.
			require.Equal(t, pre, p.state.GetRevision())

			err = p.restoreAdvisorPostCommitTarget()
			require.ErrorContains(t, err, "unexpected ahead advisor post-commit checkpoint")
			require.Nil(t, p.currentAdvisorPostCommitTarget())
			require.FileExists(t, path, "unknown ahead record must be retained, not deleted")
		})
	}
}

// TestWALRecoveryKnownUncommittedStagingDiscardedWithoutReplay guards the
// permitted discard path: a known BeforeCommit staging record (canonical still
// at preCommitRevision, staging revision = pre+1) is an uncommitted intent that
// may be discarded without replaying physical state.
func TestWALRecoveryKnownUncommittedStagingDiscardedWithoutReplay(t *testing.T) {
	t.Parallel()

	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()
	p.advisorPostCommitCheckpointDir = t.TempDir()
	pre := p.state.GetRevision()
	post, err := nextAdvisorRevision(pre)
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(
		p.advisorPostCommitStagingPath(),
		legacyV2CheckpointBytes(t, pre, post,
			&advisorapi.ListAndWatchResponse{
				ExtraEntries: []*advisorsvc.CalculationInfo{{CgroupPath: "/uncommitted"}},
			}, nil, false),
		0o600))
	// Canonical still at pre == preCommitRevision: known uncommitted intent.
	require.Equal(t, pre, p.state.GetRevision())

	require.NoError(t, p.restoreAdvisorPostCommitTarget())
	require.Nil(t, p.currentAdvisorPostCommitTarget())
	require.NoFileExists(t, p.advisorPostCommitCheckpointPath())
	require.NoFileExists(t, p.advisorPostCommitStagingPath())
}

// TestWALRecoveryCrashAfterCommitBeforePromoteReplaysStaging guards the crash
// commit window: staging durable + canonical advanced + active absent → the
// staging record is replayed and atomically promoted to active.
func TestWALRecoveryCrashAfterCommitBeforePromoteReplaysStaging(t *testing.T) {
	t.Parallel()

	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()
	dir := t.TempDir()
	p.advisorPostCommitCheckpointDir = dir
	pre := p.state.GetRevision()
	post, err := nextAdvisorRevision(pre)
	require.NoError(t, err)

	resp := &advisorapi.ListAndWatchResponse{
		ExtraEntries: []*advisorsvc.CalculationInfo{{CgroupPath: "/crash-window"}},
	}
	require.NoError(t, os.WriteFile(
		p.advisorPostCommitStagingPath(),
		legacyV2CheckpointBytes(t, pre, post, resp, nil, false),
		0o600))
	require.NoFileExists(t, p.advisorPostCommitCheckpointPath())
	commitStateToNext(t, p)

	require.NoError(t, p.restoreAdvisorPostCommitTarget())
	restored := p.currentAdvisorPostCommitTarget()
	require.NotNil(t, restored)
	require.Equal(t, post, restored.revision)
	require.True(t, proto.Equal(resp, restored.response))
	require.FileExists(t, filepath.Join(dir, advisorPostCommitCheckpointName))
	require.NoFileExists(t, filepath.Join(dir, advisorPostCommitStagingName))
}
