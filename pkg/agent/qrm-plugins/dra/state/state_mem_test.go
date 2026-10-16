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

package state

import (
	"encoding/json"
	"testing"

	resourcev1 "k8s.io/api/resource/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/stretchr/testify/require"
)

func TestNewPreparedClaimCopiesClaim(t *testing.T) {
	claim := &resourcev1.ResourceClaim{
		ObjectMeta: metav1.ObjectMeta{
			UID:       types.UID("uid-a"),
			Namespace: "ns-a",
			Name:      "claim-a",
			Labels:    map[string]string{"owner": "host-managed"},
		},
	}

	prepared := NewPreparedClaim(claim, PrepareStarted)
	claim.Labels["owner"] = "changed"

	require.Equal(t, "uid-a", prepared.UID)
	require.Equal(t, "ns-a", prepared.Namespace)
	require.Equal(t, "claim-a", prepared.Name)
	require.Equal(t, "host-managed", prepared.Labels["owner"])
	require.Equal(t, PrepareStarted, prepared.Phase)
}

func TestMemoryStateClonesValues(t *testing.T) {
	memoryState := NewMemoryState()
	claim := PreparedClaim{
		UID:           "uid-a",
		Labels:        map[string]string{"owner": "host-managed"},
		Devices:       []PreparedDevice{{Requests: []string{"request-a"}, CDIDeviceIDs: []string{"vendor/device-a"}}},
		ProviderState: json.RawMessage(`{"domain":"a"}`),
	}

	require.NoError(t, memoryState.SetPreparedClaim(claim, false))

	claim.Labels["owner"] = "changed-input"
	claim.Devices[0].Requests[0] = "changed-input"
	claim.ProviderState[0] = '['

	stored, found := memoryState.GetPreparedClaim("uid-a")
	require.True(t, found)
	require.Equal(t, "host-managed", stored.Labels["owner"])
	require.Equal(t, "request-a", stored.Devices[0].Requests[0])
	require.JSONEq(t, `{"domain":"a"}`, string(stored.ProviderState))

	stored.Labels["owner"] = "changed-output"
	stored.Devices[0].CDIDeviceIDs[0] = "changed-output"
	listed := memoryState.ListPreparedClaims()
	listed["uid-a"] = PreparedClaim{}

	storedAgain, found := memoryState.GetPreparedClaim("uid-a")
	require.True(t, found)
	require.Equal(t, "host-managed", storedAgain.Labels["owner"])
	require.Equal(t, "vendor/device-a", storedAgain.Devices[0].CDIDeviceIDs[0])
}

func TestMemoryStateSetListDelete(t *testing.T) {
	memoryState := NewMemoryState()

	require.NoError(t, memoryState.SetPreparedClaims(map[string]PreparedClaim{
		"uid-a": {UID: "uid-a"},
		"uid-b": {UID: "uid-b"},
	}, false))
	require.Len(t, memoryState.ListPreparedClaims(), 2)

	require.NoError(t, memoryState.DeletePreparedClaim("uid-a", false))
	_, found := memoryState.GetPreparedClaim("uid-a")
	require.False(t, found)
	require.NoError(t, memoryState.StoreState())
}
