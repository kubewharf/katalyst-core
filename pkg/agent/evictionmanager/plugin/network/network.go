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

package network

import (
	"context"
	"fmt"
	"sync"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/events"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	pluginapi "github.com/kubewharf/katalyst-api/pkg/protocol/evictionplugin/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/agent/evictionmanager/plugin"
	"github.com/kubewharf/katalyst-core/pkg/client"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/adminqos/eviction"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
	"github.com/kubewharf/katalyst-core/pkg/util/process"
)

const (
	EvictionPluginNameNetwork = "network-eviction-plugin"
)

const (
	metricsNameNetworkEvictionUnhealthyNIC = "network_eviction_unhealthy_nic"

	healthCheckTimeout = 1 * time.Minute
)

type unhealthyNICState struct {
	lastUnhealthyTime time.Time
	nicZone           v1alpha1.TopologyZone
}

type nicEvictionPlugin struct {
	*process.StopControl

	mutex sync.RWMutex
	// unhealthyNICState is a map from NIC name to the state of the NIC
	unhealthyNICState map[string]*unhealthyNICState

	emitter       metrics.MetricEmitter
	pluginName    string
	metaServer    *metaserver.MetaServer
	dynamicConfig *dynamic.DynamicAgentConfiguration
}

func NewNICEvictionPlugin(_ *client.GenericClientSet, _ events.EventRecorder,
	metaServer *metaserver.MetaServer, emitter metrics.MetricEmitter, conf *config.Configuration,
) plugin.EvictionPlugin {
	return &nicEvictionPlugin{
		StopControl:       process.NewStopControl(time.Time{}),
		unhealthyNICState: make(map[string]*unhealthyNICState),
		emitter:           emitter,
		pluginName:        EvictionPluginNameNetwork,
		metaServer:        metaServer,
		dynamicConfig:     conf.DynamicAgentConfiguration,
	}
}

func (n *nicEvictionPlugin) Name() string {
	if n == nil {
		return ""
	}

	return n.pluginName
}

func (n *nicEvictionPlugin) Start() {
	general.RegisterHeartbeatCheck(EvictionPluginNameNetwork, healthCheckTimeout, general.HealthzCheckStateNotReady, healthCheckTimeout)
	go wait.UntilWithContext(context.TODO(), n.syncUnhealthyNICState, time.Second*10)
}

func (n *nicEvictionPlugin) ThresholdMet(_ context.Context) (*pluginapi.ThresholdMetResponse, error) {
	return &pluginapi.ThresholdMetResponse{
		MetType: pluginapi.ThresholdMetType_NOT_MET,
	}, nil
}

func (n *nicEvictionPlugin) GetTopEvictionPods(_ context.Context, _ *pluginapi.GetTopEvictionPodsRequest) (*pluginapi.GetTopEvictionPodsResponse, error) {
	return &pluginapi.GetTopEvictionPodsResponse{}, nil
}

func (n *nicEvictionPlugin) GetEvictPods(ctx context.Context, request *pluginapi.GetEvictPodsRequest) (*pluginapi.GetEvictPodsResponse, error) {
	dynamicConfig := n.dynamicConfig.GetDynamicConfiguration()
	if !dynamicConfig.EnableNICHealthEviction {
		return &pluginapi.GetEvictPodsResponse{}, nil
	}

	// get all unhealthy nic states
	nicState := n.getUnhealthyNICState()

	// get all active pods
	podMap := native.GetPodKeyMap(request.ActivePods, native.GenerateUniqObjectUIDKey)

	// get unhealthy nic allocation UIDs
	nicPods := n.getUnhealthyNICAllocationPods(nicState, podMap)

	evictPods, err := n.getEvictPods(dynamicConfig.NetworkEvictionConfiguration, nicState, nicPods)
	if err != nil {
		general.Errorf("Failed to get evict pods: %v", err)
		return nil, err
	}

	return &pluginapi.GetEvictPodsResponse{
		EvictPods: evictPods,
	}, nil
}

func getUnhealthyNICZone(topologyZone []*v1alpha1.TopologyZone) map[string]v1alpha1.TopologyZone {
	unhealthy := make(map[string]v1alpha1.TopologyZone)
	for _, zone := range topologyZone {
		if zone == nil || zone.Type != v1alpha1.TopologyTypeSocket {
			continue
		}

		for _, nicZone := range zone.Children {
			if nicZone == nil || nicZone.Type != v1alpha1.TopologyTypeNIC {
				continue
			}

			if nicZone.Resources.Allocatable == nil {
				unhealthy[nicZone.Name] = *nicZone
				continue
			}

			bw, ok := (*nicZone.Resources.Allocatable)[apiconsts.ResourceNetBandwidth]
			if !ok || bw.IsZero() {
				unhealthy[nicZone.Name] = *nicZone
			}
		}
	}

	return unhealthy
}

func (n *nicEvictionPlugin) getUnhealthyNICAllocationPods(
	state map[string]*unhealthyNICState,
	podMap map[string]*v1.Pod,
) map[string]map[string]*v1.Pod {
	zonePods := n.getUnhealthyNICAllocationUIDsFromTopologyZone(state, podMap)
	for key, p := range podMap {
		if p == nil {
			continue
		}

		if !native.PodIsActive(p) {
			continue
		}

		result, ok := p.Annotations[apiconsts.PodAnnotationNICSelectionResultKey]
		if !ok {
			continue
		}

		_, ok = state[result]
		if !ok {
			continue
		}

		if _, ok = zonePods[result]; !ok {
			zonePods[result] = make(map[string]*v1.Pod)
		}

		zonePods[result][key] = p
	}

	return zonePods
}

func (n *nicEvictionPlugin) getEvictPods(
	conf *eviction.NetworkEvictionConfiguration,
	nicState map[string]*unhealthyNICState,
	nicPods map[string]map[string]*v1.Pod,
) ([]*pluginapi.EvictPod, error) {
	var deletionOptions *pluginapi.DeletionOptions
	if conf.GracePeriod >= 0 {
		deletionOptions = &pluginapi.DeletionOptions{GracePeriodSeconds: conf.GracePeriod}
	}

	var evictPods []*pluginapi.EvictPod
	for nic, uidPods := range nicPods {
		state, ok := nicState[nic]
		if !ok || time.Since(state.lastUnhealthyTime) < conf.NICUnhealthyToleranceDuration {
			continue
		}

		reason := fmt.Sprintf("nic %s is unhealthy from %s is over %s", nic,
			state.lastUnhealthyTime.String(), conf.NICUnhealthyToleranceDuration.String())

		for _, p := range uidPods {
			evictPods = append(evictPods, &pluginapi.EvictPod{Pod: p, Reason: reason, DeletionOptions: deletionOptions})
		}
	}

	return evictPods, nil
}

func (n *nicEvictionPlugin) getUnhealthyNICAllocationUIDsFromTopologyZone(
	state map[string]*unhealthyNICState,
	podMap map[string]*v1.Pod,
) map[string]map[string]*v1.Pod {
	zonePods := make(map[string]map[string]*v1.Pod)
	for nic, nicState := range state {
		if nicState == nil {
			continue
		}

		for _, alloc := range nicState.nicZone.Allocations {
			if alloc == nil || alloc.Requests == nil {
				continue
			}

			request, ok := (*alloc.Requests)[apiconsts.ResourceNetBandwidth]
			if !ok || request.IsZero() {
				continue
			}

			if _, ok = zonePods[nic]; !ok {
				zonePods[nic] = make(map[string]*v1.Pod)
			}

			p, ok := podMap[alloc.Consumer]
			if !ok {
				continue
			}

			zonePods[nic][alloc.Consumer] = p
		}
	}

	return zonePods
}

func (n *nicEvictionPlugin) syncUnhealthyNICState(ctx context.Context) {
	var err error
	defer func() {
		_ = general.UpdateHealthzStateByError(EvictionPluginNameNetwork, err)
	}()

	getCNR, err := n.metaServer.GetCNR(ctx)
	if err != nil {
		klog.Errorf("Failed to get CNR: %v", err)
		return
	}

	unHealthyNICZone := getUnhealthyNICZone(getCNR.Status.TopologyZone)

	n.mutex.Lock()
	defer n.mutex.Unlock()

	for nic := range unHealthyNICZone {
		if _, ok := n.unhealthyNICState[nic]; !ok {
			n.unhealthyNICState[nic] = &unhealthyNICState{
				lastUnhealthyTime: time.Now(),
				nicZone:           unHealthyNICZone[nic],
			}
		} else {
			n.unhealthyNICState[nic].nicZone = unHealthyNICZone[nic]
		}
		_ = n.emitter.StoreInt64(metricsNameNetworkEvictionUnhealthyNIC, 1, metrics.MetricTypeNameRaw,
			metrics.MetricTag{Key: "nic", Val: nic})
	}

	for nic := range n.unhealthyNICState {
		if _, ok := unHealthyNICZone[nic]; !ok {
			delete(n.unhealthyNICState, nic)
		}
	}
}

func (n *nicEvictionPlugin) getUnhealthyNICState() map[string]*unhealthyNICState {
	n.mutex.RLock()
	defer n.mutex.RUnlock()
	return n.unhealthyNICState
}
