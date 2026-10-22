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
	"slices"
	"sync"
	"testing"
	"time"

	resourcev1 "k8s.io/api/resource/v1"
	kubefake "k8s.io/client-go/kubernetes/fake"

	"github.com/stretchr/testify/require"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/dra/state"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/qrm/statedirectory"
)

type lifecycleRecorder struct {
	mu     sync.Mutex
	events []string
}

func (r *lifecycleRecorder) add(event string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
}

func (r *lifecycleRecorder) list() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.events...)
}

type lifecycleProvider struct {
	recorder       *lifecycleRecorder
	inventory      Inventory
	startErr       error
	inventoryErr   error
	stopErrors     []error
	startCalls     int
	stopCalls      int
	restoredState  map[string]state.PreparedClaim
	prepareStarted chan struct{}
	prepareRelease chan struct{}
}

func (p *lifecycleProvider) Start(_ context.Context, reader StateReader) error {
	p.startCalls++
	p.recorder.add("provider-start")
	p.restoredState = reader.ListPreparedClaims()
	return p.startErr
}

func (p *lifecycleProvider) Stop() error {
	index := p.stopCalls
	p.stopCalls++
	p.recorder.add("provider-stop")
	if index < len(p.stopErrors) {
		return p.stopErrors[index]
	}
	return nil
}

func (p *lifecycleProvider) Inventory(context.Context) (Inventory, error) {
	p.recorder.add("provider-inventory")
	return p.inventory, p.inventoryErr
}

func (p *lifecycleProvider) Prepare(
	context.Context,
	*resourcev1.ResourceClaim,
) (PrepareResult, error) {
	if p.prepareStarted != nil {
		close(p.prepareStarted)
	}
	if p.prepareRelease != nil {
		<-p.prepareRelease
	}
	return PrepareResult{}, nil
}

func (*lifecycleProvider) Unprepare(
	context.Context,
	state.PreparedClaim,
) error {
	return nil
}

type lifecyclePublisher struct {
	recorder       *lifecycleRecorder
	startErr       error
	startCalls     int
	stopCalls      int
	startInventory Inventory
}

func (p *lifecyclePublisher) Start(
	_ context.Context,
	inventory Inventory,
) error {
	p.startCalls++
	p.startInventory = inventory
	p.recorder.add("publisher-start")
	return p.startErr
}

func (p *lifecyclePublisher) Stop() {
	p.stopCalls++
	p.recorder.add("publisher-stop")
}

func TestNewStaticPolicyValidatesOptionsAndSetsDefaults(t *testing.T) {
	valid := func() Options {
		return Options{
			StateDirectoryConfiguration: &statedirectory.StateDirectoryConfiguration{
				StateFileDirectory: t.TempDir(),
			},
			KubeClient:             kubefake.NewSimpleClientset(),
			Backend:                &lifecycleProvider{recorder: &lifecycleRecorder{}},
			ResourceSlicePublisher: &lifecyclePublisher{recorder: &lifecycleRecorder{}},
		}
	}

	tests := []struct {
		name    string
		mutate  func(*Options)
		wantErr string
	}{
		{
			name: "backend",
			mutate: func(options *Options) {
				options.Backend = nil
			},
			wantErr: "backend",
		},
		{
			name: "publisher",
			mutate: func(options *Options) {
				options.ResourceSlicePublisher = nil
			},
			wantErr: "publisher",
		},
		{
			name: "kube client",
			mutate: func(options *Options) {
				options.KubeClient = nil
			},
			wantErr: "kube client",
		},
		{
			name: "state directory",
			mutate: func(options *Options) {
				options.StateDirectoryConfiguration = nil
			},
			wantErr: "state directory",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			options := valid()
			tt.mutate(&options)
			_, err := NewStaticPolicy(options)
			require.ErrorContains(t, err, tt.wantErr)
		})
	}

	plugin, err := NewStaticPolicy(valid())
	require.NoError(t, err)
	require.Equal(t, 45*time.Second, plugin.retryTimeout)
	require.NotNil(t, plugin.stateDirectoryConfiguration)
	require.NotNil(t, plugin.kubeClient)
	require.NotNil(t, plugin.backend)
	require.NotNil(t, plugin.publisher)
	require.Equal(t, "compute-domain.nvidia.com", plugin.Name())
}

func TestPluginLifecycleRestoresPublishesStopsAndRestarts(t *testing.T) {
	recorder := &lifecycleRecorder{}
	inventory := Inventory{
		PoolName: "pool-a",
		Devices: []resourcev1.Device{{
			Name: "device-a",
		}},
	}
	provider := &lifecycleProvider{
		recorder:  recorder,
		inventory: inventory,
	}
	publisher := &lifecyclePublisher{recorder: recorder}
	stateDirectory := &statedirectory.StateDirectoryConfiguration{
		StateFileDirectory: t.TempDir(),
	}
	checkpointState, err := state.NewCheckpointState(stateDirectory)
	require.NoError(t, err)
	require.NoError(t, checkpointState.SetPreparedClaim(
		state.PreparedClaim{UID: "uid-restored", Phase: state.PrepareStarted},
		true,
	))
	plugin, err := NewStaticPolicy(Options{
		StateDirectoryConfiguration: stateDirectory,
		KubeClient:                  kubefake.NewSimpleClientset(),
		Backend:                     provider,
		ResourceSlicePublisher:      publisher,
	})
	require.NoError(t, err)

	require.NoError(t, plugin.Start())
	require.NoError(t, plugin.Start())
	require.Equal(t, inventory, publisher.startInventory)
	require.Contains(t, provider.restoredState, "uid-restored")
	require.Equal(t, 1, provider.startCalls)
	require.Equal(t, 1, publisher.startCalls)

	require.NoError(t, plugin.Stop())
	require.NoError(t, plugin.Stop())
	require.Equal(
		t,
		[]string{
			"provider-start",
			"provider-inventory",
			"publisher-start",
			"publisher-stop",
			"provider-stop",
		},
		recorder.list(),
	)

	require.NoError(t, plugin.Start())
	require.NoError(t, plugin.Stop())
	require.Equal(t, 2, provider.startCalls)
	require.Equal(t, 2, provider.stopCalls)
	require.Equal(t, 2, publisher.startCalls)
	require.Equal(t, 2, publisher.stopCalls)
}

func TestPluginStartRollsBackProviderWhenPublisherFails(t *testing.T) {
	recorder := &lifecycleRecorder{}
	provider := &lifecycleProvider{recorder: recorder}
	publisher := &lifecyclePublisher{
		recorder: recorder,
		startErr: errors.New("publish failed"),
	}
	plugin, err := NewStaticPolicy(Options{
		StateDirectoryConfiguration: &statedirectory.StateDirectoryConfiguration{
			StateFileDirectory: t.TempDir(),
		},
		KubeClient:             kubefake.NewSimpleClientset(),
		Backend:                provider,
		ResourceSlicePublisher: publisher,
	})
	require.NoError(t, err)

	err = plugin.Start()
	require.ErrorContains(t, err, "start ResourceSlice publisher")
	require.Equal(t, 1, provider.stopCalls)
	require.Zero(t, publisher.stopCalls)
	require.False(t, plugin.started)
}

func TestPluginStopCanRetryProviderFailure(t *testing.T) {
	recorder := &lifecycleRecorder{}
	provider := &lifecycleProvider{
		recorder:   recorder,
		stopErrors: []error{errors.New("provider still running")},
	}
	publisher := &lifecyclePublisher{recorder: recorder}
	plugin, err := NewStaticPolicy(Options{
		StateDirectoryConfiguration: &statedirectory.StateDirectoryConfiguration{
			StateFileDirectory: t.TempDir(),
		},
		KubeClient:             kubefake.NewSimpleClientset(),
		Backend:                provider,
		ResourceSlicePublisher: publisher,
	})
	require.NoError(t, err)
	require.NoError(t, plugin.Start())

	err = plugin.Stop()
	require.ErrorContains(t, err, "provider still running")
	require.True(t, plugin.started)

	require.NoError(t, plugin.Stop())
	require.False(t, plugin.started)
	require.Equal(t, 2, provider.stopCalls)
}

func TestPluginStopWaitsForActiveProviderOperation(t *testing.T) {
	recorder := &lifecycleRecorder{}
	provider := &lifecycleProvider{
		recorder:       recorder,
		prepareStarted: make(chan struct{}),
		prepareRelease: make(chan struct{}),
	}
	plugin, err := NewStaticPolicy(Options{
		StateDirectoryConfiguration: &statedirectory.StateDirectoryConfiguration{
			StateFileDirectory: t.TempDir(),
		},
		KubeClient:             kubefake.NewSimpleClientset(),
		Backend:                provider,
		ResourceSlicePublisher: &lifecyclePublisher{recorder: recorder},
	})
	require.NoError(t, err)
	require.NoError(t, plugin.Start())

	prepareDone := make(chan error, 1)
	go func() {
		_, prepareErr := plugin.prepareOne(
			context.Background(),
			allocatedDeviceClaim(
				"ns-a",
				"claim-a",
				"uid-a",
				"request-a",
				false,
			),
		)
		prepareDone <- prepareErr
	}()
	<-provider.prepareStarted

	stopDone := make(chan error, 1)
	go func() {
		stopDone <- plugin.Stop()
	}()
	require.Never(t, func() bool {
		return slices.Contains(recorder.list(), "provider-stop")
	}, 50*time.Millisecond, 5*time.Millisecond)

	close(provider.prepareRelease)
	require.NoError(t, <-prepareDone)
	require.NoError(t, <-stopDone)
	require.Equal(t, 1, provider.stopCalls)
}
