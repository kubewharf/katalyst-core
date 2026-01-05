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

package isolation

import (
	"context"
	"fmt"
	"math"
	"sync"

	"github.com/klauspost/cpuid/v2"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"
	resourceutil "k8s.io/kubernetes/pkg/api/v1/resource"

	"github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/sysadvisor/qosaware/resource/cpu"
	metric_consts "github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/helper"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
	"github.com/kubewharf/katalyst-core/pkg/util/qos"
)

const (
	metricNameToIsolateByUtil   = "to_isolate_by_util"
	metricNameToIntegrateByUtil = "to_integrate_by_util"
	metricNameIsolatedByUtil    = "isolated_by_util"

	llcAlignedAnnotation = "katalyst.kubewharf.io/cpu_llc_aligned"
)

type metricAggregators map[string]general.SmoothWindow

func (m metricAggregators) gc() {
	for name, window := range m {
		if window.Empty() {
			delete(m, name)
		}
	}
}

type LLCAlignedIsolator struct {
	conf                   *config.Configuration
	isolationConfiguration *cpu.CPUIsolationConfiguration
	smoothWindowOpts       general.SmoothWindowOpts
	metricAggregators      metricAggregators
	podCPUUsages           sync.Map

	emitter    metrics.MetricEmitter
	metaReader metacache.MetaReader
	metaServer *metaserver.MetaServer
}

func NewLLCAlignedIsolator(conf *config.Configuration, _ interface{}, emitter metrics.MetricEmitter,
	metaCache metacache.MetaReader, metaServer *metaserver.MetaServer,
) Isolator {
	return &LLCAlignedIsolator{
		conf:                   conf,
		isolationConfiguration: conf.CPUIsolationConfiguration,
		smoothWindowOpts: general.SmoothWindowOpts{
			WindowSize:    int(conf.CPUIsolationConfiguration.MetricSlidingWindowTime.Seconds() / conf.CPUIsolationConfiguration.MetricSyncPeriod.Seconds()),
			TTL:           conf.CPUIsolationConfiguration.MetricSlidingWindowTime * 2,
			UsedMillValue: true,
			AggregateFunc: general.SmoothWindowAggFuncAvg,
		},
		metricAggregators: map[string]general.SmoothWindow{},
		podCPUUsages:      sync.Map{},

		emitter:    emitter,
		metaReader: metaCache,
		metaServer: metaServer,
	}
}

func (i *LLCAlignedIsolator) Start(ctx context.Context) error {
	general.Infof("LLCAlignedIsolator start")
	go wait.Until(i.updatePodMetrics, i.isolationConfiguration.MetricSyncPeriod, ctx.Done())
	return nil
}

func (i *LLCAlignedIsolator) updatePodCPUUsage(pod *v1.Pod) error {
	podCPUUsage := 0.0
	for _, container := range pod.Spec.Containers {
		data, err := i.metaReader.GetContainerMetric(string(pod.UID), container.Name, metric_consts.MetricCPUUsageContainer)
		if err != nil {
			klog.ErrorS(err, "get container metric", "pod", pod.Name)
			return err
		}
		podCPUUsage += data.Value
	}
	v := resource.NewMilliQuantity(int64(podCPUUsage*1000), resource.DecimalSI)
	aggV := i.getAggregatedMetric(v, string(pod.UID))
	if aggV != nil {
		i.podCPUUsages.Store(string(pod.UID), aggV.AsApproximateFloat64())
		return nil
	}

	return fmt.Errorf("failed to get aggregated metric for pod %s/%s", pod.Namespace, pod.Name)
}

func (i *LLCAlignedIsolator) updatePodMetrics() {
	pods, err := i.metaServer.GetPodList(context.TODO(), func(pod *v1.Pod) bool {
		return native.PodIsActive(pod)
	})
	if err != nil {
		klog.Errorf("Failed to get pod list: %v", err)
		return
	}
	podSet := sets.NewString()

	for _, pod := range pods {
		podSet.Insert(string(pod.UID))
		err := i.updatePodCPUUsage(pod)
		if err != nil {
			klog.ErrorS(err, "Failed to update pod CPU usage", "pod", pod.Name)
		}
	}
	i.metricAggregators.gc()

	i.podCPUUsages.Range(func(k, v interface{}) bool {
		if !podSet.Has(k.(string)) {
			i.podCPUUsages.Delete(k.(string))
		}
		return true
	})
}

func getMainContainerInfo(pod *v1.Pod, metaReader metacache.MetaReader) *types.ContainerInfo {
	for _, container := range pod.Spec.Containers {
		containerInfo, ok := metaReader.GetContainerInfo(string(pod.UID), container.Name)
		if ok && containerInfo.ContainerType == pluginapi.ContainerType_MAIN {
			return containerInfo
		}
	}
	return nil
}

func (i *LLCAlignedIsolator) isCandidatePod(pod *v1.Pod) bool {
	numaID, err := qos.GetActualNUMABindingResult(i.conf.QoSConfiguration, pod)
	if err != nil {
		return false
	}
	general.InfoS("pod info", "pod", pod.Name, "bindNUMA", numaID)

	containerInfo := getMainContainerInfo(pod, i.metaReader)
	if containerInfo == nil || apiconsts.QoSLevel(containerInfo.QoSLevel) != apiconsts.QoSLevelSharedCores ||
		containerInfo.RegionNames.Len() != 1 {
		return false
	}

	regionName := containerInfo.RegionNames.List()[0]
	region, ok := i.metaReader.GetRegionInfo(regionName)
	if !ok {
		return false
	}

	general.InfoS("[perfIsolation]region info", "region", region)

	if region.RegionType == v1alpha1.QoSRegionTypeIsolation {
		general.InfoS("[perfIsolation]pod is already isolated", "pod", pod.Name)
		return true
	} else if region.RegionType != v1alpha1.QoSRegionTypeShare {
		return false
	}

	// FIXME:
	return pod.Annotations[llcAlignedAnnotation] == "elastic"

	if poolSize, ok := i.metaReader.GetPoolSize(region.OwnerPoolName); ok {
		requestsSum := .0
		for podUID := range region.Pods {
			if pod, err := i.metaServer.GetPod(context.TODO(), podUID); err == nil {
				reqs := native.SumUpPodRequestResources(pod)
				if cpuReq, ok := reqs[v1.ResourceCPU]; ok {
					requestsSum += float64(cpuReq.MilliValue()) / 1000
				}
			}
		}

		// pod.req / region.podsReqs * cpuPoolSize > pod.limit，就可以隔离
		requests, limits := resourceutil.PodRequestsAndLimits(pod)
		cpuReq := requests.Cpu()
		cpuLimit := limits.Cpu()

		isCandidate := false

		if cpuReq != nil && cpuLimit != nil &&
			cpuReq.AsApproximateFloat64()/requestsSum*float64(poolSize) >= math.Ceil(cpuLimit.AsApproximateFloat64()) && cpuLimit.AsApproximateFloat64() >= 8 {
			isCandidate = true
		}
		general.InfoS("checking isCandidatePod", "pod", pod.Name, "podUID", pod.UID, "numaID", numaID,
			"poolSize", poolSize, "requestsSum", requestsSum, "cpuReq", cpuReq, "cpuLimit", cpuLimit, "isCandidate", isCandidate)
		return isCandidate
	}

	return false
}

func (i *LLCAlignedIsolator) GetIsolatedPods() ([]string, error) {
	general.InfoS("try to GetIsolatedPods")
	//metricPolicyEnabled, _ := strategygroup.IsStrategyEnabledForNode(pkgconsts.StrategyNameNonOverCommittedPodsIsolator, false, p.conf)

	if i.isolationConfiguration.IsolationDisabled {
		general.InfoS("IsolationDisabled")
		return []string{}, nil
	}

	if cpuid.CPU.VendorID != cpuid.AMD {
		general.InfoS("not AMD CPU")
		return []string{}, nil
	}

	codename := helper.GetCpuCodeName(i.metaServer.MetricsFetcher)

	sharedPods := sets.NewString()
	i.metaReader.RangeContainer(func(podUID string, containerName string, containerInfo *types.ContainerInfo) bool {
		if containerInfo.QoSLevel == apiconsts.PodAnnotationQoSLevelSharedCores {
			sharedPods.Insert(podUID)
		}
		return true
	})

	candidatePods := make([]*v1.Pod, 0)
	candidatePodsNames := make([]string, 0)
	for podUID := range sharedPods {
		pod, err := i.metaServer.GetPod(context.TODO(), podUID)
		if err != nil {
			return nil, err
		}
		if i.isCandidatePod(pod) {
			candidatePods = append(candidatePods, pod)
			candidatePodsNames = append(candidatePodsNames, pod.Name)
		}
	}

	klog.InfoS("shared_cores Pods", "allPodsUIDs", sharedPods.List(), "allPodsSize", len(sharedPods.List()),
		"candidatePodsNames", candidatePodsNames, "candidatePods nr", len(candidatePods))

	var UIDs []string
	var errList []error
	for _, pod := range candidatePods {
		isolated, err := i.checkIsolatedByPodUtilization(pod)
		if err != nil {
			errList = append(errList, err)
		}
		if isolated {
			klog.InfoS("isolate by util", "pod", pod.Name)
			UIDs = append(UIDs, string(pod.UID))

			_ = i.emitter.StoreInt64(metricNameIsolatedByUtil, 1, metrics.MetricTypeNameRaw,
				metrics.ConvertMapToTags(map[string]string{"cpu_codename": codename, "pod": pod.Name, "pod_psm": pod.Labels["psm"], "pod_paas_cluster": pod.Labels["paas_cluster"]})...)
		}
	}

	return UIDs, errors.NewAggregate(errList)
}

func (i *LLCAlignedIsolator) podIsIsolated(pod *v1.Pod) bool {
	for _, container := range pod.Spec.Containers {
		ci, ok := i.metaReader.GetContainerInfo(string(pod.UID), container.Name)
		if ok && ci.Isolated {
			return true
		}
	}
	return false
}

func (i *LLCAlignedIsolator) checkIsolatedByPodUtilization(pod *v1.Pod) (bool, error) {
	tmp, ok := i.podCPUUsages.Load(string(pod.UID))
	if !ok {
		return false, fmt.Errorf("pod %v has no CPU usage", pod.Name)
	}

	codename := helper.GetCpuCodeName(i.metaServer.MetricsFetcher)

	podCPUUsage := tmp.(float64)

	_, limits := resourceutil.PodRequestsAndLimits(pod)
	curUtil := podCPUUsage / limits.Cpu().AsApproximateFloat64()
	podIsIsolated := i.podIsIsolated(pod)
	klog.InfoS("pod utilization", "pod", pod.Name, "utilization", curUtil, "UtilWatermarkSupreme", i.isolationConfiguration.UtilWatermarkSupreme,
		"UtilWatermarkHigh", i.isolationConfiguration.UtilWatermarkHigh, "UtilWatermarkLow", i.isolationConfiguration.UtilWatermarkLow,
		"podIsIsolated", podIsIsolated)

	if !podIsIsolated {
		if curUtil > i.isolationConfiguration.UtilWatermarkHigh {
			klog.InfoS("[Isolate]", "pod", pod.Name)
			_ = i.emitter.StoreInt64(metricNameToIsolateByUtil, 1, metrics.MetricTypeNameRaw,
				metrics.ConvertMapToTags(map[string]string{"cpu_codename": codename, "pod": pod.Name,
					"pod_psm": pod.Labels["psm"], "pod_paas_cluster": pod.Labels["paas_cluster"]})...)
			return true, nil
		}
		return false, nil
	} else {
		if curUtil < i.isolationConfiguration.UtilWatermarkLow {
			klog.InfoS("[Integrate]", "pod", pod.Name)
			_ = i.emitter.StoreInt64(metricNameToIntegrateByUtil, 1, metrics.MetricTypeNameRaw,
				metrics.ConvertMapToTags(map[string]string{"cpu_codename": codename, "pod": pod.Name,
					"pod_psm": pod.Labels["psm"], "pod_paas_cluster": pod.Labels["paas_cluster"]})...)
			return false, nil
		}
		return true, nil
	}
}

func (i *LLCAlignedIsolator) getAggregatedMetric(value *resource.Quantity, uid string) *resource.Quantity {
	aggregator, ok := i.metricAggregators[uid]
	if !ok {
		aggregator = general.NewAggregatorSmoothWindow(i.smoothWindowOpts)
		i.metricAggregators[uid] = aggregator
	}
	return aggregator.GetWindowedResources(*value)
}
