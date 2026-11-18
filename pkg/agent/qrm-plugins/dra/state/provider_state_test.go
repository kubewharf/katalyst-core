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

package state

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/qrm/statedirectory"
)

func TestProviderStateUpdatePreservesCorePhase(t *testing.T) {
	t.Parallel()
	s := NewMemoryState()
	c := PreparedClaim{
		UID: "claim-a", Name: "a", Namespace: "ns", Phase: PrepareCompleted,
		Devices: []PreparedDevice{{DeviceName: "channel-0"}},
	}
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
	t.Parallel()
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
