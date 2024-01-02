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
	"strconv"
	"sync"
	"time"

	//nolint
	"github.com/golang/protobuf/proto"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/events"
	clocks "k8s.io/utils/clock"

	"github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/plugins/registration"
	pluginapi "github.com/kubewharf/katalyst-api/pkg/protocol/evictionplugin/v1alpha1"
	endpointpkg "github.com/kubewharf/katalyst-core/pkg/agent/evictionmanager/endpoint"
	"github.com/kubewharf/katalyst-core/pkg/agent/evictionmanager/plugin"
	"github.com/kubewharf/katalyst-core/pkg/agent/evictionmanager/plugin/memory"
	"github.com/kubewharf/katalyst-core/pkg/agent/evictionmanager/plugin/resource"
	"github.com/kubewharf/katalyst-core/pkg/agent/evictionmanager/plugin/rootfs"
	"github.com/kubewharf/katalyst-core/pkg/agent/evictionmanager/podkiller"
	"github.com/kubewharf/katalyst-core/pkg/agent/evictionmanager/rule"
	"github.com/kubewharf/katalyst-core/pkg/client"
	pkgconfig "github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/credential"
	"github.com/kubewharf/katalyst-core/pkg/util/credential/authorization"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

const (
	MetricsNameVictimPodCNT           = "victims_cnt"
	MetricsNameRunningPodCNT          = "running_pod_cnt"
	MetricsNameCandidatePodCNT        = "candidate_pod_cnt"
	MetricsNameDryRunVictimPodCNT     = "dryrun_victims_cnt"
	MetricsNameRequestConditionCNT    = "request_condition_cnt"
	MetricsNameEvictionPluginCalled   = "eviction_plugin_called"
	MetricsNameEvictionPluginValidate = "eviction_plugin_validate"

	ValidateFailedReasonGetTokenFailed     = "get_token_failed"
	ValidateFailedReasonAuthenticateFailed = "authenticate_failed"
	ValidateFailedReasonNoPermission       = "no_permission"

	UserUnknown = "unknown"
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

	cred credential.Credential
	auth authorization.AccessControl
}

var InnerEvictionPluginsDisabledByDefault = sets.NewString()

func NewInnerEvictionPluginInitializers() map[string]plugin.InitFunc {
	innerEvictionPluginInitializers := make(map[string]plugin.InitFunc)
	innerEvictionPluginInitializers[resource.ReclaimedResourcesEvictionPluginName] = resource.NewReclaimedResourcesEvictionPlugin
	innerEvictionPluginInitializers[memory.EvictionPluginNameNumaMemoryPressure] = memory.NewNumaMemoryPressureEvictionPlugin
	innerEvictionPluginInitializers[memory.EvictionPluginNameSystemMemoryPressure] = memory.NewSystemPressureEvictionPlugin
	innerEvictionPluginInitializers[memory.EvictionPluginNameRssOveruse] = memory.NewRssOveruseEvictionPlugin
	innerEvictionPluginInitializers[rootfs.EvictionPluginNamePodRootfsPressure] = rootfs.NewPodRootfsPressureEvictionPlugin
	return innerEvictionPluginInitializers
}

func NewPodKillerInitializers() map[string]podkiller.InitFunc {
	podKillerInitializers := make(map[string]podkiller.InitFunc)
	podKillerInitializers[consts.KillerNameEvictionKiller] = podkiller.NewEvictionAPIKiller
	podKillerInitializers[consts.KillerNameDeletionKiller] = podkiller.NewDeletionAPIKiller
	podKillerInitializers[consts.KillerNameContainerKiller] = podkiller.NewContainerKiller
	return podKillerInitializers
}

func NewEvictionManager(genericClient *client.GenericClientSet, recorder events.EventRecorder,
	metaServer *metaserver.MetaServer, emitter metrics.MetricEmitter, conf *pkgconfig.Configuration) (*EvictionManger, error) {
	queue := rule.NewFIFOEvictionQueue(conf.EvictionBurst)

	podKillerInitializers := NewPodKillerInitializers()
	var killer podkiller.Killer
	if initFunc, ok := podKillerInitializers[conf.PodKiller]; ok {
		var initErr error
		killer, initErr = initFunc(conf, genericClient.KubeClient, recorder, emitter)
		if initErr != nil {
			return nil, fmt.Errorf("failed to init pod killer %v: %v", conf.PodKiller, initErr)
		}
	} else {
		return nil, fmt.Errorf("unsupported pod killer %v", conf.PodKiller)
	}

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
		cred:                      credential.DefaultCredential(),
		auth:                      authorization.DefaultAccessControl(),
	}

	cred, credErr := credential.GetCredential(conf.GenericConfiguration, conf.DynamicAgentConfiguration)
	if credErr != nil {
		return nil, credErr
	}
	e.cred = cred

	accessControl, acErr := authorization.GetAccessControl(conf.GenericConfiguration, conf.DynamicAgentConfiguration)
	if acErr != nil {
		return nil, acErr
	}
	e.auth = accessControl

	e.getEvictionPlugins(genericClient, recorder, metaServer, emitter, conf, NewInnerEvictionPluginInitializers())
	return e, nil
}

func (m *EvictionManger) getEvictionPlugins(genericClient *client.GenericClientSet, recorder events.EventRecorder, metaServer *metaserver.MetaServer,
	emitter metrics.MetricEmitter, conf *pkgconfig.Configuration, innerEvictionPluginInitializers map[string]plugin.InitFunc) {
	m.endpointLock.Lock()
	for pluginName, initFn := range innerEvictionPluginInitializers {
		if !general.IsNameEnabled(pluginName, InnerEvictionPluginsDisabledByDefault, conf.GenericEvictionConfiguration.InnerPlugins) {
			general.Warningf(" %s is disabled", pluginName)
			continue
		}

		curPlugin := initFn(genericClient, recorder, metaServer, emitter, conf)
		m.endpoints[curPlugin.Name()] = curPlugin
	}
	m.endpointLock.Unlock()
}

func (m *EvictionManger) Run(ctx context.Context) {
	general.Infof(" run with podKiller %v", m.podKiller.Name())
	defer general.Infof(" started")

	m.podKiller.Start(ctx)
	for _, endpoint := range m.endpoints {
		endpoint.Start()
	}
	m.cred.Run(ctx)
	m.auth.Run(ctx)
	go wait.UntilWithContext(ctx, m.sync, m.conf.EvictionManagerSyncPeriod)
	go wait.UntilWithContext(ctx, m.reportConditionsAsNodeTaints, time.Second*5)
	<-ctx.Done()
}

func (m *EvictionManger) sync(ctx context.Context) {
	activePods, err := m.metaGetter.GetPodList(ctx, native.PodIsActive)
	if err != nil {
		general.Errorf("failed to list pods from metaServer: %v", err)
		return
	}

	general.Infof(" currently, there are %v active pods", len(activePods))
	_ = m.emitter.StoreInt64(MetricsNameRunningPodCNT, int64(len(activePods)), metrics.MetricTypeNameRaw)

	pods := native.FilterOutSkipEvictionPods(activePods, m.conf.EvictionSkippedAnnotationKeys, m.conf.EvictionSkippedLabelKeys)
	general.Infof(" currently, there are %v candidate pods", len(pods))
	_ = m.emitter.StoreInt64(MetricsNameCandidatePodCNT, int64(len(pods)), metrics.MetricTypeNameRaw)

	collector := m.collectEvictionResult(pods)

	m.doEvict(collector.getSoftEvictPods(), collector.getForceEvictPods())
}

func (m *EvictionManger) collectEvictionResult(pods []*v1.Pod) *evictionRespCollector {
	dynamicConfig := m.conf.GetDynamicConfiguration()
	collector := newEvictionRespCollector(dynamicConfig.DryRun, m.conf, m.emitter)

	m.endpointLock.RLock()
	for pluginName, ep := range m.endpoints {
		_ = m.emitter.StoreInt64(MetricsNameEvictionPluginCalled, 1, metrics.MetricTypeNameCount,
			metrics.MetricTag{Key: "name", Val: pluginName})

		getEvictResp, err := ep.GetEvictPods(context.Background(), &pluginapi.GetEvictPodsRequest{
			ActivePods: pods,
		})
		if err != nil {
			general.Errorf(" calling GetEvictPods of plugin: %s failed with error: %v", pluginName, err)
		} else if getEvictResp == nil {
			general.Errorf(" calling GetEvictPods of plugin: %s and getting nil resp", pluginName)
		} else {
			general.Infof(" GetEvictPods of plugin: %s with %d pods to evict", pluginName, len(getEvictResp.EvictPods))
			collector.collectEvictPods(dynamicConfig.DryRun, pluginName, getEvictResp)
		}

		metResp, err := ep.ThresholdMet(context.Background())
		if err != nil {
			general.Errorf(" calling ThresholdMet of plugin: %s failed with error: %v", pluginName, err)
			continue
		} else if metResp == nil {
			general.Errorf(" calling ThresholdMet of plugin: %s and getting nil resp", pluginName)
			continue
		}

		collector.collectMetThreshold(dynamicConfig.DryRun, pluginName, metResp)
	}
	m.endpointLock.RUnlock()

	// track when a threshold was first observed
	now := m.clock.Now()
	thresholdsFirstObservedAt := thresholdsFirstObservedAt(collector.currentMetThresholds, m.thresholdsFirstObservedAt, now)
	thresholdsMet := thresholdsMetGracePeriod(thresholdsFirstObservedAt, now)
	logConfirmedThresholdMet(thresholdsMet)

	// track when a condition was last observed
	conditionsLastObservedAt := conditionsLastObservedAt(collector.currentConditions, m.conditionsLastObservedAt, now)
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
			general.Infof(" the type: %s of met threshold from plugin: %s isn't  %s", threshold.MetType.String(), pluginName, pluginapi.ThresholdMetType_HARD_MET.String())
			continue
		}

		m.endpointLock.RLock()
		if m.endpoints[pluginName] == nil {
			general.Errorf(" pluginName points to nil endpoint, can't handle threshold from it")
		}

		resp, err := m.endpoints[pluginName].GetTopEvictionPods(context.Background(), &pluginapi.GetTopEvictionPodsRequest{
			ActivePods:    pods,
			TopN:          1,
			EvictionScope: threshold.EvictionScope,
		})

		m.endpointLock.RUnlock()
		if err != nil {
			general.Errorf(" calling GetTopEvictionPods of plugin: %s failed with error: %v", pluginName, err)
			continue
		} else if resp == nil {
			general.Errorf(" calling GetTopEvictionPods of plugin: %s and getting nil resp", pluginName)
			continue
		} else if len(resp.TargetPods) == 0 {
			general.Warningf(" calling GetTopEvictionPods of plugin: %s and getting empty target pods", pluginName)
			continue
		}

		collector.collectTopEvictionPods(dynamicConfig.DryRun, pluginName, threshold, resp)
	}

	return collector
}

func (m *EvictionManger) doEvict(softEvictPods, forceEvictPods map[string]*rule.RuledEvictPod) {
	softEvictPods = filterOutCandidatePodsWithForcePods(softEvictPods, forceEvictPods)
	bestSuitedCandidate := m.getEvictPodFromCandidates(softEvictPods)
	if bestSuitedCandidate != nil && bestSuitedCandidate.Pod != nil {
		general.Infof(" choose best suited pod: %s/%s", bestSuitedCandidate.Pod.Namespace, bestSuitedCandidate.Pod.Name)
		forceEvictPods[string(bestSuitedCandidate.Pod.UID)] = bestSuitedCandidate
	}

	rpList := rule.RuledEvictPodList{}
	for _, rp := range forceEvictPods {
		if rp != nil && rp.EvictPod.Pod != nil && m.killStrategy.CandidateValidate(rp) {
			general.Infof(" ready to evict %s/%s, reason: %s", rp.Pod.Namespace, rp.Pod.Name, rp.Reason)
			rpList = append(rpList, rp)
		} else {
			general.Warningf(" found nil pod in forceEvictPods")
		}
	}

	err := m.killWithRules(rpList)
	if err != nil {
		general.Errorf(" got err: %v in EvictPods", err)
		return
	}

	general.Infof(" evict %d pods in evictionmanager", len(rpList))
	_ = m.emitter.StoreInt64(MetricsNameVictimPodCNT, int64(len(rpList)), metrics.MetricTypeNameRaw,
		metrics.MetricTag{Key: "type", Val: "total"})
	metricPodsToEvict(m.emitter, rpList)
}

// ValidatePlugin validates a plugin if the version is correct and the name has the format of an extended resource
func (m *EvictionManger) ValidatePlugin(pluginName string, endpoint string, versions []string) error {
	general.Infof(" got plugin %s at endpoint %s with versions %v", pluginName, endpoint, versions)

	if !m.isVersionCompatibleWithPlugin(versions) {
		return fmt.Errorf("manager version, %s, is not among plugin supported versions %v", pluginapi.Version, versions)
	}

	e, err := endpointpkg.NewRemoteEndpointImpl(endpoint, pluginName)
	if err != nil {
		return fmt.Errorf(" failed to dial resource plugin with socketPath %s: %v", endpoint, err)
	}

	// try to push authentication process as far as we can even in non-strict mode, it helps to identify who
	// registers this plugin
	tokenResp, tokenErr := e.GetToken(context.TODO())
	if tokenErr != nil {
		m.emitPluginValidateResult(pluginName, m.conf.StrictAuthentication, false, ValidateFailedReasonGetTokenFailed, UserUnknown)
		if m.conf.StrictAuthentication {
			return fmt.Errorf(" failed to get token:%v", tokenErr)
		}
		general.Warningf("no valid token for plugin %s:%v", pluginName, tokenErr)
		return nil
	}

	authInfo, authErr := m.cred.AuthToken(tokenResp.Token)
	if authErr != nil {
		m.emitPluginValidateResult(pluginName, m.conf.StrictAuthentication, false, ValidateFailedReasonAuthenticateFailed, UserUnknown)
		if m.conf.StrictAuthentication {
			return fmt.Errorf(" failed to verify token:%v", authErr)
		}
		general.Warningf("failed to verify token for plugin %s:%v", pluginName, authErr)
		return nil
	}

	general.Infof("user %v request to register plugin %v", authInfo.SubjectName(), pluginName)

	verifyErr := m.auth.Verify(authInfo, authorization.PermissionTypeEvictionPlugin)
	if verifyErr != nil {
		m.emitPluginValidateResult(pluginName, m.conf.StrictAuthentication, false, ValidateFailedReasonNoPermission, authInfo.SubjectName())
		if m.conf.StrictAuthentication {
			return err
		}
		return nil
	}

	m.emitPluginValidateResult(pluginName, m.conf.StrictAuthentication, true, "", authInfo.SubjectName())
	return nil
}

func (m *EvictionManger) emitPluginValidateResult(pluginName string, strict bool, valid bool, reason string, user string) {
	_ = m.emitter.StoreInt64(MetricsNameEvictionPluginValidate, 1, metrics.MetricTypeNameCount,
		metrics.MetricTag{Key: "name", Val: pluginName},
		metrics.MetricTag{Key: "strict", Val: strconv.FormatBool(strict)},
		metrics.MetricTag{Key: "valid", Val: strconv.FormatBool(valid)},
		metrics.MetricTag{Key: "reason", Val: reason},
		metrics.MetricTag{Key: "user", Val: user})
}

func (m *EvictionManger) RegisterPlugin(pluginName string, endpoint string, _ []string) error {
	general.Infof(" Registering Plugin %s at endpoint %s", pluginName, endpoint)

	e, err := endpointpkg.NewRemoteEndpointImpl(endpoint, pluginName)
	if err != nil {
		return fmt.Errorf(" failed to dial resource plugin with socketPath %s: %v", endpoint, err)
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
		general.Infof(" stop old endpoint: %s", pluginName)
		old.Stop()
	}

	m.endpoints[pluginName] = e
	e.Start()

	general.Infof(" registered endpoint %s", pluginName)
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
			general.Errorf(" met nil threshold in observedAt of plugin: %s", pluginName)
			continue
		}

		duration := now.Sub(observedAt.timestamp)
		if duration.Seconds() < float64(observedAt.threshold.GracePeriodSeconds) {
			general.InfoS(" eviction criteria not yet met", "threshold", observedAt.threshold.String(), "duration", duration)
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
		general.Infof(" there is no condition confirmed")
	}

	for _, condition := range conditions {
		if condition == nil {
			continue
		}

		general.Infof(" confirmed condition: %s", condition.String())
	}
}

func logConfirmedThresholdMet(thresholds map[string]*pluginapi.ThresholdMetResponse) {
	if len(thresholds) == 0 {
		general.Infof(" there is no met threshold confirmed")
	}

	for pluginName, threshold := range thresholds {
		if threshold == nil {
			continue
		}

		general.Infof(" confirmed met threshold: %s from plugin: %s", threshold.String(), pluginName)
	}
}

func metricPodsToEvict(emitter metrics.MetricEmitter, rpList rule.RuledEvictPodList) {
	if emitter == nil {
		general.Errorf(" metricPodsToEvict got nil emitter")
		return
	}

	for _, rp := range rpList {
		if rp != nil && rp.EvictionPluginName != "" {
			_ = emitter.StoreInt64(MetricsNameVictimPodCNT, 1, metrics.MetricTypeNameRaw,
				metrics.MetricTag{Key: "name", Val: rp.EvictionPluginName},
				metrics.MetricTag{Key: "type", Val: "plugin"},
				metrics.MetricTag{Key: "victim_ns", Val: rp.Pod.Namespace},
				metrics.MetricTag{Key: "victim_name", Val: rp.Pod.Name})
		}
	}
}
