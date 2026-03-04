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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"
	"k8s.io/utils/ptr"

	apis "github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/config/agent"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/adminqos"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/adminqos/finegrainedresource"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/qrm"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	resourcepackage "github.com/kubewharf/katalyst-core/pkg/util/resource-package"
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
		{
			name: "GetCoresReservedForSystem with reverse order",
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
									CPUDynamicPolicyConfig: qrm.CPUDynamicPolicyConfig{
										EnableReserveCPUReversely: true,
									},
								},
							},
						},
					},
				},
				metaServer:  &metaserver.MetaServer{},
				machineInfo: machineInfo,
			},
			want:    machine.NewCPUSet(9, 11, 13, 15),
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
		regenerate     bool
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
				regenerate: false,
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
			if got := RegenerateHints(tt.args.allocationInfo, tt.args.regenerate); !reflect.DeepEqual(got, tt.want) {
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

func TestGetPodCPUBurstPolicy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                 string
		pod                  *v1.Pod
		conf                 *config.Configuration
		adminQoSConfig       *adminqos.AdminQoSConfiguration
		isSoleSharedCoresPod bool
		wantPolicy           string
		wantErr              bool
	}{
		{
			name: "test GetPodCPUBurstPolicy with nil pod",
			pod:  nil,
			conf: func() *config.Configuration {
				c := config.NewConfiguration()
				c.QoSConfiguration = generic.NewQoSConfiguration()
				return c
			}(),
			adminQoSConfig: &adminqos.AdminQoSConfiguration{
				FineGrainedResourceConfiguration: &finegrainedresource.FineGrainedResourceConfiguration{
					CPUBurstConfiguration: &finegrainedresource.CPUBurstConfiguration{
						EnableDedicatedCoresDefaultCPUBurst: ptr.To(false),
						DefaultCPUBurstPercent:              100,
					},
				},
			},
			wantErr: true,
		},
		{
			name: "test GetPodCPUBurstPolicy with nil QoSConfiguration",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test",
					Namespace: "test",
					Annotations: map[string]string{
						consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
					},
				},
			},
			conf: func() *config.Configuration { c := config.NewConfiguration(); c.QoSConfiguration = nil; return c }(),
			adminQoSConfig: &adminqos.AdminQoSConfiguration{
				FineGrainedResourceConfiguration: &finegrainedresource.FineGrainedResourceConfiguration{
					CPUBurstConfiguration: &finegrainedresource.CPUBurstConfiguration{
						EnableDedicatedCoresDefaultCPUBurst: ptr.To(false),
						DefaultCPUBurstPercent:              100,
					},
				},
			},
			wantErr: true,
		},
		{
			name: "test GetPodCPUBurstPolicy for shared pod with static policy",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test",
					Namespace: "test",
					Annotations: map[string]string{
						consts.PodAnnotationQoSLevelKey:       consts.PodAnnotationQoSLevelSharedCores,
						consts.PodAnnotationCPUEnhancementKey: `{"cpu_burst_policy":"static"}`,
					},
				},
			},
			conf: func() *config.Configuration {
				c := config.NewConfiguration()
				c.QoSConfiguration = generic.NewQoSConfiguration()
				return c
			}(),
			wantPolicy: consts.PodAnnotationCPUEnhancementCPUBurstPolicyStatic,
		},
		{
			name: "test GetPodCPUBurstPolicy for shared pod with closed policy",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test",
					Namespace: "test",
					Annotations: map[string]string{
						consts.PodAnnotationQoSLevelKey:       consts.PodAnnotationQoSLevelSharedCores,
						consts.PodAnnotationCPUEnhancementKey: `{"cpu_burst_policy":"closed"}`,
					},
				},
			},
			conf: func() *config.Configuration {
				c := config.NewConfiguration()
				c.QoSConfiguration = generic.NewQoSConfiguration()
				return c
			}(),
			wantPolicy: consts.PodAnnotationCPUEnhancementCPUBurstPolicyClosed,
		},
		{
			name: "test GetPodCPUBurstPolicy for shared pod with no policy",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test",
					Namespace: "test",
					Annotations: map[string]string{
						consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
					},
				},
			},
			conf: func() *config.Configuration {
				c := config.NewConfiguration()
				c.QoSConfiguration = generic.NewQoSConfiguration()
				return c
			}(),
			wantPolicy: consts.PodAnnotationCPUEnhancementCPUBurstPolicyDefault,
		},
		{
			name: "test GetPodCPUBurstPolicy for dedicated pod with no policy but aqc enabled returns static",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test",
					Namespace: "test",
					Annotations: map[string]string{
						consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
					},
				},
			},
			conf: func() *config.Configuration {
				c := config.NewConfiguration()
				c.QoSConfiguration = generic.NewQoSConfiguration()
				return c
			}(),
			adminQoSConfig: &adminqos.AdminQoSConfiguration{
				FineGrainedResourceConfiguration: &finegrainedresource.FineGrainedResourceConfiguration{
					CPUBurstConfiguration: &finegrainedresource.CPUBurstConfiguration{
						EnableDedicatedCoresDefaultCPUBurst: ptr.To(true),
						DefaultCPUBurstPercent:              100,
					},
				},
			},
			wantPolicy: consts.PodAnnotationCPUEnhancementCPUBurstPolicyStatic,
		},
		{
			name: "test GetPodCPUBurstPolicy for dedicated pod with no policy but aqc disabled returns closed",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test",
					Namespace: "test",
					Annotations: map[string]string{
						consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
					},
				},
			},
			conf: func() *config.Configuration {
				c := config.NewConfiguration()
				c.QoSConfiguration = generic.NewQoSConfiguration()
				return c
			}(),
			adminQoSConfig: &adminqos.AdminQoSConfiguration{
				FineGrainedResourceConfiguration: &finegrainedresource.FineGrainedResourceConfiguration{
					CPUBurstConfiguration: &finegrainedresource.CPUBurstConfiguration{
						EnableDedicatedCoresDefaultCPUBurst: ptr.To(false),
						DefaultCPUBurstPercent:              100,
					},
				},
			},
			wantPolicy: consts.PodAnnotationCPUEnhancementCPUBurstPolicyClosed,
		},
		{
			name: "test GetPodCPUBurstPolicy for shared cores with no policy with aqc enabled and pod is the sole shared cores pod in node",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test",
					Namespace: "test",
					Annotations: map[string]string{
						consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
					},
				},
			},
			conf: func() *config.Configuration {
				c := config.NewConfiguration()
				c.QoSConfiguration = generic.NewQoSConfiguration()
				return c
			}(),
			adminQoSConfig: &adminqos.AdminQoSConfiguration{
				FineGrainedResourceConfiguration: &finegrainedresource.FineGrainedResourceConfiguration{
					CPUBurstConfiguration: &finegrainedresource.CPUBurstConfiguration{
						EnableSharedCoresDefaultCPUBurst: ptr.To(true),
						DefaultCPUBurstPercent:           100,
					},
				},
			},
			isSoleSharedCoresPod: true,
			wantPolicy:           consts.PodAnnotationCPUEnhancementCPUBurstPolicyStatic,
		},
		{
			name: "test GetPodCPUBurstPolicy for shared cores with no policy with aqc enabled but pod is not the sole shared cores pod in node",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test",
					Namespace: "test",
					Annotations: map[string]string{
						consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
					},
				},
			},
			conf: func() *config.Configuration {
				c := config.NewConfiguration()
				c.QoSConfiguration = generic.NewQoSConfiguration()
				return c
			}(),
			adminQoSConfig: &adminqos.AdminQoSConfiguration{
				FineGrainedResourceConfiguration: &finegrainedresource.FineGrainedResourceConfiguration{
					CPUBurstConfiguration: &finegrainedresource.CPUBurstConfiguration{
						EnableSharedCoresDefaultCPUBurst: ptr.To(true),
						DefaultCPUBurstPercent:           100,
					},
				},
			},
			isSoleSharedCoresPod: false,
			wantPolicy:           consts.PodAnnotationCPUEnhancementCPUBurstPolicyClosed,
		},
		{
			name: "test GetPodCPUBurstPolicy for shared cores with aqc disabled",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test",
					Namespace: "test",
					Annotations: map[string]string{
						consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
					},
				},
			},
			conf: func() *config.Configuration {
				c := config.NewConfiguration()
				c.QoSConfiguration = generic.NewQoSConfiguration()
				return c
			}(),
			adminQoSConfig: &adminqos.AdminQoSConfiguration{
				FineGrainedResourceConfiguration: &finegrainedresource.FineGrainedResourceConfiguration{
					CPUBurstConfiguration: &finegrainedresource.CPUBurstConfiguration{
						EnableSharedCoresDefaultCPUBurst: ptr.To(false),
						DefaultCPUBurstPercent:           100,
					},
				},
			},
			isSoleSharedCoresPod: true,
			wantPolicy:           consts.PodAnnotationCPUEnhancementCPUBurstPolicyClosed,
		},
		{
			name: "test GetPodCPUBurstPolicy for shared cores with aqc enabled and pod already has static policy from annotation",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test",
					Namespace: "test",
					Annotations: map[string]string{
						consts.PodAnnotationQoSLevelKey:       consts.PodAnnotationQoSLevelSharedCores,
						consts.PodAnnotationCPUEnhancementKey: `{"cpu_burst_policy":"static"}`,
					},
				},
			},
			conf: func() *config.Configuration {
				c := config.NewConfiguration()
				c.QoSConfiguration = generic.NewQoSConfiguration()
				return c
			}(),
			adminQoSConfig: &adminqos.AdminQoSConfiguration{
				FineGrainedResourceConfiguration: &finegrainedresource.FineGrainedResourceConfiguration{
					CPUBurstConfiguration: &finegrainedresource.CPUBurstConfiguration{
						EnableSharedCoresDefaultCPUBurst: ptr.To(false),
						DefaultCPUBurstPercent:           100,
					},
				},
			},
			isSoleSharedCoresPod: true,
			wantPolicy:           consts.PodAnnotationCPUEnhancementCPUBurstPolicyStatic,
		},
		{
			name: "test GetPodCPUBurstPolicy for dedicated pod with no policy overridden by core conf to static",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test",
					Namespace: "test",
					Annotations: map[string]string{
						consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
					},
				},
			},
			conf: func() *config.Configuration {
				c := config.NewConfiguration()
				c.QoSConfiguration = generic.NewQoSConfiguration()
				c.EnableDefaultDedicatedCoresCPUBurst = true
				return c
			}(),
			wantPolicy: consts.PodAnnotationCPUEnhancementCPUBurstPolicyStatic,
		},
		{
			name: "test GetPodCPUBurstPolicy for shared pod overridden by core conf requires sole=true to static",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test",
					Namespace: "test",
					Annotations: map[string]string{
						consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
					},
				},
			},
			conf: func() *config.Configuration {
				c := config.NewConfiguration()
				c.QoSConfiguration = generic.NewQoSConfiguration()
				c.EnableDefaultSharedCoresCPUBurst = true
				return c
			}(),
			isSoleSharedCoresPod: true,
			wantPolicy:           consts.PodAnnotationCPUEnhancementCPUBurstPolicyStatic,
		},
		{
			name: "test GetPodCPUBurstPolicy for shared pod overridden by core conf but sole=false returns closed",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test",
					Namespace: "test",
					Annotations: map[string]string{
						consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
					},
				},
			},
			conf: func() *config.Configuration {
				c := config.NewConfiguration()
				c.QoSConfiguration = generic.NewQoSConfiguration()
				c.EnableDefaultSharedCoresCPUBurst = true
				return c
			}(),
			isSoleSharedCoresPod: false,
			wantPolicy:           consts.PodAnnotationCPUEnhancementCPUBurstPolicyClosed,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dynamicConfig := dynamic.NewDynamicAgentConfiguration()
			if tt.adminQoSConfig != nil {
				dynamicConfig.SetDynamicConfiguration(&dynamic.Configuration{
					AdminQoSConfiguration: tt.adminQoSConfig,
				})
			}

			gotPolicy, err := GetPodCPUBurstPolicy(tt.conf, tt.pod, dynamicConfig, tt.isSoleSharedCoresPod)

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.wantPolicy, gotPolicy)
			}
		})
	}
}

func TestGetPodCPUBurstPercent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		pod            *v1.Pod
		qosConfig      *generic.QoSConfiguration
		adminQoSConfig *adminqos.AdminQoSConfiguration
		wantPercent    float64
		wantErr        bool
	}{
		{
			name: "test GetPodCPUBurstPercent with nil qosConfig",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test",
					Namespace: "test",
					Annotations: map[string]string{
						consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
					},
				},
			},
			qosConfig: nil,
			wantErr:   true,
		},
		{
			name:      "test GetPodCPUBurstPercent with nil pod",
			pod:       nil,
			qosConfig: generic.NewQoSConfiguration(),
			wantErr:   true,
		},
		{
			name: "test GetPodCPUBurstPercent for pod from cpu enhancement",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test",
					Namespace: "test",
					Annotations: map[string]string{
						consts.PodAnnotationQoSLevelKey:       consts.PodAnnotationQoSLevelSharedCores,
						consts.PodAnnotationCPUEnhancementKey: `{"cpu_burst_percent":"100""}`,
					},
				},
			},
			qosConfig:   generic.NewQoSConfiguration(),
			wantPercent: 100,
		},
		{
			name: "test GetPodCPUBurstPercent for pod from dynamic config",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test",
					Namespace: "test",
					Annotations: map[string]string{
						consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
					},
				},
			},
			qosConfig: generic.NewQoSConfiguration(),
			adminQoSConfig: &adminqos.AdminQoSConfiguration{
				FineGrainedResourceConfiguration: &finegrainedresource.FineGrainedResourceConfiguration{
					CPUBurstConfiguration: &finegrainedresource.CPUBurstConfiguration{
						EnableDedicatedCoresDefaultCPUBurst: ptr.To(false),
						DefaultCPUBurstPercent:              80,
					},
				},
			},
			wantPercent: 80,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dynamicConfig := dynamic.NewDynamicAgentConfiguration()
			if tt.adminQoSConfig != nil {
				dynamicConfig.SetDynamicConfiguration(&dynamic.Configuration{
					AdminQoSConfiguration: tt.adminQoSConfig,
				})
			}

			gotPercent, err := GetPodCPUBurstPercent(tt.qosConfig, tt.pod, dynamicConfig)

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.wantPercent, gotPercent)
			}
		})
	}
}

// generatePod returns a pod with the given name and QoS level annotation.
func generatePod(name, qosLevel string) *v1.Pod {
	return &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Annotations: map[string]string{
				consts.PodAnnotationQoSLevelKey: qosLevel,
			},
		},
	}
}

func TestIsSoleSharedCoresPod(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		conf           *config.Configuration
		podList        []*v1.Pod
		adminQoSConfig *adminqos.AdminQoSConfiguration
		expected       bool
	}{
		{
			name: "EnableSharedCoresDefaultCPUBurst is nil",
			conf: func() *config.Configuration {
				c := config.NewConfiguration()
				c.QoSConfiguration = generic.NewQoSConfiguration()
				return c
			}(),
			podList: []*v1.Pod{
				generatePod("pod-1", consts.PodAnnotationQoSLevelSharedCores),
			},
			expected: false,
		},
		{
			name: "EnableSharedCoresDefaultCPUBurst is false",
			conf: func() *config.Configuration {
				c := config.NewConfiguration()
				c.QoSConfiguration = generic.NewQoSConfiguration()
				return c
			}(),
			podList: []*v1.Pod{
				generatePod("pod-1", consts.PodAnnotationQoSLevelSharedCores),
			},
			adminQoSConfig: &adminqos.AdminQoSConfiguration{
				FineGrainedResourceConfiguration: &finegrainedresource.FineGrainedResourceConfiguration{
					CPUBurstConfiguration: &finegrainedresource.CPUBurstConfiguration{
						EnableSharedCoresDefaultCPUBurst: ptr.To(false),
						DefaultCPUBurstPercent:           100,
					},
				},
			},
			expected: false,
		},
		{
			name: "Empty pod list",
			conf: func() *config.Configuration {
				c := config.NewConfiguration()
				c.QoSConfiguration = generic.NewQoSConfiguration()
				return c
			}(),
			podList: []*v1.Pod{},
			adminQoSConfig: &adminqos.AdminQoSConfiguration{
				FineGrainedResourceConfiguration: &finegrainedresource.FineGrainedResourceConfiguration{
					CPUBurstConfiguration: &finegrainedresource.CPUBurstConfiguration{
						EnableSharedCoresDefaultCPUBurst: ptr.To(true),
						DefaultCPUBurstPercent:           100,
					},
				},
			},
			expected: false,
		},
		{
			name: "No shared cores pods",
			conf: func() *config.Configuration {
				c := config.NewConfiguration()
				c.QoSConfiguration = generic.NewQoSConfiguration()
				return c
			}(),
			podList: []*v1.Pod{
				generatePod("pod-1", consts.PodAnnotationQoSLevelDedicatedCores),
				generatePod("pod-2", consts.PodAnnotationQoSLevelDedicatedCores),
			},
			adminQoSConfig: &adminqos.AdminQoSConfiguration{
				FineGrainedResourceConfiguration: &finegrainedresource.FineGrainedResourceConfiguration{
					CPUBurstConfiguration: &finegrainedresource.CPUBurstConfiguration{
						EnableSharedCoresDefaultCPUBurst: ptr.To(true),
						DefaultCPUBurstPercent:           100,
					},
				},
			},
			expected: false,
		},
		{
			name: "One shared cores pod",
			conf: func() *config.Configuration {
				c := config.NewConfiguration()
				c.QoSConfiguration = generic.NewQoSConfiguration()
				return c
			}(),
			podList: []*v1.Pod{
				generatePod("pod-1", consts.PodAnnotationQoSLevelSharedCores),
			},
			adminQoSConfig: &adminqos.AdminQoSConfiguration{
				FineGrainedResourceConfiguration: &finegrainedresource.FineGrainedResourceConfiguration{
					CPUBurstConfiguration: &finegrainedresource.CPUBurstConfiguration{
						EnableSharedCoresDefaultCPUBurst: ptr.To(true),
						DefaultCPUBurstPercent:           100,
					},
				},
			},
			expected: true,
		},
		{
			name: "Multiple shared cores pods",
			conf: func() *config.Configuration {
				c := config.NewConfiguration()
				c.QoSConfiguration = generic.NewQoSConfiguration()
				return c
			}(),
			podList: []*v1.Pod{
				generatePod("pod-1", consts.PodAnnotationQoSLevelSharedCores),
				generatePod("pod-2", consts.PodAnnotationQoSLevelSharedCores),
			},
			adminQoSConfig: &adminqos.AdminQoSConfiguration{
				FineGrainedResourceConfiguration: &finegrainedresource.FineGrainedResourceConfiguration{
					CPUBurstConfiguration: &finegrainedresource.CPUBurstConfiguration{
						EnableSharedCoresDefaultCPUBurst: ptr.To(true),
						DefaultCPUBurstPercent:           100,
					},
				},
			},
			expected: false,
		},
		{
			name: "Mixed QoS levels, one shared cores pod",
			conf: func() *config.Configuration {
				c := config.NewConfiguration()
				c.QoSConfiguration = generic.NewQoSConfiguration()
				return c
			}(),
			podList: []*v1.Pod{
				generatePod("pod-1", consts.PodAnnotationQoSLevelDedicatedCores),
				generatePod("pod-2", consts.PodAnnotationQoSLevelSharedCores),
				generatePod("pod-3", consts.PodAnnotationQoSLevelSystemCores),
			},
			adminQoSConfig: &adminqos.AdminQoSConfiguration{
				FineGrainedResourceConfiguration: &finegrainedresource.FineGrainedResourceConfiguration{
					CPUBurstConfiguration: &finegrainedresource.CPUBurstConfiguration{
						EnableSharedCoresDefaultCPUBurst: ptr.To(true),
						DefaultCPUBurstPercent:           100,
					},
				},
			},
			expected: true,
		},
		{
			name: "Mixed QoS levels, multiple shared cores pods",
			conf: func() *config.Configuration {
				c := config.NewConfiguration()
				c.QoSConfiguration = generic.NewQoSConfiguration()
				return c
			}(),
			podList: []*v1.Pod{
				generatePod("pod-1", consts.PodAnnotationQoSLevelDedicatedCores),
				generatePod("pod-2", consts.PodAnnotationQoSLevelSharedCores),
				generatePod("pod-3", consts.PodAnnotationQoSLevelSharedCores),
			},
			adminQoSConfig: &adminqos.AdminQoSConfiguration{
				FineGrainedResourceConfiguration: &finegrainedresource.FineGrainedResourceConfiguration{
					CPUBurstConfiguration: &finegrainedresource.CPUBurstConfiguration{
						EnableSharedCoresDefaultCPUBurst: ptr.To(true),
						DefaultCPUBurstPercent:           100,
					},
				},
			},
			expected: false,
		},
		// Core conf gating: calculate when conf.EnableDefaultSharedCoresCPUBurst is true
		{
			name: "Core conf true: one shared cores pod",
			conf: func() *config.Configuration {
				c := config.NewConfiguration()
				c.QoSConfiguration = generic.NewQoSConfiguration()
				c.EnableDefaultSharedCoresCPUBurst = true
				return c
			}(),
			podList: []*v1.Pod{
				generatePod("pod-1", consts.PodAnnotationQoSLevelSharedCores),
			},
			expected: true,
		},
		{
			name: "Core conf true: multiple shared cores pods",
			conf: func() *config.Configuration {
				c := config.NewConfiguration()
				c.QoSConfiguration = generic.NewQoSConfiguration()
				c.EnableDefaultSharedCoresCPUBurst = true
				return c
			}(),
			podList: []*v1.Pod{
				generatePod("pod-1", consts.PodAnnotationQoSLevelSharedCores),
				generatePod("pod-2", consts.PodAnnotationQoSLevelSharedCores),
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dynamicConfig := dynamic.NewDynamicAgentConfiguration()
			if tt.adminQoSConfig != nil {
				dynamicConfig.SetDynamicConfiguration(&dynamic.Configuration{
					AdminQoSConfiguration: tt.adminQoSConfig,
				})
			}

			actual := IsSoleSharedCoresPod(tt.conf, tt.podList, dynamicConfig)
			assert.Equal(t, tt.expected, actual)
		})
	}
}

func TestGetAggResourcePackagePinnedCPUSet(t *testing.T) {
	t.Parallel()

	type args struct {
		resourcePackageMap resourcepackage.NUMAResourcePackageItems
		attributeSelector  labels.Selector
		machineState       state.NUMANodeMap
	}
	tests := []struct {
		name string
		args args
		want machine.CPUSet
	}{
		{
			name: "empty resource package map",
			args: args{
				resourcePackageMap: resourcepackage.NUMAResourcePackageItems{},
				attributeSelector:  labels.Everything(),
				machineState:       state.NUMANodeMap{},
			},
			want: machine.NewCPUSet(),
		},
		{
			name: "resource package matches selector",
			args: args{
				resourcePackageMap: resourcepackage.NUMAResourcePackageItems{
					0: {
						"pkg1": {
							ResourcePackage: apis.ResourcePackage{
								Attributes: []apis.Attribute{
									{
										Name:  "key1",
										Value: "value1",
									},
								},
							},
						},
					},
				},
				attributeSelector: labels.SelectorFromSet(labels.Set{"key1": "value1"}),
				machineState: state.NUMANodeMap{
					0: {
						ResourcePackagePinnedCPUSet: map[string]machine.CPUSet{
							"pkg1": machine.NewCPUSet(1, 2),
						},
					},
				},
			},
			want: machine.NewCPUSet(1, 2),
		},
		{
			name: "resource package does not match selector",
			args: args{
				resourcePackageMap: resourcepackage.NUMAResourcePackageItems{
					0: {
						"pkg1": {
							ResourcePackage: apis.ResourcePackage{
								Attributes: []apis.Attribute{
									{
										Name:  "key1",
										Value: "value1",
									},
								},
							},
						},
					},
				},
				attributeSelector: labels.SelectorFromSet(labels.Set{"key1": "value2"}),
				machineState: state.NUMANodeMap{
					0: {
						ResourcePackagePinnedCPUSet: map[string]machine.CPUSet{
							"pkg1": machine.NewCPUSet(1, 2),
						},
					},
				},
			},
			want: machine.NewCPUSet(),
		},
		{
			name: "missing machine state",
			args: args{
				resourcePackageMap: resourcepackage.NUMAResourcePackageItems{
					0: {
						"pkg1": {
							ResourcePackage: apis.ResourcePackage{
								Attributes: []apis.Attribute{
									{
										Name:  "key1",
										Value: "value1",
									},
								},
							},
						},
					},
				},
				attributeSelector: labels.SelectorFromSet(labels.Set{"key1": "value1"}),
				machineState:      state.NUMANodeMap{},
			},
			want: machine.NewCPUSet(),
		},
		{
			name: "multiple numa nodes and packages",
			args: args{
				resourcePackageMap: resourcepackage.NUMAResourcePackageItems{
					0: {
						"pkg1": {
							ResourcePackage: apis.ResourcePackage{
								Attributes: []apis.Attribute{
									{
										Name:  "type",
										Value: "A",
									},
								},
							},
						},
					},
					1: {
						"pkg2": {
							ResourcePackage: apis.ResourcePackage{
								Attributes: []apis.Attribute{
									{
										Name:  "type",
										Value: "A",
									},
								},
							},
						},
						"pkg3": {
							ResourcePackage: apis.ResourcePackage{
								Attributes: []apis.Attribute{
									{
										Name:  "type",
										Value: "B",
									},
								},
							},
						},
					},
				},
				attributeSelector: labels.SelectorFromSet(labels.Set{"type": "A"}),
				machineState: state.NUMANodeMap{
					0: {
						ResourcePackagePinnedCPUSet: map[string]machine.CPUSet{
							"pkg1": machine.NewCPUSet(0, 1),
						},
					},
					1: {
						ResourcePackagePinnedCPUSet: map[string]machine.CPUSet{
							"pkg2": machine.NewCPUSet(2, 3),
							"pkg3": machine.NewCPUSet(4, 5),
						},
					},
				},
			},
			want: machine.NewCPUSet(0, 1, 2, 3),
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := GetAggResourcePackagePinnedCPUSet(tt.args.resourcePackageMap, tt.args.attributeSelector, tt.args.machineState)
			if !got.Equals(tt.want) {
				t.Errorf("GetAggResourcePackagePinnedCPUSet() = %v, want %v", got, tt.want)
			}
		})
	}
}
