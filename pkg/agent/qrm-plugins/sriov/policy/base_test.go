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

	. "github.com/smartystreets/goconvey/convey"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/kubernetes/fake"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	katalystbase "github.com/kubewharf/katalyst-core/cmd/base"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/agent"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/sriov/state"
	"github.com/kubewharf/katalyst-core/pkg/client"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/global"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/qrm"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/qrm/statedirectory"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	metaserveragent "github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	metricspool "github.com/kubewharf/katalyst-core/pkg/metrics/metrics-pool"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"

	qrmconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/qrm"
)

const (
	pciAnnotationKey   = "pci-devices"
	netNsAnnotationKey = "net-ns"
)

func TestBasePolicy(t *testing.T) {
	t.Parallel()

	Convey("BasePolicy", t, func() {
		policy := &basePolicy{
			allocationConfig: qrm.SriovAllocationConfig{
				PCIAnnotationKey:   "pci",
				NetNsAnnotationKey: "netns",
				ExtraAnnotations:   map[string]string{"extraKey": "extraValue"},
			},
		}

		Convey("generateResourceAllocationInfo", func() {
			resourceAllocationInfo, err := policy.generateResourceAllocationInfo(&state.AllocationInfo{
				VFInfo: state.VFInfo{
					RepName:  "eth0_0",
					Index:    0,
					NumaNode: 0,
					PCIAddr:  "0000:40:00.1",
					ExtraVFInfo: &state.ExtraVFInfo{
						Name:      "enp65s0v0",
						IBDevices: []string{"umad0", "uverbs0"},
					},
				},
			})

			So(err, ShouldBeNil)
			So(resourceAllocationInfo, ShouldResemble, &pluginapi.ResourceAllocationInfo{
				IsNodeResource:    true,
				IsScalarResource:  true,
				AllocatedQuantity: 1,
				Annotations: map[string]string{
					"pci":      `[{"address":"0000:40:00.1","repName":"eth0_0","vfName":"enp65s0v0"}]`,
					"extraKey": "extraValue",
				},
				Devices: []*pluginapi.DeviceSpec{
					{
						ContainerPath: filepath.Join(rdmaDevicePrefix, "umad0"),
						HostPath:      filepath.Join(rdmaDevicePrefix, "umad0"),
						Permissions:   "rwm",
					},
					{
						ContainerPath: filepath.Join(rdmaDevicePrefix, "uverbs0"),
						HostPath:      filepath.Join(rdmaDevicePrefix, "uverbs0"),
						Permissions:   "rwm",
					},
					{
						ContainerPath: rdmaCmPath,
						HostPath:      rdmaCmPath,
						Permissions:   "rw",
					},
				},
			})
		})

		Convey("packAllocationResponse", func() {
			Convey("nil resourceAllocationInfo", func() {
				response := policy.packAllocationResponse(&pluginapi.ResourceRequest{PodName: "pod"}, nil)
				So(response, ShouldResemble, &pluginapi.ResourceAllocationResponse{PodName: "pod"})
			})

			Convey("non-nil resourceAllocationInfo", func() {
				response := policy.packAllocationResponse(&pluginapi.ResourceRequest{PodName: "pod"}, &pluginapi.ResourceAllocationInfo{})
				So(response, ShouldResemble, &pluginapi.ResourceAllocationResponse{PodName: "pod", AllocationResult: &pluginapi.ResourceAllocation{
					ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{
						ResourceName: {},
					},
				}})
			})
		})
	})
}

func generateBasePolicy(t *testing.T, dryRun bool, bondingHostNetwork bool, vfState state.VFState, podEntries state.PodEntries) *basePolicy {
	tmpDir := t.TempDir()
	stateImpl, err := state.NewCheckpointState(nil, &global.MachineInfoConfiguration{}, &statedirectory.StateDirectoryConfiguration{
		StateFileDirectory:         filepath.Join(tmpDir, "state_file"),
		InMemoryStateFileDirectory: filepath.Join(tmpDir, "state_memory"),
		EnableInMemoryState:        false,
	}, "checkpoint", "sriov", true, metrics.DummyMetrics{})
	require.NoError(t, err)
	stateImpl.SetMachineState(vfState, false)
	stateImpl.SetPodEntries(podEntries, false)
	err = stateImpl.StoreState()
	require.NoError(t, err)

	cpuTopology, err := machine.GenerateDummyCPUTopology(256, 2, 4)
	require.NoError(t, err)

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
					ExtraNetworkInfo: &machine.ExtraNetworkInfo{},
					CPUTopology:      cpuTopology,
				},
			},
		},
		PluginManager: nil,
	}

	return &basePolicy{
		state:           stateImpl,
		agentCtx:        agentCtx,
		dryRun:          dryRun,
		machineInfoConf: &global.MachineInfoConfiguration{NetNSDirAbsPath: "/var/run/netns"},
		qosConfig:       &generic.QoSConfiguration{},
		allocationConfig: qrmconfig.SriovAllocationConfig{
			PCIAnnotationKey:   pciAnnotationKey,
			NetNsAnnotationKey: netNsAnnotationKey,
		},
		bondingHostNetwork: bondingHostNetwork,
	}
}
