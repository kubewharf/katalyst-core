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
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	resourcev1 "k8s.io/api/resource/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	kubefake "k8s.io/client-go/kubernetes/fake"
	drapluginapi "k8s.io/kubelet/pkg/apis/dra/v1"

	"github.com/stretchr/testify/require"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/dra/state"
)

type fakeProvider struct {
	mu             sync.Mutex
	prepareResults []PrepareResult
	prepareErrors  []error
	prepareCalls   int
	unprepareCalls int
	prepareFunc    func(*resourcev1.ResourceClaim) (PrepareResult, error)
}

func (*fakeProvider) Start(context.Context, StateReader) error { return nil }

func (*fakeProvider) Stop() error { return nil }

func (*fakeProvider) Inventory(context.Context) (Inventory, error) {
	return Inventory{}, nil
}

func (p *fakeProvider) Prepare(
	_ context.Context,
	claim *resourcev1.ResourceClaim,
) (PrepareResult, error) {
	p.mu.Lock()
	index := p.prepareCalls
	p.prepareCalls++
	prepareFunc := p.prepareFunc
	p.mu.Unlock()
	if prepareFunc != nil {
		return prepareFunc(claim)
	}
	if index < len(p.prepareErrors) && p.prepareErrors[index] != nil {
		return PrepareResult{}, p.prepareErrors[index]
	}
	if index < len(p.prepareResults) {
		return p.prepareResults[index], nil
	}
	return PrepareResult{}, nil
}

func (p *fakeProvider) Unprepare(context.Context, state.PreparedClaim) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.unprepareCalls++
	return nil
}

func TestPrepareOnePersistsTransitionsAndIsIdempotent(t *testing.T) {
	provider := &fakeProvider{prepareResults: []PrepareResult{{
		Devices: []state.PreparedDevice{{
			Requests:     []string{"request-a/subrequest-a"},
			PoolName:     "pool-a",
			DeviceName:   "device-a",
			CDIDeviceIDs: []string{"vendor/device-a"},
		}},
		ProviderState: json.RawMessage(`{"channel":0}`),
	}}}
	plugin := newOperationTestPlugin(provider)
	claim := allocatedDeviceClaim(
		"ns-a", "claim-a", "uid-a", "request-a", false,
	)

	devices, err := plugin.prepareOne(context.Background(), claim)
	require.NoError(t, err)
	require.Equal(t, provider.prepareResults[0].Devices, devices)

	stored, found := plugin.state.GetPreparedClaim("uid-a")
	require.True(t, found)
	require.Equal(t, state.PrepareCompleted, stored.Phase)
	require.Equal(t, provider.prepareResults[0].ProviderState, stored.ProviderState)

	devices, err = plugin.prepareOne(context.Background(), claim)
	require.NoError(t, err)
	require.Equal(t, provider.prepareResults[0].Devices, devices)
	require.Equal(t, 1, provider.prepareCalls)
}

func TestPrepareOneKeepsStartedStateAfterProviderFailure(t *testing.T) {
	provider := &fakeProvider{prepareErrors: []error{
		errors.New("host service unavailable"),
	}}
	plugin := newOperationTestPlugin(provider)
	claim := allocatedDeviceClaim(
		"ns-a", "claim-a", "uid-a", "request-a", false,
	)

	_, err := plugin.prepareOne(context.Background(), claim)
	require.ErrorContains(t, err, "host service unavailable")

	stored, found := plugin.state.GetPreparedClaim("uid-a")
	require.True(t, found)
	require.Equal(t, state.PrepareStarted, stored.Phase)
}

func TestPrepareOneRejectsOverlappingNonAdminDevice(t *testing.T) {
	provider := &fakeProvider{}
	plugin := newOperationTestPlugin(provider)
	existing := allocatedDeviceClaim(
		"ns-a", "existing", "uid-existing", "request-a", false,
	)
	require.NoError(t, plugin.state.SetPreparedClaim(
		state.NewPreparedClaim(existing, state.PrepareCompleted),
		false,
	))

	claim := allocatedDeviceClaim(
		"ns-a", "new", "uid-new", "request-b", false,
	)
	_, err := plugin.prepareOne(context.Background(), claim)
	require.ErrorContains(t, err, "already prepared")
	require.True(t, IsNonRetryable(err))
	require.Zero(t, provider.prepareCalls)
}

func TestPrepareOneAllowsAdminDeviceOverlap(t *testing.T) {
	provider := &fakeProvider{}
	plugin := newOperationTestPlugin(provider)
	existing := allocatedDeviceClaim(
		"ns-a", "existing", "uid-existing", "request-a", false,
	)
	require.NoError(t, plugin.state.SetPreparedClaim(
		state.NewPreparedClaim(existing, state.PrepareCompleted),
		false,
	))

	claim := allocatedDeviceClaim(
		"ns-a", "admin", "uid-admin", "request-b", true,
	)
	_, err := plugin.prepareOne(context.Background(), claim)
	require.NoError(t, err)
	require.Equal(t, 1, provider.prepareCalls)
}

func TestRunWithRetry(t *testing.T) {
	t.Run("retryable error", func(t *testing.T) {
		attempts := 0
		got, err := runWithRetry(
			context.Background(),
			time.Second,
			"uid-a",
			func(context.Context) (string, error) {
				attempts++
				if attempts < 3 {
					return "", errors.New("temporary")
				}
				return "prepared", nil
			},
		)
		require.NoError(t, err)
		require.Equal(t, "prepared", got)
		require.Equal(t, 3, attempts)
	})

	t.Run("non-retryable error", func(t *testing.T) {
		attempts := 0
		_, err := runWithRetry(
			context.Background(),
			time.Second,
			"uid-a",
			func(context.Context) (string, error) {
				attempts++
				return "", NonRetryable(errors.New("invalid allocation"))
			},
		)
		require.ErrorContains(t, err, "invalid allocation")
		require.Equal(t, 1, attempts)
	})

	t.Run("deadline", func(t *testing.T) {
		attempts := 0
		_, err := runWithRetry(
			context.Background(),
			20*time.Millisecond,
			"uid-a",
			func(context.Context) (string, error) {
				attempts++
				return "", errors.New("temporary")
			},
		)
		require.ErrorIs(t, err, context.DeadlineExceeded)
		require.GreaterOrEqual(t, attempts, 1)
	})
}

func TestNodePrepareResourcesReturnsOneResponsePerClaim(t *testing.T) {
	valid := allocatedDeviceClaim(
		"ns-a", "valid", "uid-valid", "request-a", false,
	)
	unallocated := unallocatedClaim("ns-a", "invalid", "uid-invalid")
	client := kubefake.NewSimpleClientset(valid, unallocated)
	provider := &fakeProvider{prepareResults: []PrepareResult{{
		Devices: []state.PreparedDevice{{
			Requests:     []string{"request-a/subrequest-a"},
			PoolName:     "pool-a",
			DeviceName:   "device-a",
			CDIDeviceIDs: []string{"vendor/device-a"},
		}},
	}}}
	plugin := newOperationTestPlugin(provider)
	plugin.kubeClient = client
	plugin.retryTimeout = time.Second

	response, err := plugin.NodePrepareResources(
		context.Background(),
		&drapluginapi.NodePrepareResourcesRequest{Claims: []*drapluginapi.Claim{
			claimRef("ns-a", "valid", "uid-valid"),
			claimRef("ns-a", "invalid", "uid-invalid"),
		}},
	)
	require.NoError(t, err)
	require.Len(t, response.Claims, 2)
	require.Empty(t, response.Claims["uid-valid"].Error)
	require.Equal(
		t,
		[]string{"request-a"},
		response.Claims["uid-valid"].Devices[0].RequestNames,
	)
	require.ErrorContains(
		t,
		errors.New(response.Claims["uid-invalid"].Error),
		"is not allocated",
	)
}

func TestNodePrepareResourcesDoesNotBlockOtherClaims(t *testing.T) {
	retrying := allocatedDeviceClaim(
		"ns-a", "retrying", "uid-retrying", "request-a", false,
	)
	valid := allocatedDeviceClaim(
		"ns-a", "valid", "uid-valid", "request-b", false,
	)
	valid.Status.Allocation.Devices.Results[0].Device = "device-b"
	client := kubefake.NewSimpleClientset(retrying, valid)
	provider := &fakeProvider{
		prepareFunc: func(claim *resourcev1.ResourceClaim) (PrepareResult, error) {
			if claim.Name == "retrying" {
				return PrepareResult{}, errors.New("temporary")
			}
			return PrepareResult{}, nil
		},
	}
	plugin := newOperationTestPlugin(provider)
	plugin.kubeClient = client
	plugin.retryTimeout = time.Second
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	response, err := plugin.NodePrepareResources(
		ctx,
		&drapluginapi.NodePrepareResourcesRequest{Claims: []*drapluginapi.Claim{
			claimRef("ns-a", "retrying", "uid-retrying"),
			claimRef("ns-a", "valid", "uid-valid"),
		}},
	)
	require.NoError(t, err)
	require.ErrorContains(
		t,
		errors.New(response.Claims["uid-retrying"].Error),
		context.DeadlineExceeded.Error(),
	)
	require.Empty(t, response.Claims["uid-valid"].Error)
}

func TestUnprepareOneIsIdempotent(t *testing.T) {
	provider := &fakeProvider{}
	plugin := newOperationTestPlugin(provider)
	claim := allocatedDeviceClaim(
		"ns-a", "claim-a", "uid-a", "request-a", false,
	)
	require.NoError(t, plugin.state.SetPreparedClaim(
		state.NewPreparedClaim(claim, state.PrepareCompleted),
		false,
	))

	ref := claimRefValue("ns-a", "claim-a", "uid-a")
	require.NoError(t, plugin.unprepareOne(context.Background(), ref))
	require.NoError(t, plugin.unprepareOne(context.Background(), ref))
	require.Equal(t, 1, provider.unprepareCalls)
	_, found := plugin.state.GetPreparedClaim("uid-a")
	require.False(t, found)
}

func newOperationTestPlugin(provider ResourceBackend) *StaticPolicy {
	return &StaticPolicy{
		backend:      provider,
		retryTimeout: time.Second,
		state:        state.NewMemoryState(),
	}
}

func allocatedDeviceClaim(
	namespace, name, uid, request string,
	adminAccess bool,
) *resourcev1.ResourceClaim {
	claim := &resourcev1.ResourceClaim{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      name,
			UID:       types.UID(uid),
		},
		Status: resourcev1.ResourceClaimStatus{
			Allocation: &resourcev1.AllocationResult{
				Devices: resourcev1.DeviceAllocationResult{
					Results: []resourcev1.DeviceRequestAllocationResult{{
						Request:     request,
						Driver:      "test.dra.example.com",
						Pool:        "pool-a",
						Device:      "device-a",
						AdminAccess: boolPointer(adminAccess),
					}},
				},
			},
		},
	}
	return claim
}

func boolPointer(value bool) *bool {
	return &value
}
