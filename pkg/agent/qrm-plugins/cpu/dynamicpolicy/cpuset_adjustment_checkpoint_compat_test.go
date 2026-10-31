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
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/gogo/protobuf/proto"
	"github.com/stretchr/testify/require"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/advisorsvc"
	checkpointutils "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/utils"
	advisorapi "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/cpuadvisor"
)

// commitStateTo advances the canonical state revision to the revision that
// follows current, so a post-commit WAL record can be replayed against it.
func commitStateToNext(t *testing.T, p *DynamicPolicy) {
	t.Helper()
	require.NoError(t, p.state.CommitAdvisorStateIfRevision(
		p.state.GetRevision(),
		p.state.GetPodEntries(),
		p.state.GetMachineState(),
		p.state.GetAllowSharedCoresOverlapReclaimedCores(),
		p.state.GetDisableDedicatedCoresOverlapReclaimedCores(),
		true,
	))
}

// legacyV0CheckpointBytes hand-builds the v0 (pre-checksum) WAL envelope, exactly
// as the pre-V2 writer emitted it: no version, no checksum, no pre-commit
// revision, no magic prefix, just {revision, response}.
func legacyV0CheckpointBytes(t *testing.T, revision uint64, resp *advisorapi.ListAndWatchResponse) []byte {
	t.Helper()
	respBytes, err := proto.Marshal(resp)
	require.NoError(t, err)
	data, err := json.Marshal(advisorPostCommitCheckpoint{
		Revision: revision,
		Response: respBytes,
	})
	require.NoError(t, err)
	return data
}

// legacyV2CheckpointBytes hand-builds the v2 envelope with a correct checksum and
// magic prefix, as the legacy writer emitted it on disk.
func legacyV2CheckpointBytes(
	t *testing.T,
	pre, post uint64,
	resp *advisorapi.ListAndWatchResponse,
	transition *advisorMigrationCheckpointTransitionWAL,
	applied bool,
) []byte {
	t.Helper()
	respBytes, err := proto.Marshal(resp)
	require.NoError(t, err)
	record := advisorPostCommitCheckpoint{
		Version:                       advisorPostCommitCheckpointVersion,
		PreCommitRevision:             &pre,
		Revision:                      post,
		Response:                      append([]byte(advisorPostCommitWALV2Magic), respBytes...),
		MigrationCheckpointTransition: transition,
		Applied:                       applied,
	}
	record.Checksum = advisorPostCommitCheckpointChecksum(
		record.Version, &pre, post, respBytes, transition, applied)
	data, err := json.Marshal(record)
	require.NoError(t, err)
	return data
}

// TestAdvisorCheckpointCompatV0LegacyBytesLoadViaStore proves the new
// CheckpointStore-backed restore can read a v0 legacy staging record written by
// the pre-checksum writer, replay it, and atomically promote it to active.
func TestAdvisorCheckpointCompatV0LegacyBytesLoadViaStore(t *testing.T) {
	t.Parallel()

	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()
	dir := t.TempDir()
	p.advisorPostCommitCheckpointDir = dir
	revision := p.state.GetRevision()

	resp := &advisorapi.ListAndWatchResponse{
		ExtraEntries: []*advisorsvc.CalculationInfo{{CgroupPath: "/legacy-v0-store"}},
	}
	require.NoError(t, os.WriteFile(
		p.advisorPostCommitStagingPath(),
		legacyV0CheckpointBytes(t, revision, resp),
		0o600))

	require.NoError(t, p.restoreAdvisorPostCommitTarget())
	restored := p.currentAdvisorPostCommitTarget()
	require.NotNil(t, restored)
	require.Zero(t, restored.checkpointVersion, "v0 record must keep its zero version")
	require.True(t, proto.Equal(resp, restored.response))
	require.FileExists(t, p.advisorPostCommitCheckpointPath(), "replayed v0 staging must be promoted to active")
	require.NoFileExists(t, p.advisorPostCommitStagingPath())
}

// TestAdvisorCheckpointCompatV2LegacyEnvelopeLoadViaStore proves the new
// CheckpointStore-backed restore reads a hand-built v2 envelope (correct magic +
// checksum) byte-for-byte, including the migration transition.
func TestAdvisorCheckpointCompatV2LegacyEnvelopeLoadViaStore(t *testing.T) {
	t.Parallel()

	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()
	p.advisorPostCommitCheckpointDir = t.TempDir()
	pre := p.state.GetRevision()
	post, err := nextAdvisorRevision(pre)
	require.NoError(t, err)

	resp := &advisorapi.ListAndWatchResponse{
		ExtraEntries: []*advisorsvc.CalculationInfo{{CgroupPath: "/legacy-v2-active"}},
	}
	require.NoError(t, os.WriteFile(
		p.advisorPostCommitCheckpointPath(),
		legacyV2CheckpointBytes(t, pre, post, resp, nil, false),
		0o600))
	commitStateToNext(t, p)

	require.NoError(t, p.restoreAdvisorPostCommitTarget())
	restored := p.currentAdvisorPostCommitTarget()
	require.NotNil(t, restored)
	require.Equal(t, advisorPostCommitCheckpointVersion, restored.checkpointVersion)
	require.Equal(t, pre, restored.preCommitRevision)
	require.Equal(t, post, restored.revision)
	require.True(t, proto.Equal(resp, restored.response))
}

// TestAdvisorCheckpointCompatCrashStagingOnlyPromotesAfterRestart simulates a
// crash that happened after staging was written and the canonical state advanced,
// but before the staging->active rename. The new restore must select the staging
// record and promote it.
func TestAdvisorCheckpointCompatCrashStagingOnlyPromotesAfterRestart(t *testing.T) {
	t.Parallel()

	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()
	p.advisorPostCommitCheckpointDir = t.TempDir()
	pre := p.state.GetRevision()
	post, err := nextAdvisorRevision(pre)
	require.NoError(t, err)

	resp := &advisorapi.ListAndWatchResponse{
		ExtraEntries: []*advisorsvc.CalculationInfo{{CgroupPath: "/crash-staging-only"}},
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
	require.Equal(t, "/crash-staging-only", restored.response.ExtraEntries[0].CgroupPath)
	require.FileExists(t, p.advisorPostCommitCheckpointPath(), "staging must be promoted to active")
	require.NoFileExists(t, p.advisorPostCommitStagingPath())
}

// TestAdvisorCheckpointCompatPromoteInterruptedPrefersNewerStaging simulates a
// crash mid-promotion: both active (older revision) and staging (newer revision)
// exist. With canonical state advanced to the staging revision, the four-way
// decision must replay the newer staging and promote it over the stale active.
func TestAdvisorCheckpointCompatPromoteInterruptedPrefersNewerStaging(t *testing.T) {
	t.Parallel()

	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()
	p.advisorPostCommitCheckpointDir = t.TempDir()
	// Advance once so the stale active fixture below uses a valid (pre-1, pre)
	// revision transition instead of underflowing a zero starting revision.
	commitStateToNext(t, p)
	pre := p.state.GetRevision()
	post, err := nextAdvisorRevision(pre)
	require.NoError(t, err)

	oldResp := &advisorapi.ListAndWatchResponse{
		ExtraEntries: []*advisorsvc.CalculationInfo{{CgroupPath: "/old-active"}},
	}
	newResp := &advisorapi.ListAndWatchResponse{
		ExtraEntries: []*advisorsvc.CalculationInfo{{CgroupPath: "/new-staging"}},
	}
	// Active holds the decodable pre-commit (stale) record; staging holds the
	// post-commit record. The stale active must decode cleanly so fail-closed
	// recovery only blocks on genuinely undecodable slots.
	require.NoError(t, os.WriteFile(
		p.advisorPostCommitCheckpointPath(),
		legacyV2CheckpointBytes(t, pre-1, pre, oldResp, nil, false),
		0o600))
	require.NoError(t, os.WriteFile(
		p.advisorPostCommitStagingPath(),
		legacyV2CheckpointBytes(t, pre, post, newResp, nil, false),
		0o600))
	commitStateToNext(t, p) // state now at post

	require.NoError(t, p.restoreAdvisorPostCommitTarget())
	restored := p.currentAdvisorPostCommitTarget()
	require.NotNil(t, restored)
	require.Equal(t, "/new-staging", restored.response.ExtraEntries[0].CgroupPath,
		"newer staging must win over stale active after interrupted promote")
	require.FileExists(t, p.advisorPostCommitCheckpointPath())
	require.NoFileExists(t, p.advisorPostCommitStagingPath())
}

// TestAdvisorCheckpointCompatCorruptStagingFailsClosed proves a corrupt staging
// record (bad checksum) surfaces as a corrupted-slot error and is left on disk,
// rather than being silently promoted or deleted.
func TestAdvisorCheckpointCompatCorruptStagingFailsClosed(t *testing.T) {
	t.Parallel()

	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()
	dir := t.TempDir()
	p.advisorPostCommitCheckpointDir = dir
	stagingPath := p.advisorPostCommitStagingPath()
	require.NoError(t, os.WriteFile(stagingPath, []byte("{broken-staging"), 0o600))

	err := p.restoreAdvisorPostCommitTarget()
	require.ErrorContains(t, err, "corrupted staging advisor post-commit checkpoint")
	require.Nil(t, p.currentAdvisorPostCommitTarget())
	require.FileExists(t, stagingPath, "a corrupted staging record must remain for operator recovery")
}

// TestAdvisorCheckpointCompatNewWriterOutputIsLegacyReadable proves the bytes
// produced by the new CheckpointStore-backed writer are consumable by an
// independent legacy-format parser (raw JSON decode + magic check + recomputed
// sha256 checksum), i.e. on-disk format is unchanged.
func TestAdvisorCheckpointCompatNewWriterOutputIsLegacyReadable(t *testing.T) {
	t.Parallel()

	p, cleanup := newReclaimReuseTestPolicy(t)
	defer cleanup()
	p.advisorPostCommitCheckpointDir = t.TempDir()
	pre := p.state.GetRevision()
	post, err := nextAdvisorRevision(pre)
	require.NoError(t, err)

	target := cloneAdvisorPostCommitTarget(
		&advisorapi.ListAndWatchResponse{
			ExtraEntries: []*advisorsvc.CalculationInfo{{CgroupPath: "/new-writer"}},
		},
		post,
	)
	target.preCommitRevision = pre
	require.NoError(t, p.storeAdvisorPostCommitTarget(target, p.advisorPostCommitCheckpointPath()))

	// Independent reader: bypass the codec and parse the raw envelope exactly as
	// a legacy reader would.
	raw, err := os.ReadFile(p.advisorPostCommitCheckpointPath())
	require.NoError(t, err)
	var record advisorPostCommitCheckpoint
	require.NoError(t, json.Unmarshal(raw, &record))
	require.Equal(t, advisorPostCommitCheckpointVersion, record.Version)
	require.NotNil(t, record.PreCommitRevision)
	require.Equal(t, pre, *record.PreCommitRevision)
	require.Equal(t, post, record.Revision)
	require.True(t, bytes.HasPrefix(record.Response, []byte(advisorPostCommitWALV2Magic)),
		"new writer must still emit the V2 magic fence")
	body := record.Response[len(advisorPostCommitWALV2Magic):]
	require.NotEmpty(t, record.Checksum)
	require.Equal(t,
		advisorPostCommitCheckpointChecksum(record.Version, record.PreCommitRevision, record.Revision,
			body, record.MigrationCheckpointTransition, record.Applied),
		record.Checksum, "new writer checksum must verify under the legacy digest")

	// The independent reader must also be able to proto-decode the body.
	resp := &advisorapi.ListAndWatchResponse{}
	require.NoError(t, proto.Unmarshal(body, resp))
	require.Equal(t, "/new-writer", resp.ExtraEntries[0].CgroupPath)
}

// TestAdvisorCheckpointCompatPromoteIsAtomicRenameThroughStore exercises the new
// CheckpointStore.Promote directly: staging is renamed to active and the staging
// name no longer resolves, while the bytes are unchanged.
func TestAdvisorCheckpointCompatPromoteIsAtomicRenameThroughStore(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	store := checkpointutils.NewFileCheckpointStore(dir)

	// Seed a staging record directly through the store, then promote it.
	require.NoError(t, store.Store(advisorPostCommitStagingName, &advisorPostCommitCheckpointCodec{
		revision:      42,
		responseBytes: mustMarshalProto(t, &advisorapi.ListAndWatchResponse{}),
	}))
	require.FileExists(t, filepath.Join(dir, advisorPostCommitStagingName))
	require.NoFileExists(t, filepath.Join(dir, advisorPostCommitCheckpointName))

	require.NoError(t, store.Promote(advisorPostCommitStagingName, advisorPostCommitCheckpointName))
	require.FileExists(t, filepath.Join(dir, advisorPostCommitCheckpointName))
	require.NoFileExists(t, filepath.Join(dir, advisorPostCommitStagingName))

	// Promote of a missing staging record must surface the rename error.
	require.Error(t, store.Promote(advisorPostCommitStagingName, advisorPostCommitCheckpointName))
}

func mustMarshalProto(t *testing.T, resp *advisorapi.ListAndWatchResponse) []byte {
	t.Helper()
	b, err := proto.Marshal(resp)
	require.NoError(t, err)
	return b
}
