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

package loadaware

import (
	"context"
	"fmt"
	"math"
	"time"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/scheduler/framework"

	"github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
)

func (p *Plugin) Filter(_ context.Context, _ *framework.CycleState, pod *v1.Pod, nodeInfo *framework.NodeInfo) *framework.Status {
	if !p.IsLoadAwareEnabled(pod) {
		return nil
	}

	// fit by node metrics
	status := p.fitByNPD(nodeInfo)
	if status != nil || status.IsUnschedulable() {
		return status
	}

	if p.enablePortrait() {
		// fit by workload portrait
		status = p.fitByPortrait(pod, nodeInfo)
	}

	return status
}

func (p *Plugin) fitByNPD(nodeInfo *framework.NodeInfo) *framework.Status {
	node := nodeInfo.Node()
	if node == nil {
		return framework.NewStatus(framework.Unschedulable, "node not found")
	}

	if len(p.args.ResourceToThresholdMap) == 0 {
		klog.Warningf("load aware fit missing required args")
		return nil
	}

	// get npd from informer
	npd, err := p.npdLister.Get(node.Name)
	if err != nil {
		klog.Errorf("get npd of node %v failed, err: %v", node.Name, err)
		return nil
	}
	if npd.Status.NodeMetrics == nil {
		klog.Errorf("npd of node %s status NodeUsage is nil", node.Name)
		return nil
	}

	usageInfo := p.getNodeMetrics(npd, loadAwareMetricScope, 15*time.Minute)
	if usageInfo == nil {
		klog.Errorf("npd of node %s status NodeUsage miss avg_15min metrics", node.Name)
		return nil
	}
	for resourceName, threshold := range p.args.ResourceToThresholdMap {
		if threshold == 0 {
			continue
		}
		total := node.Status.Allocatable[resourceName]
		if total.IsZero() {
			continue
		}
		used := usageInfo[resourceName]
		usage := int64(math.Round(float64(used.MilliValue()) / float64(total.MilliValue()) * 100))
		klog.V(6).Infof("loadAware fit node: %v resource: %v usage: %v, threshold: %v", node.Name, resourceName, usage, threshold)
		if usage > threshold {
			return framework.NewStatus(framework.Unschedulable, fmt.Sprintf("node(s) %s usage exceed threshold, usage:%v, threshold: %v ", resourceName, usage, threshold))
		}
	}

	return nil
}

func (p *Plugin) fitByPortrait(pod *v1.Pod, nodeInfo *framework.NodeInfo) *framework.Status {
	if pod == nil {
		return nil
	}
	if nodeInfo == nil || nodeInfo.Node() == nil {
		return nil
	}

	nodePredictUsage, err := p.getNodePredictUsage(pod, nodeInfo.Node().Name)
	if err != nil {
		klog.Error(err)
		return nil
	}

	// check if nodePredictUsage is greater than threshold
	for _, resourceName := range []v1.ResourceName{v1.ResourceCPU, v1.ResourceMemory} {
		threshold, ok := p.args.ResourceToThresholdMap[resourceName]
		if !ok {
			continue
		}
		total := nodeInfo.Node().Status.Allocatable[resourceName]
		if total.IsZero() {
			continue
		}
		var totalValue int64
		if resourceName == v1.ResourceCPU {
			totalValue = total.MilliValue()
		} else {
			totalValue = total.Value()
		}

		maxUsage := nodePredictUsage.max(resourceName)
		usageRatio := int64(math.Round(maxUsage / float64(totalValue) * 100))
		klog.V(6).Infof("loadAware fit pod %v, node %v, resource %v, threshold: %v, usageRatio: %v, maxUsage: %v, nodeTotal %v",
			pod.Name, nodeInfo.Node().Name, resourceName, threshold, usageRatio, maxUsage, totalValue)

		if usageRatio > threshold {
			return framework.NewStatus(framework.Unschedulable, fmt.Sprintf("node(s) %s usage exceed threshold, usage:%v, threshold: %v ", resourceName, usageRatio, threshold))
		}
	}

	return nil
}

func (p *Plugin) getNodeMetrics(npd *v1alpha1.NodeProfileDescriptor, scope string, window time.Duration) v1.ResourceList {
	res := v1.ResourceList{}

	for i := range npd.Status.NodeMetrics {
		if npd.Status.NodeMetrics[i].Scope != scope {
			continue
		}

		for _, metricValue := range npd.Status.NodeMetrics[i].Metrics {
			if metricValue.Window.Duration == window {
				if isMetricExpired(metricValue.Timestamp, *p.args.NodeMetricsExpiredSeconds) {
					klog.Warningf("node %v skip expired metric %v, timestamp: %v, now: %v, expiredSeconds: %v",
						npd.Name, metricValue.MetricName, metricValue.Timestamp, time.Now(), *p.args.NodeMetricsExpiredSeconds)
					continue
				}

				res[v1.ResourceName(metricValue.MetricName)] = metricValue.Value
			}
		}

		break
	}
	if len(res) <= 0 {
		return nil
	}

	return res
}

func isMetricExpired(t metav1.Time, nodeMetricExpirationSeconds int64) bool {
	return nodeMetricExpirationSeconds > 0 && time.Since(t.Time) > time.Duration(nodeMetricExpirationSeconds)*time.Second
}
