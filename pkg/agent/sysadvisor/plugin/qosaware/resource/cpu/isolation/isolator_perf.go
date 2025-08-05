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
	"sync"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"
	resourceutil "k8s.io/kubernetes/pkg/api/v1/resource"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/sysadvisor/qosaware/resource/cpu"
	metric_consts "github.com/kubewharf/katalyst-core/pkg/consts"
	pkgconsts "github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
	"github.com/kubewharf/katalyst-core/pkg/util/strategygroup"
)

type metricAggregators map[string]general.SmoothWindow

func (m metricAggregators) gc() {
	for name, window := range m {
		if window.Empty() {
			delete(m, name)
		}
	}
}

type PerfIsolator struct {
	conf                   *config.Configuration
	isolationConfiguration *cpu.CPUIsolationConfiguration
	smoothWindowOpts       general.SmoothWindowOpts
	metricAggregators      metricAggregators
	podCPUUsages           sync.Map

	emitter    metrics.MetricEmitter
	metaReader metacache.MetaReader
	metaServer *metaserver.MetaServer
}

func NewPerfIsolator(conf *config.Configuration, _ interface{}, emitter metrics.MetricEmitter,
	metaCache metacache.MetaReader, metaServer *metaserver.MetaServer,
) Isolator {
	return &PerfIsolator{
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

func (p *PerfIsolator) Start(ctx context.Context) error {
	go wait.Until(p.updatePodMetrics, p.isolationConfiguration.MetricSyncPeriod, ctx.Done())
	return nil
}

func (p *PerfIsolator) updatePodCPUUsage(pod *v1.Pod) error {
	podCPUUsage := 0.0
	for _, container := range pod.Spec.Containers {
		data, err := p.metaReader.GetContainerMetric(string(pod.UID), container.Name, metric_consts.MetricCPUUsageContainer)
		if err != nil {
			klog.ErrorS(err, "get container metric", "pod", pod.Name)
			return err
		}
		podCPUUsage += data.Value
	}
	v := resource.NewMilliQuantity(int64(podCPUUsage*1000), resource.DecimalSI)
	aggV := p.getAggregatedMetric(v, string(pod.UID))
	if aggV != nil {
		p.podCPUUsages.Store(string(pod.UID), aggV.AsApproximateFloat64())
		return nil
	}

	return fmt.Errorf("failed to get aggregated metric for pod %s/%s", pod.Namespace, pod.Name)
}

func (p *PerfIsolator) updatePodMetrics() {
	pods, err := p.metaServer.GetPodList(context.TODO(), func(pod *v1.Pod) bool {
		return native.PodIsActive(pod)
	})
	if err != nil {
		klog.Errorf("Failed to get pod list: %v", err)
		return
	}
	podSet := sets.NewString()

	for _, pod := range pods {
		podSet.Insert(string(pod.UID))
		err := p.updatePodCPUUsage(pod)
		if err != nil {
			klog.ErrorS(err, "Failed to update pod CPU usage", "pod", pod.Name)
		}
	}
	p.metricAggregators.gc()

	p.podCPUUsages.Range(func(k, v interface{}) bool {
		if !podSet.Has(k.(string)) {
			p.podCPUUsages.Delete(k.(string))
		}
		return true
	})
}

func (p *PerfIsolator) podIsOverCommited(pod *v1.Pod) bool {
	requests, limits := resourceutil.PodRequestsAndLimits(pod)
	cpuReq := requests.Cpu()
	cpuLimit := limits.Cpu()
	if cpuReq != nil && cpuLimit != nil &&
		cpuReq.AsApproximateFloat64() <= cpuLimit.AsApproximateFloat64() &&
		cpuLimit.AsApproximateFloat64()/cpuReq.AsApproximateFloat64() < 1.01 {
		return false
	}
	return true
}

func (p *PerfIsolator) GetIsolatedPods() ([]string, error) {
	metricPolicyEnabled, _ := strategygroup.IsStrategyEnabledForNode(pkgconsts.StrategyNameNonOverCommittedPodsIsolator, false, p.conf)
	if p.isolationConfiguration.IsolationDisabled || !metricPolicyEnabled {
		return []string{}, nil
	}

	sharedPods := sets.NewString()
	p.metaReader.RangeContainer(func(podUID string, containerName string, containerInfo *types.ContainerInfo) bool {
		if containerInfo.QoSLevel == apiconsts.PodAnnotationQoSLevelSharedCores {
			sharedPods.Insert(podUID)
		}
		return true
	})

	unOverCommitedPods := make([]*v1.Pod, 0)
	unOverCommitedpodNames := make([]string, 0)
	for podUID := range sharedPods {
		pod, err := p.metaServer.GetPod(context.TODO(), podUID)
		if err != nil {
			return nil, err
		}
		if !p.podIsOverCommited(pod) {
			unOverCommitedPods = append(unOverCommitedPods, pod)
			unOverCommitedpodNames = append(unOverCommitedpodNames, pod.Name)
		}
	}

	klog.InfoS("non-overcommitted shared_cores pods", "pods", unOverCommitedpodNames)

	var uids []string
	var errList []error
	for _, pod := range unOverCommitedPods {
		isolated, err := p.checkIsolatedByPodUtilization(pod)
		if err != nil {
			errList = append(errList, err)
		}
		if isolated {
			klog.InfoS("isolate by util", "pod", pod.Name)
			uids = append(uids, string(pod.UID))
		}
	}

	return uids, errors.NewAggregate(errList)
}

func (p *PerfIsolator) podIsIsolated(pod *v1.Pod) bool {
	for _, container := range pod.Spec.Containers {
		ci, ok := p.metaReader.GetContainerInfo(string(pod.UID), container.Name)
		if ok && ci.Isolated {
			return true
		}
	}
	return false
}

func (p *PerfIsolator) checkIsolatedByPodUtilization(pod *v1.Pod) (bool, error) {
	tmp, ok := p.podCPUUsages.Load(string(pod.UID))
	if !ok {
		return false, fmt.Errorf("pod %v has no CPU usage", pod.Name)
	}

	podCPUUsage := tmp.(float64)

	_, limits := resourceutil.PodRequestsAndLimits(pod)
	curUtil := podCPUUsage / limits.Cpu().AsApproximateFloat64()
	podIsIsolated := p.podIsIsolated(pod)
	klog.InfoS("pod utilization", "pod", pod.Name, "utilization", curUtil,
		"UtilWatermarkHigh", p.isolationConfiguration.UtilWatermarkHigh, "UtilWatermarkLow", p.isolationConfiguration.UtilWatermarkLow, "podIsIsolated", podIsIsolated)

	if !podIsIsolated {
		if curUtil > p.isolationConfiguration.UtilWatermarkHigh {
			klog.InfoS("do isolation", "pod", pod.Name)
			return true, nil
		}
		return false, nil
	} else {
		if curUtil < p.isolationConfiguration.UtilWatermarkLow {
			klog.InfoS("do disisolation", "pod", pod.Name)
			return false, nil
		}
		return true, nil
	}
}

func (p *PerfIsolator) getAggregatedMetric(value *resource.Quantity, uid string) *resource.Quantity {
	aggregator, ok := p.metricAggregators[uid]
	if !ok {
		aggregator = general.NewAggregatorSmoothWindow(p.smoothWindowOpts)
		p.metricAggregators[uid] = aggregator
	}
	return aggregator.GetWindowedResources(*value)
}
