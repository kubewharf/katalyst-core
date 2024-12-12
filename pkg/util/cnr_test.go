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

package util

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	nodeapis "github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/consts"
)

func TestAddOrUpdateCNRTaint(t *testing.T) {
	t.Parallel()

	type args struct {
		cnr   *nodeapis.CustomNodeResource
		taint nodeapis.Taint
	}
	tests := []struct {
		name    string
		args    args
		want    *nodeapis.CustomNodeResource
		want1   bool
		wantErr assert.ErrorAssertionFunc
	}{
		{
			name: "add taint",
			args: args{
				cnr: &nodeapis.CustomNodeResource{},
				taint: nodeapis.Taint{
					QoSLevel: consts.QoSLevelReclaimedCores,
					Taint: v1.Taint{
						Key:    "test-key",
						Value:  "test-value",
						Effect: v1.TaintEffectNoSchedule,
					},
				},
			},
			want: &nodeapis.CustomNodeResource{
				Spec: nodeapis.CustomNodeResourceSpec{
					Taints: []nodeapis.Taint{
						{
							QoSLevel: consts.QoSLevelReclaimedCores,
							Taint: v1.Taint{
								Key:    "test-key",
								Value:  "test-value",
								Effect: v1.TaintEffectNoSchedule,
							},
						},
					},
				},
			},
			want1: true,
			wantErr: func(t assert.TestingT, err error, i ...interface{}) bool {
				return true
			},
		},
		{
			name: "update taint",
			args: args{
				cnr: &nodeapis.CustomNodeResource{
					Spec: nodeapis.CustomNodeResourceSpec{
						Taints: []nodeapis.Taint{
							{
								QoSLevel: consts.QoSLevelReclaimedCores,
								Taint: v1.Taint{
									Key:    "test-key",
									Value:  "test-value",
									Effect: v1.TaintEffectNoSchedule,
								},
							},
						},
					},
				},
				taint: nodeapis.Taint{
					QoSLevel: consts.QoSLevelReclaimedCores,
					Taint: v1.Taint{
						Key:    "test-key",
						Value:  "test-value-1",
						Effect: v1.TaintEffectNoSchedule,
					},
				},
			},
			want: &nodeapis.CustomNodeResource{
				Spec: nodeapis.CustomNodeResourceSpec{
					Taints: []nodeapis.Taint{
						{
							QoSLevel: consts.QoSLevelReclaimedCores,
							Taint: v1.Taint{
								Key:    "test-key",
								Value:  "test-value-1",
								Effect: v1.TaintEffectNoSchedule,
							},
						},
					},
				},
			},
			want1: true,
			wantErr: func(t assert.TestingT, err error, i ...interface{}) bool {
				return true
			},
		},
		{
			name: "no update taint",
			args: args{
				cnr: &nodeapis.CustomNodeResource{
					Spec: nodeapis.CustomNodeResourceSpec{
						Taints: []nodeapis.Taint{
							{
								QoSLevel: consts.QoSLevelReclaimedCores,
								Taint: v1.Taint{
									Key:    "test-key",
									Value:  "test-value",
									Effect: v1.TaintEffectNoSchedule,
								},
							},
						},
					},
				},
				taint: nodeapis.Taint{
					QoSLevel: consts.QoSLevelReclaimedCores,
					Taint: v1.Taint{
						Key:    "test-key",
						Value:  "test-value",
						Effect: v1.TaintEffectNoSchedule,
					},
				},
			},
			want: &nodeapis.CustomNodeResource{
				Spec: nodeapis.CustomNodeResourceSpec{
					Taints: []nodeapis.Taint{
						{
							QoSLevel: consts.QoSLevelReclaimedCores,
							Taint: v1.Taint{
								Key:    "test-key",
								Value:  "test-value",
								Effect: v1.TaintEffectNoSchedule,
							},
						},
					},
				},
			},
			want1: false,
			wantErr: func(t assert.TestingT, err error, i ...interface{}) bool {
				return true
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, got1, err := AddOrUpdateCNRTaint(tt.args.cnr, tt.args.taint)
			if !tt.wantErr(t, err, fmt.Sprintf("AddOrUpdateCNRTaint(%v, %v)", tt.args.cnr, tt.args.taint)) {
				return
			}
			assert.Equalf(t, tt.want, got, "AddOrUpdateCNRTaint(%v, %v)", tt.args.cnr, tt.args.taint)
			assert.Equalf(t, tt.want1, got1, "AddOrUpdateCNRTaint(%v, %v)", tt.args.cnr, tt.args.taint)
		})
	}
}

func TestCNRTaintExists(t *testing.T) {
	t.Parallel()

	type args struct {
		taints      []nodeapis.Taint
		taintToFind nodeapis.Taint
	}
	tests := []struct {
		name string
		args args
		want bool
	}{
		{
			name: "taint exists",
			args: args{
				taints: []nodeapis.Taint{
					{
						QoSLevel: consts.QoSLevelReclaimedCores,
						Taint: v1.Taint{
							Key:    "test-key",
							Value:  "test-value",
							Effect: v1.TaintEffectNoSchedule,
						},
					},
				},
				taintToFind: nodeapis.Taint{
					QoSLevel: consts.QoSLevelReclaimedCores,
					Taint: v1.Taint{
						Key:    "test-key",
						Value:  "test-value",
						Effect: v1.TaintEffectNoSchedule,
					},
				},
			},
			want: true,
		},
		{
			name: "taint no exists",
			args: args{
				taints: []nodeapis.Taint{
					{
						QoSLevel: consts.QoSLevelReclaimedCores,
						Taint: v1.Taint{
							Key:    "test-key",
							Value:  "test-value",
							Effect: v1.TaintEffectNoSchedule,
						},
					},
				},
				taintToFind: nodeapis.Taint{
					QoSLevel: consts.QoSLevelReclaimedCores,
					Taint: v1.Taint{
						Key:    "test-key-1",
						Value:  "test-value-1",
						Effect: v1.TaintEffectNoSchedule,
					},
				},
			},
			want: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equalf(t, tt.want, CNRTaintExists(tt.args.taints, tt.args.taintToFind), "CNRTaintExists(%v, %v)", tt.args.taints, tt.args.taintToFind)
		})
	}
}

func TestMergeAllocations(t *testing.T) {
	t.Parallel()

	type args struct {
		dst []*nodeapis.Allocation
		src []*nodeapis.Allocation
	}
	tests := []struct {
		name string
		args args
		want []*nodeapis.Allocation
	}{
		{
			name: "merge all",
			args: args{
				src: []*nodeapis.Allocation{
					{
						Consumer: "aa",
						Requests: &v1.ResourceList{
							v1.ResourceCPU: resource.MustParse("10"),
						},
					},
				},
				dst: []*nodeapis.Allocation{
					{
						Consumer: "bb",
						Requests: &v1.ResourceList{
							v1.ResourceCPU: resource.MustParse("10"),
						},
					},
				},
			},
			want: []*nodeapis.Allocation{
				{
					Consumer: "aa",
					Requests: &v1.ResourceList{
						v1.ResourceCPU: resource.MustParse("10"),
					},
				},
				{
					Consumer: "bb",
					Requests: &v1.ResourceList{
						v1.ResourceCPU: resource.MustParse("10"),
					},
				},
			},
		},
		{
			name: "merge resources",
			args: args{
				src: []*nodeapis.Allocation{
					{
						Consumer: "aa",
						Requests: &v1.ResourceList{
							v1.ResourceCPU: resource.MustParse("10"),
						},
					},
				},
				dst: []*nodeapis.Allocation{
					{
						Consumer: "aa",
						Requests: &v1.ResourceList{
							v1.ResourceMemory: resource.MustParse("10"),
						},
					},
				},
			},
			want: []*nodeapis.Allocation{
				{
					Consumer: "aa",
					Requests: &v1.ResourceList{
						v1.ResourceCPU:    resource.MustParse("10"),
						v1.ResourceMemory: resource.MustParse("10"),
					},
				},
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equalf(t, tt.want, MergeAllocations(tt.args.dst, tt.args.src), "MergeAllocations(%v, %v)", tt.args.dst, tt.args.src)
		})
	}
}

func TestMergeAttributes(t *testing.T) {
	t.Parallel()

	type args struct {
		dst []nodeapis.Attribute
		src []nodeapis.Attribute
	}
	tests := []struct {
		name string
		args args
		want []nodeapis.Attribute
	}{
		{
			name: "merge two attribute",
			args: args{
				src: []nodeapis.Attribute{
					{
						Name:  "aa",
						Value: "aa-value",
					},
				},
				dst: []nodeapis.Attribute{
					{
						Name:  "bb",
						Value: "bb-value",
					},
				},
			},
			want: []nodeapis.Attribute{
				{
					Name:  "aa",
					Value: "aa-value",
				},
				{
					Name:  "bb",
					Value: "bb-value",
				},
			},
		},
		{
			name: "merge same attribute",
			args: args{
				src: []nodeapis.Attribute{
					{
						Name:  "aa",
						Value: "aa-value",
					},
				},
				dst: []nodeapis.Attribute{
					{
						Name:  "aa",
						Value: "aa-value-1",
					},
				},
			},
			want: []nodeapis.Attribute{
				{
					Name:  "aa",
					Value: "aa-value-1",
				},
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equalf(t, tt.want, MergeAttributes(tt.args.dst, tt.args.src), "MergeAttributes(%v, %v)", tt.args.dst, tt.args.src)
		})
	}
}

func TestMergeResources(t *testing.T) {
	t.Parallel()

	type args struct {
		dst nodeapis.Resources
		src nodeapis.Resources
	}
	tests := []struct {
		name string
		args args
		want nodeapis.Resources
	}{
		{
			name: "merge capacity and allocatable",
			args: args{
				src: nodeapis.Resources{
					Capacity: &v1.ResourceList{
						v1.ResourceCPU:    resource.MustParse("10"),
						v1.ResourceMemory: resource.MustParse("10"),
					},
				},
				dst: nodeapis.Resources{
					Allocatable: &v1.ResourceList{
						v1.ResourceCPU:    resource.MustParse("10"),
						v1.ResourceMemory: resource.MustParse("10"),
					},
				},
			},
			want: nodeapis.Resources{
				Capacity: &v1.ResourceList{
					v1.ResourceCPU:    resource.MustParse("10"),
					v1.ResourceMemory: resource.MustParse("10"),
				},
				Allocatable: &v1.ResourceList{
					v1.ResourceCPU:    resource.MustParse("10"),
					v1.ResourceMemory: resource.MustParse("10"),
				},
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equalf(t, tt.want, MergeResources(tt.args.dst, tt.args.src), "MergeResources(%v, %v)", tt.args.dst, tt.args.src)
		})
	}
}

func TestMergeTopologyZone(t *testing.T) {
	t.Parallel()

	type args struct {
		dst []*nodeapis.TopologyZone
		src []*nodeapis.TopologyZone
	}
	tests := []struct {
		name string
		args args
		want []*nodeapis.TopologyZone
	}{
		{
			name: "merge two topology zone",
			args: args{
				src: []*nodeapis.TopologyZone{
					{
						Type: nodeapis.TopologyTypeSocket,
						Name: "0",
						Children: []*nodeapis.TopologyZone{
							{
								Type: nodeapis.TopologyTypeNuma,
								Name: "0",
								Resources: nodeapis.Resources{
									Capacity: &v1.ResourceList{
										"gpu": resource.MustParse("2"),
									},
									Allocatable: &v1.ResourceList{
										"gpu": resource.MustParse("2"),
									},
								},
								Allocations: []*nodeapis.Allocation{
									{
										Consumer: "default/pod-1/pod-1-uid",
										Requests: &v1.ResourceList{
											"gpu": resource.MustParse("1"),
										},
									},
									{
										Consumer: "default/pod-2/pod-2-uid",
										Requests: &v1.ResourceList{
											"gpu": resource.MustParse("1"),
										},
									},
									{
										Consumer: "default/pod-3/pod-3-uid",
										Requests: &v1.ResourceList{
											"gpu": resource.MustParse("1"),
										},
									},
								},
								Children: []*nodeapis.TopologyZone{
									{
										Type: nodeapis.TopologyTypeNIC,
										Name: "eth0",
										Resources: nodeapis.Resources{
											Capacity: &v1.ResourceList{
												"nic": resource.MustParse("10G"),
											},
											Allocatable: &v1.ResourceList{
												"nic": resource.MustParse("10G"),
											},
										},
										Allocations: []*nodeapis.Allocation{
											{
												Consumer: "default/pod-2/pod-2-uid",
												Requests: &v1.ResourceList{
													"nic": resource.MustParse("10G"),
												},
											},
										},
									},
								},
							},
						},
					},
					{
						Type: nodeapis.TopologyTypeSocket,
						Name: "1",
						Children: []*nodeapis.TopologyZone{
							{
								Type: nodeapis.TopologyTypeNuma,
								Name: "1",
								Resources: nodeapis.Resources{
									Capacity: &v1.ResourceList{
										"cpu":    resource.MustParse("24"),
										"memory": resource.MustParse("32G"),
									},
									Allocatable: &v1.ResourceList{
										"cpu":    resource.MustParse("24"),
										"memory": resource.MustParse("32G"),
									},
								},
								Allocations: []*nodeapis.Allocation{
									{
										Consumer: "default/pod-1/pod-1-uid",
										Requests: &v1.ResourceList{
											"cpu":    resource.MustParse("15"),
											"memory": resource.MustParse("15G"),
										},
									},
									{
										Consumer: "default/pod-2/pod-2-uid",
										Requests: &v1.ResourceList{
											"gpu":    resource.MustParse("1"),
											"cpu":    resource.MustParse("24"),
											"memory": resource.MustParse("32G"),
										},
									},
									{
										Consumer: "default/pod-3/pod-3-uid",
										Requests: &v1.ResourceList{
											"gpu":    resource.MustParse("1"),
											"cpu":    resource.MustParse("24"),
											"memory": resource.MustParse("32G"),
										},
									},
								},
								Children: []*nodeapis.TopologyZone{
									{
										Type: nodeapis.TopologyTypeNIC,
										Name: "eth1",
										Resources: nodeapis.Resources{
											Capacity: &v1.ResourceList{
												"nic": resource.MustParse("10G"),
											},
											Allocatable: &v1.ResourceList{
												"nic": resource.MustParse("10G"),
											},
										},
									},
								},
							},
						},
					},
				},
				dst: []*nodeapis.TopologyZone{
					{
						Type: nodeapis.TopologyTypeSocket,
						Name: "0",
						Children: []*nodeapis.TopologyZone{
							{
								Type: nodeapis.TopologyTypeNuma,
								Name: "0",
								Resources: nodeapis.Resources{
									Capacity: &v1.ResourceList{
										"cpu":    resource.MustParse("24"),
										"memory": resource.MustParse("32G"),
									},
									Allocatable: &v1.ResourceList{
										"cpu":    resource.MustParse("24"),
										"memory": resource.MustParse("32G"),
									},
								},
								Allocations: []*nodeapis.Allocation{
									{
										Consumer: "default/pod-1/pod-1-uid",
										Requests: &v1.ResourceList{
											"cpu":    resource.MustParse("12"),
											"memory": resource.MustParse("12G"),
										},
									},
									{
										Consumer: "default/pod-2/pod-2-uid",
										Requests: &v1.ResourceList{
											"cpu":    resource.MustParse("24"),
											"memory": resource.MustParse("32G"),
										},
									},
									{
										Consumer: "default/pod-3/pod-3-uid",
										Requests: &v1.ResourceList{
											"cpu":    resource.MustParse("24"),
											"memory": resource.MustParse("32G"),
										},
									},
								},
							},
						},
					},
					{
						Type: nodeapis.TopologyTypeSocket,
						Name: "1",
						Children: []*nodeapis.TopologyZone{
							{
								Type: nodeapis.TopologyTypeNuma,
								Name: "1",
								Resources: nodeapis.Resources{
									Capacity: &v1.ResourceList{
										"cpu":    resource.MustParse("24"),
										"memory": resource.MustParse("32G"),
									},
									Allocatable: &v1.ResourceList{
										"cpu":    resource.MustParse("24"),
										"memory": resource.MustParse("32G"),
									},
								},
								Allocations: []*nodeapis.Allocation{
									{
										Consumer: "default/pod-1/pod-1-uid",
										Requests: &v1.ResourceList{
											"cpu":    resource.MustParse("15"),
											"memory": resource.MustParse("15G"),
										},
									},
									{
										Consumer: "default/pod-2/pod-2-uid",
										Requests: &v1.ResourceList{
											"cpu":    resource.MustParse("24"),
											"memory": resource.MustParse("32G"),
										},
									},
									{
										Consumer: "default/pod-3/pod-3-uid",
										Requests: &v1.ResourceList{
											"cpu":    resource.MustParse("24"),
											"memory": resource.MustParse("32G"),
										},
									},
								},
							},
						},
					},
				},
			},
			want: []*nodeapis.TopologyZone{
				{
					Type: nodeapis.TopologyTypeSocket,
					Name: "0",
					Children: []*nodeapis.TopologyZone{
						{
							Type: nodeapis.TopologyTypeNuma,
							Name: "0",
							Resources: nodeapis.Resources{
								Capacity: &v1.ResourceList{
									"gpu":    resource.MustParse("2"),
									"cpu":    resource.MustParse("24"),
									"memory": resource.MustParse("32G"),
								},
								Allocatable: &v1.ResourceList{
									"gpu":    resource.MustParse("2"),
									"cpu":    resource.MustParse("24"),
									"memory": resource.MustParse("32G"),
								},
							},
							Allocations: []*nodeapis.Allocation{
								{
									Consumer: "default/pod-1/pod-1-uid",
									Requests: &v1.ResourceList{
										"gpu":    resource.MustParse("1"),
										"cpu":    resource.MustParse("12"),
										"memory": resource.MustParse("12G"),
									},
								},
								{
									Consumer: "default/pod-2/pod-2-uid",
									Requests: &v1.ResourceList{
										"gpu":    resource.MustParse("1"),
										"cpu":    resource.MustParse("24"),
										"memory": resource.MustParse("32G"),
									},
								},
								{
									Consumer: "default/pod-3/pod-3-uid",
									Requests: &v1.ResourceList{
										"gpu":    resource.MustParse("1"),
										"cpu":    resource.MustParse("24"),
										"memory": resource.MustParse("32G"),
									},
								},
							},
							Children: []*nodeapis.TopologyZone{
								{
									Type: nodeapis.TopologyTypeNIC,
									Name: "eth0",
									Resources: nodeapis.Resources{
										Capacity: &v1.ResourceList{
											"nic": resource.MustParse("10G"),
										},
										Allocatable: &v1.ResourceList{
											"nic": resource.MustParse("10G"),
										},
									},
									Allocations: []*nodeapis.Allocation{
										{
											Consumer: "default/pod-2/pod-2-uid",
											Requests: &v1.ResourceList{
												"nic": resource.MustParse("10G"),
											},
										},
									},
								},
							},
						},
					},
				},
				{
					Type: nodeapis.TopologyTypeSocket,
					Name: "1",
					Children: []*nodeapis.TopologyZone{
						{
							Type: nodeapis.TopologyTypeNuma,
							Name: "1",
							Resources: nodeapis.Resources{
								Capacity: &v1.ResourceList{
									"cpu":    resource.MustParse("24"),
									"memory": resource.MustParse("32G"),
								},
								Allocatable: &v1.ResourceList{
									"cpu":    resource.MustParse("24"),
									"memory": resource.MustParse("32G"),
								},
							},
							Allocations: []*nodeapis.Allocation{
								{
									Consumer: "default/pod-1/pod-1-uid",
									Requests: &v1.ResourceList{
										"cpu":    resource.MustParse("15"),
										"memory": resource.MustParse("15G"),
									},
								},
								{
									Consumer: "default/pod-2/pod-2-uid",
									Requests: &v1.ResourceList{
										"gpu":    resource.MustParse("1"),
										"cpu":    resource.MustParse("24"),
										"memory": resource.MustParse("32G"),
									},
								},
								{
									Consumer: "default/pod-3/pod-3-uid",
									Requests: &v1.ResourceList{
										"gpu":    resource.MustParse("1"),
										"cpu":    resource.MustParse("24"),
										"memory": resource.MustParse("32G"),
									},
								},
							},
							Children: []*nodeapis.TopologyZone{
								{
									Type: nodeapis.TopologyTypeNIC,
									Name: "eth1",
									Resources: nodeapis.Resources{
										Capacity: &v1.ResourceList{
											"nic": resource.MustParse("10G"),
										},
										Allocatable: &v1.ResourceList{
											"nic": resource.MustParse("10G"),
										},
									},
								},
							},
						},
					},
				},
			},
		},
		{
			name: "merge two topology zone with siblings",
			args: args{
				src: []*nodeapis.TopologyZone{
					{
						Type: nodeapis.TopologyTypeSocket,
						Name: "0",
						Children: []*nodeapis.TopologyZone{
							{
								Type: nodeapis.TopologyTypeNuma,
								Name: "0",
								Resources: nodeapis.Resources{
									Capacity: &v1.ResourceList{
										"gpu":    resource.MustParse("2"),
										"cpu":    resource.MustParse("24"),
										"memory": resource.MustParse("32G"),
									},
									Allocatable: &v1.ResourceList{
										"gpu":    resource.MustParse("2"),
										"cpu":    resource.MustParse("24"),
										"memory": resource.MustParse("32G"),
									},
								},
								Allocations: []*nodeapis.Allocation{
									{
										Consumer: "default/pod-1/pod-1-uid",
										Requests: &v1.ResourceList{
											"gpu":    resource.MustParse("1"),
											"cpu":    resource.MustParse("12"),
											"memory": resource.MustParse("12G"),
										},
									},
									{
										Consumer: "default/pod-2/pod-2-uid",
										Requests: &v1.ResourceList{
											"gpu":    resource.MustParse("1"),
											"cpu":    resource.MustParse("24"),
											"memory": resource.MustParse("32G"),
										},
									},
									{
										Consumer: "default/pod-3/pod-3-uid",
										Requests: &v1.ResourceList{
											"gpu":    resource.MustParse("1"),
											"cpu":    resource.MustParse("24"),
											"memory": resource.MustParse("32G"),
										},
									},
								},
								Children: []*nodeapis.TopologyZone{
									{
										Type: nodeapis.TopologyTypeNIC,
										Name: "eth0",
										Resources: nodeapis.Resources{
											Capacity: &v1.ResourceList{
												"nic": resource.MustParse("10G"),
											},
											Allocatable: &v1.ResourceList{
												"nic": resource.MustParse("10G"),
											},
										},
										Allocations: []*nodeapis.Allocation{
											{
												Consumer: "default/pod-2/pod-2-uid",
												Requests: &v1.ResourceList{
													"nic": resource.MustParse("10G"),
												},
											},
										},
									},
								},
								Siblings: []nodeapis.Sibling{
									{
										Type: nodeapis.TopologyTypeNuma,
										Name: "1",
									},
								},
							},
							{
								Type: nodeapis.TopologyTypeNuma,
								Name: "1",
								Resources: nodeapis.Resources{
									Capacity: &v1.ResourceList{
										"gpu":    resource.MustParse("2"),
										"cpu":    resource.MustParse("24"),
										"memory": resource.MustParse("32G"),
									},
									Allocatable: &v1.ResourceList{
										"gpu":    resource.MustParse("2"),
										"cpu":    resource.MustParse("24"),
										"memory": resource.MustParse("32G"),
									},
								},
								Allocations: []*nodeapis.Allocation{
									{
										Consumer: "default/pod-1/pod-1-uid",
										Requests: &v1.ResourceList{
											"gpu":    resource.MustParse("1"),
											"cpu":    resource.MustParse("12"),
											"memory": resource.MustParse("12G"),
										},
									},
									{
										Consumer: "default/pod-2/pod-2-uid",
										Requests: &v1.ResourceList{
											"gpu":    resource.MustParse("1"),
											"cpu":    resource.MustParse("24"),
											"memory": resource.MustParse("32G"),
										},
									},
									{
										Consumer: "default/pod-3/pod-3-uid",
										Requests: &v1.ResourceList{
											"gpu":    resource.MustParse("1"),
											"cpu":    resource.MustParse("24"),
											"memory": resource.MustParse("32G"),
										},
									},
								},
								Children: []*nodeapis.TopologyZone{
									{
										Type: nodeapis.TopologyTypeNIC,
										Name: "eth0",
										Resources: nodeapis.Resources{
											Capacity: &v1.ResourceList{
												"nic": resource.MustParse("10G"),
											},
											Allocatable: &v1.ResourceList{
												"nic": resource.MustParse("10G"),
											},
										},
										Allocations: []*nodeapis.Allocation{
											{
												Consumer: "default/pod-2/pod-2-uid",
												Requests: &v1.ResourceList{
													"nic": resource.MustParse("10G"),
												},
											},
										},
									},
								},
								Siblings: []nodeapis.Sibling{
									{
										Type: nodeapis.TopologyTypeNuma,
										Name: "0",
									},
								},
							},
						},
					},
					{
						Type: nodeapis.TopologyTypeSocket,
						Name: "1",
						Children: []*nodeapis.TopologyZone{
							{
								Type: nodeapis.TopologyTypeNuma,
								Name: "2",
								Resources: nodeapis.Resources{
									Capacity: &v1.ResourceList{
										"cpu":    resource.MustParse("24"),
										"memory": resource.MustParse("32G"),
									},
									Allocatable: &v1.ResourceList{
										"cpu":    resource.MustParse("24"),
										"memory": resource.MustParse("32G"),
									},
								},
								Allocations: []*nodeapis.Allocation{
									{
										Consumer: "default/pod-1/pod-1-uid",
										Requests: &v1.ResourceList{
											"cpu":    resource.MustParse("15"),
											"memory": resource.MustParse("15G"),
										},
									},
									{
										Consumer: "default/pod-2/pod-2-uid",
										Requests: &v1.ResourceList{
											"gpu":    resource.MustParse("1"),
											"cpu":    resource.MustParse("24"),
											"memory": resource.MustParse("32G"),
										},
									},
									{
										Consumer: "default/pod-3/pod-3-uid",
										Requests: &v1.ResourceList{
											"gpu":    resource.MustParse("1"),
											"cpu":    resource.MustParse("24"),
											"memory": resource.MustParse("32G"),
										},
									},
								},
								Children: []*nodeapis.TopologyZone{
									{
										Type: nodeapis.TopologyTypeNIC,
										Name: "eth1",
										Resources: nodeapis.Resources{
											Capacity: &v1.ResourceList{
												"nic": resource.MustParse("10G"),
											},
											Allocatable: &v1.ResourceList{
												"nic": resource.MustParse("10G"),
											},
										},
									},
								},
								Siblings: []nodeapis.Sibling{
									{
										Type: nodeapis.TopologyTypeNuma,
										Name: "3",
									},
								},
							},
							{
								Type: nodeapis.TopologyTypeNuma,
								Name: "3",
								Resources: nodeapis.Resources{
									Capacity: &v1.ResourceList{
										"cpu":    resource.MustParse("24"),
										"memory": resource.MustParse("32G"),
									},
									Allocatable: &v1.ResourceList{
										"cpu":    resource.MustParse("24"),
										"memory": resource.MustParse("32G"),
									},
								},
								Allocations: []*nodeapis.Allocation{
									{
										Consumer: "default/pod-1/pod-1-uid",
										Requests: &v1.ResourceList{
											"cpu":    resource.MustParse("15"),
											"memory": resource.MustParse("15G"),
										},
									},
									{
										Consumer: "default/pod-2/pod-2-uid",
										Requests: &v1.ResourceList{
											"gpu":    resource.MustParse("1"),
											"cpu":    resource.MustParse("24"),
											"memory": resource.MustParse("32G"),
										},
									},
									{
										Consumer: "default/pod-3/pod-3-uid",
										Requests: &v1.ResourceList{
											"gpu":    resource.MustParse("1"),
											"cpu":    resource.MustParse("24"),
											"memory": resource.MustParse("32G"),
										},
									},
								},
								Children: []*nodeapis.TopologyZone{
									{
										Type: nodeapis.TopologyTypeNIC,
										Name: "eth1",
										Resources: nodeapis.Resources{
											Capacity: &v1.ResourceList{
												"nic": resource.MustParse("10G"),
											},
											Allocatable: &v1.ResourceList{
												"nic": resource.MustParse("10G"),
											},
										},
									},
								},
								Siblings: []nodeapis.Sibling{
									{
										Type: nodeapis.TopologyTypeNuma,
										Name: "2",
									},
								},
							},
						},
					},
				},
				dst: []*nodeapis.TopologyZone{
					{
						Type: nodeapis.TopologyTypeSocket,
						Name: "0",
						Children: []*nodeapis.TopologyZone{
							{
								Type: nodeapis.TopologyTypeNuma,
								Name: "0",
								Siblings: []nodeapis.Sibling{
									{
										Type: nodeapis.TopologyTypeNuma,
										Name: "1",
										Attributes: []nodeapis.Attribute{
											{
												Name:  "aa",
												Value: "bb",
											},
										},
									},
								},
							},
							{
								Type: nodeapis.TopologyTypeNuma,
								Name: "1",
								Siblings: []nodeapis.Sibling{
									{
										Type: nodeapis.TopologyTypeNuma,
										Name: "0",
										Attributes: []nodeapis.Attribute{
											{
												Name:  "aa",
												Value: "bb",
											},
										},
									},
								},
							},
						},
					},
					{
						Type: nodeapis.TopologyTypeSocket,
						Name: "1",
						Children: []*nodeapis.TopologyZone{
							{
								Type: nodeapis.TopologyTypeNuma,
								Name: "2",
								Siblings: []nodeapis.Sibling{
									{
										Type: nodeapis.TopologyTypeNuma,
										Name: "3",
										Attributes: []nodeapis.Attribute{
											{
												Name:  "aa",
												Value: "bb",
											},
										},
									},
								},
							},
							{
								Type: nodeapis.TopologyTypeNuma,
								Name: "3",
								Siblings: []nodeapis.Sibling{
									{
										Type: nodeapis.TopologyTypeNuma,
										Name: "2",
										Attributes: []nodeapis.Attribute{
											{
												Name:  "aa",
												Value: "bb",
											},
										},
									},
								},
							},
						},
					},
				},
			},
			want: []*nodeapis.TopologyZone{
				{
					Type: nodeapis.TopologyTypeSocket,
					Name: "0",
					Children: []*nodeapis.TopologyZone{
						{
							Type: nodeapis.TopologyTypeNuma,
							Name: "0",
							Resources: nodeapis.Resources{
								Capacity: &v1.ResourceList{
									"gpu":    resource.MustParse("2"),
									"cpu":    resource.MustParse("24"),
									"memory": resource.MustParse("32G"),
								},
								Allocatable: &v1.ResourceList{
									"gpu":    resource.MustParse("2"),
									"cpu":    resource.MustParse("24"),
									"memory": resource.MustParse("32G"),
								},
							},
							Allocations: []*nodeapis.Allocation{
								{
									Consumer: "default/pod-1/pod-1-uid",
									Requests: &v1.ResourceList{
										"gpu":    resource.MustParse("1"),
										"cpu":    resource.MustParse("12"),
										"memory": resource.MustParse("12G"),
									},
								},
								{
									Consumer: "default/pod-2/pod-2-uid",
									Requests: &v1.ResourceList{
										"gpu":    resource.MustParse("1"),
										"cpu":    resource.MustParse("24"),
										"memory": resource.MustParse("32G"),
									},
								},
								{
									Consumer: "default/pod-3/pod-3-uid",
									Requests: &v1.ResourceList{
										"gpu":    resource.MustParse("1"),
										"cpu":    resource.MustParse("24"),
										"memory": resource.MustParse("32G"),
									},
								},
							},
							Children: []*nodeapis.TopologyZone{
								{
									Type: nodeapis.TopologyTypeNIC,
									Name: "eth0",
									Resources: nodeapis.Resources{
										Capacity: &v1.ResourceList{
											"nic": resource.MustParse("10G"),
										},
										Allocatable: &v1.ResourceList{
											"nic": resource.MustParse("10G"),
										},
									},
									Allocations: []*nodeapis.Allocation{
										{
											Consumer: "default/pod-2/pod-2-uid",
											Requests: &v1.ResourceList{
												"nic": resource.MustParse("10G"),
											},
										},
									},
								},
							},
							Siblings: []nodeapis.Sibling{
								{
									Type: nodeapis.TopologyTypeNuma,
									Name: "1",
									Attributes: []nodeapis.Attribute{
										{
											Name:  "aa",
											Value: "bb",
										},
									},
								},
							},
						},
						{
							Type: nodeapis.TopologyTypeNuma,
							Name: "1",
							Resources: nodeapis.Resources{
								Capacity: &v1.ResourceList{
									"gpu":    resource.MustParse("2"),
									"cpu":    resource.MustParse("24"),
									"memory": resource.MustParse("32G"),
								},
								Allocatable: &v1.ResourceList{
									"gpu":    resource.MustParse("2"),
									"cpu":    resource.MustParse("24"),
									"memory": resource.MustParse("32G"),
								},
							},
							Allocations: []*nodeapis.Allocation{
								{
									Consumer: "default/pod-1/pod-1-uid",
									Requests: &v1.ResourceList{
										"gpu":    resource.MustParse("1"),
										"cpu":    resource.MustParse("12"),
										"memory": resource.MustParse("12G"),
									},
								},
								{
									Consumer: "default/pod-2/pod-2-uid",
									Requests: &v1.ResourceList{
										"gpu":    resource.MustParse("1"),
										"cpu":    resource.MustParse("24"),
										"memory": resource.MustParse("32G"),
									},
								},
								{
									Consumer: "default/pod-3/pod-3-uid",
									Requests: &v1.ResourceList{
										"gpu":    resource.MustParse("1"),
										"cpu":    resource.MustParse("24"),
										"memory": resource.MustParse("32G"),
									},
								},
							},
							Children: []*nodeapis.TopologyZone{
								{
									Type: nodeapis.TopologyTypeNIC,
									Name: "eth0",
									Resources: nodeapis.Resources{
										Capacity: &v1.ResourceList{
											"nic": resource.MustParse("10G"),
										},
										Allocatable: &v1.ResourceList{
											"nic": resource.MustParse("10G"),
										},
									},
									Allocations: []*nodeapis.Allocation{
										{
											Consumer: "default/pod-2/pod-2-uid",
											Requests: &v1.ResourceList{
												"nic": resource.MustParse("10G"),
											},
										},
									},
								},
							},
							Siblings: []nodeapis.Sibling{
								{
									Type: nodeapis.TopologyTypeNuma,
									Name: "0",
									Attributes: []nodeapis.Attribute{
										{
											Name:  "aa",
											Value: "bb",
										},
									},
								},
							},
						},
					},
				},
				{
					Type: nodeapis.TopologyTypeSocket,
					Name: "1",
					Children: []*nodeapis.TopologyZone{
						{
							Type: nodeapis.TopologyTypeNuma,
							Name: "2",
							Resources: nodeapis.Resources{
								Capacity: &v1.ResourceList{
									"cpu":    resource.MustParse("24"),
									"memory": resource.MustParse("32G"),
								},
								Allocatable: &v1.ResourceList{
									"cpu":    resource.MustParse("24"),
									"memory": resource.MustParse("32G"),
								},
							},
							Allocations: []*nodeapis.Allocation{
								{
									Consumer: "default/pod-1/pod-1-uid",
									Requests: &v1.ResourceList{
										"cpu":    resource.MustParse("15"),
										"memory": resource.MustParse("15G"),
									},
								},
								{
									Consumer: "default/pod-2/pod-2-uid",
									Requests: &v1.ResourceList{
										"gpu":    resource.MustParse("1"),
										"cpu":    resource.MustParse("24"),
										"memory": resource.MustParse("32G"),
									},
								},
								{
									Consumer: "default/pod-3/pod-3-uid",
									Requests: &v1.ResourceList{
										"gpu":    resource.MustParse("1"),
										"cpu":    resource.MustParse("24"),
										"memory": resource.MustParse("32G"),
									},
								},
							},
							Children: []*nodeapis.TopologyZone{
								{
									Type: nodeapis.TopologyTypeNIC,
									Name: "eth1",
									Resources: nodeapis.Resources{
										Capacity: &v1.ResourceList{
											"nic": resource.MustParse("10G"),
										},
										Allocatable: &v1.ResourceList{
											"nic": resource.MustParse("10G"),
										},
									},
								},
							},
							Siblings: []nodeapis.Sibling{
								{
									Type: nodeapis.TopologyTypeNuma,
									Name: "3",
									Attributes: []nodeapis.Attribute{
										{
											Name:  "aa",
											Value: "bb",
										},
									},
								},
							},
						},
						{
							Type: nodeapis.TopologyTypeNuma,
							Name: "3",
							Resources: nodeapis.Resources{
								Capacity: &v1.ResourceList{
									"cpu":    resource.MustParse("24"),
									"memory": resource.MustParse("32G"),
								},
								Allocatable: &v1.ResourceList{
									"cpu":    resource.MustParse("24"),
									"memory": resource.MustParse("32G"),
								},
							},
							Allocations: []*nodeapis.Allocation{
								{
									Consumer: "default/pod-1/pod-1-uid",
									Requests: &v1.ResourceList{
										"cpu":    resource.MustParse("15"),
										"memory": resource.MustParse("15G"),
									},
								},
								{
									Consumer: "default/pod-2/pod-2-uid",
									Requests: &v1.ResourceList{
										"gpu":    resource.MustParse("1"),
										"cpu":    resource.MustParse("24"),
										"memory": resource.MustParse("32G"),
									},
								},
								{
									Consumer: "default/pod-3/pod-3-uid",
									Requests: &v1.ResourceList{
										"gpu":    resource.MustParse("1"),
										"cpu":    resource.MustParse("24"),
										"memory": resource.MustParse("32G"),
									},
								},
							},
							Children: []*nodeapis.TopologyZone{
								{
									Type: nodeapis.TopologyTypeNIC,
									Name: "eth1",
									Resources: nodeapis.Resources{
										Capacity: &v1.ResourceList{
											"nic": resource.MustParse("10G"),
										},
										Allocatable: &v1.ResourceList{
											"nic": resource.MustParse("10G"),
										},
									},
								},
							},
							Siblings: []nodeapis.Sibling{
								{
									Type: nodeapis.TopologyTypeNuma,
									Name: "2",
									Attributes: []nodeapis.Attribute{
										{
											Name:  "aa",
											Value: "bb",
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			expect := MergeTopologyZone(tt.args.dst, tt.args.src)
			assert.Equalf(t, tt.want, expect, "MergeTopologyZone(%v, %v)", tt.args.dst, tt.args.src)
		})
	}
}
