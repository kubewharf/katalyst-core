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

package dynamicpolicy

import (
	"encoding/json"
	"reflect"
	"testing"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/memory/dynamicpolicy/state"
	coreconsts "github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func TestGetFullyDropCacheBytes(t *testing.T) {
	t.Parallel()

	type args struct {
		container *v1.Container
	}
	tests := []struct {
		name string
		args args
		want int64
	}{
		{
			name: "contaienr with both request and limit",
			args: args{
				container: &v1.Container{
					Name: "c1",
					Resources: v1.ResourceRequirements{
						Limits: map[v1.ResourceName]resource.Quantity{
							v1.ResourceMemory: resource.MustParse("3Gi"),
						},
						Requests: map[v1.ResourceName]resource.Quantity{
							v1.ResourceMemory: resource.MustParse("2Gi"),
						},
					},
				},
			},
			want: 3221225472,
		},
		{
			name: "contaienr only with request",
			args: args{
				container: &v1.Container{
					Name: "c1",
					Resources: v1.ResourceRequirements{
						Requests: map[v1.ResourceName]resource.Quantity{
							v1.ResourceMemory: resource.MustParse("2Gi"),
						},
					},
				},
			},
			want: 2147483648,
		},
		{
			name: "nil container",
			args: args{},
			want: 0,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := GetFullyDropCacheBytes(tt.args.container); got != tt.want {
				t.Errorf("GetFullyDropCacheBytes() = %v, want %v", got, tt.want)
			}
		})
	}
}

// helper to extract topology allocation from annotations JSON
func parseTopologyAllocationFromAnno(t *testing.T, annos map[string]string) v1alpha1.TopologyAllocation {
	t.Helper()
	if annos == nil {
		return nil
	}
	raw, ok := annos[coreconsts.QRMPodAnnotationTopologyAllocationKey]
	if !ok {
		return nil
	}
	var ta v1alpha1.TopologyAllocation
	if err := json.Unmarshal([]byte(raw), &ta); err != nil {
		t.Fatalf("failed to unmarshal topology allocation: %v", err)
	}
	return ta
}

func TestGetMemoryTopologyAllocationsAnnotations(t *testing.T) {
	t.Parallel()

	giB := func(n int) uint64 { return uint64(n) << 30 }
	hugepages2Mi := v1.ResourceName("hugepages-2Mi")

	tests := []struct {
		name         string
		ai           map[v1.ResourceName]*state.AllocationInfo
		wantNilAnno  bool
		wantTopology map[v1.ResourceName]v1alpha1.TopologyAllocation
	}{
		{
			name:        "nil allocation info returns nil",
			ai:          nil,
			wantNilAnno: true,
		},
		{
			name: "no topology allocations and empty NUMA result returns nil",
			ai: map[v1.ResourceName]*state.AllocationInfo{
				v1.ResourceMemory: {},
			},
			wantNilAnno: true,
		},
		{
			name: "no topology allocations but with NUMA result lists zones only",
			ai: map[v1.ResourceName]*state.AllocationInfo{
				v1.ResourceMemory: {
					NumaAllocationResult: machine.NewCPUSet(0, 1),
				},
			},
			wantTopology: map[v1.ResourceName]v1alpha1.TopologyAllocation{
				v1.ResourceMemory: {
					v1alpha1.TopologyTypeNuma: map[string]v1alpha1.ZoneAllocation{
						"0": {},
						"1": {},
					},
				},
			},
		},
		{
			name: "with topology allocations includes allocated quantities",
			ai: map[v1.ResourceName]*state.AllocationInfo{
				v1.ResourceMemory: {
					TopologyAwareAllocations: map[int]uint64{
						0: giB(1),
						1: giB(2),
					},
				},
			},
			wantTopology: map[v1.ResourceName]v1alpha1.TopologyAllocation{
				v1.ResourceMemory: {
					v1alpha1.TopologyTypeNuma: map[string]v1alpha1.ZoneAllocation{
						"0": {
							Allocated: map[v1.ResourceName]resource.Quantity{
								v1.ResourceMemory: resource.MustParse("1Gi"),
							},
						},
						"1": {
							Allocated: map[v1.ResourceName]resource.Quantity{
								v1.ResourceMemory: resource.MustParse("2Gi"),
							},
						},
					},
				},
			},
		},
		{
			name: "with topology allocations includes allocated quantities (including zero)",
			ai: map[v1.ResourceName]*state.AllocationInfo{
				v1.ResourceMemory: {
					TopologyAwareAllocations: map[int]uint64{
						0: giB(3),
						2: 0,
					},
				},
			},
			wantTopology: map[v1.ResourceName]v1alpha1.TopologyAllocation{
				v1.ResourceMemory: {
					v1alpha1.TopologyTypeNuma: map[string]v1alpha1.ZoneAllocation{
						"0": {
							Allocated: map[v1.ResourceName]resource.Quantity{
								v1.ResourceMemory: resource.MustParse("3Gi"),
							},
						},
						"2": {
							Allocated: map[v1.ResourceName]resource.Quantity{
								v1.ResourceMemory: resource.MustParse("0"),
							},
						},
					},
				},
			},
		},
		{
			name: "multiple resources include hugepages-2Mi allocations",
			ai: map[v1.ResourceName]*state.AllocationInfo{
				v1.ResourceMemory: {
					TopologyAwareAllocations: map[int]uint64{
						0: giB(1),
					},
				},
				hugepages2Mi: {
					TopologyAwareAllocations: map[int]uint64{
						1: giB(2),
					},
				},
			},
			wantTopology: map[v1.ResourceName]v1alpha1.TopologyAllocation{
				v1.ResourceMemory: {
					v1alpha1.TopologyTypeNuma: map[string]v1alpha1.ZoneAllocation{
						"0": {
							Allocated: map[v1.ResourceName]resource.Quantity{
								v1.ResourceMemory: resource.MustParse("1Gi"),
							},
						},
					},
				},
				hugepages2Mi: {
					v1alpha1.TopologyTypeNuma: map[string]v1alpha1.ZoneAllocation{
						"1": {
							Allocated: map[v1.ResourceName]resource.Quantity{
								hugepages2Mi: resource.MustParse("2Gi"),
							},
						},
					},
				},
			},
		},
		{
			name: "mixed resources with and without topology allocations",
			ai: map[v1.ResourceName]*state.AllocationInfo{
				v1.ResourceMemory: {
					TopologyAwareAllocations: map[int]uint64{
						0: giB(1),
					},
				},
				hugepages2Mi: {
					NumaAllocationResult: machine.NewCPUSet(1),
				},
			},
			wantTopology: map[v1.ResourceName]v1alpha1.TopologyAllocation{
				v1.ResourceMemory: {
					v1alpha1.TopologyTypeNuma: map[string]v1alpha1.ZoneAllocation{
						"0": {
							Allocated: map[v1.ResourceName]resource.Quantity{
								v1.ResourceMemory: resource.MustParse("1Gi"),
							},
						},
					},
				},
				hugepages2Mi: {
					v1alpha1.TopologyTypeNuma: map[string]v1alpha1.ZoneAllocation{
						"1": {},
					},
				},
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := getMemoryTopologyAllocationsAnnotations(tt.ai, coreconsts.QRMPodAnnotationTopologyAllocationKey)
			if tt.wantNilAnno {
				if got != nil {
					t.Fatalf("expected nil annotations, got: %#v", got)
				}
				return
			}

			for resourceName, want := range tt.wantTopology {
				ta := parseTopologyAllocationFromAnno(t, got[resourceName])
				if !reflect.DeepEqual(ta, want) {
					t.Fatalf("unexpected topology allocation for resource %q. got=%v, want=%v", resourceName, ta, want)
				}
			}
		})
	}
}
