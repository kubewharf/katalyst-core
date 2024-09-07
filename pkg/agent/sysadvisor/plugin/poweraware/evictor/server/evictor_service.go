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

package server

import (
	"context"
	"sync"

	"github.com/pkg/errors"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/kubewharf/katalyst-api/pkg/plugins/registration"
	"github.com/kubewharf/katalyst-api/pkg/plugins/skeleton"

	pluginapi "github.com/kubewharf/katalyst-api/pkg/protocol/evictionplugin/v1alpha1"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/poweraware/evictor"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

const (
	EvictionPluginNameNodePowerPressure = "node_power_pressure"
	evictReason                         = "host under power pressure"
)

type powerPressureEvictServer struct {
	mutex  sync.Mutex
	evicts map[types.UID]*v1.Pod
}

// Reset method clears all pending eviction requests not fetched by remote client
func (p *powerPressureEvictServer) Reset(ctx context.Context) {
	p.mutex.Lock()
	defer p.mutex.Unlock()
	p.evicts = make(map[types.UID]*v1.Pod)
}

// Evict method puts request to evict a pod in the pool; it will be sent out to plugin client via the eviction protocol
// the real eviction will be done by the (remote) eviction manager where the plugin client is registered with
func (p *powerPressureEvictServer) Evict(ctx context.Context, pod *v1.Pod) error {
	if pod == nil {
		return errors.New("unexpected nil pod")
	}

	p.mutex.Lock()
	defer p.mutex.Unlock()

	p.evicts[pod.GetUID()] = pod
	return nil
}

func (p *powerPressureEvictServer) Name() string {
	return EvictionPluginNameNodePowerPressure
}

func (p *powerPressureEvictServer) Start() error {
	return nil
}

func (p *powerPressureEvictServer) Stop() error {
	return nil
}

func (p *powerPressureEvictServer) GetToken(ctx context.Context, empty *pluginapi.Empty) (*pluginapi.GetTokenResponse, error) {
	return &pluginapi.GetTokenResponse{Token: ""}, nil
}

func (p *powerPressureEvictServer) ThresholdMet(ctx context.Context, empty *pluginapi.Empty) (*pluginapi.ThresholdMetResponse, error) {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	if len(p.evicts) == 0 {
		return &pluginapi.ThresholdMetResponse{
			MetType: pluginapi.ThresholdMetType_NOT_MET,
		}, nil
	}

	return &pluginapi.ThresholdMetResponse{
		ThresholdValue: float64(len(p.evicts)),
		MetType:        pluginapi.ThresholdMetType_HARD_MET,
	}, nil
}

func (p *powerPressureEvictServer) GetTopEvictionPods(ctx context.Context, request *pluginapi.GetTopEvictionPodsRequest) (*pluginapi.GetTopEvictionPodsResponse, error) {
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

// GetEvictPods is called from a remote evict plugin client to get evict candidates
func (p *powerPressureEvictServer) GetEvictPods(ctx context.Context, request *pluginapi.GetEvictPodsRequest) (*pluginapi.GetEvictPodsResponse, error) {
	general.InfofV(6, "pap: evict: GetEvictPods request with %d active pods", len(request.GetActivePods()))
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

	general.InfofV(6, "pap: evict: GetEvictPods respond with %d pods to evict", len(evictPods))
	return &pluginapi.GetEvictPodsResponse{EvictPods: evictPods}, nil
}

func newPowerPressureEvictServer() *powerPressureEvictServer {
	return &powerPressureEvictServer{
		evicts: make(map[types.UID]*v1.Pod),
	}
}

func newPowerPressureEvictService(conf *config.Configuration, emitter metrics.MetricEmitter) (evictor.PodEvictor, *skeleton.PluginRegistrationWrapper, error) {
	plugin := newPowerPressureEvictServer()
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

func NewPowerPressureEvictionPlugin(conf *config.Configuration, emitter metrics.MetricEmitter) (podEvictor evictor.PodEvictor, err error) {
	return startPowerPressurePodEvictorService(conf, emitter)
}

func startPowerPressurePodEvictorService(conf *config.Configuration, emitter metrics.MetricEmitter) (evictor.PodEvictor, error) {
	podEvictor, service, err := newPowerPressureEvictService(conf, emitter)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create power pressure eviction plugin server")
	}

	if err := service.Start(); err != nil {
		return nil, errors.Wrap(err, "failed to start power pressure eviction plugin server")
	}

	return podEvictor, nil
}
