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
	"fmt"
	"sync"
	"time"

	"k8s.io/client-go/kubernetes"
	drapluginapi "k8s.io/kubelet/pkg/apis/dra/v1"

	"github.com/kubewharf/katalyst-api/pkg/plugins/skeleton"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/dra/state"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/qrm/statedirectory"
)

const staticPolicyName = "compute-domain.nvidia.com"

type StaticPolicy struct {
	drapluginapi.UnimplementedDRAPluginServer

	operationMu                 sync.Mutex
	cleanupMu                   sync.Mutex
	stateDirectoryConfiguration *statedirectory.StateDirectoryConfiguration
	kubeClient                  kubernetes.Interface
	backend                     ResourceBackend
	publisher                   ResourceSlicePublisher
	retryTimeout                time.Duration
	state                       state.State
	residualHitMap              map[string]int64
	runContext                  context.Context
	cancel                      context.CancelFunc
	started                     bool
}

func NewStaticPolicy(options Options) (*StaticPolicy, error) {
	switch {
	case options.Backend == nil:
		return nil, fmt.Errorf("DRA resource backend is required")
	case options.ResourceSlicePublisher == nil:
		return nil, fmt.Errorf("ResourceSlice publisher is required")
	case options.KubeClient == nil:
		return nil, fmt.Errorf("kube client is required")
	case options.StateDirectoryConfiguration == nil:
		return nil, fmt.Errorf("DRA state directory configuration is required")
	}
	if options.RetryTimeout == 0 {
		options.RetryTimeout = 45 * time.Second
	}
	return &StaticPolicy{
		stateDirectoryConfiguration: options.StateDirectoryConfiguration,
		kubeClient:                  options.KubeClient,
		backend:                     options.Backend,
		publisher:                   options.ResourceSlicePublisher,
		retryTimeout:                options.RetryTimeout,
		residualHitMap:              make(map[string]int64),
	}, nil
}

func (p *StaticPolicy) Name() string {
	return staticPolicyName
}

// Start and Stop are serialized by skeleton.PluginRegistrationWrapper.
func (p *StaticPolicy) Start() error {
	if p.started {
		return nil
	}

	runContext, cancel := context.WithCancel(context.Background())
	stateImpl, err := p.startDependencies(runContext)
	if err != nil {
		cancel()
		return err
	}
	p.state = stateImpl
	p.runContext = runContext
	if err := p.registerClearResidualState(); err != nil {
		_ = p.stopDependencies()
		cancel()
		return err
	}
	p.startCleanup()
	p.cancel = cancel
	p.started = true
	return nil
}

func (p *StaticPolicy) startDependencies(ctx context.Context) (state.State, error) {
	stateImpl, err := state.NewCheckpointState(
		p.stateDirectoryConfiguration,
	)
	if err != nil {
		return nil, err
	}
	if err := p.backend.Start(ctx, stateImpl); err != nil {
		return nil, err
	}
	inventory, err := p.backend.Inventory(ctx)
	if err != nil {
		_ = p.backend.Stop()
		return nil, err
	}
	if err := p.publisher.Start(ctx, inventory); err != nil {
		_ = p.backend.Stop()
		return nil, fmt.Errorf("start ResourceSlice publisher: %w", err)
	}
	return stateImpl, nil
}

func (p *StaticPolicy) stopDependencies() error {
	p.publisher.Stop()
	return p.backend.Stop()
}

func (p *StaticPolicy) Stop() error {
	if !p.started {
		return nil
	}

	p.stopCleanup()
	p.cancel()
	p.cleanupMu.Lock()
	defer p.cleanupMu.Unlock()
	p.operationMu.Lock()
	defer p.operationMu.Unlock()

	err := p.stopDependencies()
	if err == nil {
		p.started = false
	}
	return err
}

var _ skeleton.DRAPlugin = (*StaticPolicy)(nil)
