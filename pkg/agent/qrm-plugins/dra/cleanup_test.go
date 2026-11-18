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
	"errors"
	"testing"
	"time"

	resourcev1 "k8s.io/api/resource/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kubefake "k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"

	"github.com/stretchr/testify/require"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/dra/state"
)

type cleanupProvider struct {
	unprepareErrors []error
	unprepareCalls  int
	unprepareFunc   func(context.Context) error
}

func (*cleanupProvider) Start(context.Context, StateReader) error { return nil }

func (*cleanupProvider) Stop() error { return nil }

func (*cleanupProvider) Inventory(context.Context) (Inventory, error) {
	return Inventory{}, nil
}

func (*cleanupProvider) Prepare(
	context.Context,
	*resourcev1.ResourceClaim,
) (PrepareResult, error) {
	return PrepareResult{}, nil
}

func (p *cleanupProvider) Unprepare(
	ctx context.Context,
	_ state.PreparedClaim,
) error {
	index := p.unprepareCalls
	p.unprepareCalls++
	if p.unprepareFunc != nil {
		return p.unprepareFunc(ctx)
	}
	if index < len(p.unprepareErrors) {
		return p.unprepareErrors[index]
	}
	return nil
}

type failingDeleteState struct {
	state.State
	deleteErrors []error
	deleteCalls  int
}

func (s *failingDeleteState) DeletePreparedClaim(
	uid string,
	persist bool,
) error {
	index := s.deleteCalls
	s.deleteCalls++
	if index < len(s.deleteErrors) && s.deleteErrors[index] != nil {
		requireNoError := s.State.DeletePreparedClaim(uid, false)
		if requireNoError != nil {
			return requireNoError
		}
		return s.deleteErrors[index]
	}
	return s.State.DeletePreparedClaim(uid, persist)
}

func TestCleanupResidualClaimWaitsForGracePeriod(t *testing.T) {
	for _, phase := range []state.ClaimPhase{
		state.PrepareStarted,
		state.PrepareCompleted,
	} {
		t.Run(string(phase), func(t *testing.T) {
			provider := &cleanupProvider{}
			plugin := newCleanupTestPlugin(
				provider,
				kubefake.NewSimpleClientset(),
			)
			addPreparedClaim(t, plugin.state, phase, "uid-a")

			for range 9 {
				require.NoError(t, plugin.cleanupResidualClaims())
			}
			require.Zero(t, provider.unprepareCalls)
			_, found := plugin.state.GetPreparedClaim("uid-a")
			require.True(t, found)

			require.NoError(t, plugin.cleanupResidualClaims())
			require.Equal(t, 1, provider.unprepareCalls)
			_, found = plugin.state.GetPreparedClaim("uid-a")
			require.False(t, found)
			require.NotContains(t, plugin.residualHitMap, "uid-a")
		})
	}
}

func TestCleanupResidualClaimResetsWhenClaimReappears(t *testing.T) {
	provider := &cleanupProvider{}
	client := kubefake.NewSimpleClientset()
	plugin := newCleanupTestPlugin(provider, client)
	addPreparedClaim(t, plugin.state, state.PrepareCompleted, "uid-a")

	require.NoError(t, plugin.cleanupResidualClaims())
	require.Equal(t, int64(1), plugin.residualHitMap["uid-a"])

	_, err := client.ResourceV1().ResourceClaims("ns-a").Create(
		context.Background(),
		allocatedClaim("ns-a", "claim-a", "uid-a"),
		metav1.CreateOptions{},
	)
	require.NoError(t, err)
	require.NoError(t, plugin.cleanupResidualClaims())
	require.NotContains(t, plugin.residualHitMap, "uid-a")

	require.NoError(t, client.ResourceV1().ResourceClaims("ns-a").Delete(
		context.Background(),
		"claim-a",
		metav1.DeleteOptions{},
	))
	for range 9 {
		require.NoError(t, plugin.cleanupResidualClaims())
	}
	require.Zero(t, provider.unprepareCalls)
}

func TestCleanupResidualClaimIgnoresTemporaryGetError(t *testing.T) {
	provider := &cleanupProvider{}
	client := kubefake.NewSimpleClientset()
	temporaryFailure := true
	client.PrependReactor(
		"get",
		"resourceclaims",
		func(ktesting.Action) (bool, runtime.Object, error) {
			if temporaryFailure {
				return true, nil, errors.New("API unavailable")
			}
			return false, nil, nil
		},
	)
	plugin := newCleanupTestPlugin(provider, client)
	addPreparedClaim(t, plugin.state, state.PrepareCompleted, "uid-a")

	err := plugin.cleanupResidualClaims()
	require.ErrorContains(t, err, "API unavailable")
	require.NotContains(t, plugin.residualHitMap, "uid-a")

	temporaryFailure = false
	require.NoError(t, plugin.cleanupResidualClaims())
	require.Equal(t, int64(1), plugin.residualHitMap["uid-a"])
}

func TestCleanupResidualClaimTreatsReplacementAsMissing(t *testing.T) {
	provider := &cleanupProvider{}
	client := kubefake.NewSimpleClientset(
		allocatedClaim("ns-a", "claim-a", "uid-new"),
	)
	plugin := newCleanupTestPlugin(provider, client)
	addPreparedClaim(t, plugin.state, state.PrepareCompleted, "uid-old")

	for range 10 {
		require.NoError(t, plugin.cleanupResidualClaims())
	}
	require.Equal(t, 1, provider.unprepareCalls)
	_, found := plugin.state.GetPreparedClaim("uid-old")
	require.False(t, found)
}

func TestCleanupResidualClaimRetriesFailedCleanup(t *testing.T) {
	tests := []struct {
		name          string
		providerError error
		deleteError   error
	}{
		{
			name:          "provider",
			providerError: errors.New("host cleanup failed"),
		},
		{
			name:        "checkpoint",
			deleteError: errors.New("checkpoint write failed"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := &cleanupProvider{
				unprepareErrors: []error{tt.providerError},
			}
			plugin := newCleanupTestPlugin(
				provider,
				kubefake.NewSimpleClientset(),
			)
			if tt.deleteError != nil {
				plugin.state = &failingDeleteState{
					State:        plugin.state,
					deleteErrors: []error{tt.deleteError},
				}
			}
			addPreparedClaim(
				t,
				plugin.state,
				state.PrepareCompleted,
				"uid-a",
			)

			for range 9 {
				require.NoError(t, plugin.cleanupResidualClaims())
			}
			err := plugin.cleanupResidualClaims()
			require.Error(t, err)
			_, found := plugin.state.GetPreparedClaim("uid-a")
			require.True(t, found)
			require.Equal(t, int64(10), plugin.residualHitMap["uid-a"])

			require.NoError(t, plugin.cleanupResidualClaims())
			_, found = plugin.state.GetPreparedClaim("uid-a")
			require.False(t, found)
			require.NotContains(t, plugin.residualHitMap, "uid-a")
		})
	}
}

func TestCleanupResidualClaimUsesPluginContext(t *testing.T) {
	unprepareStarted := make(chan struct{})
	provider := &cleanupProvider{
		unprepareFunc: func(ctx context.Context) error {
			close(unprepareStarted)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(100 * time.Millisecond):
				return errors.New("cleanup context was not canceled")
			}
		},
	}
	plugin := newCleanupTestPlugin(
		provider,
		kubefake.NewSimpleClientset(),
	)
	runContext, cancel := context.WithCancel(context.Background())
	plugin.runContext = runContext
	addPreparedClaim(t, plugin.state, state.PrepareCompleted, "uid-a")
	for range 9 {
		require.NoError(t, plugin.cleanupResidualClaims())
	}

	cleanupDone := make(chan error, 1)
	go func() {
		cleanupDone <- plugin.cleanupResidualClaims()
	}()
	<-unprepareStarted
	cancel()

	err := <-cleanupDone
	require.ErrorIs(t, err, context.Canceled)
}

func newCleanupTestPlugin(
	provider ResourceBackend,
	client *kubefake.Clientset,
) *StaticPolicy {
	return &StaticPolicy{
		backend:        provider,
		kubeClient:     client,
		state:          state.NewMemoryState(),
		residualHitMap: make(map[string]int64),
	}
}

func addPreparedClaim(
	t *testing.T,
	pluginState state.State,
	phase state.ClaimPhase,
	uid string,
) {
	t.Helper()
	claim := state.NewPreparedClaim(
		allocatedClaim("ns-a", "claim-a", uid),
		phase,
	)
	require.NoError(t, pluginState.SetPreparedClaim(claim, false))
}
