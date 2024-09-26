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

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

// LoadEvictor is the interface used in advisor policy
type LoadEvictor interface {
	Evict(ctx context.Context, targetPercent int)
}

type loadEvictor struct {
	qosConfig  *generic.QoSConfiguration
	podFetcher pod.PodFetcher
	podEvictor PodEvictor
}

func (l loadEvictor) isBE(pod *v1.Pod) bool {
	qosLevel, err := l.qosConfig.GetQoSLevelForPod(pod)
	if err != nil {
		// unknown, not BE anyway
		return false
	}

	return qosLevel == apiconsts.PodAnnotationQoSLevelReclaimedCores
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

	bePods, err := l.podFetcher.GetPodList(ctx, l.isBE)
	if err != nil {
		general.Errorf("pap: evict: failed to get BE pods: %v", err)
		return
	}

	general.InfofV(6, "pap: evict: %d pods, %d BE; going to evict BE up to %d%%%% pods = %d",
		len(pods), len(bePods), targetPercent, countToEvict)

	// discard pending requests not handled yet; we will provide a new sleet of evict requests anyway
	l.podEvictor.Reset(ctx)

	// todo: replace this FIFO evict policy with one having sort of randomization
	for i, p := range bePods {
		// not care much for returned error as power alert eviction is the best effort by design
		if i >= countToEvict {
			break
		}
		_ = l.podEvictor.Evict(ctx, p)
	}
}

func NewPowerLoadEvict(qosConfig *generic.QoSConfiguration,
	podFetcher pod.PodFetcher,
	podEvictor PodEvictor,
) LoadEvictor {
	return &loadEvictor{
		qosConfig:  qosConfig,
		podFetcher: podFetcher,
		podEvictor: podEvictor,
	}
}
