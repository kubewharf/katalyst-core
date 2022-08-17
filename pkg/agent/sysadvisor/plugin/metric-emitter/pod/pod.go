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

package pod

import (
	"context"
	"fmt"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/config"
	metricemitter "github.com/kubewharf/katalyst-core/pkg/config/agent/sysadvisor/metric-emitter"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/custom-metric/store/data"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

const MetricSyncerNamePod = "pod"

// podRawMetricNameMapping maps the raw metricName (collected from agent.MetricsFetcher)
// to the standard metricName (used by custom-metric-api-server)
var podRawMetricNameMapping = map[string]string{
	consts.MetricCPUUsageContainer: "cpu_usage",
}

type podRawChanel struct {
	keys  []string
	rChan chan metric.NotifiedResponse
}

// podCachedMetricNameMapping maps the cached metricName (processed by plugin.SysAdvisorPlugin)
// to the standard metricName (used by custom-metric-api-server)

type MetricSyncerPod struct {
	ctx  context.Context
	conf *metricemitter.MetricEmitterPluginConfiguration

	// no need to lock this map, since we only refer to it in syncChanel
	rawNotifier map[string]podRawChanel

	metricEmitter metrics.MetricEmitter
	dataEmitter   metrics.MetricEmitter

	metaServer *metaserver.MetaServer
	metaCache  *metacache.MetaCache
}

func NewMetricSyncerPod(conf *config.Configuration, metricEmitter, dataEmitter metrics.MetricEmitter,
	metaServer *metaserver.MetaServer, metaCache *metacache.MetaCache) *MetricSyncerPod {
	return &MetricSyncerPod{
		conf:        conf.AgentConfiguration.MetricEmitterPluginConfiguration,
		rawNotifier: make(map[string]podRawChanel),

		metricEmitter: metricEmitter,
		dataEmitter:   dataEmitter,
		metaServer:    metaServer,
		metaCache:     metaCache,
	}
}

func (p *MetricSyncerPod) Name() string {
	return MetricSyncerNamePod
}

func (p *MetricSyncerPod) Run(ctx context.Context) {
	p.ctx = ctx
	go wait.Until(p.syncChanel, p.conf.PodSyncPeriod, ctx.Done())
}

func (p *MetricSyncerPod) syncChanel() {
	podList, err := p.metaServer.GetPodList(p.ctx, func(_ *v1.Pod) bool {
		return true
	})
	if err != nil {
		klog.Errorf("failed to get pod list: %v", err)
		return
	}
	klog.Infof("find total %v pods, notifier length %v", len(podList), len(p.rawNotifier))

	podUIDSet := sets.NewString()
	for i := range podList {
		uid := fmt.Sprintf("%v", podList[i].UID)
		podUIDSet.Insert(uid)

		if _, ok := p.rawNotifier[uid]; ok {
			continue
		}

		var keys []string
		rChan := make(chan metric.NotifiedResponse, 20)
		for rawMetricName := range podRawMetricNameMapping {
			for _, container := range podList[i].Spec.Containers {
				key := p.metaServer.MetricsFetcher.RegisterNotifier(metric.MetricsScopeContainer, metric.NotifiedRequest{
					PodUID:        uid,
					ContainerName: container.Name,
					MetricName:    rawMetricName,
				}, rChan)

				klog.Infof("register raw pod metric: %v for pod/container: %v/%v, key %v",
					rawMetricName, podList[i].Name, container.Name, key)
				keys = append(keys, key)
			}
		}

		p.rawNotifier[uid] = podRawChanel{
			rChan: rChan,
			keys:  keys,
		}
		go p.receiveRawPod(p.ctx, podList[i], rChan)
	}

	for uid, pChanel := range p.rawNotifier {
		if podUIDSet.Has(uid) {
			continue
		}

		for _, key := range pChanel.keys {
			klog.Infof("deregister, key %v", key)
			p.metaServer.MetricsFetcher.DeRegisterNotifier(metric.MetricsScopeContainer, key)
		}
		close(pChanel.rChan)

		delete(p.rawNotifier, uid)
	}
}

// receiveRawPod receives notified response from raw data source
func (p *MetricSyncerPod) receiveRawPod(ctx context.Context, pod *v1.Pod, rChan chan metric.NotifiedResponse) {
	for {
		select {
		case response, ok := <-rChan:
			if !ok {
				klog.Infof("pod %v receive chanel has been stopped", pod.Name)
				return
			}

			if response.Req.MetricName == "" {
				continue
			}

			targetMetricName, ok := podRawMetricNameMapping[response.Req.MetricName]
			if !ok {
				klog.Warningf("invalid pod raw metric name: %v", response.Req.MetricName)
				continue
			}

			klog.Infof("get metric %v for pod %v, collect time %v, left len %v",
				response.Req.MetricName, pod.Name, response.Timestamp, len(rChan))
			if tags := p.generateMetricTag(pod); len(tags) > 0 {
				_ = p.dataEmitter.StoreFloat64(targetMetricName, response.Result, metrics.MetricTypeNameRaw, append(tags,
					metrics.MetricTag{
						Key: fmt.Sprintf("%s", data.CustomMetricLabelKeyTimestamp),
						Val: fmt.Sprintf("%v", response.Timestamp.UnixMilli()),
					},
					metrics.MetricTag{
						Key: fmt.Sprintf("%scontainer", data.CustomMetricLabelSelectorPrefixKey),
						Val: response.Req.ContainerName,
					},
				)...)
			}
		case <-ctx.Done():
			klog.Infof("all metric emitters should be stopped, pod %v", pod.Name)
			return
		}
	}
}

// generateMetricTag generates tags that are bounded to current Pod object
func (p *MetricSyncerPod) generateMetricTag(pod *v1.Pod) (tags []metrics.MetricTag) {
	tags = []metrics.MetricTag{
		{
			Key: fmt.Sprintf("%s", data.CustomMetricLabelKeyObject),
			Val: "pods",
		},
		{
			Key: fmt.Sprintf("%s", data.CustomMetricLabelKeyObjectName),
			Val: pod.Name,
		},
		{
			Key: fmt.Sprintf("%s", data.CustomMetricLabelKeyNamespace),
			Val: pod.Namespace,
		},
	}
	for key, value := range pod.Labels {
		if p.conf.PodMetricLabel.Has(key) {
			tags = append(tags, metrics.MetricTag{
				Key: fmt.Sprintf("%s%s", data.CustomMetricLabelSelectorPrefixKey, key),
				Val: value,
			})
		}
	}

	return
}
