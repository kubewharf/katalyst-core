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

package policy

import (
	rawContext "context"
	"math"
	"path/filepath"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/kubernetes/fake"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"
	"k8s.io/kubernetes/pkg/kubelet/cri/remote"

	apinode "github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	katalystbase "github.com/kubewharf/katalyst-core/cmd/base"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/agent"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/sriov/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/sriov/utils"
	"github.com/kubewharf/katalyst-core/pkg/client"
	"github.com/kubewharf/katalyst-core/pkg/config"
	qrmconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/qrm"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	metaserveragent "github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	metricspool "github.com/kubewharf/katalyst-core/pkg/metrics/metrics-pool"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"

	. "github.com/bytedance/mockey"
)

func generateStaticPolicy(t *testing.T, dryRun bool, bondingHostNetwork bool, vfState state.VFState, podEntries state.PodEntries) *StaticPolicy {
	basePolicy := generateBasePolicy(t, dryRun, bondingHostNetwork, vfState, podEntries)

	return &StaticPolicy{
		name:       "sriov",
		basePolicy: basePolicy,
		emitter:    metrics.DummyMetrics{},
		policyConfig: qrmconfig.SriovStaticPolicyConfig{
			MinBondingVFQueueCount: 32,
			MaxBondingVFQueueCount: math.MaxInt32,
		},
	}
}

func TestStaticPolicy_New(t *testing.T) {
	PatchConvey("NewStaticPolicy", t, func() {
		conf := config.NewConfiguration()

		tmpDir := t.TempDir()
		conf.GenericQRMPluginConfiguration.StateDirectoryConfiguration.StateFileDirectory = filepath.Join(tmpDir, "state_file")
		conf.GenericQRMPluginConfiguration.StateDirectoryConfiguration.InMemoryStateFileDirectory = filepath.Join(tmpDir, "state_memory")

		agentCtx := &agent.GenericContext{
			GenericContext: &katalystbase.GenericContext{
				EmitterPool: metricspool.DummyMetricsEmitterPool{},
				Client: &client.GenericClientSet{
					KubeClient: fake.NewSimpleClientset(),
				},
			},
			MetaServer: &metaserver.MetaServer{
				MetaAgent: &metaserveragent.MetaAgent{
					KatalystMachineInfo: &machine.KatalystMachineInfo{
						ExtraNetworkInfo: &machine.ExtraNetworkInfo{
							Interface: []machine.InterfaceInfo{},
						},
					},
				},
			},
			PluginManager: nil,
		}

		Mock(remote.NewRemoteRuntimeService).Return(nil, nil).Build()

		shouldRun, _, err := NewStaticPolicy(agentCtx, conf, nil, "sriov")

		So(err, ShouldBeNil)
		So(shouldRun, ShouldBeTrue)
	})
}

func TestStaticPolicy_GetTopologyHints(t *testing.T) {
	t.Parallel()

	Convey("both socket has available VFs", t, func() {
		vfState, podEntries := state.GenerateDummyState(2, 2, nil)
		policy := generateStaticPolicy(t, false, false, vfState, podEntries)

		Mock(utils.UpdateSriovVFResultAnnotation).Return(nil).Build()

		resp, err := policy.GetTopologyHints(rawContext.Background(),
			&pluginapi.ResourceRequest{
				PodUid:        "pod",
				PodName:       "pod",
				ContainerName: "container",
				ResourceName:  policy.ResourceName(),
				ResourceRequests: map[string]float64{
					ResourceName: 1,
				},
			},
		)

		So(err, ShouldBeNil)
		So(resp, ShouldResemble, &pluginapi.ResourceHintsResponse{
			PodUid:        "pod",
			PodName:       "pod",
			ContainerName: "container",
			ResourceName:  ResourceName,
			ResourceHints: map[string]*pluginapi.ListOfTopologyHints{
				policy.ResourceName(): {
					Hints: []*pluginapi.TopologyHint{
						{
							Nodes:     []uint64{0, 1},
							Preferred: true,
						},
						{
							Nodes:     []uint64{2, 3},
							Preferred: true,
						},
					},
				},
			},
		})
	})

	Convey("only one socket has available VFs", t, func() {
		vfState, podEntries := state.GenerateDummyState(2, 2, map[int]sets.Int{
			1: sets.NewInt(0, 1),
		})
		policy := generateStaticPolicy(t, false, false, vfState, podEntries)

		resp, err := policy.GetTopologyHints(rawContext.Background(),
			&pluginapi.ResourceRequest{
				PodUid:        "pod",
				PodName:       "pod",
				ContainerName: "container",
				ResourceRequests: map[string]float64{
					ResourceName: 1,
				},
			},
		)

		So(err, ShouldBeNil)
		So(resp, ShouldResemble, &pluginapi.ResourceHintsResponse{
			PodUid:        "pod",
			PodName:       "pod",
			ContainerName: "container",
			ResourceName:  ResourceName,
			ResourceHints: map[string]*pluginapi.ListOfTopologyHints{
				policy.ResourceName(): {
					Hints: []*pluginapi.TopologyHint{
						{
							Nodes:     []uint64{0, 1},
							Preferred: true,
						},
					},
				},
			},
		})
	})

	Convey("no available VFs", t, func() {
		vfState, podEntries := state.GenerateDummyState(2, 2, map[int]sets.Int{
			0: sets.NewInt(0, 1),
			1: sets.NewInt(0, 1),
		})

		policy := generateStaticPolicy(t, false, false, vfState, podEntries)

		resp, err := policy.GetTopologyHints(rawContext.Background(),
			&pluginapi.ResourceRequest{
				PodUid:        "pod",
				PodName:       "pod",
				ContainerName: "container",
				ResourceRequests: map[string]float64{
					ResourceName: 1,
				},
			},
		)

		So(resp, ShouldBeNil)
		So(err, ShouldNotBeNil)
		So(err.Error(), ShouldEqual, "no available VFs")
	})

	Convey("dryRun", t, func() {
		vfState, podEntries := state.GenerateDummyState(2, 2, nil)

		policy := generateStaticPolicy(t, true, false, vfState, podEntries)

		resp, err := policy.GetTopologyHints(rawContext.Background(),
			&pluginapi.ResourceRequest{
				PodUid:        "pod",
				PodName:       "pod",
				ContainerName: "container",
				ResourceRequests: map[string]float64{
					ResourceName: 1,
				},
			},
		)

		So(err, ShouldBeNil)
		So(resp, ShouldResemble, &pluginapi.ResourceHintsResponse{
			PodUid:        "pod",
			PodName:       "pod",
			ContainerName: "container",
			ResourceName:  ResourceName,
		})
	})
}

func TestStaticPolicy_GetTopologyAwareResources(t *testing.T) {
	t.Parallel()

	Convey("pod exists", t, func() {
		vfState, podEntries := state.GenerateDummyState(2, 2, map[int]sets.Int{
			0: sets.NewInt(0, 1),
			1: sets.NewInt(0, 1),
		})
		policy := generateStaticPolicy(t, false, false, vfState, podEntries)

		resp, err := policy.GetTopologyAwareResources(rawContext.Background(),
			&pluginapi.GetTopologyAwareResourcesRequest{
				PodUid:        "pod2",
				ContainerName: "container2",
			},
		)

		So(err, ShouldBeNil)
		So(resp, ShouldResemble, &pluginapi.GetTopologyAwareResourcesResponse{
			PodName: "pod2",
			PodUid:  "pod2",
			ContainerTopologyAwareResources: &pluginapi.ContainerTopologyAwareResources{
				ContainerName: "container2",
				AllocatedResources: map[string]*pluginapi.TopologyAwareResource{
					policy.ResourceName(): {
						TopologyAwareQuantityList: []*pluginapi.TopologyAwareQuantity{
							{
								ResourceValue: 1,
								Node:          1,
								Name:          "eth1_0",
								Type:          string(apinode.TopologyTypeNIC),
								TopologyLevel: pluginapi.TopologyLevel_SOCKET,
							},
						},
					},
				},
			},
		})
	})

	Convey("pod not exists", t, func() {
		vfState, podEntries := state.GenerateDummyState(2, 2, nil)
		policy := generateStaticPolicy(t, false, false, vfState, podEntries)

		resp, err := policy.GetTopologyAwareResources(rawContext.Background(),
			&pluginapi.GetTopologyAwareResourcesRequest{
				PodUid:        "pod2",
				ContainerName: "container2",
			},
		)

		So(err, ShouldBeNil)
		So(resp, ShouldResemble, &pluginapi.GetTopologyAwareResourcesResponse{})
	})
}

func TestStaticPolicy_GetTopologyAwareAllocatableResources(t *testing.T) {
	t.Parallel()

	Convey("bondingHostNetwork is true", t, func() {
		vfState, podEntries := state.GenerateDummyState(2, 2, nil)
		policy := generateStaticPolicy(t, false, true, vfState, podEntries)

		resp, err := policy.GetTopologyAwareAllocatableResources(rawContext.Background(), &pluginapi.GetTopologyAwareAllocatableResourcesRequest{})

		expectedTopologyAwareQuantityList := []*pluginapi.TopologyAwareQuantity{
			{
				ResourceValue: 1,
				Node:          0,
				Name:          "eth0_1",
				Type:          string(apinode.TopologyTypeNIC),
				TopologyLevel: pluginapi.TopologyLevel_SOCKET,
			},
			{
				ResourceValue: 1,
				Node:          1,
				Name:          "eth1_1",
				Type:          string(apinode.TopologyTypeNIC),
				TopologyLevel: pluginapi.TopologyLevel_SOCKET,
			},
		}

		So(err, ShouldBeNil)
		So(resp, ShouldResemble, &pluginapi.GetTopologyAwareAllocatableResourcesResponse{
			AllocatableResources: map[string]*pluginapi.AllocatableTopologyAwareResource{
				policy.ResourceName(): {
					IsNodeResource:                       true,
					IsScalarResource:                     true,
					AggregatedAllocatableQuantity:        2,
					TopologyAwareAllocatableQuantityList: expectedTopologyAwareQuantityList,
					AggregatedCapacityQuantity:           2,
					TopologyAwareCapacityQuantityList:    expectedTopologyAwareQuantityList,
				},
			},
		})
	})

	Convey("bondingHostNetwork is false", t, func() {
		vfState, podEntries := state.GenerateDummyState(2, 2, nil)
		policy := generateStaticPolicy(t, false, false, vfState, podEntries)

		resp, err := policy.GetTopologyAwareAllocatableResources(rawContext.Background(), &pluginapi.GetTopologyAwareAllocatableResourcesRequest{})

		expectedTopologyAwareQuantityList := []*pluginapi.TopologyAwareQuantity{
			{
				ResourceValue: 1,
				Node:          0,
				Name:          "eth0_0",
				Type:          string(apinode.TopologyTypeNIC),
				TopologyLevel: pluginapi.TopologyLevel_SOCKET,
			},
			{
				ResourceValue: 1,
				Node:          1,
				Name:          "eth1_0",
				Type:          string(apinode.TopologyTypeNIC),
				TopologyLevel: pluginapi.TopologyLevel_SOCKET,
			},
			{
				ResourceValue: 1,
				Node:          0,
				Name:          "eth0_1",
				Type:          string(apinode.TopologyTypeNIC),
				TopologyLevel: pluginapi.TopologyLevel_SOCKET,
			},
			{
				ResourceValue: 1,
				Node:          1,
				Name:          "eth1_1",
				Type:          string(apinode.TopologyTypeNIC),
				TopologyLevel: pluginapi.TopologyLevel_SOCKET,
			},
		}

		So(err, ShouldBeNil)
		So(resp, ShouldResemble, &pluginapi.GetTopologyAwareAllocatableResourcesResponse{
			AllocatableResources: map[string]*pluginapi.AllocatableTopologyAwareResource{
				policy.ResourceName(): {
					IsNodeResource:                       true,
					IsScalarResource:                     true,
					AggregatedAllocatableQuantity:        4,
					TopologyAwareAllocatableQuantityList: expectedTopologyAwareQuantityList,
					AggregatedCapacityQuantity:           4,
					TopologyAwareCapacityQuantityList:    expectedTopologyAwareQuantityList,
				},
			},
		})
	})
}

func TestStaticPolicy_Allocate(t *testing.T) {
	t.Parallel()

	Convey("bondingHostNetwork is false", t, func() {
		vfState, podEntries := state.GenerateDummyState(2, 2, nil)
		policy := generateStaticPolicy(t, false, false, vfState, podEntries)

		resp, err := policy.Allocate(rawContext.Background(),
			&pluginapi.ResourceRequest{
				PodUid:        "pod",
				PodName:       "pod",
				ContainerName: "container",
				ResourceName:  policy.ResourceName(),
				Hint: &pluginapi.TopologyHint{
					Nodes:     []uint64{0, 1},
					Preferred: true,
				},
				ResourceRequests: map[string]float64{
					policy.ResourceName(): 1,
				},
			},
		)

		So(err, ShouldBeNil)
		So(resp, ShouldResemble, &pluginapi.ResourceAllocationResponse{
			PodUid:        "pod",
			PodName:       "pod",
			ContainerName: "container",
			ResourceName:  policy.ResourceName(),
			Labels:        map[string]string{},
			Annotations:   map[string]string{},
			AllocationResult: &pluginapi.ResourceAllocation{
				ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{
					policy.ResourceName(): {
						IsNodeResource:    true,
						IsScalarResource:  true,
						AllocatedQuantity: 1,
						Annotations: map[string]string{
							pciAnnotationKey:   `[{"address":"0000:40:00.0","repName":"eth0_0","vfName":"eth0_0"}]`,
							netNsAnnotationKey: "/var/run/netns/ns2",
						},
						Devices: []*pluginapi.DeviceSpec{
							{
								HostPath:      filepath.Join(rdmaDevicePrefix, "umad0"),
								ContainerPath: filepath.Join(rdmaDevicePrefix, "umad0"),
								Permissions:   "rwm",
							},
							{
								HostPath:      filepath.Join(rdmaDevicePrefix, "uverbs0"),
								ContainerPath: filepath.Join(rdmaDevicePrefix, "uverbs0"),
								Permissions:   "rwm",
							},
							{
								HostPath:      rdmaCmPath,
								ContainerPath: rdmaCmPath,
								Permissions:   "rw",
							},
						},
					},
				},
			},
		})
	})

	Convey("bondingHostNetwork is true", t, func() {
		vfState, podEntries := state.GenerateDummyState(2, 2, nil)
		policy := generateStaticPolicy(t, false, true, vfState, podEntries)

		resp, err := policy.Allocate(rawContext.Background(),
			&pluginapi.ResourceRequest{
				PodUid:        "pod",
				PodName:       "pod",
				ContainerName: "container",
				ResourceName:  policy.ResourceName(),
				Hint: &pluginapi.TopologyHint{
					Nodes:     []uint64{0, 1},
					Preferred: true,
				},
				ResourceRequests: map[string]float64{
					policy.ResourceName(): 1,
				},
			},
		)

		So(err, ShouldBeNil)
		So(resp, ShouldResemble, &pluginapi.ResourceAllocationResponse{
			PodUid:        "pod",
			PodName:       "pod",
			ContainerName: "container",
			ResourceName:  policy.ResourceName(),
			Labels:        map[string]string{},
			Annotations:   map[string]string{},
			AllocationResult: &pluginapi.ResourceAllocation{
				ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{
					policy.ResourceName(): {
						IsNodeResource:    true,
						IsScalarResource:  true,
						AllocatedQuantity: 1,
						Annotations: map[string]string{
							pciAnnotationKey:   `[{"address":"0000:40:00.1","repName":"eth0_1","vfName":"eth0_1"}]`,
							netNsAnnotationKey: "/var/run/netns/ns2",
						},
						Devices: []*pluginapi.DeviceSpec{
							{
								HostPath:      filepath.Join(rdmaDevicePrefix, "umad1"),
								ContainerPath: filepath.Join(rdmaDevicePrefix, "umad1"),
								Permissions:   "rwm",
							},
							{
								HostPath:      filepath.Join(rdmaDevicePrefix, "uverbs1"),
								ContainerPath: filepath.Join(rdmaDevicePrefix, "uverbs1"),
								Permissions:   "rwm",
							},
							{
								HostPath:      rdmaCmPath,
								ContainerPath: rdmaCmPath,
								Permissions:   "rw",
							},
						},
					},
				},
			},
		})
	})

	Convey("dryRun", t, func() {
		vfState, podEntries := state.GenerateDummyState(2, 2, nil)
		policy := generateStaticPolicy(t, true, false, vfState, podEntries)

		resp, err := policy.Allocate(rawContext.Background(),
			&pluginapi.ResourceRequest{
				PodUid:        "pod",
				PodName:       "pod",
				ContainerName: "container",
				ResourceName:  policy.ResourceName(),
				Hint:          &pluginapi.TopologyHint{},
				ResourceRequests: map[string]float64{
					policy.ResourceName(): 1,
				},
			},
		)

		So(err, ShouldBeNil)
		So(resp, ShouldResemble, &pluginapi.ResourceAllocationResponse{
			PodUid:        "pod",
			PodName:       "pod",
			ContainerName: "container",
			ResourceName:  policy.ResourceName(),
			Labels:        map[string]string{},
			Annotations:   map[string]string{},
		})
	})

	Convey("no available VFs", t, func() {
		vfState, podEntries := state.GenerateDummyState(2, 2, map[int]sets.Int{
			0: sets.NewInt(0, 1),
			1: sets.NewInt(0, 1),
		})
		policy := generateStaticPolicy(t, false, false, vfState, podEntries)

		resp, err := policy.Allocate(rawContext.Background(),
			&pluginapi.ResourceRequest{
				PodUid:        "pod",
				PodName:       "pod",
				ContainerName: "container",
				ResourceName:  policy.ResourceName(),
				Hint:          &pluginapi.TopologyHint{},
				ResourceRequests: map[string]float64{
					policy.ResourceName(): 1,
				},
			},
		)

		So(resp, ShouldBeNil)
		So(err, ShouldNotBeNil)
		So(err.Error(), ShouldContainSubstring, "no available VFs")
	})
}

func TestNewStaticPolicy_GetTopologyAwareAllocatableResources(t *testing.T) {
	t.Parallel()

	Convey("bondingHostNetwork is false", t, func() {
		vfState, podEntries := state.GenerateDummyState(2, 2, nil)
		policy := generateStaticPolicy(t, false, false, vfState, podEntries)

		resp, err := policy.GetTopologyAwareAllocatableResources(rawContext.Background(), nil)
		So(err, ShouldBeNil)

		expectedTopologyAwareQuantityList := []*pluginapi.TopologyAwareQuantity{
			{
				ResourceValue: 1,
				Node:          0,
				Name:          "eth0_0",
				Type:          string(apinode.TopologyTypeNIC),
				TopologyLevel: pluginapi.TopologyLevel_SOCKET,
			},
			{
				ResourceValue: 1,
				Node:          1,
				Name:          "eth1_0",
				Type:          string(apinode.TopologyTypeNIC),
				TopologyLevel: pluginapi.TopologyLevel_SOCKET,
			},
			{
				ResourceValue: 1,
				Node:          0,
				Name:          "eth0_1",
				Type:          string(apinode.TopologyTypeNIC),
				TopologyLevel: pluginapi.TopologyLevel_SOCKET,
			},
			{
				ResourceValue: 1,
				Node:          1,
				Name:          "eth1_1",
				Type:          string(apinode.TopologyTypeNIC),
				TopologyLevel: pluginapi.TopologyLevel_SOCKET,
			},
		}

		So(resp, ShouldResemble, &pluginapi.GetTopologyAwareAllocatableResourcesResponse{
			AllocatableResources: map[string]*pluginapi.AllocatableTopologyAwareResource{
				policy.ResourceName(): {
					IsNodeResource:                       true,
					IsScalarResource:                     true,
					AggregatedAllocatableQuantity:        4,
					TopologyAwareAllocatableQuantityList: expectedTopologyAwareQuantityList,
					AggregatedCapacityQuantity:           4,
					TopologyAwareCapacityQuantityList:    expectedTopologyAwareQuantityList,
				},
			},
		})
	})

	Convey("bondingHostNetwork is true", t, func() {
		vfState, podEntries := state.GenerateDummyState(2, 2, nil)
		policy := generateStaticPolicy(t, false, true, vfState, podEntries)

		resp, err := policy.GetTopologyAwareAllocatableResources(rawContext.Background(), nil)
		So(err, ShouldBeNil)

		expectedTopologyAwareQuantityList := []*pluginapi.TopologyAwareQuantity{
			{
				ResourceValue: 1,
				Node:          0,
				Name:          "eth0_1",
				Type:          string(apinode.TopologyTypeNIC),
				TopologyLevel: pluginapi.TopologyLevel_SOCKET,
			},
			{
				ResourceValue: 1,
				Node:          1,
				Name:          "eth1_1",
				Type:          string(apinode.TopologyTypeNIC),
				TopologyLevel: pluginapi.TopologyLevel_SOCKET,
			},
		}

		So(resp, ShouldResemble, &pluginapi.GetTopologyAwareAllocatableResourcesResponse{
			AllocatableResources: map[string]*pluginapi.AllocatableTopologyAwareResource{
				policy.ResourceName(): {
					IsNodeResource:                       true,
					IsScalarResource:                     true,
					AggregatedAllocatableQuantity:        2,
					TopologyAwareAllocatableQuantityList: expectedTopologyAwareQuantityList,
					AggregatedCapacityQuantity:           2,
					TopologyAwareCapacityQuantityList:    expectedTopologyAwareQuantityList,
				},
			},
		})
	})
}
