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

package component

import (
	"context"

	v1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/evictionmanager/podkiller"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
)

const (
	// gracePeriodSeconds is chosen 8 seconds as the interval to next round is 10 seconds
	gracePeriodSeconds = 8

	reasonEviction = "power alert"
	pluginName     = "power_aware_plugin"
)

type LoadEvictor interface {
	Evict(ctx context.Context, targetPercent int)
}

type loadEvictor struct {
	qosConfig  *generic.QoSConfiguration
	podFetcher pod.PodFetcher

	// todo: change podkill (or pod evict) API after KAtalyst-agent has it in place
	// for now let's hook up with a dummy of pod killer type
	// wiser design of API is to support batch kill - not finalized yet
	podKiller podkiller.Killer
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
	pods, err := l.podFetcher.GetPodList(ctx, l.isBE)
	if err != nil {
		klog.Errorf("failed to get pods: %v", err)
		return
	}

	countToLive := len(pods) * targetPercent / 100
	for _, p := range pods[:countToLive] {
		// not care much for returned error as power alert eviction is the best effort by design
		_ = l.podKiller.Evict(ctx, p, gracePeriodSeconds, reasonEviction, pluginName)
	}
}

var _ LoadEvictor = &loadEvictor{}

type dummyPodKiller struct {
	podKiller podkiller.Killer
	called    int
}

func (d dummyPodKiller) Name() string {
	panic("dummyPodKiller")
}

func (d *dummyPodKiller) Evict(ctx context.Context, pod *v1.Pod, gracePeriodSeconds int64, reason, plugin string) error {
	// dummy does no op, besides recording called times
	d.called += 1
	return nil
}
