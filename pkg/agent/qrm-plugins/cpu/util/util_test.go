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
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/config/agent"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/qrm"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func TestGetCoresReservedForSystem(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	assert.Nil(t, err)
	machineInfo := &machine.KatalystMachineInfo{
		CPUTopology: topology,
	}

	type args struct {
		conf        *config.Configuration
		metaServer  *metaserver.MetaServer
		machineInfo *machine.KatalystMachineInfo
		allCPUs     machine.CPUSet
	}
	tests := []struct {
		name    string
		args    args
		want    machine.CPUSet
		wantErr bool
	}{
		{
			name:    "GetCoresReservedForSystem with nil conf",
			want:    machine.NewCPUSet(),
			wantErr: true,
		},
		{
			name: "GetCoresReservedForSystem with nil metaServer",
			args: args{
				conf: &config.Configuration{
					AgentConfiguration: &agent.AgentConfiguration{
						GenericAgentConfiguration: &agent.GenericAgentConfiguration{
							GenericQRMPluginConfiguration: &qrm.GenericQRMPluginConfiguration{},
						},
					},
				},
				machineInfo: &machine.KatalystMachineInfo{},
			},
			want:    machine.NewCPUSet(),
			wantErr: true,
		},
		{
			name: "GetCoresReservedForSystem with nil machineInfo",
			args: args{
				conf: &config.Configuration{
					AgentConfiguration: &agent.AgentConfiguration{
						GenericAgentConfiguration: &agent.GenericAgentConfiguration{
							GenericQRMPluginConfiguration: &qrm.GenericQRMPluginConfiguration{},
						},
					},
				},
				metaServer: &metaserver.MetaServer{},
			},
			want:    machine.NewCPUSet(),
			wantErr: true,
		},
		{
			name: "GetCoresReservedForSystem with conf",
			args: args{
				allCPUs: topology.CPUDetails.CPUs(),
				conf: &config.Configuration{
					AgentConfiguration: &agent.AgentConfiguration{
						GenericAgentConfiguration: &agent.GenericAgentConfiguration{
							GenericQRMPluginConfiguration: &qrm.GenericQRMPluginConfiguration{},
						},
						StaticAgentConfiguration: &agent.StaticAgentConfiguration{
							QRMPluginsConfiguration: &qrm.QRMPluginsConfiguration{
								CPUQRMPluginConfig: &qrm.CPUQRMPluginConfig{
									ReservedCPUCores: 4,
								},
							},
						},
					},
				},
				metaServer:  &metaserver.MetaServer{},
				machineInfo: machineInfo,
			},
			want:    machine.NewCPUSet(0, 2, 4, 6),
			wantErr: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := GetCoresReservedForSystem(tt.args.conf, tt.args.metaServer, tt.args.machineInfo, tt.args.allCPUs)
			if (err != nil) != tt.wantErr {
				t.Errorf("GetCoresReservedForSystem() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("GetCoresReservedForSystem() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRegenerateHints(t *testing.T) {
	t.Parallel()

	type args struct {
		allocationInfo *state.AllocationInfo
		reqInt         int
	}
	tests := []struct {
		name string
		args args
		want map[string]*pluginapi.ListOfTopologyHints
	}{
		{
			name: "test RegenerateHints",
			args: args{
				allocationInfo: &state.AllocationInfo{
					AllocationMeta: commonstate.AllocationMeta{
						PodUid:         "test",
						PodNamespace:   "test",
						PodName:        "test",
						ContainerName:  "test",
						ContainerType:  pluginapi.ContainerType_MAIN.String(),
						ContainerIndex: 0,
						OwnerPoolName:  commonstate.PoolNameDedicated,
						Labels: map[string]string{
							consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
						},
						Annotations: map[string]string{
							consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelDedicatedCores,
							consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
						},
						QoSLevel: consts.PodAnnotationQoSLevelDedicatedCores,
					},
					RampUp:                   false,
					AllocationResult:         machine.NewCPUSet(1, 3, 8, 9, 10, 11),
					OriginalAllocationResult: machine.NewCPUSet(1, 3, 8, 9, 10, 11),
					TopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.NewCPUSet(1, 8, 9),
						1: machine.NewCPUSet(3, 10, 11),
					},
					OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.NewCPUSet(1, 8, 9),
						1: machine.NewCPUSet(3, 10, 11),
					},
					RequestQuantity: 2,
				},
				reqInt: 2,
			},
			want: map[string]*pluginapi.ListOfTopologyHints{
				string(v1.ResourceCPU): {
					Hints: []*pluginapi.TopologyHint{
						{
							Nodes:     []uint64{0, 1},
							Preferred: true,
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
			if got := RegenerateHints(tt.args.allocationInfo, tt.args.reqInt); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("RegenerateHints() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPackAllocationResponse(t *testing.T) {
	t.Parallel()

	type args struct {
		allocationInfo   *state.AllocationInfo
		resourceName     string
		ociPropertyName  string
		isNodeResource   bool
		isScalarResource bool
		req              *pluginapi.ResourceRequest
	}
	tests := []struct {
		name    string
		args    args
		want    *pluginapi.ResourceAllocationResponse
		wantErr bool
	}{
		{
			name:    "test PackAllocationResponse with nil allocationInfo",
			args:    args{},
			want:    nil,
			wantErr: true,
		},
		{
			name: "test PackAllocationResponse with nil req",
			args: args{
				allocationInfo: &state.AllocationInfo{},
			},
			want:    nil,
			wantErr: true,
		},
		{
			name: "test PackAllocationResponse",
			args: args{
				allocationInfo: &state.AllocationInfo{
					AllocationMeta: commonstate.AllocationMeta{
						PodUid:         "test",
						PodNamespace:   "test",
						PodName:        "test",
						ContainerName:  "test",
						ContainerType:  pluginapi.ContainerType_MAIN.String(),
						ContainerIndex: 0,
						OwnerPoolName:  commonstate.PoolNameDedicated,
						Labels: map[string]string{
							consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
						},
						Annotations: map[string]string{
							consts.PodAnnotationQoSLevelKey:                  consts.PodAnnotationQoSLevelDedicatedCores,
							consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
						},
						QoSLevel: consts.PodAnnotationQoSLevelDedicatedCores,
					},
					RampUp:                   false,
					AllocationResult:         machine.NewCPUSet(1, 3, 8, 9, 10, 11),
					OriginalAllocationResult: machine.NewCPUSet(1, 3, 8, 9, 10, 11),
					TopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.NewCPUSet(1, 8, 9),
						1: machine.NewCPUSet(3, 10, 11),
					},
					OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.NewCPUSet(1, 8, 9),
						1: machine.NewCPUSet(3, 10, 11),
					},
					RequestQuantity: 2,
				},
				resourceName:     string(v1.ResourceCPU),
				ociPropertyName:  util.OCIPropertyNameCPUSetCPUs,
				isNodeResource:   false,
				isScalarResource: true,
				req: &pluginapi.ResourceRequest{
					PodUid:         "test",
					PodNamespace:   "test",
					PodName:        "test",
					ContainerName:  "test",
					ContainerType:  pluginapi.ContainerType_MAIN,
					ContainerIndex: 0,
				},
			},
			want: &pluginapi.ResourceAllocationResponse{
				PodUid:         "test",
				PodNamespace:   "test",
				PodName:        "test",
				ContainerName:  "test",
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(v1.ResourceCPU),
				AllocationResult: &pluginapi.ResourceAllocation{
					ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{
						string(v1.ResourceCPU): {
							OciPropertyName:   util.OCIPropertyNameCPUSetCPUs,
							IsNodeResource:    false,
							IsScalarResource:  true,
							AllocatedQuantity: float64(6),
							AllocationResult:  machine.NewCPUSet(1, 3, 8, 9, 10, 11).String(),
							ResourceHints: &pluginapi.ListOfTopologyHints{
								Hints: []*pluginapi.TopologyHint{
									nil,
								},
							},
						},
					},
				},
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := PackAllocationResponse(tt.args.allocationInfo, tt.args.resourceName, tt.args.ociPropertyName, tt.args.isNodeResource, tt.args.isScalarResource, tt.args.req)
			if (err != nil) != tt.wantErr {
				t.Errorf("PackAllocationResponse() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("PackAllocationResponse() = %v, want %v", got, tt.want)
			}
		})
	}
}
