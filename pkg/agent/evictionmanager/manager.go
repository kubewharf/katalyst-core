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

// Package evictionmanager is the package that contains the libraries that drive the Kubelet binary.
// The kubelet is responsible for node level pod management.  It runs on each worker in the cluster.
package evictionmanager // import "github.com/kubewharf/katalyst-core/pkg/evictionmanager"

import (
	"context"
	"fmt"
	"sync"
	"time"

	//nolint
	"github.com/golang/protobuf/proto"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/events"
	"k8s.io/klog/v2"
	clocks "k8s.io/utils/clock"

	"github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/plugins/registration"
	pluginapi "github.com/kubewharf/katalyst-api/pkg/protocol/evictionplugin/v1alpha1"
	endpointpkg "github.com/kubewharf/katalyst-core/pkg/agent/evictionmanager/endpoint"
	"github.com/kubewharf/katalyst-core/pkg/agent/evictionmanager/plugin"
	"github.com/kubewharf/katalyst-core/pkg/agent/evictionmanager/podkiller"
	"github.com/kubewharf/katalyst-core/pkg/agent/evictionmanager/rule"
	"github.com/kubewharf/katalyst-core/pkg/client"
	pkgconfig "github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

const (
	MetricsNameVictimPodCNT    = "victims_cnt"
	MetricsNameRunningPodCNT   = "running_pod_cnt"
	MetricsNameCandidatePodCNT = "candidate_pod_cnt"
)

// LatestCNRGetter returns the latest CNR resources.
type LatestCNRGetter func() *v1alpha1.CustomNodeResource

// LatestPodsGetter returns the latest pods that are running.
type LatestPodsGetter func() []*v1.Pod

// EvictionManger reconciles to check if some threshold has been met, and
// trigger pod eviction actions if needed.
type EvictionManger struct {
	conf          *pkgconfig.Configuration
	genericClient *client.GenericClientSet

	endpointLock  sync.RWMutex
	conditionLock sync.RWMutex

	// clock is an interface that provides time related functionality in a way that makes it
	// easy to test the code.
	clock clocks.WithTickerAndDelayedExecution

	podKiller podkiller.PodKiller

	killQueue    rule.EvictionQueue
	killStrategy rule.EvictionStrategy

	// metaGetter is used to collect metadata universal metaServer.
	metaGetter *metaserver.MetaServer
	// emitter is used to emit metrics.
	emitter metrics.MetricEmitter

	// endpoints cache registered eviction plugin endpoints.
	endpoints map[string]endpointpkg.Endpoint
	// conditions map condition name to *pluginapi.Condition, and they will be reported to node or CNR.
	conditions map[string]*pluginapi.Condition

	// conditionsLastObservedAt map condition name to *pluginapi.Condition with latest observed timestamp.
	conditionsLastObservedAt map[string]conditionObservedAt
	// thresholdsFirstObservedAt map eviction plugin name to *pluginapi.Condition with firstly observed timestamp.
	thresholdsFirstObservedAt map[string]thresholdObservedAt
}

var InnerEvictionPluginsDisabledByDefault = sets.NewString()

func NewInnerEvictionPluginInitializers() map[string]plugin.InitFunc {
	innerEvictionPluginInitializers := make(map[string]plugin.InitFunc)
	innerEvictionPluginInitializers["reclaimed-resources"] = plugin.NewReclaimedResourcesEvictionPlugin
	innerEvictionPluginInitializers["memory-pressure"] = plugin.NewMemoryPressureEvictionPlugin
	return innerEvictionPluginInitializers
}

func NewEvictionManager(genericClient *client.GenericClientSet, recorder events.EventRecorder,
	metaServer *metaserver.MetaServer, emitter metrics.MetricEmitter, conf *pkgconfig.Configuration) *EvictionManger {
	queue := rule.NewFIFOEvictionQueue(conf.EvictionBurst)

	killer := podkiller.NewEvictionAPIKiller(genericClient.KubeClient, recorder)
	podKiller := podkiller.NewAsynchronizedPodKiller(killer, genericClient.KubeClient)

	e := &EvictionManger{
		killQueue:    queue,
		killStrategy: rule.NewEvictionStrategyImpl(conf),

		metaGetter:                metaServer,
		emitter:                   emitter,
		podKiller:                 podKiller,
		endpoints:                 make(map[string]endpointpkg.Endpoint),
		conf:                      conf,
		conditions:                make(map[string]*pluginapi.Condition),
		conditionsLastObservedAt:  make(map[string]conditionObservedAt),
		thresholdsFirstObservedAt: make(map[string]thresholdObservedAt),
		clock:                     clocks.RealClock{},
		genericClient:             genericClient,
	}

	e.getEvictionPlugins(genericClient, recorder, metaServer, emitter, conf, NewInnerEvictionPluginInitializers())
	return e
}

func (m *EvictionManger) getEvictionPlugins(genericClient *client.GenericClientSet, recorder events.EventRecorder, metaServer *metaserver.MetaServer,
	emitter metrics.MetricEmitter, conf *pkgconfig.Configuration, innerEvictionPluginInitializers map[string]plugin.InitFunc) {
	m.endpointLock.Lock()
	for pluginName, initFn := range innerEvictionPluginInitializers {
		if !general.IsNameEnabled(pluginName, InnerEvictionPluginsDisabledByDefault, conf.GenericEvictionConfiguration.InnerPlugins) {
			klog.Warningf("[eviction manager] %s is disabled", pluginName)
			continue
		}

		curPlugin := initFn(genericClient, recorder, metaServer, emitter, conf)
		m.endpoints[curPlugin.Name()] = curPlugin
	}
	m.endpointLock.Unlock()
}

func (m *EvictionManger) Run(ctx context.Context) {
	klog.Infof("[eviction manager] run with podKiller %v", m.podKiller.Name())
	defer klog.Infof("[eviction manager] started")

	m.podKiller.Start(ctx)
	go wait.UntilWithContext(ctx, m.sync, m.conf.EvictionManagerSyncPeriod)
	go wait.UntilWithContext(ctx, m.reportConditionsAsNodeTaints, time.Second*5)
	<-ctx.Done()
}

func (m *EvictionManger) sync(ctx context.Context) {
	activePods, err := m.metaGetter.GetPodList(ctx, native.PodIsActive)
	if err != nil {
		klog.Errorf("failed to list pods from metaServer: %v", err)
		return
	}

	klog.Infof("[eviction manager] currently, there are %v active pods", len(activePods))
	_ = m.emitter.StoreInt64(MetricsNameRunningPodCNT, int64(len(activePods)), metrics.MetricTypeNameRaw)

	pods := native.FilterOutSkipEvictionPods(activePods, m.conf.EvictionSkippedAnnotationKeys, m.conf.EvictionSkippedLabelKeys)
	klog.Infof("[eviction manager] currently, there are %v candidate pods", len(pods))
	_ = m.emitter.StoreInt64(MetricsNameCandidatePodCNT, int64(len(pods)), metrics.MetricTypeNameRaw)

	currentMetThresholds := make(map[string]*pluginapi.ThresholdMetResponse)
	currentConditions := make(map[string]*pluginapi.Condition)

	// softEvictPods are candidates (among which only one will be chosen);
	// forceEvictPods are pods that should be killed immediately (but can be withdrawn)
	softEvictPods := make(map[string]*rule.RuledEvictPod)
	forceEvictPods := make(map[string]*rule.RuledEvictPod)

	m.endpointLock.RLock()
	for pluginName, ep := range m.endpoints {
		getEvictResp, err := ep.GetEvictPods(context.Background(), &pluginapi.GetEvictPodsRequest{
			ActivePods: pods,
		})
		if err != nil {
			klog.Errorf("[eviction manager] calling GetEvictPods of plugin: %s failed with error: %v", pluginName, err)
		} else if getEvictResp == nil {
			klog.Errorf("[eviction manager] calling GetEvictPods of plugin: %s and getting nil resp", pluginName)
		} else {
			klog.Infof("[eviction manager] GetEvictPods of plugin: %s with %d pods to evict", pluginName, len(getEvictResp.EvictPods))
			for _, evictPod := range getEvictResp.EvictPods {
				if evictPod == nil || evictPod.Pod == nil {
					klog.Errorf("[eviction manager] skip nil evict pod of plugin: %s", pluginName)
					continue
				}

				// to avoid plugins forget to set EvictionPluginName property
				evictPod.EvictionPluginName = pluginName
				klog.Infof("[eviction manager] plugin: %s requests to evict pod: %s/%s with reason: %s, forceEvict: %v",
					pluginName, evictPod.Pod.Namespace, evictPod.Pod.Name, evictPod.Reason, evictPod.ForceEvict)

				if evictPod.ForceEvict {
					forceEvictPods[string(evictPod.Pod.UID)] = &rule.RuledEvictPod{
						EvictPod: proto.Clone(evictPod).(*pluginapi.EvictPod),
						Scope:    rule.EvictionScopeForce,
					}
				} else {
					softEvictPods[string(evictPod.Pod.UID)] = &rule.RuledEvictPod{
						EvictPod: proto.Clone(evictPod).(*pluginapi.EvictPod),
						Scope:    rule.EvictionScopeSoft,
					}
				}
			}

			if getEvictResp.Condition != nil && getEvictResp.Condition.MetCondition {
				klog.Infof("[eviction manager] plugin: %s requests set condition: %s of type: %s",
					pluginName, getEvictResp.Condition.ConditionName, getEvictResp.Condition.ConditionType.String())

				currentConditions[getEvictResp.Condition.ConditionName] = proto.Clone(getEvictResp.Condition).(*pluginapi.Condition)
			}
		}

		metResp, err := ep.ThresholdMet(context.Background())
		if err != nil {
			klog.Errorf("[eviction manager] calling ThresholdMet of plugin: %s failed with error: %v", pluginName, err)
			continue
		} else if metResp == nil {
			klog.Errorf("[eviction manager] calling ThresholdMet of plugin: %s and getting nil resp", pluginName)
			continue
		}

		if metResp.MetType == pluginapi.ThresholdMetType_NOT_MET {
			klog.V(6).Infof("[eviction manager] plugin: %s threshold isn't met", pluginName)
			continue
		}

		klog.Infof("[eviction manager] plugin: %s met threshold: %s", pluginName, metResp.String())
		if metResp.Condition != nil && metResp.Condition.MetCondition {
			klog.Infof("[eviction manager] plugin: %s requests to set condition: %s of type: %s",
				pluginName, metResp.Condition.ConditionName, metResp.Condition.ConditionType.String())

			currentConditions[metResp.Condition.ConditionName] = proto.Clone(metResp.Condition).(*pluginapi.Condition)
		}

		currentMetThresholds[pluginName] = proto.Clone(metResp).(*pluginapi.ThresholdMetResponse)
	}
	m.endpointLock.RUnlock()

	// track when a threshold was first observed
	now := m.clock.Now()
	thresholdsFirstObservedAt := thresholdsFirstObservedAt(currentMetThresholds, m.thresholdsFirstObservedAt, now)
	thresholdsMet := thresholdsMetGracePeriod(thresholdsFirstObservedAt, now)
	logConfirmedThresholdMet(thresholdsMet)

	// track when a condition was last observed
	conditionsLastObservedAt := conditionsLastObservedAt(currentConditions, m.conditionsLastObservedAt, now)
	// conditions report true if it has been observed within the transition period window
	conditions := conditionsObservedSince(conditionsLastObservedAt, m.conf.ConditionTransitionPeriod, now)
	logConfirmedConditions(conditions)

	m.conditionLock.Lock()
	m.conditions = conditions
	m.conditionsLastObservedAt = conditionsLastObservedAt
	m.thresholdsFirstObservedAt = thresholdsFirstObservedAt
	m.conditionLock.Unlock()

	for pluginName, threshold := range thresholdsMet {
		if threshold.MetType != pluginapi.ThresholdMetType_HARD_MET {
			klog.Infof("[eviction manager] the type: %s of met threshold from plugin: %s isn't  %s", threshold.MetType.String(), pluginName, pluginapi.ThresholdMetType_HARD_MET.String())
			continue
		}

		m.endpointLock.RLock()
		if m.endpoints[pluginName] == nil {
			klog.Errorf("[eviction manager] pluginName points to nil endpoint, can't handle threshold from it")
		}

		resp, err := m.endpoints[pluginName].GetTopEvictionPods(context.Background(), &pluginapi.GetTopEvictionPodsRequest{
			ActivePods:    pods,
			TopN:          1,
			EvictionScope: threshold.EvictionScope,
		})

		m.endpointLock.RUnlock()
		if err != nil {
			klog.Errorf("[eviction manager] calling GetTopEvictionPods of plugin: %s failed with error: %v", pluginName, err)
			continue
		} else if resp == nil {
			klog.Errorf("[eviction manager] calling GetTopEvictionPods of plugin: %s and getting nil resp", pluginName)
			continue
		} else if len(resp.TargetPods) == 0 {
			klog.Warningf("[eviction manager] calling GetTopEvictionPods of plugin: %s and getting empty target pods", pluginName)
			continue
		}

		for _, pod := range resp.TargetPods {
			if pod == nil {
				continue
			}

			deletionOptions := resp.DeletionOptions
			reason := fmt.Sprintf("met threshold in scope: %s from plugin: %s", threshold.EvictionScope, pluginName)

			forceEvictPod := forceEvictPods[string(pod.UID)]
			if forceEvictPod != nil {
				if deletionOptions != nil && forceEvictPod.EvictPod.DeletionOptions != nil {
					deletionOptions.GracePeriodSeconds = general.MaxInt64(deletionOptions.GracePeriodSeconds,
						forceEvictPod.EvictPod.DeletionOptions.GracePeriodSeconds)
				} else if forceEvictPod.EvictPod.DeletionOptions != nil {
					deletionOptions.GracePeriodSeconds = forceEvictPod.EvictPod.DeletionOptions.GracePeriodSeconds
				}
				reason = fmt.Sprintf("%s; %s", reason, forceEvictPod.EvictPod.Reason)
			}

			forceEvictPods[string(pod.UID)] = &rule.RuledEvictPod{
				EvictPod: &pluginapi.EvictPod{
					Pod:                pod.DeepCopy(),
					Reason:             reason,
					DeletionOptions:    deletionOptions,
					ForceEvict:         true,
					EvictionPluginName: pluginName, // only count this pod to one plugin
				},
				Scope: threshold.EvictionScope,
			}
		}
	}

	softEvictPods = filterOutCandidatePodsWithForcePods(softEvictPods, forceEvictPods)
	bestSuitedCandidate := m.getEvictPodFromCandidates(softEvictPods)
	if bestSuitedCandidate != nil && bestSuitedCandidate.Pod != nil {
		klog.Infof("[eviction manager] choose best suited pod: %s/%s", bestSuitedCandidate.Pod.Namespace, bestSuitedCandidate.Pod.Name)
		forceEvictPods[string(bestSuitedCandidate.Pod.UID)] = bestSuitedCandidate
	}

	rpList := rule.RuledEvictPodList{}
	for _, rp := range forceEvictPods {
		if rp != nil && rp.EvictPod.Pod != nil && m.killStrategy.CandidateValidate(rp) {
			klog.Infof("[eviction manager] ready to evict %s/%s, reason: %s", rp.Pod.Namespace, rp.Pod.Name, rp.Reason)
			rpList = append(rpList, rp)
		} else {
			klog.Warningf("[eviction manager] found nil pod in forceEvictPods")
		}
	}

	err = m.killWithRules(rpList)
	if err != nil {
		klog.Errorf("[eviction manager] got err: %v in EvictPods", err)
		return
	}

	klog.Infof("[eviction manager] evict %d pods in evictionmanager", len(rpList))
	_ = m.emitter.StoreInt64(MetricsNameVictimPodCNT, int64(len(rpList)), metrics.MetricTypeNameRaw,
		metrics.MetricTag{Key: "type", Val: "total"})
	metricPodsToEvict(m.emitter, rpList)
}

// ValidatePlugin validates a plugin if the version is correct and the name has the format of an extended resource
func (m *EvictionManger) ValidatePlugin(pluginName string, endpoint string, versions []string) error {
	klog.Infof("[eviction manager] got plugin %s at endpoint %s with versions %v", pluginName, endpoint, versions)

	if !m.isVersionCompatibleWithPlugin(versions) {
		return fmt.Errorf("manager version, %s, is not among plugin supported versions %v", pluginapi.Version, versions)
	}

	return nil
}

func (m *EvictionManger) RegisterPlugin(pluginName string, endpoint string, versions []string) error {
	klog.Infof("[eviction manager] Registering Plugin %s at endpoint %s", pluginName, endpoint)

	e, err := endpointpkg.NewRemoteEndpointImpl(endpoint, pluginName)
	if err != nil {
		return fmt.Errorf("[eviction manager] failed to dial resource plugin with socketPath %s: %v", endpoint, err)
	}

	m.registerEndpoint(pluginName, e)

	return nil
}

func (m *EvictionManger) DeRegisterPlugin(pluginName string) {
	m.endpointLock.Lock()
	defer m.endpointLock.Unlock()

	if eI, ok := m.endpoints[pluginName]; ok {
		eI.Stop()
	}
}

func (m *EvictionManger) GetHandlerType() string {
	return registration.EvictionPlugin
}

func (m *EvictionManger) registerEndpoint(pluginName string, e endpointpkg.Endpoint) {
	m.endpointLock.Lock()
	defer m.endpointLock.Unlock()

	old, ok := m.endpoints[pluginName]
	if ok && !old.IsStopped() {
		klog.Infof("[eviction manager] stop old endpoint: %s", pluginName)
		old.Stop()
	}

	m.endpoints[pluginName] = e

	klog.Infof("[eviction manager] registered endpoint %s", pluginName)
}

func (m *EvictionManger) isVersionCompatibleWithPlugin(versions []string) bool {
	for _, version := range versions {
		for _, supportedVersion := range pluginapi.SupportedVersions {
			if version == supportedVersion {
				return true
			}
		}
	}
	return false
}

// killWithRules send killing requests according to pre-defined rules
// currently, we will use FIFO (with rate limiting) to
func (m *EvictionManger) killWithRules(rpList rule.RuledEvictPodList) error {
	// withdraw previous candidate killing pods by set override params as true
	m.killQueue.Add(rpList, true)
	return m.podKiller.EvictPods(m.killQueue.Pop())
}

// getEvictPodFromCandidates returns the most critical pod to be evicted
func (m *EvictionManger) getEvictPodFromCandidates(candidateEvictPods map[string]*rule.RuledEvictPod) *rule.RuledEvictPod {
	rpList := rule.RuledEvictPodList{}
	for _, rp := range candidateEvictPods {
		// only killing pods that pass candidate validation
		if rp != nil && rp.Pod != nil && m.killStrategy.CandidateValidate(rp) {
			rpList = append(rpList, rp)
		}
	}
	if len(rpList) == 0 {
		return nil
	}

	m.killStrategy.CandidateSort(rpList)
	return rpList[0]
}

// thresholdsFirstObservedAt merges the input set of thresholds with the previous observation to determine when active set of thresholds were initially met.
func thresholdsFirstObservedAt(thresholds map[string]*pluginapi.ThresholdMetResponse, lastObservedAt map[string]thresholdObservedAt, now time.Time) map[string]thresholdObservedAt {
	results := make(map[string]thresholdObservedAt)
	for pluginName, threshold := range thresholds {
		if threshold == nil {
			continue
		}

		observedAt, found := lastObservedAt[pluginName]
		if !found {
			observedAt = thresholdObservedAt{
				timestamp: now,
			}
		}
		observedAt.threshold = proto.Clone(threshold).(*pluginapi.ThresholdMetResponse)

		results[pluginName] = observedAt
	}
	return results
}

// conditionsLastObservedAt merges the input with the previous observation to determine when a condition was most recently met.
func conditionsLastObservedAt(conditions map[string]*pluginapi.Condition, lastObservedAt map[string]conditionObservedAt, now time.Time) map[string]conditionObservedAt {
	results := make(map[string]conditionObservedAt)

	// the input conditions were observed "now"
	for conditionName, condition := range conditions {
		results[conditionName] = conditionObservedAt{
			condition: proto.Clone(condition).(*pluginapi.Condition),
			timestamp: now,
		}
	}

	// the conditions that were not observed now are merged in with their old time
	for key, value := range lastObservedAt {
		_, found := results[key]
		if !found {
			results[key] = value
		}
	}
	return results
}

// conditionsObservedSince returns the set of conditions that have been observed within the specified period
func conditionsObservedSince(conditionsObservedAt map[string]conditionObservedAt, period time.Duration, now time.Time) map[string]*pluginapi.Condition {
	results := make(map[string]*pluginapi.Condition)

	for conditionName, observedAt := range conditionsObservedAt {
		duration := now.Sub(observedAt.timestamp)
		if duration < period {
			results[conditionName] = proto.Clone(observedAt.condition).(*pluginapi.Condition)
		}
	}
	return results
}

// thresholdsMetGracePeriod returns the set of thresholds that have satisfied associated grace period
func thresholdsMetGracePeriod(thresholdsObservedAt map[string]thresholdObservedAt, now time.Time) map[string]*pluginapi.ThresholdMetResponse {
	results := make(map[string]*pluginapi.ThresholdMetResponse)

	for pluginName, observedAt := range thresholdsObservedAt {
		if observedAt.threshold == nil {
			klog.Errorf("[eviction manager] met nil threshold in observedAt of plugin: %s", pluginName)
			continue
		}

		duration := now.Sub(observedAt.timestamp)
		if duration.Seconds() < float64(observedAt.threshold.GracePeriodSeconds) {
			klog.InfoS("[eviction manager] eviction criteria not yet met", "threshold", observedAt.threshold.String(), "duration", duration)
			continue
		}
		results[pluginName] = proto.Clone(observedAt.threshold).(*pluginapi.ThresholdMetResponse)
	}
	return results
}

// filterOutCandidatePodsWithForcePods returns candidateEvictPods that are not forced to be evicted
func filterOutCandidatePodsWithForcePods(candidateEvictPods, forceEvictPods map[string]*rule.RuledEvictPod) map[string]*rule.RuledEvictPod {
	ret := make(map[string]*rule.RuledEvictPod)

	for podUID, pod := range candidateEvictPods {
		if forceEvictPods[podUID] != nil {
			continue
		}

		ret[podUID] = pod
	}

	return ret
}

func logConfirmedConditions(conditions map[string]*pluginapi.Condition) {
	if len(conditions) == 0 {
		klog.Infof("[eviction manager] there is no condition confirmed")
	}

	for _, condition := range conditions {
		if condition == nil {
			continue
		}

		klog.Infof("[eviction manager] confirmed condition: %s", condition.String())
	}
}

func logConfirmedThresholdMet(thresholds map[string]*pluginapi.ThresholdMetResponse) {
	if len(thresholds) == 0 {
		klog.Infof("[eviction manager] there is no met threshold confirmed")
	}

	for pluginName, threshold := range thresholds {
		if threshold == nil {
			continue
		}

		klog.Infof("[eviction manager] confirmed met threshold: %s from plugin: %s", threshold.String(), pluginName)
	}
}

func metricPodsToEvict(emitter metrics.MetricEmitter, rpList rule.RuledEvictPodList) {
	if emitter == nil {
		klog.Errorf("[eviction manager] metricPodsToEvict got nil emitter")
		return
	}

	for _, rp := range rpList {
		if rp != nil && rp.EvictionPluginName != "" {
			_ = emitter.StoreInt64(MetricsNameVictimPodCNT, 1, metrics.MetricTypeNameRaw,
				metrics.MetricTag{Key: "name", Val: rp.EvictionPluginName},
				metrics.MetricTag{Key: "type", Val: "plugin"})
		}
	}
}
