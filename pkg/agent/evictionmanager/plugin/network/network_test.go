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

package network

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	pluginapi "github.com/kubewharf/katalyst-api/pkg/protocol/evictionplugin/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/adminqos/eviction"
	coreconsts "github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/cnr"
	metametric "github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	utilmetric "github.com/kubewharf/katalyst-core/pkg/util/metric"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

const (
	bandwidthTestLowBPS      = 1000 * 1000
	bandwidthTestMediumBPS   = 6 * 1000 * 1000
	bandwidthTestPressureBPS = 10 * 1000 * 1000
)

func TestSyncNICState(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name              string
		cnr               *v1alpha1.CustomNodeResource
		expectedHealthy   map[string]float64
		expectedNICStates map[string]*unhealthyNICState
	}{{
		name: "sync healthy and unhealthy nics",
		cnr: &v1alpha1.CustomNodeResource{
			Status: v1alpha1.CustomNodeResourceStatus{
				TopologyZone: []*v1alpha1.TopologyZone{{
					Type: v1alpha1.TopologyTypeSocket,
					Children: []*v1alpha1.TopologyZone{{
						Name: "eth0",
						Type: v1alpha1.TopologyTypeNIC,
						Resources: v1alpha1.Resources{
							Allocatable: &v1.ResourceList{
								apiconsts.ResourceNetBandwidth: resource.MustParse("0"),
							},
						},
					}, {
						Name: "eth1",
						Type: v1alpha1.TopologyTypeNIC,
						Resources: v1alpha1.Resources{
							Allocatable: &v1.ResourceList{
								apiconsts.ResourceNetBandwidth: resource.MustParse("100"),
							},
						},
					}},
				}},
			},
		},
		expectedHealthy: map[string]float64{
			formatBandwidthScope(bandwidthScope{netns: machine.DefaultNICNamespace, nic: "eth1", direction: bandwidthDirectionRX}): 100,
			formatBandwidthScope(bandwidthScope{netns: machine.DefaultNICNamespace, nic: "eth1", direction: bandwidthDirectionTX}): 100,
		},
		expectedNICStates: map[string]*unhealthyNICState{
			"eth0": {nicZone: v1alpha1.TopologyZone{Name: "eth0"}},
		},
	}, {
		name: "remove recovered nic",
		cnr: &v1alpha1.CustomNodeResource{
			Status: v1alpha1.CustomNodeResourceStatus{
				TopologyZone: []*v1alpha1.TopologyZone{},
			},
		},
		expectedHealthy:   map[string]float64{},
		expectedNICStates: map[string]*unhealthyNICState{},
	}, {
		name: "update nic zone allocation",
		cnr: &v1alpha1.CustomNodeResource{
			Status: v1alpha1.CustomNodeResourceStatus{
				TopologyZone: []*v1alpha1.TopologyZone{{
					Type: v1alpha1.TopologyTypeSocket,
					Children: []*v1alpha1.TopologyZone{{
						Name: "eth0",
						Type: v1alpha1.TopologyTypeNIC,
						Resources: v1alpha1.Resources{
							Allocatable: &v1.ResourceList{
								apiconsts.ResourceNetBandwidth: resource.MustParse("0"),
							},
						},
						Allocations: []*v1alpha1.Allocation{{
							Consumer: "test-pod",
						}},
					}},
				}},
			},
		},
		expectedNICStates: map[string]*unhealthyNICState{
			"eth0": {
				nicZone: v1alpha1.TopologyZone{
					Name:        "eth0",
					Allocations: []*v1alpha1.Allocation{{Consumer: "test-pod"}},
				},
			},
		},
	}}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dynamicConfig := dynamic.NewDynamicAgentConfiguration()
			dynamicConfig.GetDynamicConfiguration().NetworkEvictionConfiguration = &eviction.NetworkEvictionConfiguration{
				EnableNICBandwidthEviction: true,
				NICBandwidthRingSize:       1,
			}
			deviceMetrics := make(map[string]fakeNICMetric, len(tt.expectedHealthy))
			for scope, expectedCapacity := range tt.expectedHealthy {
				deviceMetrics[scope] = fakeNICMetric{
					bps:   convertMbpsToBytesPerSecond(expectedCapacity),
					speed: expectedCapacity,
				}
			}
			plugin := &nicEvictionPlugin{
				unhealthyNICState: make(map[string]*unhealthyNICState),
				healthyNICState:   make(map[string]*bandwidthState),
				dynamicConfig:     dynamicConfig,
				bandwidthMetricQuerier: &fakeBandwidthMetricQuerier{
					deviceMetrics: deviceMetrics,
				},
				metaServer: &metaserver.MetaServer{
					MetaAgent: &agent.MetaAgent{
						CNRFetcher: &cnr.CNRFetcherStub{CNR: tt.cnr},
					},
				},
				emitter: metrics.DummyMetrics{},
			}

			plugin.syncNICState(context.TODO())

			require.Len(t, plugin.healthyNICState, len(tt.expectedHealthy))
			for scope, expectedCapacity := range tt.expectedHealthy {
				actualState, exists := plugin.healthyNICState[scope]
				require.True(t, exists, "expected healthy NIC scope %v not found", scope)
				require.NotNil(t, actualState)
				latest, ok := actualState.latestSample()
				require.True(t, ok)
				require.Equal(t, expectedCapacity, latest.capacityMbps)
			}

			// Verify NIC states count
			if len(plugin.unhealthyNICState) != len(tt.expectedNICStates) {
				t.Fatalf("Expected %d NIC states, got %d",
					len(tt.expectedNICStates), len(plugin.unhealthyNICState))
			}

			// Verify each NIC state
			for nic, expectedState := range tt.expectedNICStates {
				actualState, exists := plugin.unhealthyNICState[nic]
				if !exists {
					t.Fatalf("Expected NIC %s not found", nic)
				}
				if actualState.nicZone.Name != expectedState.nicZone.Name {
					t.Errorf("NIC name mismatch: expected %s, got %s",
						expectedState.nicZone.Name, actualState.nicZone.Name)
				}
				if time.Since(actualState.lastUnhealthyTime) > time.Second {
					t.Error("Last unhealthy time should be recent")
				}
			}
		})
	}
}

func TestGetEvictPods(t *testing.T) {
	t.Parallel()
	// Create a context
	ctx := context.TODO()

	// Define test cases
	tests := []struct {
		name              string
		conf              eviction.NetworkEvictionConfiguration
		cnr               *v1alpha1.CustomNodeResource
		activePods        []*v1.Pod
		unhealthyNICState map[string]*unhealthyNICState
		expectedEvicts    int
	}{
		{
			name:       "NIC health eviction disabled",
			activePods: []*v1.Pod{},
			conf: eviction.NetworkEvictionConfiguration{
				EnableNICHealthEviction: false,
			},
			expectedEvicts: 0,
		},
		{
			name: "Pods exist but NIC is healthy",
			activePods: []*v1.Pod{
				{ObjectMeta: metav1.ObjectMeta{Name: "pod1"}},
			},
			conf: eviction.NetworkEvictionConfiguration{
				EnableNICHealthEviction: true,
			},
			unhealthyNICState: map[string]*unhealthyNICState{},
			expectedEvicts:    0,
		},
		{
			name: "Pods (allocation fromm annotation) exist and NIC is unhealthy",
			activePods: []*v1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pod1",
						Namespace: "default",
						Annotations: map[string]string{
							apiconsts.PodAnnotationNICSelectionResultKey: "eth0",
						},
					},
					Spec: v1.PodSpec{
						Containers: []v1.Container{
							{
								Resources: v1.ResourceRequirements{
									Requests: v1.ResourceList{
										apiconsts.ResourceNetBandwidth: resource.MustParse("10G"),
									},
								},
							},
						},
					},
				},
			},
			conf: eviction.NetworkEvictionConfiguration{
				EnableNICHealthEviction: true,
			},
			unhealthyNICState: map[string]*unhealthyNICState{
				"eth0": {},
			},
			expectedEvicts: 1,
		},
		{
			name: "Pods (allocation fromm state) exist and NIC is unhealthy",
			activePods: []*v1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pod1",
						Namespace: "default",
						UID:       types.UID("pod1-uid"),
					},
				},
			},
			conf: eviction.NetworkEvictionConfiguration{
				EnableNICHealthEviction: true,
			},
			unhealthyNICState: map[string]*unhealthyNICState{
				"eth0": {
					nicZone: v1alpha1.TopologyZone{
						Name: "eth0",
						Type: v1alpha1.TopologyTypeNIC,
						Allocations: []*v1alpha1.Allocation{
							{
								Consumer: "default/pod1/pod1-uid",
								Requests: &v1.ResourceList{
									apiconsts.ResourceNetBandwidth: resource.MustParse("10G"),
								},
							},
						},
					},
				},
			},
			expectedEvicts: 1,
		},
		{
			name: "Only some pods affected by unhealthy NIC",
			activePods: []*v1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pod1",
						Namespace: "default",
						Annotations: map[string]string{
							apiconsts.PodAnnotationNICSelectionResultKey: "eth0",
						},
					},
					Spec: v1.PodSpec{
						Containers: []v1.Container{
							{
								Resources: v1.ResourceRequirements{
									Requests: v1.ResourceList{
										apiconsts.ResourceNetBandwidth: resource.MustParse("10G"),
									},
								},
							},
						},
					},
				},
				{ObjectMeta: metav1.ObjectMeta{Name: "pod2", Namespace: "default"}},
			},
			conf: eviction.NetworkEvictionConfiguration{
				EnableNICHealthEviction: true,
			},
			unhealthyNICState: map[string]*unhealthyNICState{
				"eth0": {},
			},
			expectedEvicts: 1,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Initialize the necessary components for nicEvictionPlugin
			metaServer := &metaserver.MetaServer{
				MetaAgent: &agent.MetaAgent{
					CNRFetcher: &cnr.CNRFetcherStub{
						CNR: tt.cnr,
					},
				},
			}
			emitter := &metrics.DummyMetrics{}
			dynamicConfig := dynamic.NewDynamicAgentConfiguration()
			dynamicConfig.GetDynamicConfiguration().NetworkEvictionConfiguration = &tt.conf

			// Create the request
			request := &pluginapi.GetEvictPodsRequest{
				ActivePods: tt.activePods,
			}

			plugin := &nicEvictionPlugin{
				metaServer:        metaServer,
				emitter:           emitter,
				dynamicConfig:     dynamicConfig,
				unhealthyNICState: tt.unhealthyNICState,
			}

			// Call GetEvictPods
			response, err := plugin.GetEvictPods(ctx, request)
			if err != nil {
				t.Fatalf("GetEvictPods() error = %v", err)
			}

			// Check the number of evicted pods
			if len(response.EvictPods) != tt.expectedEvicts {
				t.Errorf("Expected %d evicted pods, got %d", tt.expectedEvicts, len(response.EvictPods))
			}
		})
	}
}

func TestNICBandwidthEvictionThresholdMetFeatureDisabled(t *testing.T) {
	t.Parallel()

	plugin := newBandwidthTestPlugin(&eviction.NetworkEvictionConfiguration{
		EnableNICBandwidthEviction: false,
	}, &fakeBandwidthMetricQuerier{
		deviceMetrics: map[string]fakeNICMetric{
			"bandwidth/ns1/eth0/rx": {bps: bandwidthTestPressureBPS, speed: 100},
		},
	}, []machine.InterfaceInfo{newBandwidthTestNIC("ns1", "eth0")}, nil)

	resp, err := plugin.ThresholdMet(context.Background(), &pluginapi.GetThresholdMetRequest{
		ActivePods: []*v1.Pod{newNetworkTestPod("default", "pod1", "uid1", "ns1-eth0")},
	})
	require.NoError(t, err)
	require.Equal(t, pluginapi.ThresholdMetType_NOT_MET, resp.MetType)
}

func TestNICBandwidthEvictionThresholdMetContinuous(t *testing.T) {
	t.Parallel()

	plugin := newBandwidthTestPlugin(&eviction.NetworkEvictionConfiguration{
		EnableNICBandwidthEviction:         true,
		NICBandwidthUtilizationThreshold:   0.75,
		NICBandwidthContinuousMetThreshold: 3,
		NICBandwidthRingSize:               6,
		NICBandwidthRingMetThreshold:       4,
		NICBandwidthGracePeriod:            12,
	}, &fakeBandwidthMetricQuerier{
		deviceMetrics: map[string]fakeNICMetric{
			"bandwidth/ns1/eth0/rx": {bps: bandwidthTestPressureBPS, speed: 100},
		},
	}, []machine.InterfaceInfo{newBandwidthTestNIC("ns1", "eth0")}, nil)

	for i := 0; i < 2; i++ {
		plugin.syncNICState(context.Background())
		resp, err := plugin.ThresholdMet(context.Background(), &pluginapi.GetThresholdMetRequest{
			ActivePods: []*v1.Pod{newNetworkTestPod("default", "pod1", "uid1", "ns1-eth0")},
		})
		require.NoError(t, err)
		require.Equal(t, pluginapi.ThresholdMetType_NOT_MET, resp.MetType)
	}

	plugin.syncNICState(context.Background())
	resp, err := plugin.ThresholdMet(context.Background(), &pluginapi.GetThresholdMetRequest{
		ActivePods: []*v1.Pod{newNetworkTestPod("default", "pod1", "uid1", "ns1-eth0")},
	})
	require.NoError(t, err)
	require.Equal(t, pluginapi.ThresholdMetType_HARD_MET, resp.MetType)
	require.Zero(t, resp.GracePeriodSeconds)
	require.Equal(t, "bandwidth/ns1/eth0/rx", resp.EvictionScope)
}

func TestNICBandwidthEvictionThresholdMetChecksHealthyNICWithoutPods(t *testing.T) {
	t.Parallel()

	plugin := newBandwidthTestPlugin(&eviction.NetworkEvictionConfiguration{
		EnableNICBandwidthEviction:         true,
		NICBandwidthUtilizationThreshold:   0.75,
		NICBandwidthContinuousMetThreshold: 1,
		NICBandwidthRingSize:               6,
		NICBandwidthRingMetThreshold:       4,
	}, &fakeBandwidthMetricQuerier{
		deviceMetrics: map[string]fakeNICMetric{
			"bandwidth/ns1/eth0/rx": {bps: bandwidthTestPressureBPS, speed: 100},
		},
	}, []machine.InterfaceInfo{newBandwidthTestNIC("ns1", "eth0")}, nil)

	plugin.syncNICState(context.Background())
	resp, err := plugin.ThresholdMet(context.Background(), &pluginapi.GetThresholdMetRequest{})
	require.NoError(t, err)
	require.Equal(t, pluginapi.ThresholdMetType_HARD_MET, resp.MetType)
	require.Equal(t, "bandwidth/ns1/eth0/rx", resp.EvictionScope)
}

func TestNICBandwidthEvictionThresholdMetParsesScopeFromHealthyNICStateKey(t *testing.T) {
	t.Parallel()

	plugin := newBandwidthTestPlugin(&eviction.NetworkEvictionConfiguration{
		EnableNICBandwidthEviction:         true,
		NICBandwidthUtilizationThreshold:   0.75,
		NICBandwidthContinuousMetThreshold: 1,
		NICBandwidthRingSize:               6,
		NICBandwidthRingMetThreshold:       4,
	}, &fakeBandwidthMetricQuerier{
		deviceMetrics: map[string]fakeNICMetric{
			"bandwidth/ns1/eth0/rx": {bps: bandwidthTestPressureBPS, speed: 100},
		},
	}, nil, newBandwidthTestCNRWithCapacity(map[string][]string{
		"ns1-eth0": nil,
	}, "100"))

	plugin.syncNICState(context.Background())
	resp, err := plugin.ThresholdMet(context.Background(), &pluginapi.GetThresholdMetRequest{})
	require.NoError(t, err)
	require.Equal(t, pluginapi.ThresholdMetType_HARD_MET, resp.MetType)
	require.Equal(t, "bandwidth/ns1/eth0/rx", resp.EvictionScope)
}

func TestNICBandwidthEvictionThresholdMetChoosesMoreSevereDirection(t *testing.T) {
	t.Parallel()

	plugin := newBandwidthTestPlugin(&eviction.NetworkEvictionConfiguration{
		EnableNICBandwidthEviction:         true,
		NICBandwidthUtilizationThreshold:   0.75,
		NICBandwidthContinuousMetThreshold: 1,
		NICBandwidthRingSize:               6,
		NICBandwidthRingMetThreshold:       4,
		NICBandwidthGracePeriod:            9,
	}, &fakeBandwidthMetricQuerier{
		deviceMetrics: map[string]fakeNICMetric{
			"bandwidth/ns1/eth0/rx": {bps: bandwidthTestPressureBPS, speed: 100},
			"bandwidth/ns1/eth0/tx": {bps: bandwidthTestPressureBPS + 1000*1000, speed: 100},
		},
	}, []machine.InterfaceInfo{newBandwidthTestNIC("ns1", "eth0")}, nil)

	plugin.syncNICState(context.Background())
	resp, err := plugin.ThresholdMet(context.Background(), &pluginapi.GetThresholdMetRequest{
		ActivePods: []*v1.Pod{newNetworkTestPod("default", "pod1", "uid1", "ns1-eth0")},
	})
	require.NoError(t, err)
	require.Equal(t, pluginapi.ThresholdMetType_HARD_MET, resp.MetType)
	require.Equal(t, "bandwidth/ns1/eth0/tx", resp.EvictionScope)
}

func TestBandwidthStateMet(t *testing.T) {
	t.Parallel()

	now := time.Now()
	state := &bandwidthState{
		ring: []bandwidthSample{
			{bps: bandwidthTestPressureBPS, capacityMbps: 100, observedAt: now.Add(-2 * time.Second)},
			{bps: bandwidthTestPressureBPS, capacityMbps: 100, observedAt: now.Add(-1 * time.Second)},
			{bps: bandwidthTestPressureBPS, capacityMbps: 100, observedAt: now},
		},
		nextIndex:   0,
		sampleCount: 3,
	}

	evaluation, met := state.met(0.75, 3, 4)
	require.True(t, met)
	require.Equal(t, 3, evaluation.currentHits)
	require.Equal(t, 3, evaluation.consecutiveHits)
}

func TestNICBandwidthEvictionThresholdMetIgnoresMissingSpeed(t *testing.T) {
	t.Parallel()

	plugin := newBandwidthTestPlugin(&eviction.NetworkEvictionConfiguration{
		EnableNICBandwidthEviction:         true,
		NICBandwidthUtilizationThreshold:   0.75,
		NICBandwidthContinuousMetThreshold: 1,
		NICBandwidthRingSize:               6,
		NICBandwidthRingMetThreshold:       4,
	}, &fakeBandwidthMetricQuerier{
		deviceMetrics: map[string]fakeNICMetric{
			"bandwidth/ns1/eth0/rx": {bps: bandwidthTestPressureBPS, speed: 0},
		},
	}, []machine.InterfaceInfo{newBandwidthTestNIC("ns1", "eth0")}, nil)

	plugin.syncNICState(context.Background())
	resp, err := plugin.ThresholdMet(context.Background(), &pluginapi.GetThresholdMetRequest{
		ActivePods: []*v1.Pod{newNetworkTestPod("default", "pod1", "uid1", "ns1-eth0")},
	})
	require.NoError(t, err)
	require.Equal(t, pluginapi.ThresholdMetType_HARD_MET, resp.MetType)
	require.Equal(t, "bandwidth/ns1/eth0/rx", resp.EvictionScope)
}

func TestNICBandwidthEvictionThresholdMetMissingDirectionalMetric(t *testing.T) {
	t.Parallel()

	plugin := newBandwidthTestPlugin(&eviction.NetworkEvictionConfiguration{
		EnableNICBandwidthEviction:         true,
		NICBandwidthUtilizationThreshold:   0.75,
		NICBandwidthContinuousMetThreshold: 1,
		NICBandwidthRingSize:               6,
		NICBandwidthRingMetThreshold:       4,
	}, &fakeBandwidthMetricQuerier{
		deviceMetrics: map[string]fakeNICMetric{
			"bandwidth/ns1/eth0/rx": {bps: bandwidthTestPressureBPS, speed: 100},
		},
	}, []machine.InterfaceInfo{newBandwidthTestNIC("ns1", "eth0")}, nil)

	plugin.syncNICState(context.Background())
	resp, err := plugin.ThresholdMet(context.Background(), &pluginapi.GetThresholdMetRequest{
		ActivePods: []*v1.Pod{newNetworkTestPod("default", "pod1", "uid1", "ns1-eth0")},
	})
	require.NoError(t, err)
	require.Equal(t, pluginapi.ThresholdMetType_HARD_MET, resp.MetType)
	require.Equal(t, "bandwidth/ns1/eth0/rx", resp.EvictionScope)
}

func TestNICBandwidthEvictionThresholdMetPerNetNSIsolation(t *testing.T) {
	t.Parallel()

	pod1 := newNetworkTestPod("default", "pod1", "uid1", "")
	pod2 := newNetworkTestPod("default", "pod2", "uid2", "")
	plugin := newBandwidthTestPlugin(&eviction.NetworkEvictionConfiguration{
		EnableNICBandwidthEviction:         true,
		NICBandwidthUtilizationThreshold:   0.75,
		NICBandwidthContinuousMetThreshold: 1,
		NICBandwidthRingSize:               6,
		NICBandwidthRingMetThreshold:       4,
	}, &fakeBandwidthMetricQuerier{
		deviceMetrics: map[string]fakeNICMetric{
			"bandwidth/ns1/eth0/rx": {bps: bandwidthTestMediumBPS, speed: 100},
			"bandwidth/ns2/eth0/rx": {bps: bandwidthTestPressureBPS, speed: 100},
		},
	}, []machine.InterfaceInfo{
		newBandwidthTestNIC("ns1", "eth0"),
		newBandwidthTestNIC("ns2", "eth0"),
	}, newBandwidthTestCNRWithCapacity(map[string][]string{
		"ns1-eth0": {native.GenerateUniqObjectUIDKey(pod1)},
		"ns2-eth0": {native.GenerateUniqObjectUIDKey(pod2)},
	}, "100"))

	plugin.syncNICState(context.Background())
	resp, err := plugin.ThresholdMet(context.Background(), &pluginapi.GetThresholdMetRequest{
		ActivePods: []*v1.Pod{pod1, pod2},
	})
	require.NoError(t, err)
	require.Equal(t, pluginapi.ThresholdMetType_HARD_MET, resp.MetType)
	require.Equal(t, "bandwidth/ns2/eth0/rx", resp.EvictionScope)
}

func TestNICBandwidthEvictionThresholdMetUsesProductionMetricIdentifier(t *testing.T) {
	t.Parallel()

	now := time.Now()
	metricsFetcher := metametric.NewFakeMetricsFetcher(metrics.DummyMetrics{}).(*metametric.FakeMetricsFetcher)
	metricsFetcher.SetNSNetworkMetric("ns1", "eth0", coreconsts.MetricNetReceiveBPS, utilmetric.MetricData{Value: bandwidthTestPressureBPS, Time: &now})

	plugin := newBandwidthInjectedTestPlugin(&eviction.NetworkEvictionConfiguration{
		EnableNICBandwidthEviction:         true,
		NICBandwidthUtilizationThreshold:   0.75,
		NICBandwidthContinuousMetThreshold: 1,
		NICBandwidthRingSize:               6,
		NICBandwidthRingMetThreshold:       4,
	}, metricsFetcher, []machine.InterfaceInfo{newBandwidthTestNIC("ns1", "eth0")}, nil)

	plugin.syncNICState(context.Background())
	resp, err := plugin.ThresholdMet(context.Background(), &pluginapi.GetThresholdMetRequest{
		ActivePods: []*v1.Pod{newNetworkTestPod("default", "pod1", "uid1", "ns1-eth0")},
	})
	require.NoError(t, err)
	require.Equal(t, pluginapi.ThresholdMetType_HARD_MET, resp.MetType)
	require.Equal(t, "bandwidth/ns1/eth0/rx", resp.EvictionScope)
}

func TestNICBandwidthEvictionThresholdMetUsesCNRCapacity(t *testing.T) {
	t.Parallel()

	now := time.Now()
	pod := newNetworkTestPod("default", "pod1", "uid1", "")
	metricsFetcher := metametric.NewFakeMetricsFetcher(metrics.DummyMetrics{}).(*metametric.FakeMetricsFetcher)
	metricsFetcher.SetNSNetworkMetric("ns1", "eth0", coreconsts.MetricNetReceiveBPS, utilmetric.MetricData{Value: bandwidthTestPressureBPS, Time: &now})

	plugin := newBandwidthInjectedTestPlugin(&eviction.NetworkEvictionConfiguration{
		EnableNICBandwidthEviction:         true,
		NICBandwidthUtilizationThreshold:   0.75,
		NICBandwidthContinuousMetThreshold: 1,
		NICBandwidthRingSize:               6,
		NICBandwidthRingMetThreshold:       4,
	}, metricsFetcher, []machine.InterfaceInfo{newBandwidthTestNIC("ns1", "eth0")}, newBandwidthTestCNRWithCapacity(map[string][]string{
		"ns1-eth0": {native.GenerateUniqObjectUIDKey(pod)},
	}, "100"))

	plugin.syncNICState(context.Background())
	resp, err := plugin.ThresholdMet(context.Background(), &pluginapi.GetThresholdMetRequest{
		ActivePods: []*v1.Pod{pod},
	})
	require.NoError(t, err)
	require.Equal(t, pluginapi.ThresholdMetType_HARD_MET, resp.MetType)
	require.Equal(t, "bandwidth/ns1/eth0/rx", resp.EvictionScope)
}

func TestNICBandwidthEvictionThresholdMetConvertsCNRMbpsToBytesPerSecond(t *testing.T) {
	t.Parallel()

	now := time.Now()
	pod := newNetworkTestPod("default", "pod1", "uid1", "")
	metricsFetcher := metametric.NewFakeMetricsFetcher(metrics.DummyMetrics{}).(*metametric.FakeMetricsFetcher)
	metricsFetcher.SetNSNetworkMetric("ns1", "eth0", coreconsts.MetricNetReceiveBPS, utilmetric.MetricData{Value: bandwidthTestLowBPS, Time: &now})

	plugin := newBandwidthInjectedTestPlugin(&eviction.NetworkEvictionConfiguration{
		EnableNICBandwidthEviction:         true,
		NICBandwidthUtilizationThreshold:   0.75,
		NICBandwidthContinuousMetThreshold: 1,
		NICBandwidthRingSize:               6,
		NICBandwidthRingMetThreshold:       4,
	}, metricsFetcher, []machine.InterfaceInfo{newBandwidthTestNIC("ns1", "eth0")}, newBandwidthTestCNRWithCapacity(map[string][]string{
		"ns1-eth0": {native.GenerateUniqObjectUIDKey(pod)},
	}, "100"))

	plugin.syncNICState(context.Background())
	resp, err := plugin.ThresholdMet(context.Background(), &pluginapi.GetThresholdMetRequest{
		ActivePods: []*v1.Pod{pod},
	})
	require.NoError(t, err)
	require.Equal(t, pluginapi.ThresholdMetType_NOT_MET, resp.MetType)
}

func TestNICBandwidthEvictionThresholdMetSkipsUnhealthyCNRNIC(t *testing.T) {
	t.Parallel()

	now := time.Now()
	pod := newNetworkTestPod("default", "pod1", "uid1", "")
	metricsFetcher := metametric.NewFakeMetricsFetcher(metrics.DummyMetrics{}).(*metametric.FakeMetricsFetcher)
	metricsFetcher.SetNSNetworkMetric("ns1", "eth0", coreconsts.MetricNetReceiveBPS, utilmetric.MetricData{Value: bandwidthTestPressureBPS, Time: &now})

	plugin := newBandwidthInjectedTestPlugin(&eviction.NetworkEvictionConfiguration{
		EnableNICBandwidthEviction:         true,
		NICBandwidthUtilizationThreshold:   0.75,
		NICBandwidthContinuousMetThreshold: 1,
		NICBandwidthRingSize:               6,
		NICBandwidthRingMetThreshold:       4,
	}, metricsFetcher, []machine.InterfaceInfo{newBandwidthTestNIC("ns1", "eth0")}, newBandwidthTestCNRWithCapacity(map[string][]string{
		"ns1-eth0": {native.GenerateUniqObjectUIDKey(pod)},
	}, "0"))

	plugin.syncNICState(context.Background())
	resp, err := plugin.ThresholdMet(context.Background(), &pluginapi.GetThresholdMetRequest{
		ActivePods: []*v1.Pod{pod},
	})
	require.NoError(t, err)
	require.Equal(t, pluginapi.ThresholdMetType_NOT_MET, resp.MetType)
}

func TestNICBandwidthEvictionThresholdMetLatestFourOfSixSamples(t *testing.T) {
	t.Parallel()

	querier := &fakeBandwidthMetricQuerier{
		deviceMetrics: map[string]fakeNICMetric{
			"bandwidth/ns1/eth0/rx": {bps: bandwidthTestLowBPS, speed: 100},
		},
	}
	plugin := newBandwidthTestPlugin(&eviction.NetworkEvictionConfiguration{
		EnableNICBandwidthEviction:         true,
		NICBandwidthUtilizationThreshold:   0.75,
		NICBandwidthContinuousMetThreshold: 5,
		NICBandwidthRingSize:               6,
		NICBandwidthRingMetThreshold:       4,
	}, querier, []machine.InterfaceInfo{newBandwidthTestNIC("ns1", "eth0")}, nil)

	samples := []float64{bandwidthTestPressureBPS, bandwidthTestLowBPS, bandwidthTestPressureBPS, bandwidthTestLowBPS, bandwidthTestPressureBPS, bandwidthTestPressureBPS}
	for i, sample := range samples {
		querier.deviceMetrics["bandwidth/ns1/eth0/rx"] = fakeNICMetric{bps: sample, speed: 100}
		plugin.syncNICState(context.Background())
		resp, err := plugin.ThresholdMet(context.Background(), &pluginapi.GetThresholdMetRequest{
			ActivePods: []*v1.Pod{newNetworkTestPod("default", "pod1", "uid1", "ns1-eth0")},
		})
		require.NoError(t, err)
		if i < len(samples)-1 {
			require.Equal(t, pluginapi.ThresholdMetType_NOT_MET, resp.MetType)
			continue
		}
		require.Equal(t, pluginapi.ThresholdMetType_HARD_MET, resp.MetType)
		require.Equal(t, "bandwidth/ns1/eth0/rx", resp.EvictionScope)
	}
}

func TestNICBandwidthEvictionThresholdMetRecalculatesHistoryAfterThresholdChange(t *testing.T) {
	t.Parallel()

	conf := &eviction.NetworkEvictionConfiguration{
		EnableNICBandwidthEviction:         true,
		NICBandwidthUtilizationThreshold:   0.75,
		NICBandwidthContinuousMetThreshold: 3,
		NICBandwidthRingSize:               6,
		NICBandwidthRingMetThreshold:       4,
	}
	plugin := newBandwidthTestPlugin(conf, &fakeBandwidthMetricQuerier{
		deviceMetrics: map[string]fakeNICMetric{
			"bandwidth/ns1/eth0/rx": {bps: 9 * 1000 * 1000, speed: 100},
		},
	}, []machine.InterfaceInfo{newBandwidthTestNIC("ns1", "eth0")}, nil)

	for i := 0; i < 2; i++ {
		plugin.syncNICState(context.Background())
		resp, err := plugin.ThresholdMet(context.Background(), &pluginapi.GetThresholdMetRequest{})
		require.NoError(t, err)
		require.Equal(t, pluginapi.ThresholdMetType_NOT_MET, resp.MetType)
	}

	conf.NICBandwidthUtilizationThreshold = 0.7
	plugin.syncNICState(context.Background())
	resp, err := plugin.ThresholdMet(context.Background(), &pluginapi.GetThresholdMetRequest{})
	require.NoError(t, err)
	require.Equal(t, pluginapi.ThresholdMetType_HARD_MET, resp.MetType)
	require.Equal(t, "bandwidth/ns1/eth0/rx", resp.EvictionScope)
}

func TestNICBandwidthEvictionThresholdMetUsesSampledCapacityAfterCapacityChange(t *testing.T) {
	t.Parallel()

	querier := &fakeBandwidthMetricQuerier{
		deviceMetrics: map[string]fakeNICMetric{
			"bandwidth/ns1/eth0/rx": {bps: bandwidthTestPressureBPS, speed: 100},
		},
	}
	plugin := newBandwidthTestPlugin(&eviction.NetworkEvictionConfiguration{
		EnableNICBandwidthEviction:         true,
		NICBandwidthUtilizationThreshold:   0.75,
		NICBandwidthContinuousMetThreshold: 3,
		NICBandwidthRingSize:               6,
		NICBandwidthRingMetThreshold:       4,
	}, querier, []machine.InterfaceInfo{newBandwidthTestNIC("ns1", "eth0")}, newBandwidthTestCNRWithCapacity(map[string][]string{
		"ns1-eth0": nil,
	}, "200"))

	for i := 0; i < 2; i++ {
		plugin.syncNICState(context.Background())
		resp, err := plugin.ThresholdMet(context.Background(), &pluginapi.GetThresholdMetRequest{})
		require.NoError(t, err)
		require.Equal(t, pluginapi.ThresholdMetType_NOT_MET, resp.MetType)
	}

	plugin.metaServer.MetaAgent.CNRFetcher.(*cnr.CNRFetcherStub).CNR = newBandwidthTestCNRWithCapacity(map[string][]string{
		"ns1-eth0": nil,
	}, "100")
	plugin.syncNICState(context.Background())

	plugin.syncNICState(context.Background())
	resp, err := plugin.ThresholdMet(context.Background(), &pluginapi.GetThresholdMetRequest{})
	require.NoError(t, err)
	require.Equal(t, pluginapi.ThresholdMetType_NOT_MET, resp.MetType)
}

func TestNICBandwidthEvictionThresholdMetKeepsHistoryWithoutActivePods(t *testing.T) {
	t.Parallel()

	plugin := newBandwidthTestPlugin(&eviction.NetworkEvictionConfiguration{
		EnableNICBandwidthEviction:         true,
		NICBandwidthUtilizationThreshold:   0.75,
		NICBandwidthContinuousMetThreshold: 3,
		NICBandwidthRingSize:               6,
		NICBandwidthRingMetThreshold:       4,
	}, &fakeBandwidthMetricQuerier{
		deviceMetrics: map[string]fakeNICMetric{
			"bandwidth/ns1/eth0/rx": {bps: bandwidthTestPressureBPS, speed: 100},
		},
	}, []machine.InterfaceInfo{newBandwidthTestNIC("ns1", "eth0")}, nil)

	pod1 := newNetworkTestPod("default", "pod1", "uid1", "ns1-eth0")
	for i := 0; i < 2; i++ {
		plugin.syncNICState(context.Background())
		resp, err := plugin.ThresholdMet(context.Background(), &pluginapi.GetThresholdMetRequest{
			ActivePods: []*v1.Pod{pod1},
		})
		require.NoError(t, err)
		require.Equal(t, pluginapi.ThresholdMetType_NOT_MET, resp.MetType)
	}

	plugin.syncNICState(context.Background())
	resp, err := plugin.ThresholdMet(context.Background(), &pluginapi.GetThresholdMetRequest{})
	require.NoError(t, err)
	require.Equal(t, pluginapi.ThresholdMetType_HARD_MET, resp.MetType)

	pod2 := newNetworkTestPod("default", "pod2", "uid2", "ns1-eth0")
	plugin.syncNICState(context.Background())
	resp, err = plugin.ThresholdMet(context.Background(), &pluginapi.GetThresholdMetRequest{
		ActivePods: []*v1.Pod{pod2},
	})
	require.NoError(t, err)
	require.Equal(t, pluginapi.ThresholdMetType_HARD_MET, resp.MetType)
	state := plugin.healthyNICState[formatBandwidthScope(bandwidthScope{netns: "ns1", nic: "eth0", direction: bandwidthDirectionRX})]
	require.NotNil(t, state)
	evaluation := state.evaluate(0.75)
	require.Equal(t, 4, evaluation.consecutiveHits)
}

func TestNICBandwidthEvictionThresholdMetKeepsHistoryAfterPodSwitch(t *testing.T) {
	t.Parallel()

	plugin := newBandwidthTestPlugin(&eviction.NetworkEvictionConfiguration{
		EnableNICBandwidthEviction:         true,
		NICBandwidthUtilizationThreshold:   0.75,
		NICBandwidthContinuousMetThreshold: 3,
		NICBandwidthRingSize:               6,
		NICBandwidthRingMetThreshold:       4,
	}, &fakeBandwidthMetricQuerier{
		deviceMetrics: map[string]fakeNICMetric{
			"bandwidth/ns1/eth0/rx": {bps: bandwidthTestPressureBPS, speed: 100},
		},
	}, []machine.InterfaceInfo{newBandwidthTestNIC("ns1", "eth0")}, nil)

	pod1 := newNetworkTestPod("default", "pod1", "uid1", "ns1-eth0")
	for i := 0; i < 2; i++ {
		plugin.syncNICState(context.Background())
		resp, err := plugin.ThresholdMet(context.Background(), &pluginapi.GetThresholdMetRequest{
			ActivePods: []*v1.Pod{pod1},
		})
		require.NoError(t, err)
		require.Equal(t, pluginapi.ThresholdMetType_NOT_MET, resp.MetType)
	}

	pod2 := newNetworkTestPod("default", "pod2", "uid2", "ns1-eth0")
	plugin.syncNICState(context.Background())
	resp, err := plugin.ThresholdMet(context.Background(), &pluginapi.GetThresholdMetRequest{
		ActivePods: []*v1.Pod{pod2},
	})
	require.NoError(t, err)
	require.Equal(t, pluginapi.ThresholdMetType_HARD_MET, resp.MetType)
	state := plugin.healthyNICState[formatBandwidthScope(bandwidthScope{netns: "ns1", nic: "eth0", direction: bandwidthDirectionRX})]
	require.NotNil(t, state)
	evaluation := state.evaluate(0.75)
	require.Equal(t, 3, evaluation.consecutiveHits)
}

func TestNICBandwidthEvictionThresholdMetResetsExpiredState(t *testing.T) {
	t.Parallel()

	plugin := newBandwidthTestPlugin(&eviction.NetworkEvictionConfiguration{
		EnableNICBandwidthEviction:         true,
		NICBandwidthUtilizationThreshold:   0.75,
		NICBandwidthContinuousMetThreshold: 3,
		NICBandwidthRingSize:               6,
		NICBandwidthRingMetThreshold:       4,
	}, &fakeBandwidthMetricQuerier{
		deviceMetrics: map[string]fakeNICMetric{
			"bandwidth/ns1/eth0/rx": {bps: bandwidthTestPressureBPS, speed: 100},
		},
	}, []machine.InterfaceInfo{newBandwidthTestNIC("ns1", "eth0")}, nil)
	oldObservedAt := time.Now().Add(-10 * time.Minute)
	plugin.healthyNICState[formatBandwidthScope(bandwidthScope{netns: "ns1", nic: "eth0", direction: bandwidthDirectionRX})] = &bandwidthState{
		ring: []bandwidthSample{
			{bps: bandwidthTestPressureBPS, capacityMbps: 100, observedAt: oldObservedAt},
			{bps: bandwidthTestPressureBPS, capacityMbps: 100, observedAt: oldObservedAt},
			{bps: bandwidthTestPressureBPS, capacityMbps: 100, observedAt: oldObservedAt},
			{bps: bandwidthTestPressureBPS, capacityMbps: 100, observedAt: oldObservedAt},
			{},
			{},
		},
		nextIndex:   4,
		sampleCount: 4,
	}

	plugin.syncNICState(context.Background())
	resp, err := plugin.ThresholdMet(context.Background(), &pluginapi.GetThresholdMetRequest{
		ActivePods: []*v1.Pod{newNetworkTestPod("default", "pod1", "uid1", "ns1-eth0")},
	})
	require.NoError(t, err)
	require.Equal(t, pluginapi.ThresholdMetType_NOT_MET, resp.MetType)
	state := plugin.healthyNICState[formatBandwidthScope(bandwidthScope{netns: "ns1", nic: "eth0", direction: bandwidthDirectionRX})]
	require.NotNil(t, state)
	evaluation := state.evaluate(0.75)
	require.Equal(t, 1, evaluation.currentHits)
	require.Equal(t, 1, evaluation.consecutiveHits)
}

func TestNICBandwidthEvictionThresholdMetKeepsHistoryWhenMetricTimeWithinRetention(t *testing.T) {
	t.Parallel()

	oldObservedAt := time.Now().Add(-4 * time.Minute)
	currentMetricTime := time.Now().Add(-1 * time.Minute)
	querier := &fakeBandwidthMetricQuerier{
		deviceMetrics: map[string]fakeNICMetric{
			"bandwidth/ns1/eth0/rx": {bps: bandwidthTestPressureBPS, speed: 100, observedAt: currentMetricTime},
		},
	}
	plugin := newBandwidthTestPlugin(&eviction.NetworkEvictionConfiguration{
		EnableNICBandwidthEviction:         true,
		NICBandwidthUtilizationThreshold:   0.75,
		NICBandwidthContinuousMetThreshold: 3,
		NICBandwidthRingSize:               6,
		NICBandwidthRingMetThreshold:       4,
	}, querier, []machine.InterfaceInfo{newBandwidthTestNIC("ns1", "eth0")}, nil)
	plugin.healthyNICState[formatBandwidthScope(bandwidthScope{netns: "ns1", nic: "eth0", direction: bandwidthDirectionRX})] = &bandwidthState{
		ring: []bandwidthSample{
			{bps: bandwidthTestPressureBPS, capacityMbps: 100, observedAt: oldObservedAt},
			{},
			{},
			{},
			{},
			{},
		},
		nextIndex:   1,
		sampleCount: 1,
	}

	plugin.syncNICState(context.Background())
	state := plugin.healthyNICState[formatBandwidthScope(bandwidthScope{netns: "ns1", nic: "eth0", direction: bandwidthDirectionRX})]
	require.NotNil(t, state)
	evaluation := state.evaluate(0.75)
	require.Equal(t, 2, evaluation.currentHits)
	require.Equal(t, 2, evaluation.consecutiveHits)
}

func TestNICBandwidthEvictionThresholdMetPrunesStaleState(t *testing.T) {
	t.Parallel()

	plugin := newBandwidthTestPlugin(&eviction.NetworkEvictionConfiguration{
		EnableNICBandwidthEviction:         true,
		NICBandwidthUtilizationThreshold:   0.75,
		NICBandwidthContinuousMetThreshold: 1,
		NICBandwidthRingSize:               6,
		NICBandwidthRingMetThreshold:       4,
	}, &fakeBandwidthMetricQuerier{
		deviceMetrics: map[string]fakeNICMetric{
			"bandwidth/ns1/eth0/rx": {bps: bandwidthTestPressureBPS, speed: 100},
		},
	}, []machine.InterfaceInfo{newBandwidthTestNIC("ns1", "eth0")}, nil)
	staleMetricTime := time.Now().Add(-10 * time.Minute)
	plugin.healthyNICState[formatBandwidthScope(bandwidthScope{netns: "stale", nic: "eth0", direction: bandwidthDirectionRX})] = &bandwidthState{
		ring: []bandwidthSample{{
			bps:          bandwidthTestPressureBPS,
			capacityMbps: 100,
			observedAt:   staleMetricTime,
		}, {}, {}, {}, {}, {}},
		sampleCount: 1,
	}

	plugin.syncNICState(context.Background())
	resp, err := plugin.ThresholdMet(context.Background(), &pluginapi.GetThresholdMetRequest{
		ActivePods: []*v1.Pod{newNetworkTestPod("default", "pod1", "uid1", "ns1-eth0")},
	})
	require.NoError(t, err)
	require.Equal(t, pluginapi.ThresholdMetType_HARD_MET, resp.MetType)
	require.NotContains(t, plugin.healthyNICState, formatBandwidthScope(bandwidthScope{netns: "stale", nic: "eth0", direction: bandwidthDirectionRX}))
}

func TestNICBandwidthEvictionThresholdMetUsesMetricsCollectedBySyncNICState(t *testing.T) {
	t.Parallel()

	querier := &fakeBandwidthMetricQuerier{
		deviceMetrics: map[string]fakeNICMetric{
			"bandwidth/ns1/eth0/rx": {bps: bandwidthTestPressureBPS, speed: 100},
			"bandwidth/ns1/eth0/tx": {bps: 0, speed: 100},
		},
	}

	plugin := newBandwidthTestPlugin(&eviction.NetworkEvictionConfiguration{
		EnableNICBandwidthEviction:         true,
		NICBandwidthUtilizationThreshold:   0.75,
		NICBandwidthContinuousMetThreshold: 1,
		NICBandwidthRingSize:               6,
		NICBandwidthRingMetThreshold:       4,
	}, querier, []machine.InterfaceInfo{newBandwidthTestNIC("ns1", "eth0")}, nil)
	querier.bandwidthMetricCallCount = 0
	plugin.healthyNICState = map[string]*bandwidthState{}

	plugin.syncNICState(context.Background())
	require.Equal(t, 2, querier.bandwidthMetricCallCount)

	scope := bandwidthScope{netns: "ns1", nic: "eth0", direction: bandwidthDirectionRX}
	state := plugin.healthyNICState[formatBandwidthScope(scope)]
	require.NotNil(t, state)
	require.Equal(t, 1, state.sampleCount)
	latest, ok := state.latestSample()
	require.True(t, ok)
	require.Equal(t, querier.deviceMetrics[formatBandwidthScope(scope)].observedAt, latest.observedAt)

	querier.deviceMetrics["bandwidth/ns1/eth0/rx"] = fakeNICMetric{bps: 0, speed: 100}
	beforeThresholdCall := querier.bandwidthMetricCallCount

	resp, err := plugin.ThresholdMet(context.Background(), &pluginapi.GetThresholdMetRequest{})
	require.NoError(t, err)
	require.Equal(t, pluginapi.ThresholdMetType_HARD_MET, resp.MetType)
	require.Equal(t, beforeThresholdCall, querier.bandwidthMetricCallCount)
}

func TestNICHealthEvictionGetEvictPodsDoesNotTouchBandwidthMetrics(t *testing.T) {
	t.Parallel()

	pod := newNetworkTestPod("default", "pod1", "uid1", "ns1-eth0")
	pod.Spec.Containers = []v1.Container{{Name: "c1"}}
	pod2 := newNetworkTestPod("default", "pod2", "uid2", "ns1-eth0")
	pod2.Spec.Containers = []v1.Container{{Name: "c1"}}
	querier := &fakeBandwidthMetricQuerier{
		deviceMetrics: map[string]fakeNICMetric{
			"bandwidth/ns1/eth0/rx": {bps: bandwidthTestPressureBPS, speed: 100},
		},
		containerDirectionUsage: map[string]map[string]map[string]float64{
			string(pod.UID): {
				"c1": {bandwidthDirectionRX: 90},
			},
			string(pod2.UID): {
				"c1": {bandwidthDirectionRX: 60},
			},
		},
	}

	plugin := newBandwidthTestPlugin(&eviction.NetworkEvictionConfiguration{
		EnableNICHealthEviction:            false,
		EnableNICBandwidthEviction:         true,
		NICBandwidthUtilizationThreshold:   0.75,
		NICBandwidthContinuousMetThreshold: 1,
		NICBandwidthRingSize:               6,
		NICBandwidthRingMetThreshold:       4,
		NICBandwidthGracePeriod:            11,
	}, querier, []machine.InterfaceInfo{newBandwidthTestNIC("ns1", "eth0")}, nil)

	getEvictResp, err := plugin.GetEvictPods(context.Background(), &pluginapi.GetEvictPodsRequest{
		ActivePods: []*v1.Pod{pod},
	})
	require.NoError(t, err)
	require.Empty(t, getEvictResp.EvictPods)
	require.Zero(t, querier.bandwidthMetricCallCount)
	require.Empty(t, querier.directionUsageCalls)
}

func TestNICBandwidthEvictionCoexistenceHealthMissBandwidthHit(t *testing.T) {
	t.Parallel()

	pod := newNetworkTestPod("default", "pod1", "uid1", "ns1-eth0")
	pod.Spec.Containers = []v1.Container{{Name: "c1"}}
	pod2 := newNetworkTestPod("default", "pod2", "uid2", "ns1-eth0")
	pod2.Spec.Containers = []v1.Container{{Name: "c1"}}
	querier := &fakeBandwidthMetricQuerier{
		deviceMetrics: map[string]fakeNICMetric{
			"bandwidth/ns1/eth0/rx": {bps: bandwidthTestPressureBPS, speed: 100},
		},
		containerDirectionUsage: map[string]map[string]map[string]float64{
			string(pod.UID): {
				"c1": {bandwidthDirectionRX: 90},
			},
			string(pod2.UID): {
				"c1": {bandwidthDirectionRX: 60},
			},
		},
	}

	requests := v1.ResourceList{
		apiconsts.ResourceNetBandwidth: resource.MustParse("10G"),
	}
	plugin := newBandwidthTestPlugin(&eviction.NetworkEvictionConfiguration{
		EnableNICHealthEviction:            true,
		EnableNICBandwidthEviction:         true,
		NICUnhealthyToleranceDuration:      time.Minute,
		NICBandwidthUtilizationThreshold:   0.75,
		NICBandwidthContinuousMetThreshold: 1,
		NICBandwidthRingSize:               6,
		NICBandwidthRingMetThreshold:       4,
		NICBandwidthGracePeriod:            11,
	}, querier, []machine.InterfaceInfo{newBandwidthTestNIC("ns1", "eth0")}, nil)
	plugin.unhealthyNICState = map[string]*unhealthyNICState{
		"eth0": {
			lastUnhealthyTime: time.Now(),
			nicZone: v1alpha1.TopologyZone{
				Name: "eth0",
				Type: v1alpha1.TopologyTypeNIC,
				Allocations: []*v1alpha1.Allocation{{
					Consumer: native.GenerateUniqObjectUIDKey(pod),
					Requests: &requests,
				}},
			},
		},
	}

	getEvictResp, err := plugin.GetEvictPods(context.Background(), &pluginapi.GetEvictPodsRequest{
		ActivePods: []*v1.Pod{pod},
	})
	require.NoError(t, err)
	require.Empty(t, getEvictResp.EvictPods)
	require.Zero(t, querier.bandwidthMetricCallCount)
	require.Empty(t, querier.directionUsageCalls)

	plugin.syncNICState(context.Background())
	thresholdResp, err := plugin.ThresholdMet(context.Background(), &pluginapi.GetThresholdMetRequest{
		ActivePods: []*v1.Pod{pod},
	})
	require.NoError(t, err)
	require.Equal(t, pluginapi.ThresholdMetType_HARD_MET, thresholdResp.MetType)
	require.Equal(t, "bandwidth/ns1/eth0/rx", thresholdResp.EvictionScope)
	require.Zero(t, thresholdResp.GracePeriodSeconds)

	topResp, err := plugin.GetTopEvictionPods(context.Background(), &pluginapi.GetTopEvictionPodsRequest{
		ActivePods:    []*v1.Pod{pod, pod2},
		TopN:          1,
		EvictionScope: thresholdResp.EvictionScope,
	})
	require.NoError(t, err)
	require.Len(t, topResp.TargetPods, 1)
	require.Equal(t, "pod1", topResp.TargetPods[0].Name)
	require.NotNil(t, topResp.DeletionOptions)
	require.EqualValues(t, 11, topResp.DeletionOptions.GracePeriodSeconds)
}

func TestNICBandwidthEvictionCoexistenceDefaultNetNSScope(t *testing.T) {
	t.Parallel()

	pod := newNetworkTestPod("default", "pod1", "uid1", "eth0")
	pod.Spec.Containers = []v1.Container{{Name: "c1"}}
	pod2 := newNetworkTestPod("default", "pod2", "uid2", "")
	pod2.Spec.Containers = []v1.Container{{Name: "c1"}}
	querier := &fakeBandwidthMetricQuerier{
		deviceMetrics: map[string]fakeNICMetric{
			"bandwidth//eth0/rx": {bps: bandwidthTestPressureBPS, speed: 100},
		},
		containerDirectionUsage: map[string]map[string]map[string]float64{
			string(pod.UID): {
				"c1": {bandwidthDirectionRX: 90},
			},
			string(pod2.UID): {
				"c1": {bandwidthDirectionRX: 40},
			},
		},
	}

	plugin := newBandwidthTestPlugin(&eviction.NetworkEvictionConfiguration{
		EnableNICBandwidthEviction:         true,
		NICBandwidthUtilizationThreshold:   0.75,
		NICBandwidthContinuousMetThreshold: 1,
		NICBandwidthRingSize:               6,
		NICBandwidthRingMetThreshold:       4,
		NICBandwidthGracePeriod:            3,
	}, querier, []machine.InterfaceInfo{newBandwidthTestNIC(machine.DefaultNICNamespace, "eth0")}, nil)

	plugin.syncNICState(context.Background())
	thresholdResp, err := plugin.ThresholdMet(context.Background(), &pluginapi.GetThresholdMetRequest{
		ActivePods: []*v1.Pod{pod},
	})
	require.NoError(t, err)
	require.Equal(t, pluginapi.ThresholdMetType_HARD_MET, thresholdResp.MetType)
	require.Equal(t, "bandwidth//eth0/rx", thresholdResp.EvictionScope)

	topResp, err := plugin.GetTopEvictionPods(context.Background(), &pluginapi.GetTopEvictionPodsRequest{
		ActivePods:    []*v1.Pod{pod, pod2},
		TopN:          1,
		EvictionScope: thresholdResp.EvictionScope,
	})
	require.NoError(t, err)
	require.Len(t, topResp.TargetPods, 1)
	require.Equal(t, "pod1", topResp.TargetPods[0].Name)
	require.NotNil(t, topResp.DeletionOptions)
	require.EqualValues(t, 3, topResp.DeletionOptions.GracePeriodSeconds)
}

func TestNICBandwidthEvictionGetTopEvictionPodsAggregatesByPod(t *testing.T) {
	t.Parallel()

	pod1 := newNetworkTestPod("default", "pod1", "uid1", "ns1-eth0")
	pod1.Spec.Containers = []v1.Container{{Name: "c1"}, {Name: "c2"}}
	pod2 := newNetworkTestPod("default", "pod2", "uid2", "ns1-eth0")
	pod2.Spec.Containers = []v1.Container{{Name: "c1"}}
	pod3 := newNetworkTestPod("default", "pod3", "uid3", "ns1-eth0")
	pod3.Spec.Containers = []v1.Container{{Name: "c1"}}

	plugin := newBandwidthTestPlugin(&eviction.NetworkEvictionConfiguration{
		EnableNICBandwidthEviction: true,
		NICBandwidthGracePeriod:    7,
	}, &fakeBandwidthMetricQuerier{
		containerDirectionUsage: map[string]map[string]map[string]float64{
			string(pod1.UID): {
				"c1": {bandwidthDirectionRX: 70, bandwidthDirectionTX: 10},
				"c2": {bandwidthDirectionRX: 50, bandwidthDirectionTX: 15},
			},
			string(pod2.UID): {
				"c1": {bandwidthDirectionRX: 100, bandwidthDirectionTX: 80},
			},
			string(pod3.UID): {
				"c1": {bandwidthDirectionRX: 40, bandwidthDirectionTX: 120},
			},
		},
	}, []machine.InterfaceInfo{newBandwidthTestNIC("ns1", "eth0")}, nil)

	resp, err := plugin.GetTopEvictionPods(context.Background(), &pluginapi.GetTopEvictionPodsRequest{
		ActivePods:    []*v1.Pod{pod1, pod2, pod3},
		TopN:          2,
		EvictionScope: "bandwidth/ns1/eth0/rx",
	})
	require.NoError(t, err)
	require.Len(t, resp.TargetPods, 2)
	require.Equal(t, "pod1", resp.TargetPods[0].Name)
	require.Equal(t, "pod2", resp.TargetPods[1].Name)
	require.NotNil(t, resp.DeletionOptions)
	require.EqualValues(t, 7, resp.DeletionOptions.GracePeriodSeconds)
}

func TestNICBandwidthEvictionGetTopEvictionPodsAssignsPodWithoutAnnotationToDefaultNS(t *testing.T) {
	t.Parallel()

	defaultPod := newNetworkTestPod("default", "default-pod", "uid1", "")
	defaultPod.Spec.Containers = []v1.Container{{Name: "c1"}}
	defaultPod2 := newNetworkTestPod("default", "default-pod-2", "uid3", "")
	defaultPod2.Spec.Containers = []v1.Container{{Name: "c1"}}
	nsPod := newNetworkTestPod("default", "ns-pod", "uid2", "ns1-eth0")
	nsPod.Spec.Containers = []v1.Container{{Name: "c1"}}
	nsPod2 := newNetworkTestPod("default", "ns-pod-2", "uid4", "ns1-eth0")
	nsPod2.Spec.Containers = []v1.Container{{Name: "c1"}}

	plugin := newBandwidthTestPlugin(&eviction.NetworkEvictionConfiguration{
		EnableNICBandwidthEviction: true,
	}, &fakeBandwidthMetricQuerier{
		containerDirectionUsage: map[string]map[string]map[string]float64{
			string(defaultPod.UID): {
				"c1": {bandwidthDirectionRX: 120},
			},
			string(defaultPod2.UID): {
				"c1": {bandwidthDirectionRX: 60},
			},
			string(nsPod.UID): {
				"c1": {bandwidthDirectionRX: 200},
			},
			string(nsPod2.UID): {
				"c1": {bandwidthDirectionRX: 80},
			},
		},
	}, []machine.InterfaceInfo{
		newBandwidthTestNIC(machine.DefaultNICNamespace, "eth0"),
		newBandwidthTestNIC("ns1", "eth0"),
	}, nil)

	defaultResp, err := plugin.GetTopEvictionPods(context.Background(), &pluginapi.GetTopEvictionPodsRequest{
		ActivePods:    []*v1.Pod{defaultPod, defaultPod2, nsPod, nsPod2},
		TopN:          2,
		EvictionScope: "bandwidth//eth0/rx",
	})
	require.NoError(t, err)
	require.Len(t, defaultResp.TargetPods, 2)
	require.Equal(t, "default-pod", defaultResp.TargetPods[0].Name)
	require.Equal(t, "default-pod-2", defaultResp.TargetPods[1].Name)

	nsResp, err := plugin.GetTopEvictionPods(context.Background(), &pluginapi.GetTopEvictionPodsRequest{
		ActivePods:    []*v1.Pod{defaultPod, defaultPod2, nsPod, nsPod2},
		TopN:          2,
		EvictionScope: "bandwidth/ns1/eth0/rx",
	})
	require.NoError(t, err)
	require.Len(t, nsResp.TargetPods, 2)
	require.Equal(t, "ns-pod", nsResp.TargetPods[0].Name)
	require.Equal(t, "ns-pod-2", nsResp.TargetPods[1].Name)
}

func TestNICBandwidthEvictionGetTopEvictionPodsSortsBySaleModeBeforeUsage(t *testing.T) {
	t.Parallel()

	spotPod := newNetworkTestPod("default", "spot-pod", "uid1", "ns1-eth0")
	spotPod.Spec.Containers = []v1.Container{{Name: "c1"}}
	spotPod.Annotations[apiconsts.PodAnnotationSaleModeKey] = apiconsts.PodSaleModeSpot
	scheduledPod := newNetworkTestPod("default", "scheduled-pod", "uid2", "ns1-eth0")
	scheduledPod.Spec.Containers = []v1.Container{{Name: "c1"}}
	scheduledPod.Annotations[apiconsts.PodAnnotationSaleModeKey] = apiconsts.PodSaleModeScheduled
	reservedPod := newNetworkTestPod("default", "reserved-pod", "uid3", "ns1-eth0")
	reservedPod.Spec.Containers = []v1.Container{{Name: "c1"}}
	reservedPod.Annotations[apiconsts.PodAnnotationSaleModeKey] = apiconsts.PodSaleModeReserved

	plugin := newBandwidthTestPlugin(&eviction.NetworkEvictionConfiguration{
		EnableNICBandwidthEviction: true,
	}, &fakeBandwidthMetricQuerier{
		containerDirectionUsage: map[string]map[string]map[string]float64{
			string(spotPod.UID): {
				"c1": {bandwidthDirectionRX: 50},
			},
			string(scheduledPod.UID): {
				"c1": {bandwidthDirectionRX: 500},
			},
			string(reservedPod.UID): {
				"c1": {bandwidthDirectionRX: 1000},
			},
		},
	}, []machine.InterfaceInfo{newBandwidthTestNIC("ns1", "eth0")}, nil)

	resp, err := plugin.GetTopEvictionPods(context.Background(), &pluginapi.GetTopEvictionPodsRequest{
		ActivePods:    []*v1.Pod{reservedPod, scheduledPod, spotPod},
		TopN:          2,
		EvictionScope: "bandwidth/ns1/eth0/rx",
	})
	require.NoError(t, err)
	require.Len(t, resp.TargetPods, 2)
	require.Equal(t, "spot-pod", resp.TargetPods[0].Name)
	require.Equal(t, "scheduled-pod", resp.TargetPods[1].Name)
}

func TestNICBandwidthEvictionGetTopEvictionPodsPrefersScheduledOverReserved(t *testing.T) {
	t.Parallel()

	scheduledPod := newNetworkTestPod("default", "scheduled-pod", "uid1", "ns1-eth0")
	scheduledPod.Spec.Containers = []v1.Container{{Name: "c1"}}
	scheduledPod.Annotations[apiconsts.PodAnnotationSaleModeKey] = apiconsts.PodSaleModeScheduled
	reservedPod := newNetworkTestPod("default", "reserved-pod", "uid2", "ns1-eth0")
	reservedPod.Spec.Containers = []v1.Container{{Name: "c1"}}
	reservedPod.Annotations[apiconsts.PodAnnotationSaleModeKey] = apiconsts.PodSaleModeReserved

	plugin := newBandwidthTestPlugin(&eviction.NetworkEvictionConfiguration{
		EnableNICBandwidthEviction: true,
	}, &fakeBandwidthMetricQuerier{
		containerDirectionUsage: map[string]map[string]map[string]float64{
			string(scheduledPod.UID): {
				"c1": {bandwidthDirectionRX: 80},
			},
			string(reservedPod.UID): {
				"c1": {bandwidthDirectionRX: 800},
			},
		},
	}, []machine.InterfaceInfo{newBandwidthTestNIC("ns1", "eth0")}, nil)

	resp, err := plugin.GetTopEvictionPods(context.Background(), &pluginapi.GetTopEvictionPodsRequest{
		ActivePods:    []*v1.Pod{reservedPod, scheduledPod},
		TopN:          2,
		EvictionScope: "bandwidth/ns1/eth0/rx",
	})
	require.NoError(t, err)
	require.Len(t, resp.TargetPods, 2)
	require.Equal(t, "scheduled-pod", resp.TargetPods[0].Name)
	require.Equal(t, "reserved-pod", resp.TargetPods[1].Name)
}

func TestNICBandwidthEvictionGetTopEvictionPodsTreatsMissingAndInvalidSaleModeAsDefault(t *testing.T) {
	t.Parallel()

	missingPod := newNetworkTestPod("default", "missing-pod", "uid1", "ns1-eth0")
	missingPod.Spec.Containers = []v1.Container{{Name: "c1"}}
	invalidPod := newNetworkTestPod("default", "invalid-pod", "uid2", "ns1-eth0")
	invalidPod.Spec.Containers = []v1.Container{{Name: "c1"}}
	invalidPod.Annotations[apiconsts.PodAnnotationSaleModeKey] = "wrong-value"

	plugin := newBandwidthTestPlugin(&eviction.NetworkEvictionConfiguration{
		EnableNICBandwidthEviction: true,
	}, &fakeBandwidthMetricQuerier{
		containerDirectionUsage: map[string]map[string]map[string]float64{
			string(missingPod.UID): {
				"c1": {bandwidthDirectionRX: 120},
			},
			string(invalidPod.UID): {
				"c1": {bandwidthDirectionRX: 200},
			},
		},
	}, []machine.InterfaceInfo{newBandwidthTestNIC("ns1", "eth0")}, nil)

	resp, err := plugin.GetTopEvictionPods(context.Background(), &pluginapi.GetTopEvictionPodsRequest{
		ActivePods:    []*v1.Pod{missingPod, invalidPod},
		TopN:          2,
		EvictionScope: "bandwidth/ns1/eth0/rx",
	})
	require.NoError(t, err)
	require.Len(t, resp.TargetPods, 2)
	require.Equal(t, "invalid-pod", resp.TargetPods[0].Name)
	require.Equal(t, "missing-pod", resp.TargetPods[1].Name)
}

func TestNICBandwidthEvictionGetTopEvictionPodsUsesConfigurableSaleModeAnnotationKey(t *testing.T) {
	t.Parallel()

	bytedPod := newNetworkTestPod("default", "byted-pod", "uid1", "ns1-eth0")
	bytedPod.Spec.Containers = []v1.Container{{Name: "c1"}}
	bytedPod.Annotations["salemode"] = apiconsts.PodSaleModeSpot
	apiPod := newNetworkTestPod("default", "api-pod", "uid2", "ns1-eth0")
	apiPod.Spec.Containers = []v1.Container{{Name: "c1"}}
	apiPod.Annotations[apiconsts.PodAnnotationSaleModeKey] = apiconsts.PodSaleModeSpot

	plugin := newBandwidthTestPlugin(&eviction.NetworkEvictionConfiguration{
		EnableNICBandwidthEviction: true,
	}, &fakeBandwidthMetricQuerier{
		containerDirectionUsage: map[string]map[string]map[string]float64{
			string(bytedPod.UID): {
				"c1": {bandwidthDirectionRX: 80},
			},
			string(apiPod.UID): {
				"c1": {bandwidthDirectionRX: 200},
			},
		},
	}, []machine.InterfaceInfo{newBandwidthTestNIC("ns1", "eth0")}, nil)
	plugin.podSaleModeAnnotationKey = "salemode"

	resp, err := plugin.GetTopEvictionPods(context.Background(), &pluginapi.GetTopEvictionPodsRequest{
		ActivePods:    []*v1.Pod{apiPod, bytedPod},
		TopN:          2,
		EvictionScope: "bandwidth/ns1/eth0/rx",
	})
	require.NoError(t, err)
	require.Len(t, resp.TargetPods, 2)
	require.Equal(t, "byted-pod", resp.TargetPods[0].Name)
	require.Equal(t, "api-pod", resp.TargetPods[1].Name)
}

func TestNICBandwidthEvictionGetTopEvictionPodsNilRequest(t *testing.T) {
	t.Parallel()

	resp, err := (&nicEvictionPlugin{}).GetTopEvictionPods(context.Background(), nil)
	require.Error(t, err)
	require.Nil(t, resp)
}

func TestNICBandwidthEvictionGetTopEvictionPodsAggregatesByPodTX(t *testing.T) {
	t.Parallel()

	pod1 := newNetworkTestPod("default", "pod1", "uid1", "ns1-eth0")
	pod1.Spec.Containers = []v1.Container{{Name: "c1"}, {Name: "c2"}}
	pod2 := newNetworkTestPod("default", "pod2", "uid2", "ns1-eth0")
	pod2.Spec.Containers = []v1.Container{{Name: "c1"}}

	plugin := newBandwidthTestPlugin(&eviction.NetworkEvictionConfiguration{
		EnableNICBandwidthEviction: true,
	}, &fakeBandwidthMetricQuerier{
		containerDirectionUsage: map[string]map[string]map[string]float64{
			string(pod1.UID): {
				"c1": {bandwidthDirectionRX: 120, bandwidthDirectionTX: 20},
				"c2": {bandwidthDirectionRX: 10, bandwidthDirectionTX: 35},
			},
			string(pod2.UID): {
				"c1": {bandwidthDirectionRX: 5, bandwidthDirectionTX: 50},
			},
		},
	}, []machine.InterfaceInfo{newBandwidthTestNIC("ns1", "eth0")}, nil)

	resp, err := plugin.GetTopEvictionPods(context.Background(), &pluginapi.GetTopEvictionPodsRequest{
		ActivePods:    []*v1.Pod{pod1, pod2},
		TopN:          1,
		EvictionScope: "bandwidth/ns1/eth0/tx",
	})
	require.NoError(t, err)
	require.Len(t, resp.TargetPods, 1)
	require.Equal(t, "pod1", resp.TargetPods[0].Name)
}

func TestNICBandwidthEvictionGetTopEvictionPodsTieUsesNativePodUniqKeyOrdering(t *testing.T) {
	t.Parallel()

	pod1 := newNetworkTestPod("default-a", "pod-a", "uid-z", "ns1-eth0")
	pod1.Spec.Containers = []v1.Container{{Name: "c1"}}
	pod2 := newNetworkTestPod("default-b", "pod-b", "uid-a", "ns1-eth0")
	pod2.Spec.Containers = []v1.Container{{Name: "c1"}}

	plugin := newBandwidthTestPlugin(&eviction.NetworkEvictionConfiguration{
		EnableNICBandwidthEviction: true,
	}, &fakeBandwidthMetricQuerier{
		containerDirectionUsage: map[string]map[string]map[string]float64{
			string(pod1.UID): {
				"c1": {bandwidthDirectionRX: 60},
			},
			string(pod2.UID): {
				"c1": {bandwidthDirectionRX: 60},
			},
		},
	}, []machine.InterfaceInfo{newBandwidthTestNIC("ns1", "eth0")}, nil)

	resp, err := plugin.GetTopEvictionPods(context.Background(), &pluginapi.GetTopEvictionPodsRequest{
		ActivePods:    []*v1.Pod{pod1, pod2},
		TopN:          2,
		EvictionScope: "bandwidth/ns1/eth0/rx",
	})
	require.NoError(t, err)
	require.Len(t, resp.TargetPods, 2)
	expectedFirst, expectedSecond := pod1, pod2
	if native.PodUniqKeyCmpFunc(pod2, pod1) < 0 {
		expectedFirst, expectedSecond = pod2, pod1
	}
	require.Equal(t, native.GenerateUniqObjectNameKey(expectedFirst), native.GenerateUniqObjectNameKey(resp.TargetPods[0]))
	require.Equal(t, native.GenerateUniqObjectNameKey(expectedSecond), native.GenerateUniqObjectNameKey(resp.TargetPods[1]))
}

func TestNICBandwidthEvictionGetTopEvictionPodsReadsDirectionUsageOnce(t *testing.T) {
	t.Parallel()

	pod1 := newNetworkTestPod("default", "pod1", "uid1", "ns1-eth0")
	pod1.Spec.Containers = []v1.Container{{Name: "c1"}}
	pod2 := newNetworkTestPod("default", "pod2", "uid2", "ns1-eth0")
	pod2.Spec.Containers = []v1.Container{{Name: "c1"}}
	pod3 := newNetworkTestPod("default", "pod3", "uid3", "ns1-eth0")
	pod3.Spec.Containers = []v1.Container{{Name: "c1"}}

	querier := &fakeBandwidthMetricQuerier{
		containerDirectionUsage: map[string]map[string]map[string]float64{
			string(pod1.UID): {"c1": {bandwidthDirectionRX: 100}},
			string(pod2.UID): {"c1": {bandwidthDirectionRX: 80}},
			string(pod3.UID): {"c1": {bandwidthDirectionRX: 60}},
		},
	}

	plugin := newBandwidthTestPlugin(&eviction.NetworkEvictionConfiguration{
		EnableNICBandwidthEviction: true,
	}, querier, []machine.InterfaceInfo{newBandwidthTestNIC("ns1", "eth0")}, nil)

	resp, err := plugin.GetTopEvictionPods(context.Background(), &pluginapi.GetTopEvictionPodsRequest{
		ActivePods:    []*v1.Pod{pod1, pod2, pod3},
		TopN:          2,
		EvictionScope: "bandwidth/ns1/eth0/rx",
	})
	require.NoError(t, err)
	require.Len(t, resp.TargetPods, 2)
	require.Equal(t, "pod1", resp.TargetPods[0].Name)
	require.Equal(t, "pod2", resp.TargetPods[1].Name)
	require.Equal(t, 1, querier.directionUsageCalls[string(pod1.UID)][bandwidthDirectionRX])
	require.Equal(t, 1, querier.directionUsageCalls[string(pod2.UID)][bandwidthDirectionRX])
	require.Equal(t, 1, querier.directionUsageCalls[string(pod3.UID)][bandwidthDirectionRX])
}

func TestNICBandwidthEvictionGetTopEvictionPodsSkipsPodWhenUsageReadFails(t *testing.T) {
	t.Parallel()

	stablePod := newNetworkTestPod("default", "stable-pod", "uid1", "ns1-eth0")
	stablePod.Spec.Containers = []v1.Container{{Name: "c1"}}
	flakyPod := newNetworkTestPod("default", "flaky-pod", "uid2", "ns1-eth0")
	flakyPod.Spec.Containers = []v1.Container{{Name: "c1"}}
	backupPod := newNetworkTestPod("default", "backup-pod", "uid3", "ns1-eth0")
	backupPod.Spec.Containers = []v1.Container{{Name: "c1"}}

	querier := &fakeBandwidthMetricQuerier{
		containerDirectionUsage: map[string]map[string]map[string]float64{
			string(stablePod.UID): {"c1": {bandwidthDirectionRX: 80}},
			string(flakyPod.UID):  {"c1": {bandwidthDirectionRX: 120}},
			string(backupPod.UID): {"c1": {bandwidthDirectionRX: 60}},
		},
		failDirectionUsageAfter: map[string]map[string]int{
			string(flakyPod.UID): {bandwidthDirectionRX: 0},
		},
	}

	plugin := newBandwidthTestPlugin(&eviction.NetworkEvictionConfiguration{
		EnableNICBandwidthEviction: true,
	}, querier, []machine.InterfaceInfo{newBandwidthTestNIC("ns1", "eth0")}, nil)

	resp, err := plugin.GetTopEvictionPods(context.Background(), &pluginapi.GetTopEvictionPodsRequest{
		ActivePods:    []*v1.Pod{stablePod, flakyPod, backupPod},
		TopN:          2,
		EvictionScope: "bandwidth/ns1/eth0/rx",
	})
	require.NoError(t, err)
	require.Len(t, resp.TargetPods, 2)
	require.Equal(t, "stable-pod", resp.TargetPods[0].Name)
	require.Equal(t, "backup-pod", resp.TargetPods[1].Name)
}

func TestNICBandwidthEvictionGetTopEvictionPodsInvalidScope(t *testing.T) {
	t.Parallel()

	plugin := newBandwidthTestPlugin(&eviction.NetworkEvictionConfiguration{
		EnableNICBandwidthEviction: true,
	}, &fakeBandwidthMetricQuerier{}, []machine.InterfaceInfo{newBandwidthTestNIC("ns1", "eth0")}, nil)

	resp, err := plugin.GetTopEvictionPods(context.Background(), &pluginapi.GetTopEvictionPodsRequest{
		ActivePods:    []*v1.Pod{newNetworkTestPod("default", "pod1", "uid1", "ns1-eth0")},
		TopN:          1,
		EvictionScope: "bandwidth/ns1/eth0",
	})
	require.NoError(t, err)
	require.Empty(t, resp.TargetPods)
	require.Nil(t, resp.DeletionOptions)
}

func TestNICBandwidthEvictionGetTopEvictionPodsBondPath(t *testing.T) {
	t.Parallel()

	bondPod := newNetworkTestPod("default", "bond-pod", "uid1", "ns1-bond0")
	bondPod.Spec.Containers = []v1.Container{{Name: "c1"}, {Name: "c2"}}
	bondPod2 := newNetworkTestPod("default", "bond-pod-2", "uid3", "ns1-bond0")
	bondPod2.Spec.Containers = []v1.Container{{Name: "c1"}}
	otherPod := newNetworkTestPod("default", "other-pod", "uid2", "ns1-eth0")
	otherPod.Spec.Containers = []v1.Container{{Name: "c1"}}

	plugin := newBandwidthTestPlugin(&eviction.NetworkEvictionConfiguration{
		EnableNICBandwidthEviction: true,
	}, &fakeBandwidthMetricQuerier{
		containerDirectionUsage: map[string]map[string]map[string]float64{
			string(bondPod.UID): {
				"c1": {bandwidthDirectionRX: 40},
				"c2": {bandwidthDirectionRX: 50},
			},
			string(bondPod2.UID): {
				"c1": {bandwidthDirectionRX: 30},
			},
			string(otherPod.UID): {
				"c1": {bandwidthDirectionRX: 100},
			},
		},
	}, []machine.InterfaceInfo{
		newBandwidthTestNIC("ns1", "bond0"),
		newBandwidthTestNIC("ns1", "eth0"),
	}, newBandwidthTestCNR(map[string][]string{
		"ns1-bond0": nil,
	}))

	resp, err := plugin.GetTopEvictionPods(context.Background(), &pluginapi.GetTopEvictionPodsRequest{
		ActivePods:    []*v1.Pod{bondPod, bondPod2, otherPod},
		TopN:          1,
		EvictionScope: "bandwidth/ns1/bond0/rx",
	})
	require.NoError(t, err)
	require.Len(t, resp.TargetPods, 1)
	require.Equal(t, "bond-pod", resp.TargetPods[0].Name)
}

func TestNICBandwidthEvictionGetTopEvictionPodsMultiNetNSIsolation(t *testing.T) {
	t.Parallel()

	pod1 := newNetworkTestPod("default", "pod1", "uid1", "ns1-eth0")
	pod1.Spec.Containers = []v1.Container{{Name: "c1"}}
	pod2 := newNetworkTestPod("default", "pod2", "uid2", "ns2-eth0")
	pod2.Spec.Containers = []v1.Container{{Name: "c1"}}
	pod3 := newNetworkTestPod("default", "pod3", "uid3", "ns2-eth0")
	pod3.Spec.Containers = []v1.Container{{Name: "c1"}}

	plugin := newBandwidthTestPlugin(&eviction.NetworkEvictionConfiguration{
		EnableNICBandwidthEviction: true,
	}, &fakeBandwidthMetricQuerier{
		containerDirectionUsage: map[string]map[string]map[string]float64{
			string(pod1.UID): {
				"c1": {bandwidthDirectionRX: 200},
			},
			string(pod2.UID): {
				"c1": {bandwidthDirectionRX: 80},
			},
			string(pod3.UID): {
				"c1": {bandwidthDirectionRX: 40},
			},
		},
	}, []machine.InterfaceInfo{
		newBandwidthTestNIC("ns1", "eth0"),
		newBandwidthTestNIC("ns2", "eth0"),
	}, nil)

	resp, err := plugin.GetTopEvictionPods(context.Background(), &pluginapi.GetTopEvictionPodsRequest{
		ActivePods:    []*v1.Pod{pod1, pod2, pod3},
		TopN:          1,
		EvictionScope: "bandwidth/ns2/eth0/rx",
	})
	require.NoError(t, err)
	require.Len(t, resp.TargetPods, 1)
	require.Equal(t, "pod2", resp.TargetPods[0].Name)
}

func TestNICBandwidthEvictionGetTopEvictionPodsEmptyCandidates(t *testing.T) {
	t.Parallel()

	plugin := newBandwidthTestPlugin(&eviction.NetworkEvictionConfiguration{
		EnableNICBandwidthEviction: true,
		NICBandwidthGracePeriod:    5,
	}, &fakeBandwidthMetricQuerier{}, []machine.InterfaceInfo{newBandwidthTestNIC("ns1", "eth0")}, nil)

	resp, err := plugin.GetTopEvictionPods(context.Background(), &pluginapi.GetTopEvictionPodsRequest{
		ActivePods:    []*v1.Pod{newNetworkTestPod("default", "pod1", "uid1", "ns2-eth0")},
		TopN:          1,
		EvictionScope: "bandwidth/ns1/eth0/rx",
	})
	require.NoError(t, err)
	require.Empty(t, resp.TargetPods)
	require.Nil(t, resp.DeletionOptions)
}

type fakeNICMetric struct {
	bps        float64
	speed      float64
	observedAt time.Time
}

type fakeBandwidthMetricQuerier struct {
	deviceMetrics            map[string]fakeNICMetric
	containerDirectionUsage  map[string]map[string]map[string]float64
	bandwidthMetricCallCount int
	directionUsageCalls      map[string]map[string]int
	failDirectionUsageAfter  map[string]map[string]int
}

func (f *fakeBandwidthMetricQuerier) GetBandwidthMetric(scope bandwidthScope) (utilmetric.MetricData, error) {
	if f == nil {
		return utilmetric.MetricData{}, fmt.Errorf("nil fakeBandwidthMetricQuerier")
	}
	f.bandwidthMetricCallCount++

	metric, ok := f.deviceMetrics[formatBandwidthScope(scope)]
	if !ok {
		return utilmetric.MetricData{}, fmt.Errorf("bandwidth metric not found for scope %s", formatBandwidthScope(scope))
	}

	if metric.observedAt.IsZero() {
		metric.observedAt = time.Now()
		f.deviceMetrics[formatBandwidthScope(scope)] = metric
	}
	return utilmetric.MetricData{Value: metric.bps, Time: &metric.observedAt}, nil
}

func (f *fakeBandwidthMetricQuerier) GetPodDirectionUsage(pod *v1.Pod, direction string) (float64, bool) {
	if f == nil || pod == nil {
		return 0, false
	}
	if f.directionUsageCalls == nil {
		f.directionUsageCalls = make(map[string]map[string]int)
	}
	if f.directionUsageCalls[string(pod.UID)] == nil {
		f.directionUsageCalls[string(pod.UID)] = make(map[string]int)
	}
	f.directionUsageCalls[string(pod.UID)][direction]++
	if f.failDirectionUsageAfter != nil {
		if directions, ok := f.failDirectionUsageAfter[string(pod.UID)]; ok {
			if failAfter, ok := directions[direction]; ok && f.directionUsageCalls[string(pod.UID)][direction] > failAfter {
				return 0, false
			}
		}
	}

	containers, ok := f.containerDirectionUsage[string(pod.UID)]
	if !ok {
		return 0, false
	}

	total := 0.0
	found := false
	for _, container := range pod.Spec.Containers {
		usageByDirection, ok := containers[container.Name]
		if !ok {
			continue
		}
		usage, ok := usageByDirection[direction]
		if !ok {
			continue
		}
		total += usage
		found = true
	}

	return total, found
}

func newBandwidthTestPlugin(conf *eviction.NetworkEvictionConfiguration, metricQuerier BandwidthMetricQuerier, interfaces []machine.InterfaceInfo, cnrObj *v1alpha1.CustomNodeResource) *nicEvictionPlugin {
	return newBandwidthTestPluginWithMetaServer(conf, metricQuerier, nil, interfaces, cnrObj)
}

func newBandwidthInjectedTestPlugin(conf *eviction.NetworkEvictionConfiguration, metricsFetcher *metametric.FakeMetricsFetcher, interfaces []machine.InterfaceInfo, cnrObj *v1alpha1.CustomNodeResource) *nicEvictionPlugin {
	return newBandwidthTestPluginWithMetaServer(conf, nil, metricsFetcher, interfaces, cnrObj)
}

func newBandwidthTestPluginWithMetaServer(conf *eviction.NetworkEvictionConfiguration, metricQuerier BandwidthMetricQuerier, metricsFetcher *metametric.FakeMetricsFetcher, interfaces []machine.InterfaceInfo, cnrObj *v1alpha1.CustomNodeResource) *nicEvictionPlugin {
	dynamicConfig := dynamic.NewDynamicAgentConfiguration()
	dynamicConfig.GetDynamicConfiguration().NetworkEvictionConfiguration = conf
	if cnrObj == nil {
		cnrObj = newBandwidthTestCNRFromInterfaces(interfaces, "100")
	}
	metaServer := &metaserver.MetaServer{MetaAgent: &agent.MetaAgent{
		MetricsFetcher: metricsFetcher,
		KatalystMachineInfo: &machine.KatalystMachineInfo{
			ExtraNetworkInfo: &machine.ExtraNetworkInfo{Interface: interfaces},
		},
		CNRFetcher: &cnr.CNRFetcherStub{CNR: cnrObj},
	}}
	if metricQuerier == nil {
		metricQuerier = newBandwidthMetricQuerier(metricsFetcher)
	}

	plugin := &nicEvictionPlugin{
		dynamicConfig:            dynamicConfig,
		unhealthyNICState:        make(map[string]*unhealthyNICState),
		healthyNICState:          make(map[string]*bandwidthState),
		emitter:                  metrics.DummyMetrics{},
		podSaleModeAnnotationKey: apiconsts.PodAnnotationSaleModeKey,
		bandwidthMetricQuerier:   metricQuerier,
		metaServer:               metaServer,
	}
	plugin.syncNICState(context.Background())
	plugin.healthyNICState = map[string]*bandwidthState{}
	if fakeQuerier, ok := metricQuerier.(*fakeBandwidthMetricQuerier); ok {
		fakeQuerier.bandwidthMetricCallCount = 0
	}
	return plugin
}

func newBandwidthTestCNRFromInterfaces(interfaces []machine.InterfaceInfo, capacity string) *v1alpha1.CustomNodeResource {
	allocations := make(map[string][]string, len(interfaces))
	for _, iface := range interfaces {
		allocations[machine.FormatNICIdentifier(iface.NSName, iface.Name)] = nil
	}
	return newBandwidthTestCNRWithCapacity(allocations, capacity)
}

func newBandwidthTestNIC(netns, nic string) machine.InterfaceInfo {
	return machine.InterfaceInfo{
		NetNSInfo: machine.NetNSInfo{NSName: netns},
		Name:      nic,
	}
}

func newBandwidthTestCNR(allocations map[string][]string) *v1alpha1.CustomNodeResource {
	return newBandwidthTestCNRWithCapacity(allocations, "10G")
}

func newBandwidthTestCNRWithCapacity(allocations map[string][]string, capacity string) *v1alpha1.CustomNodeResource {
	children := make([]*v1alpha1.TopologyZone, 0, len(allocations))
	for identifier, consumers := range allocations {
		zoneAllocations := make([]*v1alpha1.Allocation, 0, len(consumers))
		for _, consumer := range consumers {
			requests := v1.ResourceList{apiconsts.ResourceNetBandwidth: resource.MustParse("10G")}
			zoneAllocations = append(zoneAllocations, &v1alpha1.Allocation{Consumer: consumer, Requests: &requests})
		}
		allocatable := v1.ResourceList{apiconsts.ResourceNetBandwidth: resource.MustParse(capacity)}
		children = append(children, &v1alpha1.TopologyZone{
			Name:        identifier,
			Type:        v1alpha1.TopologyTypeNIC,
			Resources:   v1alpha1.Resources{Allocatable: &allocatable},
			Allocations: zoneAllocations,
		})
	}

	return &v1alpha1.CustomNodeResource{Status: v1alpha1.CustomNodeResourceStatus{TopologyZone: []*v1alpha1.TopologyZone{{
		Type:     v1alpha1.TopologyTypeSocket,
		Children: children,
	}}}}
}

func newNetworkTestPod(namespace, name, uid, nicIdentifier string) *v1.Pod {
	annotations := map[string]string{}
	if nicIdentifier != "" {
		annotations[apiconsts.PodAnnotationNICSelectionResultKey] = nicIdentifier
	}

	return &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:   namespace,
			Name:        name,
			UID:         types.UID(uid),
			Annotations: annotations,
		},
		Status: v1.PodStatus{Phase: v1.PodRunning},
	}
}
