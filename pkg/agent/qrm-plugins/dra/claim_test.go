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

package dra

import (
	"context"
	"testing"

	resourcev1 "k8s.io/api/resource/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	kubefake "k8s.io/client-go/kubernetes/fake"
	drapluginapi "k8s.io/kubelet/pkg/apis/dra/v1"

	"github.com/stretchr/testify/require"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/dra/state"
)

func TestResolveClaim(t *testing.T) {
	tests := []struct {
		name             string
		claim            *resourcev1.ResourceClaim
		ref              *drapluginapi.Claim
		wantErr          string
		wantNonRetryable bool
	}{
		{
			name:  "valid",
			claim: allocatedClaim("ns-a", "claim-a", "uid-a"),
			ref:   claimRef("ns-a", "claim-a", "uid-a"),
		},
		{
			name:             "replaced",
			claim:            allocatedClaim("ns-a", "claim-a", "uid-new"),
			ref:              claimRef("ns-a", "claim-a", "uid-old"),
			wantErr:          "was replaced",
			wantNonRetryable: true,
		},
		{
			name:             "unallocated",
			claim:            unallocatedClaim("ns-a", "claim-a", "uid-a"),
			ref:              claimRef("ns-a", "claim-a", "uid-a"),
			wantErr:          "is not allocated",
			wantNonRetryable: true,
		},
		{
			name:    "missing",
			ref:     claimRef("ns-a", "claim-a", "uid-a"),
			wantErr: "get ResourceClaim ns-a/claim-a",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := kubefake.NewSimpleClientset()
			if tt.claim != nil {
				_, err := client.ResourceV1().ResourceClaims(tt.claim.Namespace).
					Create(context.Background(), tt.claim, metav1.CreateOptions{})
				require.NoError(t, err)
			}
			plugin := &StaticPolicy{kubeClient: client}

			claim, err := plugin.resolveClaim(context.Background(), tt.ref)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				require.Equal(t, tt.wantNonRetryable, IsNonRetryable(err))
				return
			}
			require.NoError(t, err)
			require.Equal(t, types.UID("uid-a"), claim.UID)
		})
	}
}

func TestPreparedDeviceResponse(t *testing.T) {
	response := preparedDeviceResponse(state.PreparedDevice{
		Requests:     []string{"request-a/subrequest-a", "request-b"},
		PoolName:     "pool-a",
		DeviceName:   "device-a",
		CDIDeviceIDs: []string{"vendor/device-a"},
	})

	require.Equal(t, []string{"request-a", "request-b"}, response.RequestNames)
	require.Equal(t, "pool-a", response.PoolName)
	require.Equal(t, "device-a", response.DeviceName)
	require.Equal(t, []string{"vendor/device-a"}, response.CdiDeviceIds)
}

func allocatedClaim(namespace, name, uid string) *resourcev1.ResourceClaim {
	claim := unallocatedClaim(namespace, name, uid)
	claim.Status.Allocation = &resourcev1.AllocationResult{}
	return claim
}

func unallocatedClaim(namespace, name, uid string) *resourcev1.ResourceClaim {
	return &resourcev1.ResourceClaim{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      name,
			UID:       types.UID(uid),
		},
	}
}

func claimRef(namespace, name, uid string) *drapluginapi.Claim {
	return &drapluginapi.Claim{
		Namespace: namespace,
		Name:      name,
		Uid:       uid,
	}
}
