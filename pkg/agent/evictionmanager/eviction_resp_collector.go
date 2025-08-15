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

package evictionmanager

import (
	"fmt"
	"strconv"
	"strings"

	//nolint
	"github.com/golang/protobuf/proto"
	v1 "k8s.io/api/core/v1"

	pluginapi "github.com/kubewharf/katalyst-api/pkg/protocol/evictionplugin/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/agent/evictionmanager/rule"
	pkgconfig "github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

const effectTagValueSeparator = "_"

// evictionRespCollector is used to collect eviction result from plugins, it also handles some logic such as dry run.
type evictionRespCollector struct {
	conf *pkgconfig.Configuration

	currentMetThresholds map[string]*pluginapi.ThresholdMetResponse
	currentConditions    map[string]*pluginapi.Condition

	// softEvictPods are candidates (among which only one will be chosen);
	// forceEvictPods are pods that should be killed immediately (but can be withdrawn)
	softEvictPods  map[string]*rule.RuledEvictPod
	forceEvictPods map[string]*rule.RuledEvictPod

	// emitter is used to emit metrics.
	emitter metrics.MetricEmitter
}

func newEvictionRespCollector(dryRun []string, conf *pkgconfig.Configuration, emitter metrics.MetricEmitter) *evictionRespCollector {
	collector := &evictionRespCollector{
		conf:                 conf,
		currentMetThresholds: make(map[string]*pluginapi.ThresholdMetResponse),
		currentConditions:    make(map[string]*pluginapi.Condition),

		softEvictPods:  make(map[string]*rule.RuledEvictPod),
		forceEvictPods: make(map[string]*rule.RuledEvictPod),

		emitter: emitter,
	}
	general.Infof("dry run plugins is %v", dryRun)
	return collector
}

func (e *evictionRespCollector) isDryRun(dryRunPlugins []string, pluginName string) bool {
	if len(dryRunPlugins) == 0 {
		return false
	}

	return general.IsNameEnabled(pluginName, nil, dryRunPlugins)
}

func (e *evictionRespCollector) getLogPrefix(dryRun bool) string {
	if dryRun {
		return "[DryRun]"
	}

	return ""
}

func (e *evictionRespCollector) collectEvictPods(dryRunPlugins []string, pluginName string, resp *pluginapi.GetEvictPodsResponse) {
	dryRun := e.isDryRun(dryRunPlugins, pluginName)

	evictPods := make([]*pluginapi.EvictPod, 0, len(resp.EvictPods))
	for i, evictPod := range resp.EvictPods {
		if evictPod == nil || evictPod.Pod == nil {
			general.Errorf("%v skip nil evict pod of plugin: %s", e.getLogPrefix(dryRun), pluginName)
			continue
		}

		general.Infof("%v plugin: %s requests to evict pod: %s/%s with reason: %s, forceEvict: %v",
			e.getLogPrefix(dryRun), pluginName, evictPod.Pod.Namespace, evictPod.Pod.Name, evictPod.Reason, evictPod.ForceEvict)

		if dryRun {
			metricsPodToEvict(e.emitter, e.conf.GenericConfiguration.QoSConfiguration, pluginName, evictPod.Pod, dryRun, e.conf.GenericEvictionConfiguration.PodMetricLabels)
		} else {
			evictPods = append(evictPods, resp.EvictPods[i])
		}
	}

	for _, evictPod := range evictPods {

		// to avoid plugins forget to set EvictionPluginName property
		evictPod.EvictionPluginName = pluginName

		if evictPod.ForceEvict {
			e.getForceEvictPods()[string(evictPod.Pod.UID)] = &rule.RuledEvictPod{
				EvictPod: proto.Clone(evictPod).(*pluginapi.EvictPod),
				Scope:    rule.EvictionScopeForce,
			}
		} else {
			e.getSoftEvictPods()[string(evictPod.Pod.UID)] = &rule.RuledEvictPod{
				EvictPod: proto.Clone(evictPod).(*pluginapi.EvictPod),
				Scope:    rule.EvictionScopeSoft,
			}
		}
	}

	if resp.Condition != nil && resp.Condition.MetCondition {
		general.Infof("%v plugin: %s requests set condition: %s of type: %s",
			e.getLogPrefix(dryRun), pluginName, resp.Condition.ConditionName, resp.Condition.ConditionType.String())

		if !dryRun {
			e.getCurrentConditions()[resp.Condition.ConditionName] = proto.Clone(resp.Condition).(*pluginapi.Condition)
		}
	}
}

func (e *evictionRespCollector) collectMetThreshold(dryRunPlugins []string, pluginName string, resp *pluginapi.ThresholdMetResponse) {
	dryRun := e.isDryRun(dryRunPlugins, pluginName)

	if resp.MetType == pluginapi.ThresholdMetType_NOT_MET {
		general.InfofV(6, "%v plugin: %s threshold isn't met", e.getLogPrefix(dryRun), pluginName)
		return
	}

	// save thresholds to currentMetThreshold even in dry run mode so that GetTopEvictionPods function will be called
	e.getCurrentMetThresholds()[pluginName] = proto.Clone(resp).(*pluginapi.ThresholdMetResponse)

	general.Infof("%v plugin: %s met threshold: %s", e.getLogPrefix(dryRun), pluginName, resp.String())
	if resp.Condition != nil && resp.Condition.MetCondition {
		general.Infof("%v plugin: %s requests to set condition: %s of type: %s",
			e.getLogPrefix(dryRun), pluginName, resp.Condition.ConditionName, resp.Condition.ConditionType.String())
		_ = e.emitter.StoreInt64(MetricsNameRequestConditionCNT, 1, metrics.MetricTypeNameRaw,
			metrics.MetricTag{Key: "name", Val: pluginName},
			metrics.MetricTag{Key: "condition_name", Val: resp.Condition.ConditionName},
			metrics.MetricTag{Key: "condition_type", Val: fmt.Sprint(resp.Condition.ConditionType)},
			metrics.MetricTag{Key: "effects", Val: strings.Join(resp.Condition.Effects, effectTagValueSeparator)},
			metrics.MetricTag{Key: "dryrun", Val: strconv.FormatBool(dryRun)},
		)

		if !dryRun {
			e.getCurrentConditions()[resp.Condition.ConditionName] = proto.Clone(resp.Condition).(*pluginapi.Condition)
		}
	}
}

func (e *evictionRespCollector) collectTopSoftEvictionPods(dryRunPlugins []string, pluginName string,
	threshold *pluginapi.ThresholdMetResponse, resp *pluginapi.GetTopEvictionPodsResponse,
) {
	dryRun := e.isDryRun(dryRunPlugins, pluginName)

	targetPods := make([]*v1.Pod, 0, len(resp.TargetPods))
	for i, pod := range resp.TargetPods {
		if pod == nil {
			continue
		}

		general.Infof("%v plugin %v request to notify topN pod %v/%v, reason: met threshold in scope [%v]",
			e.getLogPrefix(dryRun), pluginName, pod.Namespace, pod.Name, threshold.EvictionScope)
		if dryRun {
			metricsPodToEvict(e.emitter, e.conf.GenericConfiguration.QoSConfiguration, pluginName, pod, dryRun, e.conf.GenericEvictionConfiguration.PodMetricLabels)
		} else {
			targetPods = append(targetPods, resp.TargetPods[i])
		}
	}

	for _, pod := range targetPods {
		reason := fmt.Sprintf("plugin %s met threshold in scope %s, target %v, observed %v",
			pluginName, threshold.EvictionScope, threshold.ThresholdValue, threshold.ObservedValue)

		e.getSoftEvictPods()[string(pod.UID)] = &rule.RuledEvictPod{
			EvictPod: &pluginapi.EvictPod{
				Pod:                pod.DeepCopy(),
				Reason:             reason,
				ForceEvict:         false,
				EvictionPluginName: pluginName,
			},
			Scope: threshold.EvictionScope,
		}
	}
}

func (e *evictionRespCollector) collectTopEvictionPods(dryRunPlugins []string, pluginName string,
	threshold *pluginapi.ThresholdMetResponse, resp *pluginapi.GetTopEvictionPodsResponse,
) {
	dryRun := e.isDryRun(dryRunPlugins, pluginName)

	targetPods := make([]*v1.Pod, 0, len(resp.TargetPods))
	for i, pod := range resp.TargetPods {
		if pod == nil {
			continue
		}

		general.Infof("%v plugin %v request to evict topN pod %v/%v, reason: met threshold in scope [%v]",
			e.getLogPrefix(dryRun), pluginName, pod.Namespace, pod.Name, threshold.EvictionScope)
		if dryRun {
			metricsPodToEvict(e.emitter, e.conf.GenericConfiguration.QoSConfiguration, pluginName, pod, dryRun, e.conf.GenericEvictionConfiguration.PodMetricLabels)
		} else {
			targetPods = append(targetPods, resp.TargetPods[i])
		}
	}

	for _, pod := range targetPods {
		deletionOptions := resp.DeletionOptions
		reason := fmt.Sprintf("plugin %s met threshold in scope %s, target %v, observed %v",
			pluginName, threshold.EvictionScope, threshold.ThresholdValue, threshold.ObservedValue)

		forceEvictPod := e.getForceEvictPods()[string(pod.UID)]
		if forceEvictPod != nil && forceEvictPod.EvictPod != nil {
			if deletionOptions != nil && forceEvictPod.EvictPod.DeletionOptions != nil {
				deletionOptions.GracePeriodSeconds = general.MaxInt64(deletionOptions.GracePeriodSeconds,
					forceEvictPod.EvictPod.DeletionOptions.GracePeriodSeconds)
			} else if forceEvictPod.EvictPod.DeletionOptions != nil {
				deletionOptions.GracePeriodSeconds = forceEvictPod.EvictPod.DeletionOptions.GracePeriodSeconds
			}
			reason = fmt.Sprintf("%s; %s", reason, forceEvictPod.EvictPod.Reason)
		}

		e.getForceEvictPods()[string(pod.UID)] = &rule.RuledEvictPod{
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

func (e *evictionRespCollector) getCurrentConditions() map[string]*pluginapi.Condition {
	return e.currentConditions
}

func (e *evictionRespCollector) getCurrentMetThresholds() map[string]*pluginapi.ThresholdMetResponse {
	return e.currentMetThresholds
}

func (e *evictionRespCollector) getSoftEvictPods() map[string]*rule.RuledEvictPod {
	return e.softEvictPods
}

func (e *evictionRespCollector) getForceEvictPods() map[string]*rule.RuledEvictPod {
	return e.forceEvictPods
}
