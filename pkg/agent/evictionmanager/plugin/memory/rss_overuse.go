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

package memory

import (
	"context"
	"fmt"
	"strconv"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/tools/events"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	pluginapi "github.com/kubewharf/katalyst-api/pkg/protocol/evictionplugin/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/agent/evictionmanager/plugin"
	"github.com/kubewharf/katalyst-core/pkg/client"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/eviction"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/helper"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/process"
	"github.com/kubewharf/katalyst-core/pkg/util/qos"
)

const (
	EvictionPluginNameRssOveruse = "rss-overuse-eviction-plugin"

	RssOveruseEvictionReason = "hit rss overuse policy, threshold is %.2f, current pod rss is %.2f, pod memory request is %d"
)

func NewRssOveruseEvictionPlugin(_ *client.GenericClientSet, _ events.EventRecorder,
	metaServer *metaserver.MetaServer, emitter metrics.MetricEmitter, conf *config.Configuration) plugin.EvictionPlugin {
	return &RssOveruseEvictionPlugin{
		StopControl:        process.NewStopControl(time.Time{}),
		emitter:            emitter,
		reclaimedPodFilter: conf.CheckReclaimedQoSForPod,
		pluginName:         EvictionPluginNameRssOveruse,
		metaServer:         metaServer,
		supportedQosLevels: sets.NewString(apiconsts.PodAnnotationQoSLevelReclaimedCores, apiconsts.PodAnnotationQoSLevelSharedCores),

		dynamicConfig:  conf.DynamicAgentConfiguration,
		qosConf:        conf.QoSConfiguration,
		evictionConfig: conf.MemoryPressureEvictionConfiguration,
	}
}

// RssOveruseEvictionPlugin implements the EvictPlugin interface. It triggers pod eviction based on the rss usage ratio.
// Once a pod use more rss than the specified threshold, this plugin will evict the pod.The threshold is calculated based
// on pod's memory request. Its main goal is to make sure sufficient memory for page cache in some scenarios in which
// service use page cache to improve performance.
type RssOveruseEvictionPlugin struct {
	*process.StopControl

	emitter            metrics.MetricEmitter
	reclaimedPodFilter func(pod *v1.Pod) (bool, error)
	pluginName         string
	metaServer         *metaserver.MetaServer
	supportedQosLevels sets.String

	dynamicConfig  *dynamic.DynamicAgentConfiguration
	qosConf        *generic.QoSConfiguration
	evictionConfig *eviction.MemoryPressureEvictionConfiguration
}

func (r *RssOveruseEvictionPlugin) Name() string {
	if r == nil {
		return ""
	}

	return r.pluginName
}

func (r *RssOveruseEvictionPlugin) ThresholdMet(_ context.Context) (*pluginapi.ThresholdMetResponse, error) {
	return &pluginapi.ThresholdMetResponse{
		MetType: pluginapi.ThresholdMetType_NOT_MET,
	}, nil
}

func (r *RssOveruseEvictionPlugin) GetTopEvictionPods(_ context.Context, _ *pluginapi.GetTopEvictionPodsRequest) (*pluginapi.GetTopEvictionPodsResponse, error) {
	return &pluginapi.GetTopEvictionPodsResponse{}, nil
}

func (r *RssOveruseEvictionPlugin) GetEvictPods(_ context.Context, request *pluginapi.GetEvictPodsRequest) (*pluginapi.GetEvictPodsResponse, error) {
	result := make([]*pluginapi.EvictPod, 0)

	dynamicConfig := r.dynamicConfig.GetDynamicConfiguration()
	if !dynamicConfig.EnableRSSOveruseEviction {
		return &pluginapi.GetEvictPodsResponse{EvictPods: result}, nil
	}

	filterPods := make([]*v1.Pod, 0, len(request.ActivePods))
	selector := r.evictionConfig.RSSOveruseEvictionFilter.AsSelector()

	for i := range request.ActivePods {
		pod := request.ActivePods[i]
		set := (labels.Set)(pod.Labels)
		if selector.Matches(set) {
			filterPods = append(filterPods, pod)
		}
	}

	for i := range filterPods {
		pod := filterPods[i]

		qosLevel, err := r.qosConf.GetQoSLevelForPod(pod)
		if err != nil {
			general.Errorf("get qos level failed for pod %+v/%+v, skip check rss overuse, err: %v", pod.Namespace, pod.Name, err)
			continue
		}

		if !r.supportedQosLevels.Has(qosLevel) {
			continue
		}

		userSpecifiedThreshold, invalid := qos.GetRSSOverUseEvictThreshold(r.qosConf, pod)
		// don't perform eviction for safety if user set an invalid threshold
		if invalid {
			general.Warningf("pod %+v/%+v set invalid overuse eviction threshold, skip check rss overuse", pod.Namespace, pod.Name)
			continue
		}

		threshold := dynamicConfig.RSSOveruseRateThreshold
		// user set threshold explicitly,use default value
		if userSpecifiedThreshold != nil {
			threshold = *userSpecifiedThreshold
		}

		var memRequest int64 = 0
		var requestNotSet = false
		for _, container := range pod.Spec.Containers {
			containerMemRequest := container.Resources.Requests.Memory()
			if containerMemRequest.IsZero() {
				requestNotSet = true
				continue
			}
			memRequest += containerMemRequest.Value()
		}

		// if there is at least one container without memory limit, skip it
		if requestNotSet {
			continue
		}

		podRss, err := helper.GetPodMetric(r.metaServer.MetricsFetcher, r.emitter, pod, consts.MetricMemRssContainer, nonExistNumaID)
		if err != nil {
			_ = r.emitter.StoreInt64(metricsNameFetchMetricError, 1, metrics.MetricTypeNameCount,
				metrics.ConvertMapToTags(map[string]string{
					metricsTagKeyNumaID: strconv.Itoa(nonExistNumaID),
				})...)
			continue
		}

		if podRss > threshold*float64(memRequest) {
			result = append(result, &pluginapi.EvictPod{
				Pod:        pod,
				Reason:     fmt.Sprintf(RssOveruseEvictionReason, threshold, podRss, memRequest),
				ForceEvict: false,
			})
		}
	}

	return &pluginapi.GetEvictPodsResponse{EvictPods: result}, nil
}

func (r *RssOveruseEvictionPlugin) Start() {
	return
}
