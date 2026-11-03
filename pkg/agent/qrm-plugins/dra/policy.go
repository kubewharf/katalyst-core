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
	"crypto/sha256"
	"fmt"
	"reflect"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/util/validation"

	resourcev1 "k8s.io/api/resource/v1"
	"k8s.io/client-go/kubernetes"
	drapluginapi "k8s.io/kubelet/pkg/apis/dra/v1"

	"github.com/kubewharf/katalyst-api/pkg/plugins/skeleton"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/dra/state"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/qrm/statedirectory"
)

const staticPolicyName = "compute-domain.nvidia.com"
const DefaultDriverName = staticPolicyName

type StaticPolicy struct {
	driverName     string
	checkpointName string
	drapluginapi.UnimplementedDRAPluginServer

	operationMu                 sync.Mutex
	claimMu                     sync.Mutex
	claimLocks                  map[string]*claimLock
	operationWG                 sync.WaitGroup
	stopping                    bool
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

type claimLock struct {
	mu   sync.Mutex
	refs int
}

func (p *StaticPolicy) lockClaim(uid string) func() {
	p.claimMu.Lock()
	if p.claimLocks == nil {
		p.claimLocks = map[string]*claimLock{}
	}
	l := p.claimLocks[uid]
	if l == nil {
		l = &claimLock{}
		p.claimLocks[uid] = l
	}
	l.refs++
	p.claimMu.Unlock()
	l.mu.Lock()
	return func() {
		l.mu.Unlock()
		p.claimMu.Lock()
		l.refs--
		if l.refs == 0 {
			delete(p.claimLocks, uid)
		}
		p.claimMu.Unlock()
	}
}

func NewStaticPolicy(options Options) (*StaticPolicy, error) {
	if options.DriverName == "" {
		options.DriverName = DefaultDriverName
	}
	if errors := validation.IsDNS1123Subdomain(options.DriverName); len(errors) > 0 {
		return nil, fmt.Errorf("invalid DRA driver name %q: %v", options.DriverName, errors)
	}
	if options.CheckpointName == "" && options.DriverName != DefaultDriverName {
		options.CheckpointName = fmt.Sprintf("dra_%x", sha256.Sum256([]byte(options.DriverName)))
	}
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
		driverName:                  options.DriverName,
		checkpointName:              options.CheckpointName,
		stateDirectoryConfiguration: options.StateDirectoryConfiguration,
		kubeClient:                  options.KubeClient,
		backend:                     options.Backend,
		publisher:                   options.ResourceSlicePublisher,
		retryTimeout:                options.RetryTimeout,
		residualHitMap:              make(map[string]int64),
	}, nil
}

func (p *StaticPolicy) Name() string {
	if p.driverName == "" {
		return DefaultDriverName
	}
	return p.driverName
}

// Start and Stop are serialized by skeleton.PluginRegistrationWrapper.
func (p *StaticPolicy) Start() error {
	if p.started {
		return nil
	}
	p.operationMu.Lock()
	p.stopping = false
	p.operationMu.Unlock()

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
	stateImpl, err := state.NewCheckpointStateWithName(p.stateDirectoryConfiguration, p.checkpointName)
	if err != nil {
		return nil, err
	}
	if importer, ok := p.backend.(CheckpointImporter); ok && len(stateImpl.ListPreparedClaims()) == 0 {
		claims, err := importer.ImportPreparedClaims(ctx)
		if err != nil {
			return nil, err
		}
		if len(claims) > 0 {
			if err = stateImpl.SetPreparedClaims(claims, true); err != nil {
				return nil, err
			}
		}
	}
	// Normalize old mixed-driver checkpoints before handing restored state to a backend.
	restored := stateImpl.ListPreparedClaims()
	changed := false
	for uid, prepared := range restored {
		if prepared.Status.Allocation == nil || len(prepared.Status.Allocation.Devices.Results) == 0 {
			continue
		}
		scoped, err := p.claimForDriver(&resourcev1.ResourceClaim{Status: prepared.Status})
		if err != nil {
			return nil, fmt.Errorf("checkpoint claim %s does not belong to driver %s: %w", uid, p.Name(), err)
		}
		changed = changed || !reflect.DeepEqual(prepared.Status, scoped.Status)
		prepared.Status = scoped.Status
		restored[uid] = prepared
	}
	if changed {
		if err := stateImpl.SetPreparedClaims(restored, true); err != nil {
			return nil, err
		}
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
	p.stopping = true
	p.operationMu.Unlock()
	p.operationWG.Wait()
	p.operationMu.Lock()
	defer p.operationMu.Unlock()

	err := p.stopDependencies()
	if err == nil {
		p.started = false
	}
	return err
}

// Keep the fixed API fork: DRA registration is supplied by the adapter.
var _ interface {
	skeleton.GenericPlugin
	drapluginapi.DRAPluginServer
} = (*StaticPolicy)(nil)
