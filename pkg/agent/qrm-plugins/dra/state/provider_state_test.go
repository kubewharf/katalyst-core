package state

import (
	"encoding/json"
	"testing"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/qrm/statedirectory"
	"github.com/stretchr/testify/require"
)

func TestProviderStateUpdatePreservesCorePhase(t *testing.T) {
	s := NewMemoryState()
	c := PreparedClaim{UID: "claim-a", Name: "a", Namespace: "ns", Phase: PrepareCompleted,
		Devices: []PreparedDevice{{DeviceName: "channel-0"}}}
	require.NoError(t, s.SetPreparedClaim(c, true))
	raw := json.RawMessage(`{"checkpointState":"PrepareCompleted"}`)
	require.NoError(t, s.SetProviderState(c.UID, raw))
	raw[0] = 'x'
	got, found := s.GetPreparedClaim(c.UID)
	require.True(t, found)
	require.Equal(t, c.Phase, got.Phase)
	require.Equal(t, c.Devices, got.Devices)
	require.JSONEq(t, `{"checkpointState":"PrepareCompleted"}`, string(got.ProviderState))
	require.Error(t, s.SetProviderState("unknown-claim", nil))
	require.Len(t, s.ListPreparedClaims(), 1)
}

func TestProviderStatePersistsWithCorePhase(t *testing.T) {
	conf := &statedirectory.StateDirectoryConfiguration{StateFileDirectory: t.TempDir()}
	s, err := NewCheckpointState(conf)
	require.NoError(t, err)
	c := PreparedClaim{UID: "claim-a", Phase: PrepareStarted}
	require.NoError(t, s.SetPreparedClaim(c, true))
	require.NoError(t, s.SetProviderState(c.UID, json.RawMessage(`{"checkpointState":"PrepareCompleted"}`)))
	restored, err := NewCheckpointState(conf)
	require.NoError(t, err)
	got, found := restored.GetPreparedClaim(c.UID)
	require.True(t, found)
	require.Equal(t, PrepareStarted, got.Phase)
	require.JSONEq(t, `{"checkpointState":"PrepareCompleted"}`, string(got.ProviderState))
}
