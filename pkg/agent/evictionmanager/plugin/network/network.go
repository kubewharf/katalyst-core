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
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
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
	// healthyNICState caches CNR NIC bandwidth capacity by eviction scope.
	healthyNICState map[bandwidthScope]float64

	emitter                  metrics.MetricEmitter
	pluginName               string
	metaServer               *metaserver.MetaServer
	dynamicConfig            *dynamic.DynamicAgentConfiguration
	podSaleModeAnnotationKey string
	bandwidthPressureState   map[bandwidthScope]*bandwidthPressureState
	metricQuerier            BandwidthMetricQuerier
}

func NewNICEvictionPlugin(_ *client.GenericClientSet, _ events.EventRecorder,
	metaServer *metaserver.MetaServer, emitter metrics.MetricEmitter, conf *config.Configuration,
) plugin.EvictionPlugin {
	return &nicEvictionPlugin{
		StopControl:              process.NewStopControl(time.Time{}),
		unhealthyNICState:        make(map[string]*unhealthyNICState),
		healthyNICState:          make(map[bandwidthScope]float64),
		emitter:                  emitter,
		pluginName:               EvictionPluginNameNetwork,
		metaServer:               metaServer,
		dynamicConfig:            conf.DynamicAgentConfiguration,
		podSaleModeAnnotationKey: conf.GenericConfiguration.PodSaleModeAnnotationKey,
		bandwidthPressureState:   make(map[bandwidthScope]*bandwidthPressureState),
		metricQuerier:            newMetaServerBandwidthMetricQuerier(metaServer),
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
	go wait.UntilWithContext(context.TODO(), n.syncNICState, time.Second*10)
}

// Shared NIC state lifecycle.
func (n *nicEvictionPlugin) syncNICState(ctx context.Context) {
	var err error
	defer func() {
		_ = general.UpdateHealthzStateByError(EvictionPluginNameNetwork, err)
	}()

	if n == nil || n.metaServer == nil {
		err = fmt.Errorf("nil network eviction plugin or metaserver")
		return
	}

	getCNR, err := n.metaServer.GetCNR(ctx)
	if err != nil {
		klog.Errorf("Failed to get CNR: %v", err)
		return
	}
	if getCNR == nil {
		err = fmt.Errorf("nil CNR")
		return
	}

	healthyNICZone, unhealthyNICZone := getNICZones(getCNR.Status.TopologyZone)
	now := time.Now()

	n.syncHealthyNICState(healthyNICZone, now)
	n.syncUnHealthyNICState(unhealthyNICZone, now)
}

func (n *nicEvictionPlugin) syncHealthyNICState(healthyNICZone map[string]float64, now time.Time) {
	healthyNICState := make(map[bandwidthScope]float64, len(healthyNICZone)*2)
	for identifier, capacity := range healthyNICZone {
		netns, nic, ok := machine.ParseNICIdentifier(identifier)
		if !ok {
			continue
		}
		for _, direction := range []string{bandwidthDirectionRX, bandwidthDirectionTX} {
			healthyNICState[bandwidthScope{
				netns:     netns,
				nic:       nic,
				direction: direction,
			}] = capacity
		}
	}

	conf := (*eviction.NetworkEvictionConfiguration)(nil)
	if n != nil && n.dynamicConfig != nil {
		conf = n.dynamicConfig.GetDynamicConfiguration().NetworkEvictionConfiguration
	}
	if n != nil && n.metricQuerier == nil {
		n.metricQuerier = newMetaServerBandwidthMetricQuerier(n.metaServer)
	}

	type bandwidthObservation struct {
		scope        bandwidthScope
		bps          float64
		capacityMbps float64
	}
	observations := make([]bandwidthObservation, 0, len(healthyNICState))
	if conf != nil && conf.EnableNICBandwidthEviction && n != nil && n.metricQuerier != nil {
		for scope, capacity := range healthyNICState {
			bps, ok := n.metricQuerier.GetBandwidthMetric(scope)
			if !ok || capacity <= 0 {
				continue
			}
			observations = append(observations, bandwidthObservation{
				scope:        scope,
				bps:          bps,
				capacityMbps: capacity,
			})
		}
	}

	n.mutex.Lock()
	defer n.mutex.Unlock()

	n.healthyNICState = healthyNICState
	n.pruneStaleBandwidthStates(healthyNICState, now, bandwidthPressureStateRetention)
	if conf == nil || !conf.EnableNICBandwidthEviction {
		return
	}
	for _, observation := range observations {
		state := n.getOrCreateBandwidthState(observation.scope, conf.NICBandwidthRingSize, now, bandwidthPressureStateRetention)
		state.observe(observation.bps, observation.capacityMbps, now)
	}
}

func (n *nicEvictionPlugin) syncUnHealthyNICState(unhealthyNICZone map[string]v1alpha1.TopologyZone, now time.Time) {
	n.mutex.Lock()
	defer n.mutex.Unlock()

	if n.unhealthyNICState == nil {
		n.unhealthyNICState = make(map[string]*unhealthyNICState)
	}
	for nic := range unhealthyNICZone {
		if _, ok := n.unhealthyNICState[nic]; !ok {
			n.unhealthyNICState[nic] = &unhealthyNICState{
				lastUnhealthyTime: now,
				nicZone:           unhealthyNICZone[nic],
			}
		} else {
			n.unhealthyNICState[nic].nicZone = unhealthyNICZone[nic]
		}
		if n.emitter != nil {
			_ = n.emitter.StoreInt64(metricsNameNetworkEvictionUnhealthyNIC, 1, metrics.MetricTypeNameRaw,
				metrics.MetricTag{Key: "nic", Val: nic})
		}
	}

	for nic := range n.unhealthyNICState {
		if _, ok := unhealthyNICZone[nic]; !ok {
			delete(n.unhealthyNICState, nic)
		}
	}
}

func (n *nicEvictionPlugin) getHealthyNICState() map[bandwidthScope]float64 {
	n.mutex.RLock()
	defer n.mutex.RUnlock()

	result := make(map[bandwidthScope]float64, len(n.healthyNICState))
	for scope, capacity := range n.healthyNICState {
		result[scope] = capacity
	}
	return result
}

func (n *nicEvictionPlugin) getUnhealthyNICState() map[string]*unhealthyNICState {
	n.mutex.RLock()
	defer n.mutex.RUnlock()

	result := make(map[string]*unhealthyNICState, len(n.unhealthyNICState))
	for nic, state := range n.unhealthyNICState {
		result[nic] = state
	}
	return result
}

// Bandwidth eviction flow.
func (n *nicEvictionPlugin) ThresholdMet(_ context.Context, req *pluginapi.GetThresholdMetRequest) (*pluginapi.ThresholdMetResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("GetThresholdMet got nil request")
	}
	if n == nil || n.dynamicConfig == nil {
		return &pluginapi.ThresholdMetResponse{MetType: pluginapi.ThresholdMetType_NOT_MET}, nil
	}

	conf := n.dynamicConfig.GetDynamicConfiguration().NetworkEvictionConfiguration
	if conf == nil || !conf.EnableNICBandwidthEviction {
		return &pluginapi.ThresholdMetResponse{MetType: pluginapi.ThresholdMetType_NOT_MET}, nil
	}

	var bestResp *pluginapi.ThresholdMetResponse
	var bestScope bandwidthScope
	var bestEvaluation bandwidthPressureEvaluation
	now := time.Now()
	bandwidthScopes := n.getHealthyNICState()

	n.mutex.Lock()
	defer n.mutex.Unlock()

	n.pruneStaleBandwidthStates(bandwidthScopes, now, bandwidthPressureStateRetention)

	for scope := range bandwidthScopes {
		state, ok := n.bandwidthPressureState[scope]
		if !ok {
			continue
		}
		evaluation := state.evaluate(conf.NICBandwidthUtilizationThreshold)
		if !evaluation.met(conf.NICBandwidthContinuousMetThreshold, conf.NICBandwidthRingMetThreshold) {
			continue
		}

		resp := &pluginapi.ThresholdMetResponse{
			ThresholdValue:    conf.NICBandwidthUtilizationThreshold,
			ObservedValue:     evaluation.lastUtilization,
			ThresholdOperator: pluginapi.ThresholdOperator_GREATER_THAN,
			MetType:           pluginapi.ThresholdMetType_HARD_MET,
			EvictionScope:     formatBandwidthScope(scope),
		}
		if bestResp == nil || isBandwidthPressureMoreSevere(scope, evaluation, bestScope, bestEvaluation) {
			bestResp = resp
			bestScope = scope
			bestEvaluation = evaluation
		}
	}

	if bestResp == nil {
		return &pluginapi.ThresholdMetResponse{MetType: pluginapi.ThresholdMetType_NOT_MET}, nil
	}
	return bestResp, nil
}

func (n *nicEvictionPlugin) GetTopEvictionPods(_ context.Context, req *pluginapi.GetTopEvictionPodsRequest) (*pluginapi.GetTopEvictionPodsResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("GetTopEvictionPods got nil request")
	}
	if n == nil || n.dynamicConfig == nil || len(req.ActivePods) == 0 || req.TopN == 0 {
		return &pluginapi.GetTopEvictionPodsResponse{}, nil
	}

	scope, err := parseBandwidthScope(req.EvictionScope)
	if err != nil {
		klog.Warningf("skip invalid bandwidth eviction scope %q: %v", req.EvictionScope, err)
		return &pluginapi.GetTopEvictionPodsResponse{}, nil
	}

	conf := n.dynamicConfig.GetDynamicConfiguration().NetworkEvictionConfiguration
	if conf == nil || !conf.EnableNICBandwidthEviction {
		return &pluginapi.GetTopEvictionPodsResponse{}, nil
	}
	if n.metricQuerier == nil {
		n.metricQuerier = newMetaServerBandwidthMetricQuerier(n.metaServer)
	}
	if n.metricQuerier == nil {
		return &pluginapi.GetTopEvictionPodsResponse{}, nil
	}

	// Pod usage is currently TCP-only because UDP container BPS is not exposed yet.
	candidatePods := make([]*v1.Pod, 0, len(req.ActivePods))
	usageSnapshot := make(map[string]float64, len(req.ActivePods))
	for _, pod := range req.ActivePods {
		if pod == nil || !native.PodIsActive(pod) {
			continue
		}

		podScope, ok := getPodBandwidthScope(pod, scope)
		if !ok || podScope.netns != scope.netns || podScope.nic != scope.nic {
			continue
		}
		usage, ok := n.metricQuerier.GetPodDirectionUsage(pod, scope.direction)
		if !ok {
			continue
		}

		podKey := native.GenerateUniqObjectUIDKey(pod)
		candidatePods = append(candidatePods, pod)
		usageSnapshot[podKey] = usage
	}

	if len(candidatePods) == 0 {
		return &pluginapi.GetTopEvictionPodsResponse{}, nil
	}

	general.NewMultiSorter(
		native.PodSaleModeCmpFunc(n.podSaleModeAnnotationKey),
		func(i1, i2 interface{}) int {
			leftUsage := usageSnapshot[native.GenerateUniqObjectUIDKey(i1.(*v1.Pod))]
			rightUsage := usageSnapshot[native.GenerateUniqObjectUIDKey(i2.(*v1.Pod))]
			return general.CmpFloat64(leftUsage, rightUsage)
		},
		native.PodUniqKeyCmpFunc,
	).Sort(native.NewPodSourceImpList(candidatePods))

	topN := general.MinUInt64(req.TopN, uint64(len(candidatePods)))
	targets := make([]*v1.Pod, 0, int(topN))
	for i := uint64(0); i < topN; i++ {
		targets = append(targets, candidatePods[i])
	}

	resp := &pluginapi.GetTopEvictionPodsResponse{TargetPods: targets}
	if conf.NICBandwidthGracePeriod > 0 {
		resp.DeletionOptions = &pluginapi.DeletionOptions{
			GracePeriodSeconds: conf.NICBandwidthGracePeriod,
		}
	}
	return resp, nil
}

func (n *nicEvictionPlugin) pruneStaleBandwidthStates(activeScopes map[bandwidthScope]float64, now time.Time, retention time.Duration) {
	if n == nil || n.bandwidthPressureState == nil {
		return
	}
	for scope, state := range n.bandwidthPressureState {
		if _, ok := activeScopes[scope]; !ok {
			delete(n.bandwidthPressureState, scope)
			continue
		}
		if !state.expired(now, retention) {
			continue
		}
		delete(n.bandwidthPressureState, scope)
	}
}

func (n *nicEvictionPlugin) getOrCreateBandwidthState(scope bandwidthScope, ringSize int, now time.Time, retention time.Duration) *bandwidthPressureState {
	if ringSize <= 0 {
		ringSize = 1
	}
	if n.bandwidthPressureState == nil {
		n.bandwidthPressureState = make(map[bandwidthScope]*bandwidthPressureState)
	}

	state, ok := n.bandwidthPressureState[scope]
	if !ok || len(state.ring) != ringSize || state.expired(now, retention) {
		state = &bandwidthPressureState{ring: make([]bandwidthSample, ringSize)}
		n.bandwidthPressureState[scope] = state
	}
	return state
}

// NIC health eviction flow.
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

func getNICZones(topologyZone []*v1alpha1.TopologyZone) (map[string]float64, map[string]v1alpha1.TopologyZone) {
	healthy := make(map[string]float64)
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
				continue
			}
			healthy[nicZone.Name] = float64(bw.Value())
		}
	}

	return healthy, unhealthy
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

		// sum up pod network bandwidth requests
		// and skip pods without or zero network bandwidth requests
		request, ok := (native.SumUpPodRequestResources(p))[apiconsts.ResourceNetBandwidth]
		if !ok || request.IsZero() {
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

			// skip pods without or zero network bandwidth requests
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
