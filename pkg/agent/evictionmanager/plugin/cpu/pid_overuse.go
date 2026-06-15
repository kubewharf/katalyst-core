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

package cpu

import (
	"context"
	"fmt"
	"sort"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/events"

	pluginapi "github.com/kubewharf/katalyst-api/pkg/protocol/evictionplugin/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/agent/evictionmanager/plugin"
	"github.com/kubewharf/katalyst-core/pkg/client"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/helper"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/process"
)

const EvictionPluginNamePIDOveruse = "pid-overuse-eviction-plugin"

type PIDOveruseEvictionPlugin struct {
	*process.StopControl

	pluginName    string
	dynamicConfig *dynamic.DynamicAgentConfiguration
	metaServer    *metaserver.MetaServer
	emitter       metrics.MetricEmitter
}

type podPIDUsage struct {
	pod             *v1.Pod
	total           int64
	runnable        int64
	uninterruptible int64
	ioWait          int64
	sleeping        int64
}

type podPIDUsageList []podPIDUsage

func (l podPIDUsageList) Len() int      { return len(l) }
func (l podPIDUsageList) Swap(i, j int) { l[i], l[j] = l[j], l[i] }
func (l podPIDUsageList) Less(i, j int) bool {
	return l[i].total > l[j].total
}

func NewPIDOveruseEvictionPlugin(_ *client.GenericClientSet, _ events.EventRecorder,
	metaServer *metaserver.MetaServer, emitter metrics.MetricEmitter, conf *config.Configuration,
) plugin.EvictionPlugin {
	return &PIDOveruseEvictionPlugin{
		StopControl:   process.NewStopControl(time.Time{}),
		pluginName:    EvictionPluginNamePIDOveruse,
		dynamicConfig: conf.DynamicAgentConfiguration,
		metaServer:    metaServer,
		emitter:       emitter,
	}
}

func (p *PIDOveruseEvictionPlugin) Name() string {
	if p == nil {
		return ""
	}

	return p.pluginName
}

func (p *PIDOveruseEvictionPlugin) Start() {}

func (p *PIDOveruseEvictionPlugin) ThresholdMet(_ context.Context, _ *pluginapi.GetThresholdMetRequest) (*pluginapi.ThresholdMetResponse, error) {
	return &pluginapi.ThresholdMetResponse{
		MetType: pluginapi.ThresholdMetType_NOT_MET,
	}, nil
}

func (p *PIDOveruseEvictionPlugin) GetTopEvictionPods(_ context.Context, _ *pluginapi.GetTopEvictionPodsRequest) (*pluginapi.GetTopEvictionPodsResponse, error) {
	return &pluginapi.GetTopEvictionPodsResponse{}, nil
}

func (p *PIDOveruseEvictionPlugin) GetEvictPods(_ context.Context, request *pluginapi.GetEvictPodsRequest) (*pluginapi.GetEvictPodsResponse, error) {
	if request == nil {
		return nil, fmt.Errorf("GetEvictPods got nil request")
	}

	dynamicConfig := p.dynamicConfig.GetDynamicConfiguration().CPUPressureEvictionConfiguration
	if dynamicConfig == nil || !dynamicConfig.EnablePIDOveruseEviction || dynamicConfig.PIDOveruseThreshold <= 0 {
		return &pluginapi.GetEvictPodsResponse{}, nil
	}

	usageList := make(podPIDUsageList, 0, len(request.ActivePods))
	for _, pod := range request.ActivePods {
		usage, err := p.getPodPIDUsage(pod)
		if err != nil {
			general.Warningf("failed to get pod pid usage for %s/%s: %v", pod.Namespace, pod.Name, err)
			continue
		}

		if usage.total > dynamicConfig.PIDOveruseThreshold {
			usageList = append(usageList, usage)
		}
	}

	if len(usageList) == 0 {
		return &pluginapi.GetEvictPodsResponse{}, nil
	}

	sort.Sort(usageList)
	result := make([]*pluginapi.EvictPod, 0, len(usageList))
	deletionOptions := &pluginapi.DeletionOptions{
		GracePeriodSeconds: dynamicConfig.PIDOveruseGracePeriod,
	}

	for _, item := range usageList {
		evictPod := &pluginapi.EvictPod{
			Pod:                item.pod,
			ForceEvict:         true,
			EvictionPluginName: p.pluginName,
			Reason: fmt.Sprintf("pid overuse threshold met, total: %d, threshold: %d, runnable: %d, uninterruptible: %d, iowait: %d, sleeping: %d",
				item.total, dynamicConfig.PIDOveruseThreshold, item.runnable, item.uninterruptible, item.ioWait, item.sleeping),
		}
		if deletionOptions.GracePeriodSeconds > 0 {
			evictPod.DeletionOptions = deletionOptions
		}
		result = append(result, evictPod)
	}

	return &pluginapi.GetEvictPodsResponse{EvictPods: result}, nil
}

func (p *PIDOveruseEvictionPlugin) getPodPIDUsage(pod *v1.Pod) (podPIDUsage, error) {
	runnable, err := helper.GetPodMetric(p.metaServer.MetricsFetcher, p.emitter, pod, consts.MetricCPUNrRunnableContainer, -1)
	if err != nil {
		return podPIDUsage{}, err
	}
	uninterruptible, err := helper.GetPodMetric(p.metaServer.MetricsFetcher, p.emitter, pod, consts.MetricCPUNrUninterruptibleContainer, -1)
	if err != nil {
		return podPIDUsage{}, err
	}
	ioWait, err := helper.GetPodMetric(p.metaServer.MetricsFetcher, p.emitter, pod, consts.MetricCPUNrIOWaitContainer, -1)
	if err != nil {
		return podPIDUsage{}, err
	}
	sleeping, err := helper.GetPodMetric(p.metaServer.MetricsFetcher, p.emitter, pod, consts.MetricCPUNrSleepingContainer, -1)
	if err != nil {
		return podPIDUsage{}, err
	}

	return podPIDUsage{
		pod:             pod,
		total:           int64(runnable + uninterruptible + ioWait + sleeping),
		runnable:        int64(runnable),
		uninterruptible: int64(uninterruptible),
		ioWait:          int64(ioWait),
		sleeping:        int64(sleeping),
	}, nil
}
