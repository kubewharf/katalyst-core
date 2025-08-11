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

package podkiller

import (
	"context"

	v1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/config/generic"
)

// qosAwareKiller implements the Killer interface with QoS-aware killing
// capabilities. It routes pod eviction requests to different killers based on
// the pod's QoS level.
type qosAwareKiller struct {
	defaultKiller Killer
	killerMap     map[string]Killer
	qosConfig     *generic.QoSConfiguration
}

// NewQoSAwareKiller creates a new QoS-aware killer based on configuration
func NewQoSAwareKiller(
	qosConfig *generic.QoSConfiguration,
	defaultKiller Killer,
	killerMap map[string]Killer,
) (Killer, error) {
	return &qosAwareKiller{
		defaultKiller: defaultKiller,
		killerMap:     killerMap,
		qosConfig:     qosConfig,
	}, nil
}

func (q *qosAwareKiller) Evict(ctx context.Context, pod *v1.Pod, gracePeriodSeconds int64, reason, plugin string) error {
	killer := q.getKillerForPod(pod)

	return killer.Evict(ctx, pod, gracePeriodSeconds, reason, plugin)
}

// Name returns the name of this pod killer
func (q *qosAwareKiller) Name() string {
	return "qos-aware-killer"
}

// getKillerForPod returns the appropriate killer for the given pod
func (q *qosAwareKiller) getKillerForPod(pod *v1.Pod) Killer {
	qosLevel, err := q.qosConfig.GetQoSLevelForPod(pod)
	if err != nil {
		klog.Warningf("Failed to get QoS level for pod %s/%s: %v, using default killer", pod.Namespace, pod.Name, err)
		return q.defaultKiller
	}

	if killer, exists := q.killerMap[qosLevel]; exists {
		return killer
	}

	return q.defaultKiller
}
