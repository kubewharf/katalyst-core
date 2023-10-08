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

package monitor

import (
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
	"github.com/kubewharf/katalyst-core/pkg/util/qos"
)

const (
	metricsNameCNRReportAnomaly  = "cnr_report_anomaly"
	metricsNameCNRReportLantency = "cnr_report_latency"
)

const (
	// reasonNumaExclusiveAnomaly is the reason for numa exclusive anomaly
	// when numa_binding and numa_exclusive are both set
	// the pod with numa_binding and numa_exclusive shares the numa with other pods
	reasonNumaExclusiveAnomaly = "NumaExclusiveAnomaly"
	// reasonNumaAllocatableSumAnomaly is the reason for numa allocatable sum anomaly
	// when the node's sum of numa allocatable is not equal to the node allocatable
	reasonNumaAllocatableSumAnomaly = "AllocatableSumAnomaly"
	// reasonPodAllocationSumAnomaly is the reason for pod allocation sum anomaly
	// when the numa's sum of pod allocation is greater than the numa allocatable
	reasonPodAllocationSumAnomaly = "PodAllocationSumAnomaly"
)

// checkNumaExclusiveAnomaly checks whether exist the pod with numa_binding and numa_exclusive shares the numa with other pods
func (ctrl *CNRMonitorController) checkNumaExclusiveAnomaly(cnr *v1alpha1.CustomNodeResource) bool {
	qosConf := generic.NewQoSConfiguration()
	for _, socket := range cnr.Status.TopologyZone {
		for _, numa := range socket.Children {
			if numa.Type != v1alpha1.TopologyTypeNuma {
				// only check numa
				continue
			}
			numabinding_pods := []*v1.Pod{}
			// filter the pod with numa_binding
			for _, allocation := range numa.Allocations {
				key := allocation.Consumer
				namespace, podname, _, err := native.ParseUniqObjectUIDKey(key)
				if err != nil {
					klog.Errorf("[CNRIndicatorNumaExclusiveAnomaly] failed to parse uniq object uid key %s", key)
					continue
				}
				pod, err := ctrl.podLister.Pods(namespace).Get(podname)
				if err != nil {
					klog.Errorf("[CNRIndicatorNumaExclusiveAnomaly] failed to get pod %s", key)
					continue
				}
				if qos.IsPodNumaBinding(qosConf, pod) {
					numabinding_pods = append(numabinding_pods, pod)
				}
			}
			// if the pod with numa_binding and numa_exclusive shares the numa with other pods, return true
			for _, pod := range numabinding_pods {
				if qos.IsPodNumaExclusive(qosConf, pod) && len(numabinding_pods) > 1 {
					return true
				}
			}
		}
	}
	return false
}

// checkNumaAllocatableSumAnomaly checks whether the node's sum of numa allocatable is not equal to the node allocatable
func (ctrl *CNRMonitorController) checkNumaAllocatableSumAnomaly(cnr *v1alpha1.CustomNodeResource) bool {
	node, err := ctrl.nodeLister.Get(cnr.Name)
	if err != nil {
		klog.Errorf("[CNRIndicatorNumaAllocatableSumAnomaly] failed to get node %s", cnr.Name)
		return false
	}

	nodeCpuAllocatable, nodeMemCapacity := int(node.Status.Allocatable.Cpu().AsApproximateFloat64()), int(node.Status.Capacity.Memory().AsApproximateFloat64())
	klog.Infof("[CNRIndicatorNumaAllocatableSumAnomaly] nodeCpuAllocatable: %d, nodeMemCapacity: %d", nodeCpuAllocatable, nodeMemCapacity)
	numaCpuAllocatableSum, numaMemAllocatableSum := 0, 0
	for _, socket := range cnr.Status.TopologyZone {
		for _, numa := range socket.Children {
			if numa.Type != v1alpha1.TopologyTypeNuma {
				// only check numa
				continue
			}
			numaCpuAllocatableSum += int(numa.Resources.Allocatable.Cpu().AsApproximateFloat64())
			numaMemAllocatableSum += int(numa.Resources.Allocatable.Memory().AsApproximateFloat64())
		}
	}
	klog.Infof("[CNRIndicatorNumaAllocatableSumAnomaly] numaCpuAllocatableSum: %d, numaMemAllocatableSum: %d", numaCpuAllocatableSum, numaMemAllocatableSum)
	// TODO: thie rule maybe need to adapt to the scheduler in the future
	if numaCpuAllocatableSum != nodeCpuAllocatable || numaMemAllocatableSum > nodeMemCapacity {
		return true
	}
	return false
}

// checkPodAllocationSumAnomaly checks whether the numa's sum of pod allocation is greater than the numa allocatable
func (ctrl *CNRMonitorController) checkPodAllocationSumAnomaly(cnr *v1alpha1.CustomNodeResource) bool {
	qosConf := generic.NewQoSConfiguration()
	for _, socket := range cnr.Status.TopologyZone {
		for _, numa := range socket.Children {
			if numa.Type != v1alpha1.TopologyTypeNuma {
				// only check numa
				continue
			}
			numaCpuAllocatable, numaMemAllocatable := int(numa.Resources.Allocatable.Cpu().AsApproximateFloat64()), int(numa.Resources.Allocatable.Memory().AsApproximateFloat64())
			klog.Infof("[CNRIndicatorPodAllocationSumAnomaly] numaCpuAllocatable: %d, numaMemAllocatable: %d", numaCpuAllocatable, numaMemAllocatable)
			podCpuAllocationSum, podMemAllocationSum := 0, 0
			for _, allocation := range numa.Allocations {
				key := allocation.Consumer
				namespace, podname, _, err := native.ParseUniqObjectUIDKey(key)
				if err != nil {
					klog.Errorf("[CNRIndicatorPodAllocationSumAnomaly] failed to parse uniq object uid key %s", key)
					continue
				}
				pod, err := ctrl.podLister.Pods(namespace).Get(podname)
				if err != nil {
					klog.Errorf("[CNRIndicatorPodAllocationSumAnomaly] failed to get pod %s", key)
					continue
				}
				// only check the pod with numa binding for now
				if qos.IsPodNumaBinding(qosConf, pod) {
					podCpuAllocationSum += int(allocation.Requests.Cpu().AsApproximateFloat64())
					podMemAllocationSum += int(allocation.Requests.Memory().AsApproximateFloat64())
				}
			}
			klog.Infof("[CNRIndicatorPodAllocationSumAnomaly] podCpuAllocationSum: %d, podMemAllocationSum: %d", podCpuAllocationSum, podMemAllocationSum)
			if podCpuAllocationSum > numaCpuAllocatable || podMemAllocationSum > numaMemAllocatable {
				return true
			}
		}
	}
	return false
}

// emitCNRAnomalyMetric emit CNR anomaly metric
func (ctrl *CNRMonitorController) emitCNRAnomalyMetric(cnr *v1alpha1.CustomNodeResource, reason string) error {
	_ = ctrl.metricsEmitter.StoreInt64(metricsNameCNRReportAnomaly, 1, metrics.MetricTypeNameRaw,
		metrics.MetricTag{
			Key: "node_name", Val: cnr.Name,
		},
		metrics.MetricTag{
			Key: "reason", Val: reason,
		},
	)

	return nil
}

// checkAndEmitCNRReportLantencyMetric check and emit CNR report lantency metric
func (ctrl *CNRMonitorController) checkAndEmitCNRReportLantencyMetric(cnr *v1alpha1.CustomNodeResource) error {
	for _, socket := range cnr.Status.TopologyZone {
		for _, numa := range socket.Children {
			if numa.Type != v1alpha1.TopologyTypeNuma {
				// only check numa
				continue
			}
			for _, allocation := range numa.Allocations {
				key := allocation.Consumer
				scheduledTime, ok := ctrl.podTimeMap.Load(key)
				// if the pod is not in podTimeMap or if the podTimeMap value is zero, continue
				if !ok || scheduledTime.(time.Time).IsZero() {
					continue
				}
				// emit cnr report lantency metric
				ctrl.emitCNRReportLantencyMetric(cnr.Name, key, time.Since(scheduledTime.(time.Time)).Milliseconds(), "false")
				// delete the used data from podTimeMap
				ctrl.podTimeMap.Delete(key)
			}
		}
	}
	return nil
}

// emitCNRReportLantencyMetric emit CNR report lantency metric
func (ctrl *CNRMonitorController) emitCNRReportLantencyMetric(nodeName string, key string, lantency int64, isTimeOut string) {
	namespace, podName, uid, err := native.ParseUniqObjectUIDKey(key)
	if err != nil {
		klog.Errorf("[CNRReportLantency] failed to parse uniq object uid key %s", key)
	}
	klog.Infof("[CNRReportLantency] pod %s/%s/%s report lantency: %dms", namespace, podName, uid, lantency)
	_ = ctrl.metricsEmitter.StoreFloat64(metricsNameCNRReportLantency, float64(lantency),
		metrics.MetricTypeNameRaw,
		metrics.MetricTag{
			Key: "node_name", Val: nodeName,
		},
		metrics.MetricTag{
			Key: "namespace", Val: namespace,
		},
		metrics.MetricTag{
			Key: "pod_name", Val: podName,
		},
		metrics.MetricTag{
			Key: "pod_uid", Val: uid,
		},
		metrics.MetricTag{
			Key: "time_out", Val: isTimeOut,
		},
	)
}
