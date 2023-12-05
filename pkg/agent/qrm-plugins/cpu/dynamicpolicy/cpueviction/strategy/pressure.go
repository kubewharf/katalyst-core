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

package strategy

import (
	"context"
	"fmt"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/kubewharf/katalyst-api/pkg/plugins/skeleton"
	pluginapi "github.com/kubewharf/katalyst-api/pkg/protocol/evictionplugin/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

const (
	EvictionNameDummy = "cpu-pressure-dummy"
)

type CPUPressureEviction interface {
	Start(context.Context) (err error)
	Name() string
	GetEvictPods(context.Context, *pluginapi.GetEvictPodsRequest) (*pluginapi.GetEvictPodsResponse, error)
	ThresholdMet(context.Context, *pluginapi.Empty) (*pluginapi.ThresholdMetResponse, error)
	GetTopEvictionPods(context.Context, *pluginapi.GetTopEvictionPodsRequest) (*pluginapi.GetTopEvictionPodsResponse, error)
}

type DummyCPUPressureEviction struct{}

var _ CPUPressureEviction = &DummyCPUPressureEviction{}

func (d *DummyCPUPressureEviction) Start(_ context.Context) (err error) {
	return nil
}

func (d *DummyCPUPressureEviction) Name() string {
	return EvictionNameDummy
}

func (d *DummyCPUPressureEviction) GetEvictPods(_ context.Context, _ *pluginapi.GetEvictPodsRequest) (*pluginapi.GetEvictPodsResponse, error) {
	return &pluginapi.GetEvictPodsResponse{}, nil
}

func (d *DummyCPUPressureEviction) ThresholdMet(_ context.Context, _ *pluginapi.Empty) (*pluginapi.ThresholdMetResponse, error) {
	return &pluginapi.ThresholdMetResponse{}, nil
}

func (d *DummyCPUPressureEviction) GetTopEvictionPods(_ context.Context, _ *pluginapi.GetTopEvictionPodsRequest) (*pluginapi.GetTopEvictionPodsResponse, error) {
	return &pluginapi.GetTopEvictionPodsResponse{}, nil
}

type CPUPressureEvictionPluginWrapper struct {
	CPUPressureEviction

	ctx    context.Context
	cancel context.CancelFunc

	sync.Mutex
	started bool

	emitter metrics.MetricEmitter
}

func NewCPUPressureEvictionPlugin(strategy CPUPressureEviction, emitter metrics.MetricEmitter) skeleton.EvictionPlugin {
	return &CPUPressureEvictionPluginWrapper{CPUPressureEviction: strategy, emitter: emitter}
}

func (p *CPUPressureEvictionPluginWrapper) Start() (err error) {
	p.Lock()
	defer func() {
		if err == nil {
			p.started = true
		}
		p.Unlock()
	}()

	if p.started {
		return
	}

	general.Infof("%s", p.Name())
	p.ctx, p.cancel = context.WithCancel(context.Background())
	if startErr := p.CPUPressureEviction.Start(p.ctx); startErr != nil {
		return fmt.Errorf("start %v failed: %v", p.Name(), startErr)
	}

	go wait.Until(func() {
		_ = p.emitter.StoreInt64(util.MetricNameHeartBeat, 1, metrics.MetricTypeNameRaw)
	}, time.Second*30, p.ctx.Done())
	return
}

func (p *CPUPressureEvictionPluginWrapper) Stop() error {
	p.Lock()
	defer func() {
		p.started = false
		p.Unlock()
	}()

	// plugin.Stop may be called before plugin.Start or multiple times,
	// we should ensure cancel function exist
	if !p.started {
		return nil
	}

	general.Infof("%s", p.Name())
	p.cancel()
	return nil
}

// GetToken TODO implementation
func (p *CPUPressureEvictionPluginWrapper) GetToken(_ context.Context, _ *pluginapi.Empty) (*pluginapi.GetTokenResponse, error) {
	return &pluginapi.GetTokenResponse{
		Token: "",
	}, nil
}
