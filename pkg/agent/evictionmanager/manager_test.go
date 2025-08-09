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

package evictionmanager

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"

	pluginapi "github.com/kubewharf/katalyst-api/pkg/protocol/evictionplugin/v1alpha1"
	endpointpkg "github.com/kubewharf/katalyst-core/pkg/agent/evictionmanager/endpoint"
	"github.com/kubewharf/katalyst-core/pkg/client"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/adminqos/eviction"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/credential"
	"github.com/kubewharf/katalyst-core/pkg/util/credential/authorization"
)

var (
	evictionManagerSyncPeriod = 10 * time.Second
	pods                      = []*v1.Pod{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "pod-1",
				UID:  "pod-1",
			},
			Status: v1.PodStatus{
				Phase: v1.PodRunning,
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "pod-2",
				UID:  "pod-2",
			},
			Status: v1.PodStatus{
				Phase: v1.PodRunning,
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "pod-3",
				UID:  "pod-3",
			},
			Status: v1.PodStatus{
				Phase: v1.PodRunning,
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "pod-4",
				UID:  "pod-4",
			},
			Status: v1.PodStatus{
				Phase: v1.PodRunning,
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "pod-5",
				UID:  "pod-5",
			},
			Status: v1.PodStatus{
				Phase: v1.PodRunning,
			},
		},
	}
)

func makeConf() *config.Configuration {
	conf := config.NewConfiguration()
	conf.EvictionManagerSyncPeriod = evictionManagerSyncPeriod
	conf.GetDynamicConfiguration().MemoryPressureEvictionConfiguration.GracePeriod = eviction.DefaultGracePeriod
	conf.PodKiller = consts.KillerNameEvictionKiller
	conf.GenericConfiguration.AuthConfiguration.AuthType = credential.AuthTypeInsecure
	conf.GenericConfiguration.AuthConfiguration.AccessControlType = authorization.AccessControlTypeInsecure
	conf.HostPathNotifierRootPath = "/opt/katalyst"

	return conf
}

func makeMetaServer() *metaserver.MetaServer {
	return &metaserver.MetaServer{
		MetaAgent: &agent.MetaAgent{
			PodFetcher: &pod.PodFetcherStub{PodList: pods},
		},
	}
}

type pluginSkeleton struct{}

func (p *pluginSkeleton) Start() {
}

func (p *pluginSkeleton) Stop() {
}

func (p *pluginSkeleton) IsStopped() bool {
	return false
}

func (p *pluginSkeleton) StopGracePeriodExpired() bool {
	return false
}

type plugin1 struct {
	pluginSkeleton
}

func (p *plugin1) ThresholdMet(_ context.Context) (*pluginapi.ThresholdMetResponse, error) {
	return &pluginapi.ThresholdMetResponse{
		MetType: pluginapi.ThresholdMetType_NOT_MET,
	}, nil
}

func (p *plugin1) GetTopEvictionPods(_ context.Context, _ *pluginapi.GetTopEvictionPodsRequest) (*pluginapi.GetTopEvictionPodsResponse, error) {
	return &pluginapi.GetTopEvictionPodsResponse{}, nil
}

func (p *plugin1) GetEvictPods(_ context.Context, _ *pluginapi.GetEvictPodsRequest) (*pluginapi.GetEvictPodsResponse, error) {
	return &pluginapi.GetEvictPodsResponse{
		EvictPods: []*pluginapi.EvictPod{
			{
				Pod: &v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name: "pod-1",
						UID:  "pod-1",
					},
					Status: v1.PodStatus{
						Phase: v1.PodRunning,
					},
				},
				ForceEvict: false,
			},
			{
				Pod: &v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name: "pod-2",
						UID:  "pod-2",
					},
					Status: v1.PodStatus{
						Phase: v1.PodRunning,
					},
				},
				ForceEvict: true,
			},
			{
				Pod: &v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name: "pod-5",
						UID:  "pod-5",
					},
					Status: v1.PodStatus{
						Phase: v1.PodRunning,
					},
				},
				ForceEvict: false,
			},
		},
	}, nil
}

type plugin2 struct {
	pluginSkeleton
}

func (p plugin2) ThresholdMet(_ context.Context) (*pluginapi.ThresholdMetResponse, error) {
	return &pluginapi.ThresholdMetResponse{
		MetType:            pluginapi.ThresholdMetType_HARD_MET,
		ThresholdValue:     0.8,
		ObservedValue:      0.9,
		ThresholdOperator:  pluginapi.ThresholdOperator_GREATER_THAN,
		EvictionScope:      "plugin2_scope",
		GracePeriodSeconds: -1,
		Condition: &pluginapi.Condition{
			ConditionType: pluginapi.ConditionType_NODE_CONDITION,
			Effects:       []string{"NoSchedule"},
			ConditionName: "diskPressure",
			MetCondition:  true,
		},
	}, nil
}

func (p plugin2) GetTopEvictionPods(_ context.Context, _ *pluginapi.GetTopEvictionPodsRequest) (*pluginapi.GetTopEvictionPodsResponse, error) {
	return &pluginapi.GetTopEvictionPodsResponse{TargetPods: []*v1.Pod{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "pod-3",
				UID:  "pod-3",
			},
			Status: v1.PodStatus{
				Phase: v1.PodRunning,
			},
		},
	}}, nil
}

func (p plugin2) GetEvictPods(_ context.Context, _ *pluginapi.GetEvictPodsRequest) (*pluginapi.GetEvictPodsResponse, error) {
	return &pluginapi.GetEvictPodsResponse{EvictPods: []*pluginapi.EvictPod{}}, nil
}

func makeEvictionManager(t *testing.T) *EvictionManger {
	mgr, err := NewEvictionManager(&client.GenericClientSet{}, nil, makeMetaServer(), metrics.DummyMetrics{}, makeConf())
	assert.NoError(t, err)
	mgr.endpoints = map[string]endpointpkg.Endpoint{
		"plugin1": &plugin1{},
		"plugin2": &plugin2{},
	}

	return mgr
}

func TestEvictionManger_collectEvictionResult(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name               string
		dryrun             []string
		wantSoftEvictPods  sets.String
		wantForceEvictPods sets.String
		wantConditions     sets.String
	}{
		{
			name:   "no dryrun",
			dryrun: []string{},
			wantSoftEvictPods: sets.String{
				"pod-1": sets.Empty{},
				"pod-5": sets.Empty{},
			},
			wantForceEvictPods: sets.String{
				"pod-2": sets.Empty{},
				"pod-3": sets.Empty{},
			},
			wantConditions: sets.String{
				"diskPressure": sets.Empty{},
			},
		},
		{
			name:              "dryrun plugin1",
			dryrun:            []string{"plugin1"},
			wantSoftEvictPods: sets.String{},
			wantForceEvictPods: sets.String{
				"pod-3": sets.Empty{},
			},
			wantConditions: sets.String{
				"diskPressure": sets.Empty{},
			},
		},
		{
			name:   "dryrun plugin2",
			dryrun: []string{"plugin2"},
			wantSoftEvictPods: sets.String{
				"pod-1": sets.Empty{},
				"pod-5": sets.Empty{},
			},
			wantForceEvictPods: sets.String{
				"pod-2": sets.Empty{},
			},
			wantConditions: sets.String{},
		},
		{
			name:               "dryrun plugin1 & plugin2",
			dryrun:             []string{"plugin1", "plugin2"},
			wantSoftEvictPods:  sets.String{},
			wantForceEvictPods: sets.String{},
			wantConditions:     sets.String{},
		},
		{
			name:               "dryrun *",
			dryrun:             []string{"*"},
			wantSoftEvictPods:  sets.String{},
			wantForceEvictPods: sets.String{},
			wantConditions:     sets.String{},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mgr := makeEvictionManager(t)
			mgr.conf.GetDynamicConfiguration().DryRun = tt.dryrun

			collector, _ := mgr.collectEvictionResult(pods)
			gotForceEvictPods := sets.String{}
			gotSoftEvictPods := sets.String{}
			gotConditions := sets.String{}
			for _, evictPod := range collector.getForceEvictPods() {
				gotForceEvictPods.Insert(evictPod.Pod.Name)
			}

			for _, evictPod := range collector.getSoftEvictPods() {
				gotSoftEvictPods.Insert(evictPod.Pod.Name)
			}

			for name := range collector.getCurrentConditions() {
				gotConditions.Insert(name)
			}
			assert.Equal(t, tt.wantForceEvictPods, gotForceEvictPods)
			assert.Equal(t, tt.wantSoftEvictPods, gotSoftEvictPods)
			assert.Equal(t, tt.wantConditions, gotConditions)
		})
	}
}
