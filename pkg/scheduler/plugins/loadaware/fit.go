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
