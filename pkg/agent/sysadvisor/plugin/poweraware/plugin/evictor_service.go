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

package plugin

import (
	"context"
	"sync"

	"github.com/pkg/errors"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/kubewharf/katalyst-api/pkg/plugins/registration"
	"github.com/kubewharf/katalyst-api/pkg/plugins/skeleton"
	pluginapi "github.com/kubewharf/katalyst-api/pkg/protocol/evictionplugin/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

const (
	EvictionPluginNameNodePowerPressure = "node_power_pressure"
	evictReason                         = "host under power pressure"
)

type powerPressureEvictPluginServer struct {
	mutex  sync.Mutex
	evicts map[types.UID]*v1.Pod
}

// Reset method clears all pending eviction requests not fetched by remote client
func (p *powerPressureEvictPluginServer) Reset(ctx context.Context) {
	p.mutex.Lock()
	defer p.mutex.Unlock()
	p.evicts = make(map[types.UID]*v1.Pod)
}

// Evict method sends out request to evict a pod, which will be received by a remote eviction plugin
// the real eviction will be done by the (remote) eviction manager where the remote plugin is registered with
func (p *powerPressureEvictPluginServer) Evict(ctx context.Context, pod *v1.Pod) error {
	if pod == nil {
		return errors.New("unexpected nil pod")
	}

	p.mutex.Lock()
	defer p.mutex.Unlock()

	p.evicts[pod.GetUID()] = pod
	return nil
}

func (p *powerPressureEvictPluginServer) Name() string {
	return EvictionPluginNameNodePowerPressure
}

func (p *powerPressureEvictPluginServer) Start() error {
	return nil
}

func (p *powerPressureEvictPluginServer) Stop() error {
	return nil
}

func (p *powerPressureEvictPluginServer) GetToken(ctx context.Context, empty *pluginapi.Empty) (*pluginapi.GetTokenResponse, error) {
	return &pluginapi.GetTokenResponse{Token: ""}, nil
}

func (p *powerPressureEvictPluginServer) ThresholdMet(ctx context.Context, empty *pluginapi.Empty) (*pluginapi.ThresholdMetResponse, error) {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	return &pluginapi.ThresholdMetResponse{
		ThresholdValue: float64(len(p.evicts)),
	}, nil
}

func (p *powerPressureEvictPluginServer) GetTopEvictionPods(ctx context.Context, request *pluginapi.GetTopEvictionPodsRequest) (*pluginapi.GetTopEvictionPodsResponse, error) {
	evictReq := &pluginapi.GetEvictPodsRequest{
		ActivePods: request.ActivePods,
	}

	evictResp, err := p.GetEvictPods(ctx, evictReq)
	if err != nil {
		return nil, err
	}

	retSize := int(request.TopN)
	if retSize > len(evictResp.EvictPods) {
		retSize = len(evictResp.EvictPods)
	}

	resp := &pluginapi.GetTopEvictionPodsResponse{
		TargetPods: make([]*v1.Pod, retSize),
	}
	for i, evict := range evictResp.EvictPods {
		if i >= retSize {
			break
		}
		resp.TargetPods[i] = evict.Pod
	}

	return resp, err
}

func (p *powerPressureEvictPluginServer) GetEvictPods(ctx context.Context, request *pluginapi.GetEvictPodsRequest) (*pluginapi.GetEvictPodsResponse, error) {
	activePods := map[types.UID]struct{}{}
	for _, pod := range request.GetActivePods() {
		if len(pod.GetUID()) > 0 { // just in case of invalid input
			activePods[pod.GetUID()] = struct{}{}
		}
	}

	p.mutex.Lock()
	defer p.mutex.Unlock()

	evictPods := make([]*pluginapi.EvictPod, 0)

	pods := p.evicts
	for _, v := range pods {
		if _, ok := activePods[v.GetUID()]; ok {
			evictPods = append(evictPods, &pluginapi.EvictPod{
				Pod:                v,
				Reason:             evictReason,
				ForceEvict:         true,
				EvictionPluginName: EvictionPluginNameNodePowerPressure,
			})
		}
	}

	return &pluginapi.GetEvictPodsResponse{EvictPods: evictPods}, nil
}

var _ skeleton.EvictionPlugin = &powerPressureEvictPluginServer{}

func NewPowerPressureEvictPluginServer(conf *config.Configuration, emitter metrics.MetricEmitter) (PodEvictor, *skeleton.PluginRegistrationWrapper, error) {
	plugin := &powerPressureEvictPluginServer{}
	regWrapper, err := skeleton.NewRegistrationPluginWrapper(plugin,
		[]string{conf.PluginRegistrationDir}, // unix socket dirs
		func(key string, value int64) {
			_ = emitter.StoreInt64(key, value, metrics.MetricTypeNameCount, metrics.ConvertMapToTags(map[string]string{
				"pluginName": EvictionPluginNameNodePowerPressure,
				"pluginType": registration.EvictionPlugin,
			})...)
		})
	if err != nil {
		return nil, nil, errors.Wrap(err, "failed to register pap power pressure eviction service")
	}

	return plugin, regWrapper, nil
}
