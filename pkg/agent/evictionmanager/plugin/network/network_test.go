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
	"testing"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	pluginapi "github.com/kubewharf/katalyst-api/pkg/protocol/evictionplugin/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/adminqos/eviction"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/cnr"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

func TestSyncUnhealthyNICState(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name              string
		cnr               *v1alpha1.CustomNodeResource
		expectedNICStates map[string]*unhealthyNICState
	}{{
		name: "add new unhealthy nic",
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
					}},
				}},
			},
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
			plugin := &nicEvictionPlugin{
				unhealthyNICState: make(map[string]*unhealthyNICState),
				metaServer: &metaserver.MetaServer{
					MetaAgent: &agent.MetaAgent{
						CNRFetcher: &cnr.CNRFetcherStub{CNR: tt.cnr},
					},
				},
				emitter: metrics.DummyMetrics{},
			}

			plugin.syncUnhealthyNICState(context.TODO())

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
