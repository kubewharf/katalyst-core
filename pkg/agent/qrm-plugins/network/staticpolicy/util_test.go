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

package staticpolicy

import (
	"encoding/json"
	"reflect"
	"testing"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	v1alpha1 "github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/network/state"
	katalystconsts "github.com/kubewharf/katalyst-core/pkg/consts"
)

// helper to extract topology allocation from annotations JSON
func parseNetworkTopologyFromAnno(t *testing.T, annos map[string]string) v1alpha1.TopologyAllocation {
	t.Helper()
	if annos == nil {
		return nil
	}
	raw, ok := annos[katalystconsts.QRMPodAnnotationTopologyAllocationKey]
	if !ok {
		return nil
	}
	var ta v1alpha1.TopologyAllocation
	if err := json.Unmarshal([]byte(raw), &ta); err != nil {
		t.Fatalf("failed to unmarshal topology allocation: %v", err)
	}
	return ta
}

func TestGetNetworkTopologyAllocationsAnnotations(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name               string
		ai                 *state.AllocationInfo
		currentAnnotations map[string]string
		wantAnnotations    map[string]string
		wantTopology       v1alpha1.TopologyAllocation
	}{
		{
			name:               "nil allocation returns current annotations",
			ai:                 nil,
			currentAnnotations: map[string]string{"foo": "bar"},
			wantAnnotations:    map[string]string{"foo": "bar"},
		},
		{
			name: "empty current annotations adds NIC zone",
			ai: &state.AllocationInfo{
				Identifier: "eth0",
			},
			currentAnnotations: nil,
			wantTopology: v1alpha1.TopologyAllocation{
				v1alpha1.TopologyTypeNIC: map[string]v1alpha1.ZoneAllocation{
					"eth0": {},
				},
			},
		},
		{
			name: "merges with extra keys in current annotations",
			ai: &state.AllocationInfo{
				Identifier: "netns-eth1",
			},
			currentAnnotations: map[string]string{"some": "value"},
			wantAnnotations:    map[string]string{"some": "value"},
			wantTopology: v1alpha1.TopologyAllocation{
				v1alpha1.TopologyTypeNIC: map[string]v1alpha1.ZoneAllocation{
					"netns-eth1": {},
				},
			},
		},
		{
			name: "keeps existing topology annotation if already present in current",
			ai:   &state.AllocationInfo{Identifier: "eth2"},
			currentAnnotations: func() map[string]string {
				// prepare an existing different topology annotation; current should take precedence
				ta := v1alpha1.TopologyAllocation{
					v1alpha1.TopologyTypeNIC: map[string]v1alpha1.ZoneAllocation{
						"preexist": {Allocated: map[v1.ResourceName]resource.Quantity{v1.ResourceCPU: resource.MustParse("1")}},
					},
				}
				b, _ := json.Marshal(ta)
				return map[string]string{katalystconsts.QRMPodAnnotationTopologyAllocationKey: string(b)}
			}(),
			// wantTopology reflects the preexisting one since merge favors current annotations on key collision
			wantTopology: v1alpha1.TopologyAllocation{
				v1alpha1.TopologyTypeNIC: map[string]v1alpha1.ZoneAllocation{
					"preexist": {Allocated: map[v1.ResourceName]resource.Quantity{v1.ResourceCPU: resource.MustParse("1")}},
				},
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := getNetworkTopologyAllocationsAnnotations(tt.ai, tt.currentAnnotations, katalystconsts.QRMPodAnnotationTopologyAllocationKey)

			// If an explicit wantAnnotations map is provided, ensure all its entries exist in result
			if tt.wantAnnotations != nil {
				for k, v := range tt.wantAnnotations {
					if gv, ok := got[k]; !ok || gv != v {
						t.Fatalf("expected annotation %q=%q present, got: %v", k, v, got)
					}
				}
			}

			// Validate topology annotation when wantTopology is specified
			if tt.wantTopology != nil {
				ta := parseNetworkTopologyFromAnno(t, got)
				if !reflect.DeepEqual(ta, tt.wantTopology) {
					t.Fatalf("unexpected topology allocation. got=%v, want=%v", ta, tt.wantTopology)
				}
			}
		})
	}
}
