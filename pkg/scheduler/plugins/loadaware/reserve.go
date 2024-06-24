package loadaware

import (
	"context"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/kubernetes/pkg/scheduler/framework"
)

func (p *Plugin) Reserve(ctx context.Context, _ *framework.CycleState, pod *v1.Pod, nodeName string) *framework.Status {
	cache.addPod(nodeName, pod, time.Now())
	return nil
}

func (p *Plugin) Unreserve(ctx context.Context, _ *framework.CycleState, pod *v1.Pod, nodeName string) {
	cache.removePod(nodeName, pod)
}
