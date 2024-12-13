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

package evictor

import (
	"context"

	v1 "k8s.io/api/core/v1"

	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

// PercentageEvictor is the interface used in advisor policy
type PercentageEvictor interface {
	Evict(ctx context.Context, targetPercent int)
	HasEvictablePods() bool
}

type loadEvictor struct {
	qosConfig  *generic.QoSConfiguration
	podFetcher pod.PodFetcher
	podEvictor PodEvictor
}

func (l *loadEvictor) HasEvictablePods() bool {
	evictablePods, err := l.podFetcher.GetPodList(context.Background(), l.isEvictablePod)
	if err != nil {
		// error treated as no evictable pods found
		return false
	}

	return len(evictablePods) > 0
}

func (l *loadEvictor) isEvictablePod(pod *v1.Pod) bool {
	isReclaimedQoS, err := l.qosConfig.CheckReclaimedQoSForPod(pod)
	return err == nil && isReclaimedQoS
}

func getN(pods []*v1.Pod, n int) []*v1.Pod {
	if n >= len(pods) {
		return pods
	}
	return pods[:n]
}

func (l loadEvictor) Evict(ctx context.Context, targetPercent int) {
	pods, err := l.podFetcher.GetPodList(ctx, nil)
	if err != nil {
		general.Errorf("pap: evict: failed to get pods: %v", err)
		return
	}
	countToEvict := len(pods) * targetPercent / 100
	if countToEvict == 0 {
		general.InfofV(6, "pap: evict: skip 0 to evict")
		return
	}

	evictablePods, err := l.podFetcher.GetPodList(ctx, l.isEvictablePod)
	if err != nil {
		general.Errorf("pap: evict: failed to get BE pods: %v", err)
		return
	}

	general.InfofV(6, "pap: evict: %d pods, %d BE; going to evict BE up to %d%%%% pods = %d",
		len(pods), len(evictablePods), targetPercent, countToEvict)

	// todo: replace this FIFO evict policy with one having sort of randomization
	// todo: explore more efficient ways that takes into account cpu utilization etc
	if err := l.podEvictor.Evict(ctx, getN(evictablePods, countToEvict)); err != nil {
		// power alert eviction is the best effort by design; ok to log the error here
		general.Warningf("pap: failed to request eviction of pods: %v", err)
	}
}

func NewPowerLoadEvict(qosConfig *generic.QoSConfiguration,
	podFetcher pod.PodFetcher,
	podEvictor PodEvictor,
) PercentageEvictor {
	return &loadEvictor{
		qosConfig:  qosConfig,
		podFetcher: podFetcher,
		podEvictor: podEvictor,
	}
}
