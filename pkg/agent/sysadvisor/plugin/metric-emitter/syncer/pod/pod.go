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
	"k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	apimetricpod "github.com/kubewharf/katalyst-api/pkg/metric/pod"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	borweinconsts "github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/inference/models/borwein/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/metric-emitter/syncer"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/metric-emitter/types"
	sysadvisortypes "github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	metricemitter "github.com/kubewharf/katalyst-core/pkg/config/agent/sysadvisor/metric-emitter"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/sysadvisor/qosaware/model/borwein"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/custom-metric/store/data"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	metrictypes "github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/types"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	metricspool "github.com/kubewharf/katalyst-core/pkg/metrics/metrics-pool"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

const (
	podMetricLabelSelectorNodeName              = "node_name"
	podTrainingThroughputInferenceResultBorwein = "pod_borwein_training_throughput_inference_result"
	podLatencyRegressionInferenceResultBorwein  = "pod_borwein_latency_regression_inference_result"
	nodeLatencyRegressionInferenceResultBorwein = "node_borwein_latency_regression_inference_result"
	metricBorweinInferenceResult                = "borwein_inference_result"
)

// podRawMetricNameMapping maps the raw metricName (collected from agent.MetricsFetcher)
// to the standard metricName (used by custom-metric-api-server)
var podRawMetricNameMapping = map[string]string{
	consts.MetricLoad1MinContainer:      apimetricpod.CustomMetricPodCPULoad1Min,
	consts.MetricCPUUsageContainer:      apimetricpod.CustomMetricPodCPUUsage,
	consts.MetricCPUUsageRatioContainer: apimetricpod.CustomMetricPodCPUUsageRatio,
	consts.MetricCPUCPIContainer:        apimetricpod.CustomMetricPodCPUCPI,

	consts.MetricMemRssContainer:   apimetricpod.CustomMetricPodMemoryRSS,
	consts.MetricMemUsageContainer: apimetricpod.CustomMetricPodMemoryUsage,

	consts.MetricMbmTotalCgroup: "pod_mbm_usage",
}

type podRawChanel struct {
	keys  []string
	rChan chan metrictypes.NotifiedResponse
}

// podCachedMetricNameMapping maps the cached metricName (processed by plugin.SysAdvisorPlugin)
// to the standard metricName (used by custom-metric-api-server)

type MetricSyncerPod struct {
	ctx           context.Context
	metricMapping map[string]string

	emitterConf *metricemitter.MetricEmitterPluginConfiguration
	qosConf     *generic.QoSConfiguration
	borweinConf *borwein.BorweinConfiguration

	// no need to lock this map, since we only refer to it in syncChanel
	rawNotifier map[string]podRawChanel

	metricEmitter metrics.MetricEmitter
	dataEmitter   metrics.MetricEmitter

	metaServer *metaserver.MetaServer
	metaReader metacache.MetaReader

	modelToCustomizedEmitterFunc map[string]func()

	enableBorwein bool
}

func NewMetricSyncerPod(conf *config.Configuration, _ interface{},
	metricEmitter metrics.MetricEmitter, emitterPool metricspool.MetricsEmitterPool,
	metaServer *metaserver.MetaServer, metaReader metacache.MetaReader,
) (syncer.CustomMetricSyncer, error) {
	klog.Infof("skip anno: %v, skip label: %v", conf.AgentConfiguration.PodSkipAnnotations, conf.AgentConfiguration.PodSkipLabels)
	dataEmitter, err := emitterPool.GetMetricsEmitter(metricspool.PrometheusMetricOptions{
		Path: metrics.PrometheusMetricPathNameCustomMetric,
	})
	if err != nil {
		klog.Errorf("[cus-metric-emitter] failed to init metric emitter: %v", err)
		return nil, err
	}

	metricMapping := general.MergeMap(podRawMetricNameMapping, conf.MetricEmitterPodConfiguration.MetricMapping)

	enableBorwein := conf.PolicyRama.EnableBorweinModelResultFetcher

	metricSyncerPod := &MetricSyncerPod{
		metricMapping: metricMapping,

		emitterConf: conf.AgentConfiguration.MetricEmitterPluginConfiguration,
		qosConf:     conf.GenericConfiguration.QoSConfiguration,
		borweinConf: conf.BorweinConfiguration,
		rawNotifier: make(map[string]podRawChanel),

		metricEmitter: metricEmitter,
		dataEmitter:   dataEmitter,
		metaServer:    metaServer,
		metaReader:    metaReader,
		enableBorwein: enableBorwein,
	}

	metricSyncerPod.modelToCustomizedEmitterFunc = map[string]func(){
		borweinconsts.ModelNameBorweinTrainingThroughput: metricSyncerPod.emitBorweinTrainingThroughput,
		borweinconsts.ModelNameBorweinLatencyRegression:  metricSyncerPod.emitBorweinLatencyRegression,
	}

	return metricSyncerPod, nil
}

func (p *MetricSyncerPod) Name() string {
	return types.MetricSyncerNamePod
}

func (p *MetricSyncerPod) Run(ctx context.Context) {
	p.ctx = ctx
	go wait.Until(p.syncChanel, p.emitterConf.PodSyncPeriod, ctx.Done())

	if p.enableBorwein {
		klog.Infof("borwein enabled, start to emit model metric periodically")
		go wait.Until(p.modelMetric, p.emitterConf.PodSyncPeriod, ctx.Done())
	}
}

func (p *MetricSyncerPod) syncChanel() {
	podList, err := p.metaServer.GetPodList(p.ctx, p.metricPod)
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

		rChan := make(chan metrictypes.NotifiedResponse, 20)
		go p.receiveRawPod(p.ctx, podList[i], rChan)

		var keys []string
		for rawMetricName := range p.metricMapping {
			for _, container := range podList[i].Spec.Containers {
				key := p.metaServer.MetricsFetcher.RegisterNotifier(metrictypes.MetricsScopeContainer, metrictypes.NotifiedRequest{
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
	}

	// clear all the channels and goroutines that we don't need anymore (for deleted pods)
	for uid, pChanel := range p.rawNotifier {
		if podUIDSet.Has(uid) {
			continue
		}

		for _, key := range pChanel.keys {
			klog.Infof("deregister, key %v", key)
			p.metaServer.MetricsFetcher.DeRegisterNotifier(metrictypes.MetricsScopeContainer, key)
		}
		close(pChanel.rChan)

		delete(p.rawNotifier, uid)
	}
}

// receiveRawPod receives notified response from raw data source
func (p *MetricSyncerPod) receiveRawPod(ctx context.Context, pod *v1.Pod, rChan chan metrictypes.NotifiedResponse) {
	name, tags := pod.Name, p.generateMetricTag(pod)

	for {
		select {
		case response, ok := <-rChan:
			if !ok {
				klog.Infof("pod %v receive chanel has been stopped", name)
				return
			}

			if response.Req.MetricName == "" {
				continue
			} else if response.Time == nil {
				continue
			}

			targetMetricName, ok := p.metricMapping[response.Req.MetricName]
			if !ok {
				klog.Warningf("invalid pod raw metric name: %v", response.Req.MetricName)
				continue
			}

			klog.V(4).Infof("get metric %v for pod %v, collect time %+v, left len %v",
				response.Req.MetricName, name, response.Time, len(rChan))
			if len(tags) > 0 {
				_ = p.dataEmitter.StoreFloat64(targetMetricName, response.Value, metrics.MetricTypeNameRaw, append(tags,
					metrics.MetricTag{
						Key: fmt.Sprintf("%s", data.CustomMetricLabelKeyTimestamp),
						Val: fmt.Sprintf("%v", response.Time.UnixMilli()),
					},
					metrics.MetricTag{
						Key: fmt.Sprintf("%scontainer", data.CustomMetricLabelSelectorPrefixKey),
						Val: response.Req.ContainerName,
					},
				)...)
			}
		case <-ctx.Done():
			klog.Infof("all metric emitters should be stopped, pod %v", name)
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
		{
			Key: fmt.Sprintf("%s%s", data.CustomMetricLabelSelectorPrefixKey, podMetricLabelSelectorNodeName),
			Val: pod.Spec.NodeName,
		},
	}

	for key, value := range pod.Labels {
		if p.emitterConf.PodMetricLabel.Has(key) {
			tags = append(tags, metrics.MetricTag{
				Key: fmt.Sprintf("%s%s", data.CustomMetricLabelSelectorPrefixKey, key),
				Val: value,
			})
		}
	}

	// append qos level tag
	qosLevel, err := p.qosConf.GetQoSLevelForPod(pod)
	if err != nil {
		klog.Warningf("get pod %v qos level error: %v", pod.Name, err)
		qosLevel = ""
	}

	tags = append(tags, metrics.MetricTag{
		Key: fmt.Sprintf("%s%s", data.CustomMetricLabelSelectorPrefixKey, "qos_level"),
		Val: qosLevel,
	})

	// append main container numa bit mask
	numaBitMask := int(0)
	containerInfos, ok := p.metaReader.GetContainerEntries(string(pod.UID))
	if ok {
		for _, containerInfo := range containerInfos {
			if containerInfo.ContainerType == v1alpha1.ContainerType_MAIN {
				cpuset := machine.GetCPUAssignmentNUMAs(containerInfo.TopologyAwareAssignments)
				numaBitMask = sysadvisortypes.NumaIDBitMask(cpuset.ToSliceInt())
			}
		}
	}

	tags = append(tags, metrics.MetricTag{
		Key: fmt.Sprintf("%s%s", data.CustomMetricLabelSelectorPrefixKey, "numa_bit_mask"),
		Val: fmt.Sprintf("%d", numaBitMask),
	})

	return tags
}

// metricPod filter out pods that won't be needed by custom metrics apiserver
func (p *MetricSyncerPod) metricPod(pod *v1.Pod) bool {
	if ok, err := p.qosConf.CheckSystemQoSForPod(pod); err != nil {
		klog.Errorf("failed to get qos for pod %v, err: %v", pod.Name, err)
	} else if ok {
		return false
	}

	if !native.PodIsReady(pod) || !native.PodIsActive(pod) || native.CheckDaemonPod(pod) {
		return false
	}

	for key, value := range pod.Annotations {
		if v, ok := p.emitterConf.PodSkipAnnotations[key]; ok && v == value {
			return false
		}
	}

	for key, value := range pod.Labels {
		if v, ok := p.emitterConf.PodSkipLabels[key]; ok && v == value {
			return false
		}
	}

	return true
}
