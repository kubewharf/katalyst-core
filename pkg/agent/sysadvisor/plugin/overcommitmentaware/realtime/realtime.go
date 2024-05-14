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

package realtime

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	utilkubeconfig "github.com/kubewharf/katalyst-core/pkg/util/kubelet/config"
	"github.com/kubewharf/katalyst-core/pkg/util/metric"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

const (
	realtimeOvercommitAdvisorUpdateFail   = "realtime_overcommit_advisor_update_fail"
	realtimeOvercommitAdvisorSyncNodeFail = "realtime_overcommit_advisor_sync_node_fail"
)

var (
	cpuMetricsToGather = []string{
		consts.MetricCPUUsageContainer,
		consts.MetricLoad1MinContainer,
		consts.MetricLoad5MinContainer,
	}

	memoryMetricsToGather = []string{
		consts.MetricMemRssContainer,
	}
)

// RealtimeOvercommitmentAdvisor calculate node CPU and memory overcommitment ratio
// by realtime metrics and node requested resources from metaSever
type RealtimeOvercommitmentAdvisor struct {
	mutex sync.RWMutex

	metaServer *metaserver.MetaServer
	emitter    metrics.MetricEmitter

	updatePeriod   time.Duration
	syncPodTimeout time.Duration

	nodeTargetCPULoad      float64
	nodeTargetMemoryLoad   float64
	podEstimatedCPULoad    float64
	podEstimatedMemoryLoad float64

	cpuMetricsToGather    []string
	memoryMetricsToGather []string

	resourceOvercommitRatio map[v1.ResourceName]float64
	resourceAllocatable     map[v1.ResourceName]resource.Quantity
}

type PodResourceInfo struct {
	usage   float64
	request resource.Quantity
	limit   resource.Quantity
}

func NewRealtimeOvercommitmentAdvisor(
	conf *config.Configuration,
	metaServer *metaserver.MetaServer,
	emitter metrics.MetricEmitter,
) *RealtimeOvercommitmentAdvisor {
	ra := &RealtimeOvercommitmentAdvisor{
		metaServer: metaServer,
		emitter:    emitter,

		resourceOvercommitRatio: map[v1.ResourceName]float64{
			v1.ResourceCPU:    1.0,
			v1.ResourceMemory: 1.0,
		},
		resourceAllocatable: map[v1.ResourceName]resource.Quantity{},

		updatePeriod:           conf.OvercommitAwarePluginConfiguration.SyncPeriod,
		syncPodTimeout:         conf.SyncPodTimeout,
		nodeTargetCPULoad:      conf.TargetCPULoad,
		nodeTargetMemoryLoad:   conf.TargetMemoryLoad,
		podEstimatedCPULoad:    conf.EstimatedPodCPULoad,
		podEstimatedMemoryLoad: conf.EstimatedPodMemoryLoad,
		cpuMetricsToGather:     conf.CPUMetricsToGather,
		memoryMetricsToGather:  conf.MemoryMetricsToGather,
	}

	err := ra.syncAllocatableResource()
	if err != nil {
		klog.Fatalf("syncAllocatableResource fail: %v", err)
	}

	return ra
}

func (ra *RealtimeOvercommitmentAdvisor) Run(ctx context.Context) {
	klog.Infof("RealtimeOvercommitmentAdvisor run...")

	go wait.Until(func() {
		err := ra.syncAllocatableResource()
		if err != nil {
			klog.Errorf("syncAllocatableResource fail: %v", err)
			_ = ra.emitter.StoreInt64(realtimeOvercommitAdvisorSyncNodeFail, 1, metrics.MetricTypeNameCount)
		}
	}, time.Hour, ctx.Done())

	go wait.Until(func() {
		err := ra.update()
		if err != nil {
			klog.Errorf("RealtimeOvercommitmentAdvisor update fail: %v", err)
			_ = ra.emitter.StoreInt64(realtimeOvercommitAdvisorUpdateFail, 1, metrics.MetricTypeNameCount)
		}
	}, ra.updatePeriod, ctx.Done())
}

func (ra *RealtimeOvercommitmentAdvisor) update() error {
	// list pod from metaServer
	ctx, cancel := context.WithTimeout(context.Background(), ra.syncPodTimeout)
	defer cancel()
	podList, err := ra.metaServer.GetPodList(ctx, nil)
	if err != nil {
		err = fmt.Errorf("[overcommitment-aware-realtime] list pod fail, err: %v", err)
		klog.Error(err)
		return err
	}

	// sum node request resource
	nodeResourceRequest := sumUpPodsResources(podList)
	klog.V(6).Infof("[overcommitment-aware-realtime] sumUpPodsResources, cpu: %v, memory: %v", nodeResourceRequest.Cpu().String(), nodeResourceRequest.Memory().String())

	// agg node pods usage
	nodeResourceUsage := ra.aggregateNodeMetrics(podList)
	klog.V(6).Infof("[overcommitment-aware-realtime] aggregateNodeMetrics: %v", nodeResourceUsage)

	ra.metricsToOvercommitRatio(nodeResourceRequest, nodeResourceUsage)

	return nil
}

func (ra *RealtimeOvercommitmentAdvisor) aggregateNodeMetrics(podList []*v1.Pod) map[v1.ResourceName]float64 {
	var cpuUsage, memoryUsage float64

	if len(ra.cpuMetricsToGather) != 0 {
		cpuUsage = ra.aggregateMetrics(podList, ra.cpuMetricsToGather)
	} else {
		cpuUsage = ra.aggregateMetrics(podList, cpuMetricsToGather)
	}

	if len(ra.memoryMetricsToGather) != 0 {
		memoryUsage = ra.aggregateMetrics(podList, ra.memoryMetricsToGather)
	} else {
		memoryUsage = ra.aggregateMetrics(podList, memoryMetricsToGather)
	}

	return map[v1.ResourceName]float64{
		v1.ResourceCPU:    cpuUsage,
		v1.ResourceMemory: memoryUsage,
	}
}

func (ra *RealtimeOvercommitmentAdvisor) aggregateMetrics(podList []*v1.Pod, metrics []string) float64 {
	var (
		res         float64
		metricValue float64
		reference   string
	)

	for _, pod := range podList {
		metricValue = 0
		reference = ""

		for _, metricName := range metrics {
			metricData := ra.metaServer.AggregatePodMetric([]*v1.Pod{pod}, metricName, metric.AggregatorSum, metric.DefaultContainerMetricFilter)
			if klog.V(5).Enabled() {
				general.Infof("pod %v metric %v value %v", pod.Name, metricName, metricData.Value)
			}
			if metricData.Value <= 0 {
				continue
			}

			if metricData.Value > metricValue {
				metricValue = metricData.Value
				reference = metricName
			}
		}

		if klog.V(5).Enabled() {
			general.Infof("pod %v aggregateCPU value %v reference %v", pod.Name, metricValue, reference)
		}
		res += metricValue
	}

	return res
}

func (ra *RealtimeOvercommitmentAdvisor) syncAllocatableResource() error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	kconfig, err := ra.metaServer.GetKubeletConfig(ctx)
	if err != nil {
		klog.Errorf("get kubeletconfig fail: %v", err)
		return err
	}

	reservedCPU, found, err := utilkubeconfig.GetReservedQuantity(kconfig, string(v1.ResourceCPU))
	if err != nil {
		klog.Errorf("GetKubeletReservedQuantity fail: %v", err)
		return err
	} else if !found {
		reservedCPU = *resource.NewQuantity(0, resource.DecimalSI)
	}

	reservedMemory, found, err := utilkubeconfig.GetReservedQuantity(kconfig, string(v1.ResourceMemory))
	if err != nil {
		klog.Errorf("GetKubeletReservedQuantity fail: %v", err)
		return err
	} else if !found {
		reservedMemory = *resource.NewQuantity(0, resource.BinarySI)
	}

	ra.syncAllocatableCPU(reservedCPU)
	ra.syncAllocatableMemory(reservedMemory)
	return nil
}

func (ra *RealtimeOvercommitmentAdvisor) syncAllocatableCPU(reserved resource.Quantity) {
	capacity := resource.NewMilliQuantity(int64(ra.metaServer.MachineInfo.NumCores*1000), resource.DecimalSI)
	capacity.Sub(reserved)

	ra.mutex.Lock()
	ra.resourceAllocatable[v1.ResourceCPU] = *capacity
	ra.mutex.Unlock()

	klog.V(5).Infof("node allocatable cpu %v, reserved cpu %v", capacity.String(), reserved.String())
}

func (ra *RealtimeOvercommitmentAdvisor) syncAllocatableMemory(reserved resource.Quantity) {
	capacity := resource.NewQuantity(int64(ra.metaServer.MemoryCapacity), resource.BinarySI)

	capacity.Sub(reserved)

	ra.mutex.Lock()
	ra.resourceAllocatable[v1.ResourceMemory] = *capacity
	ra.mutex.Unlock()

	klog.V(5).Infof("node allocatable memory %v, reserved memory %v", capacity.String(), reserved.String())
}

func (ra *RealtimeOvercommitmentAdvisor) metricsToOvercommitRatio(resourceRequest v1.ResourceList, resourceUsage map[v1.ResourceName]float64) {
	cpuOvercommitRatio := ra.resourceMetricsToOvercommitRatio(v1.ResourceCPU, *resourceRequest.Cpu(), resourceUsage[v1.ResourceCPU])

	memoryOvercommitRatio := ra.resourceMetricsToOvercommitRatio(v1.ResourceMemory, *resourceRequest.Memory(), resourceUsage[v1.ResourceMemory])

	ra.mutex.Lock()
	ra.resourceOvercommitRatio[v1.ResourceCPU] = cpuOvercommitRatio
	ra.resourceOvercommitRatio[v1.ResourceMemory] = memoryOvercommitRatio
	ra.mutex.Unlock()
}

func (ra *RealtimeOvercommitmentAdvisor) resourceMetricsToOvercommitRatio(resourceName v1.ResourceName, resourceRequest resource.Quantity, usage float64) float64 {
	ra.mutex.RLock()
	resourceAllocatable, ok := ra.resourceAllocatable[resourceName]
	ra.mutex.RUnlock()

	if !ok {
		klog.Errorf("resource %v not exist in resourceAllocatable map", resourceName)
		return 1.0
	}

	allocatable := resourceAllocatable.MilliValue()
	request := resourceRequest.MilliValue()
	usage = usage * 1000

	if request == 0 || allocatable == 0 {
		klog.Warningf("unexpected node resource, resourceName: %v, request: %v, allocatable: %v", resourceName, request, allocatable)
		return 1.0
	}

	existedPodLoad := usage / float64(request)
	if existedPodLoad > 1 {
		existedPodLoad = 1
	}
	var podExpectedLoad, nodeTargetLoad float64
	switch resourceName {
	case v1.ResourceCPU:
		podExpectedLoad = ra.podEstimatedCPULoad
		nodeTargetLoad = ra.nodeTargetCPULoad
	case v1.ResourceMemory:
		podExpectedLoad = ra.podEstimatedMemoryLoad
		nodeTargetLoad = ra.nodeTargetMemoryLoad
	default:
		klog.Warningf("unknow resourceName: %v", resourceName)
		return 1.0
	}
	if existedPodLoad < podExpectedLoad {
		existedPodLoad = podExpectedLoad
	}

	overcommitRatio := ((float64(allocatable)*nodeTargetLoad-usage)/existedPodLoad + float64(request)) / float64(allocatable)

	klog.V(5).Infof("resource %v request: %v, allocatable: %v, usage: %v, targetLoad: %v, existLoad: %v, overcommitRatio: %v",
		resourceName, request, allocatable, usage, nodeTargetLoad, existedPodLoad, overcommitRatio)
	if overcommitRatio < 1.0 {
		overcommitRatio = 1.0
	}
	return overcommitRatio
}

func sumUpPodsResources(podList []*v1.Pod) v1.ResourceList {
	var (
		podsCPURequest    = resource.NewQuantity(0, resource.DecimalSI)
		podsMemoryRequest = resource.NewQuantity(0, resource.BinarySI)
	)

	for _, pod := range podList {
		podResource := native.SumUpPodRequestResources(pod)

		cpuRequest := podResource.Cpu()
		memoryRequest := podResource.Memory()

		podsCPURequest.Add(*cpuRequest)
		podsMemoryRequest.Add(*memoryRequest)
	}

	return v1.ResourceList{
		v1.ResourceCPU:    podsCPURequest.DeepCopy(),
		v1.ResourceMemory: podsMemoryRequest.DeepCopy(),
	}
}

func (ra *RealtimeOvercommitmentAdvisor) GetOvercommitRatio() (map[v1.ResourceName]float64, error) {
	res := map[v1.ResourceName]float64{
		v1.ResourceCPU:    1.0,
		v1.ResourceMemory: 1.0,
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*2)
	defer cancel()
	node, err := ra.metaServer.GetNode(ctx)
	if err != nil {
		klog.Error("GetOvercommitRatio getNode fail: %v", err)
		return nil, err
	}
	if cpuOvercommitRatioAnno, ok := node.Annotations[apiconsts.NodeAnnotationCPUOvercommitRatioKey]; ok {
		cpuOvercommitRatio, err := strconv.ParseFloat(cpuOvercommitRatioAnno, 64)
		if err != nil {
			klog.Errorf("%s parse fail: %v", cpuOvercommitRatioAnno, err)
		} else {
			res[v1.ResourceCPU] = cpuOvercommitRatio
		}
	}
	if memOvercommitRatioAnno, ok := node.Annotations[apiconsts.NodeAnnotationMemoryOvercommitRatioKey]; ok {
		memOvercommitRatio, err := strconv.ParseFloat(memOvercommitRatioAnno, 64)
		if err != nil {
			klog.Errorf("%s parse fail: %v", memOvercommitRatioAnno, err)
		} else {
			res[v1.ResourceMemory] = memOvercommitRatio
		}
	}

	ra.mutex.RLock()
	defer ra.mutex.RUnlock()

	if len(ra.resourceOvercommitRatio) <= 0 {
		return map[v1.ResourceName]float64{}, nil
	}

	// only report when overcommit ratio less than the set value
	for resourceName, overcommitRatio := range ra.resourceOvercommitRatio {
		if overcommitRatio >= res[resourceName] {
			continue
		}
		res[resourceName] = overcommitRatio
	}

	return res, nil
}
