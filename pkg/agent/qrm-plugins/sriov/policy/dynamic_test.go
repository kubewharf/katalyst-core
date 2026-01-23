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
	"path/filepath"
	"testing"

	. "github.com/bytedance/mockey"
	. "github.com/smartystreets/goconvey/convey"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/kubernetes/fake"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"
	"k8s.io/kubernetes/pkg/kubelet/cri/remote"

	katalystbase "github.com/kubewharf/katalyst-core/cmd/base"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/agent"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/sriov/state"
	"github.com/kubewharf/katalyst-core/pkg/client"
	"github.com/kubewharf/katalyst-core/pkg/config"
	qrmconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/qrm"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	metaserveragent "github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	metricspool "github.com/kubewharf/katalyst-core/pkg/metrics/metrics-pool"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func generateDynamicPolicy(t *testing.T, dryRun bool, bondingHostNetwork bool, vfState state.VFState, podEntries state.PodEntries) *DynamicPolicy {
	basePolicy := generateBasePolicy(t, dryRun, bondingHostNetwork, vfState, podEntries)

	return &DynamicPolicy{
		name:       "sriov",
		basePolicy: basePolicy,
		emitter:    metrics.DummyMetrics{},
		policyConfig: qrmconfig.SriovDynamicPolicyConfig{
			LargeSizeVFQueueCount:       32,
			LargeSizeVFCPUThreshold:     24,
			LargeSizeVFFailOnExhaustion: true,
			SmallSizeVFQueueCount:       8,
			SmallSizeVFCPUThreshold:     8,
			SmallSizeVFFailOnExhaustion: false,
		},
	}
}

func TestDynamicPolicy_New(t *testing.T) {
	PatchConvey("bondingHostNetwork is true", t, func() {
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

		Mock(machine.IsHostNetworkBonding).Return(true, nil).Build()
		Mock(remote.NewRemoteRuntimeService).Return(nil, nil).Build()

		shouldRun, _, err := NewDynamicPolicy(agentCtx, conf, nil, "sriov")

		So(err, ShouldBeNil)
		So(shouldRun, ShouldBeTrue)
	})

	PatchConvey("bondingHostNetwork is false", t, func() {
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

		Mock(machine.IsHostNetworkBonding).Return(false, nil).Build()
		Mock(remote.NewRemoteRuntimeService).Return(nil, nil).Build()

		shouldRun, _, err := NewDynamicPolicy(agentCtx, conf, nil, "sriov")

		So(err, ShouldBeNil)
		So(shouldRun, ShouldBeFalse)
	})
}

func TestDynamicPolicy_GetAccompanyResourceTopologyHints(t *testing.T) {
	t.Parallel()

	Convey("dryRun", t, func() {
		vfState, podEntries := generateState(2, 2, map[int]sets.Int{
			0: sets.NewInt(0, 1),
			1: sets.NewInt(0, 1),
		})
		policy := generateDynamicPolicy(t, true, true, vfState, podEntries)

		req := &pluginapi.ResourceRequest{
			PodUid:        "pod",
			PodName:       "pod",
			ContainerName: "container",
			ResourceName:  string(v1.ResourceCPU),
			ResourceRequests: map[string]float64{
				string(v1.ResourceCPU): 16,
			},
		}

		hints := &pluginapi.ListOfTopologyHints{}

		err := policy.GetAccompanyResourceTopologyHints(req, hints)

		So(err, ShouldBeNil)
		So(hints, ShouldResemble, &pluginapi.ListOfTopologyHints{})
	})

	Convey("hints for aligned vf", t, func() {
		vfState, podEntries := generateState(2, 2, nil)
		policy := generateDynamicPolicy(t, false, true, vfState, podEntries)

		req := &pluginapi.ResourceRequest{
			PodUid:        "pod",
			PodName:       "pod",
			ContainerName: "container",
			ResourceName:  string(v1.ResourceCPU),
			ResourceRequests: map[string]float64{
				string(v1.ResourceCPU): 16,
			},
		}

		hints := &pluginapi.ListOfTopologyHints{
			Hints: []*pluginapi.TopologyHint{
				{
					Nodes:     []uint64{0, 1},
					Preferred: true,
				},
			},
		}

		err := policy.GetAccompanyResourceTopologyHints(req, hints)
		So(err, ShouldBeNil)
		So(hints, ShouldResemble, &pluginapi.ListOfTopologyHints{
			Hints: []*pluginapi.TopologyHint{
				{
					Nodes:     []uint64{0, 1},
					Preferred: true,
				},
			},
		})
	})

	Convey("hints for not-aligned vf", t, func() {
		vfState, podEntries := generateState(2, 2, map[int]sets.Int{
			0: sets.NewInt(0, 1),
		})
		policy := generateDynamicPolicy(t, false, true, vfState, podEntries)

		req := &pluginapi.ResourceRequest{
			PodUid:        "pod",
			PodName:       "pod",
			ContainerName: "container",
			ResourceName:  string(v1.ResourceCPU),
			ResourceRequests: map[string]float64{
				string(v1.ResourceCPU): 16,
			},
		}

		hints := &pluginapi.ListOfTopologyHints{
			Hints: []*pluginapi.TopologyHint{
				{
					Nodes:     []uint64{0, 1},
					Preferred: true,
				},
			},
		}

		err := policy.GetAccompanyResourceTopologyHints(req, hints)

		So(err, ShouldBeNil)
		So(hints, ShouldResemble, &pluginapi.ListOfTopologyHints{
			Hints: []*pluginapi.TopologyHint{
				{
					Nodes:     []uint64{0, 1},
					Preferred: false,
				},
			},
		})
	})

	Convey("no available VFs with small size", t, func() {
		vfState, podEntries := generateState(2, 2, map[int]sets.Int{
			0: sets.NewInt(0, 1),
			1: sets.NewInt(0, 1),
		})
		policy := generateDynamicPolicy(t, false, true, vfState, podEntries)

		req := &pluginapi.ResourceRequest{
			PodUid:        "pod",
			PodName:       "pod",
			ContainerName: "container",
			ResourceName:  string(v1.ResourceCPU),
			ResourceRequests: map[string]float64{
				string(v1.ResourceCPU): 16,
			},
		}

		hints := &pluginapi.ListOfTopologyHints{
			Hints: []*pluginapi.TopologyHint{
				{
					Nodes:     []uint64{0, 1},
					Preferred: true,
				},
			},
		}

		err := policy.GetAccompanyResourceTopologyHints(req, hints)

		So(err, ShouldBeNil)
		So(hints, ShouldResemble, &pluginapi.ListOfTopologyHints{
			Hints: []*pluginapi.TopologyHint{
				{
					Nodes:     []uint64{0, 1},
					Preferred: true,
				},
			},
		})
	})

	Convey("no available VFs with large size", t, func() {
		vfState, podEntries := generateState(2, 2, map[int]sets.Int{
			0: sets.NewInt(0, 1),
			1: sets.NewInt(0, 1),
		})
		policy := generateDynamicPolicy(t, false, true, vfState, podEntries)

		req := &pluginapi.ResourceRequest{
			PodUid:        "pod",
			PodName:       "pod",
			ContainerName: "container",
			ResourceName:  string(v1.ResourceCPU),
			ResourceRequests: map[string]float64{
				string(v1.ResourceCPU): 32,
			},
		}

		hints := &pluginapi.ListOfTopologyHints{
			Hints: []*pluginapi.TopologyHint{
				{
					Nodes:     []uint64{0, 1},
					Preferred: true,
				},
			},
		}

		err := policy.GetAccompanyResourceTopologyHints(req, hints)

		So(err, ShouldNotBeNil)
		So(err.Error(), ShouldEqual, "no available VFs")
	})
}

func TestDynamicPolicy_AllocateAccompanyResource(t *testing.T) {
	t.Parallel()

	Convey("dryRun", t, func() {
		vfState, podEntries := generateState(2, 2, nil)
		policy := generateDynamicPolicy(t, false, true, vfState, podEntries)

		req := &pluginapi.ResourceRequest{
			PodUid:        "pod",
			PodName:       "pod",
			ContainerName: "container",
			ResourceName:  string(v1.ResourceCPU),
			Hint: &pluginapi.TopologyHint{
				Nodes: []uint64{0, 1},
			},
			ResourceRequests: map[string]float64{
				string(v1.ResourceCPU): 32,
			},
		}

		resp := &pluginapi.ResourceAllocationResponse{
			AllocationResult: &pluginapi.ResourceAllocation{
				ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{
					string(v1.ResourceCPU): {
						AllocatedQuantity: 32,
						AllocationResult:  "1-32",
					},
				},
			},
		}

		err := policy.AllocateAccompanyResource(req, resp)
		So(err, ShouldBeNil)
		So(resp.AllocationResult, ShouldResemble, &pluginapi.ResourceAllocation{
			ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{
				string(v1.ResourceCPU): {
					AllocatedQuantity: 32,
					AllocationResult:  "1-32",
				},
				policy.ResourceName(): {
					IsNodeResource:    true,
					IsScalarResource:  true,
					AllocatedQuantity: 1,
					Annotations: map[string]string{
						netNsAnnotationKey: "/var/run/netns/ns2",
						pciAnnotationKey:   `[{"address":"0000:40:00.1","repName":"eth0_1","vfName":"eth0_1"}]`,
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
		})
	})

	Convey("allocate small size vf", t, func() {
		vfState, podEntries := generateState(2, 2, nil)
		policy := generateDynamicPolicy(t, false, true, vfState, podEntries)

		req := &pluginapi.ResourceRequest{
			PodUid:        "pod",
			PodName:       "pod",
			ContainerName: "container",
			ResourceName:  string(v1.ResourceCPU),
			Hint: &pluginapi.TopologyHint{
				Nodes: []uint64{0, 1},
			},
			ResourceRequests: map[string]float64{
				string(v1.ResourceCPU): 16,
			},
		}

		resp := &pluginapi.ResourceAllocationResponse{
			AllocationResult: &pluginapi.ResourceAllocation{
				ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{
					string(v1.ResourceCPU): {
						AllocatedQuantity: 16,
						AllocationResult:  "1-16",
					},
				},
			},
		}

		err := policy.AllocateAccompanyResource(req, resp)
		So(err, ShouldBeNil)
		So(resp.AllocationResult, ShouldResemble, &pluginapi.ResourceAllocation{
			ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{
				string(v1.ResourceCPU): {
					AllocatedQuantity: 16,
					AllocationResult:  "1-16",
				},
				policy.ResourceName(): {
					IsNodeResource:    true,
					IsScalarResource:  true,
					AllocatedQuantity: 1,
					Annotations: map[string]string{
						netNsAnnotationKey: "/var/run/netns/ns2",
						pciAnnotationKey:   `[{"address":"0000:40:00.0","repName":"eth0_0","vfName":"eth0_0"}]`,
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
		})
	})

	Convey("allocate large size vf", t, func() {
		vfState, podEntries := generateState(2, 2, nil)
		policy := generateDynamicPolicy(t, false, true, vfState, podEntries)

		req := &pluginapi.ResourceRequest{
			PodUid:        "pod",
			PodName:       "pod",
			ContainerName: "container",
			ResourceName:  string(v1.ResourceCPU),
			Hint: &pluginapi.TopologyHint{
				Nodes: []uint64{0, 1},
			},
			ResourceRequests: map[string]float64{
				string(v1.ResourceCPU): 32,
			},
		}

		resp := &pluginapi.ResourceAllocationResponse{
			AllocationResult: &pluginapi.ResourceAllocation{
				ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{
					string(v1.ResourceCPU): {
						AllocatedQuantity: 32,
						AllocationResult:  "1-32",
					},
				},
			},
		}

		err := policy.AllocateAccompanyResource(req, resp)
		So(err, ShouldBeNil)
		So(resp.AllocationResult, ShouldResemble, &pluginapi.ResourceAllocation{
			ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{
				string(v1.ResourceCPU): {
					AllocatedQuantity: 32,
					AllocationResult:  "1-32",
				},
				policy.ResourceName(): {
					IsNodeResource:    true,
					IsScalarResource:  true,
					AllocatedQuantity: 1,
					Annotations: map[string]string{
						netNsAnnotationKey: "/var/run/netns/ns2",
						pciAnnotationKey:   `[{"address":"0000:40:00.1","repName":"eth0_1","vfName":"eth0_1"}]`,
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
		})
	})

	Convey("fallback when no available VFs with small size", t, func() {
		vfState, podEntries := generateState(2, 2, map[int]sets.Int{
			0: sets.NewInt(0, 1),
			1: sets.NewInt(0, 1),
		})
		policy := generateDynamicPolicy(t, false, true, vfState, podEntries)

		req := &pluginapi.ResourceRequest{
			PodUid:        "pod",
			PodName:       "pod",
			ContainerName: "container",
			ResourceName:  string(v1.ResourceCPU),
			Hint: &pluginapi.TopologyHint{
				Nodes: []uint64{0, 1},
			},
			ResourceRequests: map[string]float64{
				string(v1.ResourceCPU): 16,
			},
		}

		resp := &pluginapi.ResourceAllocationResponse{
			AllocationResult: &pluginapi.ResourceAllocation{
				ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{
					string(v1.ResourceCPU): {
						AllocatedQuantity: 32,
						AllocationResult:  "1-32",
					},
				},
			},
		}

		err := policy.AllocateAccompanyResource(req, resp)
		So(err, ShouldBeNil)
		So(resp.AllocationResult, ShouldResemble, &pluginapi.ResourceAllocation{
			ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{
				string(v1.ResourceCPU): {
					AllocatedQuantity: 32,
					AllocationResult:  "1-32",
				},
			},
		})
	})

	Convey("fail when no available VFs with large size", t, func() {
		vfState, podEntries := generateState(2, 2, map[int]sets.Int{
			0: sets.NewInt(0, 1),
			1: sets.NewInt(0, 1),
		})
		policy := generateDynamicPolicy(t, false, true, vfState, podEntries)

		req := &pluginapi.ResourceRequest{
			PodUid:        "pod",
			PodName:       "pod",
			ContainerName: "container",
			ResourceName:  string(v1.ResourceCPU),
			Hint: &pluginapi.TopologyHint{
				Nodes: []uint64{0, 1},
			},
			ResourceRequests: map[string]float64{
				string(v1.ResourceCPU): 32,
			},
		}

		resp := &pluginapi.ResourceAllocationResponse{
			AllocationResult: &pluginapi.ResourceAllocation{
				ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{
					string(v1.ResourceCPU): {
						AllocatedQuantity: 32,
						AllocationResult:  "1-32",
					},
				},
			},
		}

		err := policy.AllocateAccompanyResource(req, resp)

		So(err, ShouldNotBeNil)
		So(err.Error(), ShouldContainSubstring, "no available VFs")
	})
}
