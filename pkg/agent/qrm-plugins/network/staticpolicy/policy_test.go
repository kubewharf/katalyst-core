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
	"context"
	"fmt"
	"io/ioutil"
	"net"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/bytedance/mockey"
	info "github.com/google/cadvisor/info/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/uuid"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	apinode "github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/consts"
	katalyst_base "github.com/kubewharf/katalyst-core/cmd/base"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/agent"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/network/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/network/staticpolicy/nic"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util/reactor"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	metaserveragent "github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/external"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/external/cgroupid"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	"github.com/kubewharf/katalyst-core/pkg/util/external/network"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

const (
	testSharedNetClsId    = "12345"
	testReclaimedNetClsId = "12346"

	testDefaultSharedNetClsId    = 22345
	testDefaultReclaimedNetClsId = 22346
	testDefaultDedicatedNetClsId = 22347

	testIPv4ResourceAllocationAnnotationKey             = "qrm.katalyst.kubewharf.io/inet_addr"
	testIPv6ResourceAllocationAnnotationKey             = "qrm.katalyst.kubewharf.io/inet_addr_ipv6"
	testNetNSPathResourceAllocationAnnotationKey        = "qrm.katalyst.kubewharf.io/netns_path"
	testNetInterfaceNameResourceAllocationAnnotationKey = "qrm.katalyst.kubewharf.io/nic_name"
	testNetClassIDResourceAllocationAnnotationKey       = "qrm.katalyst.kubewharf.io/netcls_id"
	testNetBandwidthResourceAllocationAnnotationKey     = "qrm.katalyst.kubewharf.io/net_bandwidth"

	testHostPreferEnhancementValue    = "{\"namespace_type\": \"host_ns_preferred\"}"
	testNotHostPreferEnhancementValue = "{\"namespace_type\": \"anti_host_ns_preferred\"}"
	testHostEnhancementValue          = "{\"namespace_type\": \"host_ns\"}"

	testNetNSDir               = "/var/run/netns"
	testEth0Name               = "eth0"
	testEth0AffinitiveNUMANode = 0
	testEth0NSAbsolutePath     = "/var/run/netns"
	testEth0NSName             = ""

	testEth1Name               = "eth1"
	testEth1AffinitiveNUMANode = 1

	testEth2Name               = "eth2"
	testEth2AffinitiveNUMANode = 2
	testEth2NSAbsolutePath     = "/var/run/netns/ns2"
	testEth2NSName             = "ns2"
)

var (
	testEth0IPv4 = net.ParseIP("1.1.1.1").String()
	testEth2IPv6 = net.ParseIP("::ffff:192.0.2.1").String()
)

func generateTestConfiguration(t *testing.T) *config.Configuration {
	testConfiguration, err := options.NewOptions().Config()
	require.NoError(t, err)
	require.NotNil(t, testConfiguration)
	return testConfiguration
}

func makeMetaServer() *metaserver.MetaServer {
	cpuTopology, _ := machine.GenerateDummyCPUTopology(16, 2, 4)

	return &metaserver.MetaServer{
		MetaAgent: &metaserveragent.MetaAgent{
			KatalystMachineInfo: &machine.KatalystMachineInfo{
				CPUTopology:      cpuTopology,
				ExtraNetworkInfo: &machine.ExtraNetworkInfo{},
			},
		},
		ExternalManager: external.InitExternalManager(&pod.PodFetcherStub{}),
	}
}

func makeTestGenericContext(t *testing.T) *agent.GenericContext {
	genericCtx, err := katalyst_base.GenerateFakeGenericContext([]runtime.Object{})
	assert.NoError(t, err)

	return &agent.GenericContext{
		GenericContext: genericCtx,
		MetaServer:     makeMetaServer(),
		PluginManager:  nil,
	}
}

func makeStaticPolicy(t *testing.T, hasNic bool) *StaticPolicy {
	agentCtx := makeTestGenericContext(t)
	agentCtx.KatalystMachineInfo.ExtraNetworkInfo.Interface = makeNICs(hasNic)
	wrappedEmitter := agentCtx.EmitterPool.GetDefaultMetricsEmitter().WithTags(NetworkResourcePluginPolicyNameStatic, metrics.MetricTag{
		Key: util.QRMPluginPolicyTagName,
		Val: NetworkResourcePluginPolicyNameStatic,
	})

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 4)
	assert.NoError(t, err)
	agentCtx.KatalystMachineInfo.CPUTopology = cpuTopology

	mockQrmConfig := generateTestConfiguration(t).QRMPluginsConfiguration
	mockQrmConfig.ReservedBandwidth = 4000
	mockQrmConfig.EgressCapacityRate = 0.9
	mockQrmConfig.IngressCapacityRate = 0.85

	conf := generateTestConfiguration(t)

	allocatableInterfaceSocketInfo, err := machine.GetInterfaceSocketInfo(
		agentCtx.KatalystMachineInfo.ExtraNetworkInfo.GetAllocatableNICs(conf.MachineInfoConfiguration),
		agentCtx.KatalystMachineInfo.CPUTopology.CPUDetails.Sockets().ToSliceInt(),
	)
	assert.NoError(t, err)

	agentCtx.KatalystMachineInfo.ExtraTopologyInfo = &machine.ExtraTopologyInfo{
		AllocatableInterfaceSocketInfo: allocatableInterfaceSocketInfo,
	}

	nicManager, err := nic.NewNICManager(agentCtx.MetaServer, wrappedEmitter, conf)
	assert.NoError(t, err)

	availableNICs := getAllNICs(nicManager)
	reservation := make(map[string]uint32)
	if hasNic {
		assert.Len(t, availableNICs, 2)

		expectedReservation := map[string]uint32{
			testEth0Name: 4000,
		}
		var err error
		reservation, err = getReservedBandwidth(availableNICs, mockQrmConfig.ReservedBandwidth, FirstNIC)
		assert.NoError(t, err)
		assert.Equal(t, expectedReservation, reservation)
	}

	tmpDir, err := ioutil.TempDir("", "checkpoint")
	assert.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	stateImpl, err := state.NewCheckpointState(mockQrmConfig, tmpDir, NetworkPluginStateFileName,
		NetworkResourcePluginPolicyNameStatic, &info.MachineInfo{}, availableNICs, reservation, false, metrics.DummyMetrics{})
	assert.NoError(t, err)

	return &StaticPolicy{
		qosConfig:  conf.QoSConfiguration,
		qrmConfig:  mockQrmConfig,
		emitter:    wrappedEmitter,
		metaServer: agentCtx.MetaServer,
		stopCh:     make(chan struct{}),
		name:       fmt.Sprintf("%s_%s", "qrm_network_plugin", NetworkResourcePluginPolicyNameStatic),
		qosLevelToNetClassMap: map[string]uint32{
			consts.PodAnnotationQoSLevelSharedCores:    testDefaultSharedNetClsId,
			consts.PodAnnotationQoSLevelReclaimedCores: testDefaultReclaimedNetClsId,
			consts.PodAnnotationQoSLevelDedicatedCores: testDefaultDedicatedNetClsId,
		},
		agentCtx:                                 agentCtx,
		applyNetworkGroupsFunc:                   agentCtx.MetaServer.ExternalManager.ApplyNetworkGroups,
		nicManager:                               nicManager,
		state:                                    stateImpl,
		residualHitMap:                           make(map[string]int64),
		podLevelNetClassAnnoKey:                  consts.PodAnnotationNetClassKey,
		podLevelNetAttributesAnnoKeys:            []string{},
		ipv4ResourceAllocationAnnotationKey:      testIPv4ResourceAllocationAnnotationKey,
		ipv6ResourceAllocationAnnotationKey:      testIPv6ResourceAllocationAnnotationKey,
		netNSPathResourceAllocationAnnotationKey: testNetNSPathResourceAllocationAnnotationKey,
		netInterfaceNameResourceAllocationAnnotationKey: testNetInterfaceNameResourceAllocationAnnotationKey,
		netClassIDResourceAllocationAnnotationKey:       testNetClassIDResourceAllocationAnnotationKey,
		netBandwidthResourceAllocationAnnotationKey:     testNetBandwidthResourceAllocationAnnotationKey,
		nicAllocationReactor:                            reactor.DummyAllocationReactor{},
	}
}

func makeNICs(hasNics bool) []machine.InterfaceInfo {
	v4 := net.ParseIP(testEth0IPv4)
	v6 := net.ParseIP(testEth2IPv6)

	if hasNics {
		return []machine.InterfaceInfo{
			{
				Name:     testEth0Name,
				IfIndex:  0,
				Speed:    25000,
				NumaNode: testEth0AffinitiveNUMANode,
				Enable:   true,
				Addr: &machine.IfaceAddr{
					IPV4: []*net.IP{&v4},
				},
				NetNSInfo: machine.NetNSInfo{
					NSAbsDir: testEth0NSAbsolutePath,
					NSName:   testEth0NSName,
				},
			},
			{
				Name:     testEth1Name,
				IfIndex:  1,
				Speed:    25000,
				NumaNode: testEth1AffinitiveNUMANode,
				Enable:   false,
				Addr:     &machine.IfaceAddr{},
			},
			{
				Name:     testEth2Name,
				IfIndex:  2,
				Speed:    25000,
				NumaNode: testEth2AffinitiveNUMANode,
				Enable:   true,
				Addr: &machine.IfaceAddr{
					IPV6: []*net.IP{&v6},
				},
				NetNSInfo: machine.NetNSInfo{
					NSAbsDir: testNetNSDir,
					NSName:   testEth2NSName,
				},
			},
		}
	} else {
		return []machine.InterfaceInfo{
			{
				Name:     testEth0Name,
				IfIndex:  0,
				Speed:    25000,
				NumaNode: testEth0AffinitiveNUMANode,
				Enable:   false,
				Addr: &machine.IfaceAddr{
					IPV4: []*net.IP{&v4},
				},
				NetNSInfo: machine.NetNSInfo{
					NSAbsDir: testEth0NSAbsolutePath,
					NSName:   testEth0NSName,
				},
			},
			{
				Name:     testEth1Name,
				IfIndex:  1,
				Speed:    25000,
				NumaNode: testEth1AffinitiveNUMANode,
				Enable:   false,
				Addr:     &machine.IfaceAddr{},
			},
			{
				Name:     testEth2Name,
				IfIndex:  2,
				Speed:    25000,
				NumaNode: testEth2AffinitiveNUMANode,
				Enable:   false,
				Addr: &machine.IfaceAddr{
					IPV6: []*net.IP{&v6},
				},
				NetNSInfo: machine.NetNSInfo{
					NSAbsDir: testNetNSDir,
					NSName:   testEth2NSName,
				},
			},
		}
	}
}

func TestNewStaticPolicy(t *testing.T) {
	t.Parallel()

	agentCtx := makeTestGenericContext(t)
	agentCtx.KatalystMachineInfo.ExtraNetworkInfo.Interface = makeNICs(true)
	agentCtx.MachineInfo = &info.MachineInfo{}

	conf := generateTestConfiguration(t)
	conf.QRMPluginsConfiguration.ReservedBandwidth = 4000
	conf.QRMPluginsConfiguration.EgressCapacityRate = 0.9
	conf.QRMPluginsConfiguration.IngressCapacityRate = 0.85

	tmpDir, err := ioutil.TempDir("", "checkpoint")
	assert.NoError(t, err)
	defer os.RemoveAll(tmpDir)
	conf.GenericQRMPluginConfiguration.StateFileDirectory = tmpDir

	neetToRun, policy, err := NewStaticPolicy(agentCtx, conf, nil, NetworkResourcePluginPolicyNameStatic)
	assert.NoError(t, err)
	assert.NotNil(t, policy)
	assert.True(t, neetToRun)

	// no valid nics on this node
	agentCtxInvalid := agentCtx
	agentCtxInvalid.KatalystMachineInfo.ExtraNetworkInfo.Interface = makeNICs(false)
	neetToRun, policy, err = NewStaticPolicy(agentCtxInvalid, conf, nil, NetworkResourcePluginPolicyNameStatic)
	assert.NoError(t, err)
	assert.NotNil(t, policy)
	assert.True(t, neetToRun)
}

func TestRemovePod(t *testing.T) {
	t.Parallel()

	policy := makeStaticPolicy(t, true)
	assert.NotNil(t, policy)

	podID := string(uuid.NewUUID())
	testName := "test"
	var bwReq float64 = 5000

	// create a new Pod with bandwidth request
	addReq := &pluginapi.ResourceRequest{
		PodUid:         podID,
		PodNamespace:   testName,
		PodName:        testName,
		ContainerName:  testName,
		ContainerType:  pluginapi.ContainerType_MAIN,
		ContainerIndex: 0,
		ResourceName:   string(consts.ResourceNetBandwidth),
		Hint: &pluginapi.TopologyHint{
			Nodes:     []uint64{0, 1},
			Preferred: true,
		},
		ResourceRequests: map[string]float64{
			string(consts.ResourceNetBandwidth): bwReq,
		},
		Labels: map[string]string{
			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
		},
		Annotations: map[string]string{
			consts.PodAnnotationNetClassKey:           testSharedNetClsId,
			consts.PodAnnotationQoSLevelKey:           consts.PodAnnotationQoSLevelSharedCores,
			consts.PodAnnotationNetworkEnhancementKey: testHostPreferEnhancementValue,
		},
	}

	resp, err := policy.Allocate(context.Background(), addReq)
	assert.NoError(t, err)

	// verify the state
	allocationInfo := policy.state.GetAllocationInfo(podID, testName)
	machineState := policy.state.GetMachineState()
	podEntries := policy.state.GetPodEntries()
	assert.NotNil(t, resp)
	assert.Equal(t, allocationInfo.IfName, testEth0Name)
	assert.Equal(t, allocationInfo.Egress, uint32(bwReq))
	assert.Equal(t, allocationInfo.Ingress, uint32(bwReq))
	assert.Len(t, machineState, 2)
	assert.Len(t, machineState[testEth0Name].PodEntries, 1)
	assert.EqualValues(t, machineState[testEth0Name].PodEntries[podID][testName], allocationInfo)
	assert.Len(t, podEntries, 1)
	assert.EqualValues(t, podEntries, machineState[testEth0Name].PodEntries)

	// remove the pod
	delReq := &pluginapi.RemovePodRequest{
		PodUid: podID,
	}

	_, err = policy.RemovePod(context.TODO(), delReq)
	assert.NoError(t, err)

	// verify the state again
	allocationInfo = policy.state.GetAllocationInfo(podID, testName)
	machineState = policy.state.GetMachineState()
	podEntries = policy.state.GetPodEntries()
	assert.Nil(t, allocationInfo)
	assert.Len(t, machineState, 2)
	assert.Len(t, machineState[testEth0Name].PodEntries, 0)
	assert.Len(t, podEntries, 0)
}

func TestAllocate(t *testing.T) {
	t.Parallel()

	testName := "test"

	// common cases
	testCases := []struct {
		description  string
		noError      bool
		req          *pluginapi.ResourceRequest
		expectedResp *pluginapi.ResourceAllocationResponse
	}{
		{
			description: "req for init container",
			noError:     true,
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_INIT,
				ContainerIndex: 0,
				ResourceName:   string(consts.ResourceNetBandwidth),
				ResourceRequests: map[string]float64{
					string(consts.ResourceNetBandwidth): 5000,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
				},
			},
			expectedResp: &pluginapi.ResourceAllocationResponse{
				PodNamespace:     testName,
				PodName:          testName,
				ContainerName:    testName,
				ContainerType:    pluginapi.ContainerType_INIT,
				ContainerIndex:   0,
				ResourceName:     string(consts.ResourceNetBandwidth),
				AllocationResult: nil,
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
				},
			},
		},
		{
			description: "req for shared_cores main container with host netns preference",
			noError:     true,
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(consts.ResourceNetBandwidth),
				Hint: &pluginapi.TopologyHint{
					Nodes:     []uint64{0, 1},
					Preferred: true,
				},
				ResourceRequests: map[string]float64{
					string(consts.ResourceNetBandwidth): 5000,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationNetClassKey:           testSharedNetClsId,
					consts.PodAnnotationQoSLevelKey:           consts.PodAnnotationQoSLevelSharedCores,
					consts.PodAnnotationNetworkEnhancementKey: testHostPreferEnhancementValue,
				},
			},
			expectedResp: &pluginapi.ResourceAllocationResponse{
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(consts.ResourceNetBandwidth),
				AllocationResult: &pluginapi.ResourceAllocation{
					ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{
						string(consts.ResourceNetBandwidth): {
							IsNodeResource:    true,
							IsScalarResource:  true,
							AllocatedQuantity: 5000,
							AllocationResult:  machine.NewCPUSet(0, 1).String(),
							Annotations: map[string]string{
								testIPv4ResourceAllocationAnnotationKey:             testEth0IPv4,
								testIPv6ResourceAllocationAnnotationKey:             "",
								testNetInterfaceNameResourceAllocationAnnotationKey: testEth0Name,
								testNetClassIDResourceAllocationAnnotationKey:       testSharedNetClsId,
								testNetBandwidthResourceAllocationAnnotationKey:     "5000",
							},
							ResourceHints: &pluginapi.ListOfTopologyHints{
								Hints: []*pluginapi.TopologyHint{
									{
										Nodes:     []uint64{0, 1},
										Preferred: true,
									},
								},
							},
						},
					},
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:                     consts.PodAnnotationQoSLevelSharedCores,
					consts.PodAnnotationNetworkEnhancementNamespaceType: consts.PodAnnotationNetworkEnhancementNamespaceTypeHostPrefer,
				},
			},
		},
		{
			description: "req for reclaimed_cores main container with not host netns preference",
			noError:     true,
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(consts.ResourceNetBandwidth),
				Hint: &pluginapi.TopologyHint{
					Nodes:     []uint64{2, 3},
					Preferred: true,
				},
				ResourceRequests: map[string]float64{
					string(consts.ResourceNetBandwidth): 5000,
				},
				Annotations: map[string]string{
					consts.PodAnnotationNetClassKey:           testReclaimedNetClsId,
					consts.PodAnnotationQoSLevelKey:           consts.PodAnnotationQoSLevelReclaimedCores,
					consts.PodAnnotationNetworkEnhancementKey: testNotHostPreferEnhancementValue,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
				},
			},
			expectedResp: &pluginapi.ResourceAllocationResponse{
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(consts.ResourceNetBandwidth),
				AllocationResult: &pluginapi.ResourceAllocation{
					ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{
						string(consts.ResourceNetBandwidth): {
							IsNodeResource:    true,
							IsScalarResource:  true,
							AllocatedQuantity: 5000,
							AllocationResult:  machine.NewCPUSet(2, 3).String(),
							Annotations: map[string]string{
								testIPv4ResourceAllocationAnnotationKey:             "",
								testIPv6ResourceAllocationAnnotationKey:             testEth2IPv6,
								testNetNSPathResourceAllocationAnnotationKey:        testEth2NSAbsolutePath,
								testNetInterfaceNameResourceAllocationAnnotationKey: testEth2Name,
								testNetClassIDResourceAllocationAnnotationKey:       testReclaimedNetClsId,
								testNetBandwidthResourceAllocationAnnotationKey:     "5000",
							},
							ResourceHints: &pluginapi.ListOfTopologyHints{
								Hints: []*pluginapi.TopologyHint{
									{
										Nodes:     []uint64{2, 3},
										Preferred: true,
									},
								},
							},
						},
					},
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:                     consts.PodAnnotationQoSLevelReclaimedCores,
					consts.PodAnnotationNetworkEnhancementNamespaceType: consts.PodAnnotationNetworkEnhancementNamespaceTypeNotHostPrefer,
				},
			},
		},
		{
			description: "req for dedicated_cores main container with host netns guarantee",
			noError:     true,
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(consts.ResourceNetBandwidth),
				Hint: &pluginapi.TopologyHint{
					Nodes:     []uint64{0, 1},
					Preferred: true,
				},
				ResourceRequests: map[string]float64{
					string(consts.ResourceNetBandwidth): 5000,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:           consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationNetworkEnhancementKey: testHostEnhancementValue,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
			},
			expectedResp: &pluginapi.ResourceAllocationResponse{
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(consts.ResourceNetBandwidth),
				AllocationResult: &pluginapi.ResourceAllocation{
					ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{
						string(consts.ResourceNetBandwidth): {
							IsNodeResource:    true,
							IsScalarResource:  true,
							AllocatedQuantity: 5000,
							AllocationResult:  machine.NewCPUSet(0, 1).String(),
							Annotations: map[string]string{
								testIPv4ResourceAllocationAnnotationKey:             testEth0IPv4,
								testIPv6ResourceAllocationAnnotationKey:             "",
								testNetInterfaceNameResourceAllocationAnnotationKey: testEth0Name,
								testNetClassIDResourceAllocationAnnotationKey:       fmt.Sprintf("%d", testDefaultDedicatedNetClsId),
								testNetBandwidthResourceAllocationAnnotationKey:     "5000",
							},
							ResourceHints: &pluginapi.ListOfTopologyHints{
								Hints: []*pluginapi.TopologyHint{
									{
										Nodes:     []uint64{0, 1},
										Preferred: true,
									},
								},
							},
						},
					},
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:                     consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationNetworkEnhancementNamespaceType: consts.PodAnnotationNetworkEnhancementNamespaceTypeHost,
				},
			},
		},
		{
			description: "req for dedicated_cores main container with host netns guarantee and exceeded bandwidth over the 1st NIC",
			noError:     false,
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(consts.ResourceNetBandwidth),
				Hint: &pluginapi.TopologyHint{
					Nodes:     []uint64{0, 1},
					Preferred: true,
				},
				ResourceRequests: map[string]float64{
					string(consts.ResourceNetBandwidth): 20000,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:           consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationNetworkEnhancementKey: testHostEnhancementValue,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
			},
			expectedResp: nil,
		},
		{
			description: "req for dedicated_cores main container with host netns guarantee and exceeded bandwidth over the 1st NIC which is preferred",
			noError:     false,
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(consts.ResourceNetBandwidth),
				Hint: &pluginapi.TopologyHint{
					Nodes:     []uint64{0, 1},
					Preferred: true,
				},
				ResourceRequests: map[string]float64{
					string(consts.ResourceNetBandwidth): 20000,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:           consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationNetworkEnhancementKey: testHostPreferEnhancementValue,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
			},
			expectedResp: &pluginapi.ResourceAllocationResponse{
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(consts.ResourceNetBandwidth),
				AllocationResult: &pluginapi.ResourceAllocation{
					ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{
						string(consts.ResourceNetBandwidth): {
							IsNodeResource:    true,
							IsScalarResource:  true,
							AllocatedQuantity: 20000,
							AllocationResult:  machine.NewCPUSet(2, 3).String(),
							Annotations: map[string]string{
								testIPv4ResourceAllocationAnnotationKey:             testEth2IPv6,
								testIPv6ResourceAllocationAnnotationKey:             "",
								testNetInterfaceNameResourceAllocationAnnotationKey: testEth2Name,
								testNetClassIDResourceAllocationAnnotationKey:       fmt.Sprintf("%d", testDefaultDedicatedNetClsId),
								testNetBandwidthResourceAllocationAnnotationKey:     "20000",
							},
							ResourceHints: &pluginapi.ListOfTopologyHints{
								Hints: []*pluginapi.TopologyHint{
									{
										Nodes:     []uint64{2, 3},
										Preferred: false,
									},
								},
							},
						},
					},
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:                     consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationNetworkEnhancementNamespaceType: consts.PodAnnotationNetworkEnhancementNamespaceTypeHostPrefer,
				},
			},
		},
	}

	for _, tc := range testCases {
		staticPolicy := makeStaticPolicy(t, true)
		resp, err := staticPolicy.Allocate(context.Background(), tc.req)
		if tc.noError {
			assert.NoError(t, err)
			assert.NotNil(t, resp)

			tc.expectedResp.PodUid = tc.req.PodUid
			t.Logf("expect: %v", tc.expectedResp.AllocationResult)
			t.Logf("actucal: %v", resp.AllocationResult)
			assert.Equalf(t, tc.expectedResp, resp, "failed in test case: %s", tc.description)
		} else {
			assert.Error(t, err)
			assert.Nil(t, resp)
		}
	}

	// no valid nics on this node
	testCasesNoNic := []struct {
		description  string
		noError      bool
		req          *pluginapi.ResourceRequest
		expectedResp *pluginapi.ResourceAllocationResponse
	}{
		{
			description: "req for shared_cores main container with host netns preference when no valid nic on this node",
			noError:     false,
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(consts.ResourceNetBandwidth),
				Hint: &pluginapi.TopologyHint{
					Nodes:     []uint64{0, 1},
					Preferred: true,
				},
				ResourceRequests: map[string]float64{
					string(consts.ResourceNetBandwidth): 5000,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationNetClassKey:           testSharedNetClsId,
					consts.PodAnnotationQoSLevelKey:           consts.PodAnnotationQoSLevelSharedCores,
					consts.PodAnnotationNetworkEnhancementKey: testHostPreferEnhancementValue,
				},
			},
			expectedResp: nil,
		},
		{
			description: "0 bandwidth req for shared_cores main container with host netns preference when no valid nic on this node",
			noError:     false,
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(consts.ResourceNetBandwidth),
				Hint: &pluginapi.TopologyHint{
					Nodes:     []uint64{0, 1},
					Preferred: true,
				},
				ResourceRequests: map[string]float64{
					string(consts.ResourceNetBandwidth): 0,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationNetClassKey:           testSharedNetClsId,
					consts.PodAnnotationQoSLevelKey:           consts.PodAnnotationQoSLevelSharedCores,
					consts.PodAnnotationNetworkEnhancementKey: testHostPreferEnhancementValue,
				},
			},
			expectedResp: nil,
		},
	}
	staticPolicy := makeStaticPolicy(t, false)
	for _, tc := range testCasesNoNic {
		resp, err := staticPolicy.Allocate(context.Background(), tc.req)
		if tc.noError {
			assert.Error(t, err)
			assert.EqualError(t, err, "failed to meet the bandwidth requirement of 5000 Mbps")
			assert.Nil(t, resp)
		}
	}
}

func TestAllocateForPod(t *testing.T) {
	t.Parallel()

	staticPolicy := makeStaticPolicy(t, true)
	testName := "test"
	req := &pluginapi.PodResourceRequest{
		PodUid:       string(uuid.NewUUID()),
		PodNamespace: testName,
		PodName:      testName,
		ResourceName: string(consts.ResourceNetBandwidth),
		Hint: &pluginapi.TopologyHint{
			Nodes:     []uint64{0, 1},
			Preferred: true,
		},
		ResourceRequests: map[string]float64{
			string(consts.ResourceNetBandwidth): 5000,
		},
		Annotations: map[string]string{
			consts.PodAnnotationQoSLevelKey:           consts.PodAnnotationQoSLevelDedicatedCores,
			consts.PodAnnotationNetworkEnhancementKey: testHostEnhancementValue,
		},
		Labels: map[string]string{
			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
		},
	}

	_, err := staticPolicy.AllocateForPod(context.Background(), req)
	assert.Error(t, err)
}

func TestGetPodTopologyHints(t *testing.T) {
	t.Parallel()

	staticPolicy := makeStaticPolicy(t, true)
	testName := "test"
	req := &pluginapi.PodResourceRequest{
		PodUid:       string(uuid.NewUUID()),
		PodNamespace: testName,
		PodName:      testName,
		ResourceName: string(consts.ResourceNetBandwidth),
		Hint: &pluginapi.TopologyHint{
			Nodes:     []uint64{0, 1},
			Preferred: true,
		},
		ResourceRequests: map[string]float64{
			string(consts.ResourceNetBandwidth): 5000,
		},
		Annotations: map[string]string{
			consts.PodAnnotationQoSLevelKey:           consts.PodAnnotationQoSLevelDedicatedCores,
			consts.PodAnnotationNetworkEnhancementKey: testHostEnhancementValue,
		},
		Labels: map[string]string{
			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
		},
	}

	_, err := staticPolicy.GetPodTopologyHints(context.Background(), req)
	assert.Error(t, err)
}

func TestGetNetClassID(t *testing.T) {
	t.Parallel()

	staticPolicy := makeStaticPolicy(t, true)
	staticPolicy.qosLevelToNetClassMap = map[string]uint32{
		consts.PodAnnotationQoSLevelReclaimedCores: 10,
		consts.PodAnnotationQoSLevelSharedCores:    20,
		consts.PodAnnotationQoSLevelDedicatedCores: 30,
		consts.PodAnnotationQoSLevelSystemCores:    70,
	}
	staticPolicy.podLevelNetClassAnnoKey = consts.PodAnnotationNetClassKey

	testCases := []struct {
		description     string
		pod             *v1.Pod
		qosLevel        string
		expectedClassID uint32
	}{
		{
			description: "get net class id for shared_cores",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pod-1",
					Annotations: map[string]string{
						consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
					},
					Labels: map[string]string{
						consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
					},
				},
			},
			qosLevel:        consts.PodAnnotationQoSLevelSharedCores,
			expectedClassID: 20,
		},
		{
			description: "get net class id for reclaimed_cores",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pod-1",
					Annotations: map[string]string{
						consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
					},
					Labels: map[string]string{
						consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
					},
				},
			},
			qosLevel:        consts.PodAnnotationQoSLevelReclaimedCores,
			expectedClassID: 10,
		},
		{
			description: "get net class id for dedicated_cores",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pod-1",
					Annotations: map[string]string{
						consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
					},
					Labels: map[string]string{
						consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
					},
				},
			},
			qosLevel:        consts.PodAnnotationQoSLevelDedicatedCores,
			expectedClassID: 30,
		},
		{
			description: "get net class id for system_cores",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pod-1",
					Annotations: map[string]string{
						consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSystemCores,
					},
					Labels: map[string]string{
						consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSystemCores,
					},
				},
			},
			qosLevel:        consts.PodAnnotationQoSLevelSystemCores,
			expectedClassID: 70,
		},
		{
			description: "get pod-level net class id",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pod-1",
					Annotations: map[string]string{
						consts.PodAnnotationQoSLevelKey:      consts.PodAnnotationQoSLevelSharedCores,
						staticPolicy.podLevelNetClassAnnoKey: fmt.Sprintf("%d", 100),
					},
					Labels: map[string]string{
						consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
					},
				},
			},
			qosLevel:        consts.PodAnnotationQoSLevelSharedCores,
			expectedClassID: 100,
		},
	}

	for _, tc := range testCases {
		gotClassID, err := staticPolicy.getNetClassID(tc.pod.Annotations, staticPolicy.podLevelNetClassAnnoKey, tc.qosLevel)
		assert.NoError(t, err)
		assert.Equal(t, tc.expectedClassID, gotClassID)
	}
}

func TestName(t *testing.T) {
	t.Parallel()

	policy := makeStaticPolicy(t, true)
	assert.NotNil(t, policy)

	assert.Equal(t, "qrm_network_plugin_static", policy.Name())
}

func TestResourceName(t *testing.T) {
	t.Parallel()

	policy := makeStaticPolicy(t, true)
	assert.NotNil(t, policy)

	assert.Equal(t, string(consts.ResourceNetBandwidth), policy.ResourceName())
}

func TestClearResidualState(t *testing.T) {
	t.Parallel()

	t.Run("with_residual_pods", func(t *testing.T) {
		t.Parallel()
		policy := makeStaticPolicy(t, true)
		policy.metaServer = &metaserver.MetaServer{
			MetaAgent: &metaserveragent.MetaAgent{
				PodFetcher: &pod.PodFetcherStub{
					PodList: []*v1.Pod{},
				},
			},
		}

		policy.residualHitMap["residual-pod-1"] = 10
		policy.residualHitMap["residual-pod-2"] = 15
		policy.state.SetPodEntries(state.PodEntries{
			"residual-pod-1": state.ContainerEntries{
				"container-1": &state.AllocationInfo{
					AllocationMeta: commonstate.AllocationMeta{
						PodNamespace:  "residual-namespace",
						PodName:       "residual-pod-1",
						PodUid:        "residual-pod-1",
						ContainerName: "container-1",
					},
				},
			},
			"residual-pod-2": state.ContainerEntries{
				"container-2": &state.AllocationInfo{
					AllocationMeta: commonstate.AllocationMeta{
						PodNamespace:  "residual-namespace",
						PodName:       "residual-pod-2",
						PodUid:        "residual-pod-2",
						ContainerName: "container-2",
					},
				},
			},
		}, true)

		policy.clearResidualState(nil, nil, nil, nil, nil)
		policy.clearResidualState(nil, nil, nil, nil, nil)

		as := assert.New(t)
		as.Empty(policy.state.GetPodEntries(), "should clean residual pods")
		as.Empty(policy.residualHitMap, "should clean hit map")
	})

	t.Run("no_residual_pods", func(t *testing.T) {
		t.Parallel()
		policy := makeStaticPolicy(t, true)
		policy.metaServer = &metaserver.MetaServer{
			MetaAgent: &metaserveragent.MetaAgent{
				PodFetcher: &pod.PodFetcherStub{},
			},
		}

		originalEntries := policy.state.GetPodEntries().Clone()
		policy.clearResidualState(nil, nil, nil, nil, nil)

		as := assert.New(t)
		as.Equal(originalEntries, policy.state.GetPodEntries(), "should maintain original state")
	})
}

func TestGetTopologyHints(t *testing.T) {
	t.Parallel()

	testName := "test"

	// common cases
	testCases := []struct {
		description  string
		req          *pluginapi.ResourceRequest
		expectedResp *pluginapi.ResourceHintsResponse
	}{
		{
			description: "req for init container",
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_INIT,
				ContainerIndex: 0,
				ResourceName:   string(consts.ResourceNetBandwidth),
				ResourceRequests: map[string]float64{
					string(consts.ResourceNetBandwidth): 5000,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
				},
			},
			expectedResp: &pluginapi.ResourceHintsResponse{
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_INIT,
				ContainerIndex: 0,
				ResourceName:   string(consts.ResourceNetBandwidth),
				ResourceHints: map[string]*pluginapi.ListOfTopologyHints{
					string(consts.ResourceNetBandwidth): nil,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
				},
			},
		},
		{
			description: "req for shared_cores main container with host netns preference",
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(consts.ResourceNetBandwidth),
				ResourceRequests: map[string]float64{
					string(consts.ResourceNetBandwidth): 5000,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationNetClassKey:           testSharedNetClsId,
					consts.PodAnnotationQoSLevelKey:           consts.PodAnnotationQoSLevelSharedCores,
					consts.PodAnnotationNetworkEnhancementKey: testHostPreferEnhancementValue,
				},
			},
			expectedResp: &pluginapi.ResourceHintsResponse{
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(consts.ResourceNetBandwidth),
				ResourceHints: map[string]*pluginapi.ListOfTopologyHints{
					string(consts.ResourceNetBandwidth): {
						Hints: []*pluginapi.TopologyHint{
							{
								Nodes:     []uint64{0, 1},
								Preferred: true,
							},
							{
								Nodes:     []uint64{2, 3},
								Preferred: false,
							},
							{
								Nodes:     []uint64{0, 1, 2, 3},
								Preferred: false,
							},
						},
					},
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:                     consts.PodAnnotationQoSLevelSharedCores,
					consts.PodAnnotationNetworkEnhancementNamespaceType: consts.PodAnnotationNetworkEnhancementNamespaceTypeHostPrefer,
				},
			},
		},
		{
			description: "req for reclaimed_cores main container with not host netns preference 1201",
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(consts.ResourceNetBandwidth),
				ResourceRequests: map[string]float64{
					string(consts.ResourceNetBandwidth): 5000,
				},
				Annotations: map[string]string{
					consts.PodAnnotationNetClassKey:           testReclaimedNetClsId,
					consts.PodAnnotationQoSLevelKey:           consts.PodAnnotationQoSLevelReclaimedCores,
					consts.PodAnnotationNetworkEnhancementKey: testNotHostPreferEnhancementValue,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
				},
			},
			expectedResp: &pluginapi.ResourceHintsResponse{
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(consts.ResourceNetBandwidth),
				ResourceHints: map[string]*pluginapi.ListOfTopologyHints{
					string(consts.ResourceNetBandwidth): {
						Hints: []*pluginapi.TopologyHint{
							{
								Nodes:     []uint64{0, 1},
								Preferred: false,
							},
							{
								Nodes:     []uint64{2, 3},
								Preferred: true,
							},
							{
								Nodes:     []uint64{0, 1, 2, 3},
								Preferred: false,
							},
						},
					},
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:                     consts.PodAnnotationQoSLevelReclaimedCores,
					consts.PodAnnotationNetworkEnhancementNamespaceType: consts.PodAnnotationNetworkEnhancementNamespaceTypeNotHostPrefer,
				},
			},
		},
		{
			description: "req for dedicated_cores main container with host netns guarantee",
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(consts.ResourceNetBandwidth),
				ResourceRequests: map[string]float64{
					string(consts.ResourceNetBandwidth): 5000,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:           consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationNetworkEnhancementKey: testHostEnhancementValue,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
			},
			expectedResp: &pluginapi.ResourceHintsResponse{
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(consts.ResourceNetBandwidth),
				ResourceHints: map[string]*pluginapi.ListOfTopologyHints{
					string(consts.ResourceNetBandwidth): {
						Hints: []*pluginapi.TopologyHint{
							{
								Nodes:     []uint64{0, 1},
								Preferred: true,
							},
							{
								Nodes:     []uint64{0, 1, 2, 3},
								Preferred: false,
							},
						},
					},
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:                     consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationNetworkEnhancementNamespaceType: consts.PodAnnotationNetworkEnhancementNamespaceTypeHost,
				},
			},
		},
		{
			description: "req for dedicated_cores main container with exceeded bandwidth over the 1st NIC which is preferred",
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(consts.ResourceNetBandwidth),
				ResourceRequests: map[string]float64{
					string(consts.ResourceNetBandwidth): 20000,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:           consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationNetworkEnhancementKey: testHostPreferEnhancementValue,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
			},
			expectedResp: &pluginapi.ResourceHintsResponse{
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(consts.ResourceNetBandwidth),
				ResourceHints: map[string]*pluginapi.ListOfTopologyHints{
					string(consts.ResourceNetBandwidth): {
						Hints: []*pluginapi.TopologyHint{
							{
								Nodes:     []uint64{2, 3},
								Preferred: false,
							},
							{
								Nodes:     []uint64{0, 1, 2, 3},
								Preferred: false,
							},
						},
					},
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:                     consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationNetworkEnhancementNamespaceType: consts.PodAnnotationNetworkEnhancementNamespaceTypeHostPrefer,
				},
			},
		},
		{
			description: "req for dedicated_cores main container with exceeded bandwidth over the 1st NIC which is required",
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(consts.ResourceNetBandwidth),
				ResourceRequests: map[string]float64{
					string(consts.ResourceNetBandwidth): 20000,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:           consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationNetworkEnhancementKey: testHostEnhancementValue,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
			},
			expectedResp: &pluginapi.ResourceHintsResponse{
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(consts.ResourceNetBandwidth),
				ResourceHints: map[string]*pluginapi.ListOfTopologyHints{
					string(consts.ResourceNetBandwidth): {
						Hints: []*pluginapi.TopologyHint{
							{
								Nodes:     []uint64{0, 1, 2, 3},
								Preferred: false,
							},
						},
					},
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey:                     consts.PodAnnotationQoSLevelDedicatedCores,
					consts.PodAnnotationNetworkEnhancementNamespaceType: consts.PodAnnotationNetworkEnhancementNamespaceTypeHost,
				},
			},
		},
	}

	for _, tc := range testCases {
		staticPolicy := makeStaticPolicy(t, true)

		resp, err := staticPolicy.GetTopologyHints(context.Background(), tc.req)
		assert.NoError(t, err)
		assert.NotNil(t, resp)

		tc.expectedResp.PodUid = tc.req.PodUid

		compareHint := func(a, b *pluginapi.TopologyHint) bool {
			if a == nil {
				return true
			} else if b == nil {
				return false
			}

			aset, _ := machine.NewCPUSetUint64(a.Nodes...)
			bset, _ := machine.NewCPUSetUint64(b.Nodes...)

			asetStr := aset.String()
			bsetStr := bset.String()

			if asetStr < bsetStr {
				return true
			} else if asetStr > bsetStr {
				return false
			} else if a.Preferred {
				return true
			} else {
				return false
			}
		}

		if resp.ResourceHints[string(consts.ResourceNetBandwidth)] != nil {
			sort.SliceStable(resp.ResourceHints[string(consts.ResourceNetBandwidth)].Hints, func(i, j int) bool {
				return compareHint(resp.ResourceHints[string(consts.ResourceNetBandwidth)].Hints[i],
					resp.ResourceHints[string(consts.ResourceNetBandwidth)].Hints[j])
			})
		}

		if tc.expectedResp.ResourceHints[string(consts.ResourceNetBandwidth)] != nil {
			sort.SliceStable(tc.expectedResp.ResourceHints[string(consts.ResourceNetBandwidth)].Hints, func(i, j int) bool {
				return compareHint(tc.expectedResp.ResourceHints[string(consts.ResourceNetBandwidth)].Hints[i],
					tc.expectedResp.ResourceHints[string(consts.ResourceNetBandwidth)].Hints[j])
			})
		}

		assert.Equalf(t, tc.expectedResp, resp, "failed in test case: %s", tc.description)
	}

	// no valid nics on this node ()
	staticPolicy := makeStaticPolicy(t, false)
	resp, err := staticPolicy.GetTopologyHints(context.Background(), testCases[1].req)
	assert.NoError(t, err)
	assert.NotNil(t, resp)
	assert.Equal(t, map[string]*pluginapi.ListOfTopologyHints{
		resp.ResourceName: {
			Hints: []*pluginapi.TopologyHint{},
		},
	}, resp.ResourceHints)
}

func TestGetResourcesAllocation(t *testing.T) {
	t.Parallel()

	policy := makeStaticPolicy(t, true)
	assert.NotNil(t, policy)

	_, err := policy.GetResourcesAllocation(context.TODO(), &pluginapi.GetResourcesAllocationRequest{})
	assert.NoError(t, err)
}

func TestGetTopologyAwareResources(t *testing.T) {
	t.Parallel()

	podID := string(uuid.NewUUID())
	testName := "test"
	var bwReq float64 = 5000

	testCases := []struct {
		description                   string
		hasNic                        bool
		addReq                        *pluginapi.ResourceRequest
		req                           *pluginapi.GetTopologyAwareResourcesRequest
		expectedTopologyAwareQuantity *pluginapi.TopologyAwareQuantity
	}{
		{
			description: "has valid nics and a valid Pod",
			hasNic:      true,
			addReq: &pluginapi.ResourceRequest{
				PodUid:         podID,
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(consts.ResourceNetBandwidth),
				Hint: &pluginapi.TopologyHint{
					Nodes:     []uint64{0, 1},
					Preferred: true,
				},
				ResourceRequests: map[string]float64{
					string(consts.ResourceNetBandwidth): bwReq,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationNetClassKey:           testSharedNetClsId,
					consts.PodAnnotationQoSLevelKey:           consts.PodAnnotationQoSLevelSharedCores,
					consts.PodAnnotationNetworkEnhancementKey: testHostPreferEnhancementValue,
				},
			},
			req: &pluginapi.GetTopologyAwareResourcesRequest{
				PodUid:        podID,
				ContainerName: testName,
			},
			expectedTopologyAwareQuantity: &pluginapi.TopologyAwareQuantity{
				ResourceValue: bwReq,
				Node:          uint64(0),
				Name:          testEth0Name,
				Type:          string(apinode.TopologyTypeNIC),
				TopologyLevel: pluginapi.TopologyLevel_SOCKET,
				Annotations: map[string]string{
					// testEth0NSName is empty, so remove the prefix
					consts.ResourceAnnotationKeyResourceIdentifier: testEth0Name,
					consts.ResourceAnnotationKeyNICNetNSName:       "",
				},
			},
		},
		{
			description: "has no valid nic",
			hasNic:      false,
			addReq: &pluginapi.ResourceRequest{
				PodUid:         podID,
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   string(consts.ResourceNetBandwidth),
				Hint: &pluginapi.TopologyHint{
					Nodes:     []uint64{0, 1},
					Preferred: true,
				},
				ResourceRequests: map[string]float64{
					string(consts.ResourceNetBandwidth): 0,
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationNetClassKey:           testSharedNetClsId,
					consts.PodAnnotationQoSLevelKey:           consts.PodAnnotationQoSLevelSharedCores,
					consts.PodAnnotationNetworkEnhancementKey: testHostPreferEnhancementValue,
				},
			},
			req: &pluginapi.GetTopologyAwareResourcesRequest{
				PodUid:        podID,
				ContainerName: testName,
			},
			expectedTopologyAwareQuantity: nil,
		},
	}

	for _, tc := range testCases {
		policy := makeStaticPolicy(t, tc.hasNic)
		assert.NotNil(t, policy)

		_, err := policy.Allocate(context.Background(), tc.addReq)
		if tc.hasNic {
			assert.NoError(t, err)
		}

		resp, err := policy.GetTopologyAwareResources(context.TODO(), tc.req)
		assert.NoError(t, err)
		assert.NotNil(t, resp)
		if tc.hasNic {
			assert.Len(t, resp.ContainerTopologyAwareResources.AllocatedResources, 1)
			assert.Equal(t, resp.ContainerTopologyAwareResources.AllocatedResources[string(consts.ResourceNetBandwidth)].AggregatedQuantity, bwReq)
			assert.Len(t, resp.ContainerTopologyAwareResources.AllocatedResources[string(consts.ResourceNetBandwidth)].TopologyAwareQuantityList, 1)

			assert.Equal(t, resp.ContainerTopologyAwareResources.AllocatedResources[string(consts.ResourceNetBandwidth)].TopologyAwareQuantityList[0], tc.expectedTopologyAwareQuantity)
		} else {
			assert.Equal(t, &pluginapi.GetTopologyAwareResourcesResponse{}, resp)
		}
	}
}

func TestGetTopologyAwareAllocatableResources(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		description                     string
		hasNic                          bool
		expectedAllocatableQuantityList []*pluginapi.TopologyAwareQuantity
		expectedCapacityQuantityList    []*pluginapi.TopologyAwareQuantity
	}{
		{
			description: "has valid nics",
			hasNic:      true,
			expectedAllocatableQuantityList: []*pluginapi.TopologyAwareQuantity{
				{
					ResourceValue: float64(17250),
					Node:          uint64(0),
					Name:          testEth0Name,
					Type:          string(apinode.TopologyTypeNIC),
					TopologyLevel: pluginapi.TopologyLevel_SOCKET,
					Annotations: map[string]string{
						// testEth0NSName is empty, so remove the prefix
						consts.ResourceAnnotationKeyResourceIdentifier: testEth0Name,
						consts.ResourceAnnotationKeyNICNetNSName:       "",
					},
				},
				{
					ResourceValue: float64(21250),
					Node:          uint64(1),
					Name:          testEth2Name,
					Type:          string(apinode.TopologyTypeNIC),
					TopologyLevel: pluginapi.TopologyLevel_SOCKET,
					Annotations: map[string]string{
						consts.ResourceAnnotationKeyResourceIdentifier: fmt.Sprintf("%s-%s", testEth2NSName, testEth2Name),
						consts.ResourceAnnotationKeyNICNetNSName:       testEth2NSName,
					},
				},
			},
			expectedCapacityQuantityList: []*pluginapi.TopologyAwareQuantity{
				{
					ResourceValue: float64(21250),
					Node:          uint64(0),
					Name:          testEth0Name,
					Type:          string(apinode.TopologyTypeNIC),
					TopologyLevel: pluginapi.TopologyLevel_SOCKET,
					Annotations: map[string]string{
						// testEth0NSName is empty, so remove the prefix
						consts.ResourceAnnotationKeyResourceIdentifier: testEth0Name,
						consts.ResourceAnnotationKeyNICNetNSName:       "",
					},
				},
				{
					ResourceValue: float64(21250),
					Node:          uint64(1),
					Name:          testEth2Name,
					Type:          string(apinode.TopologyTypeNIC),
					TopologyLevel: pluginapi.TopologyLevel_SOCKET,
					Annotations: map[string]string{
						consts.ResourceAnnotationKeyResourceIdentifier: fmt.Sprintf("%s-%s", testEth2NSName, testEth2Name),
						consts.ResourceAnnotationKeyNICNetNSName:       testEth2NSName,
					},
				},
			},
		},
		{
			description:                     "has no valid nic",
			hasNic:                          false,
			expectedAllocatableQuantityList: make([]*pluginapi.TopologyAwareQuantity, 0),
			expectedCapacityQuantityList:    make([]*pluginapi.TopologyAwareQuantity, 0),
		},
	}

	for _, tc := range testCases {
		policy := makeStaticPolicy(t, tc.hasNic)
		assert.NotNil(t, policy)

		resp, err := policy.GetTopologyAwareAllocatableResources(context.TODO(), &pluginapi.GetTopologyAwareAllocatableResourcesRequest{})
		assert.NotNil(t, resp)
		assert.NoError(t, err)

		if tc.hasNic {
			assert.Equal(t, resp.AllocatableResources[string(consts.ResourceNetBandwidth)].AggregatedAllocatableQuantity, tc.expectedAllocatableQuantityList[0].ResourceValue+tc.expectedAllocatableQuantityList[1].ResourceValue)
			assert.Equal(t, resp.AllocatableResources[string(consts.ResourceNetBandwidth)].AggregatedCapacityQuantity, tc.expectedCapacityQuantityList[0].ResourceValue+tc.expectedCapacityQuantityList[1].ResourceValue)
			assert.Len(t, resp.AllocatableResources[string(consts.ResourceNetBandwidth)].TopologyAwareAllocatableQuantityList, len(tc.expectedAllocatableQuantityList))
			assert.Len(t, resp.AllocatableResources[string(consts.ResourceNetBandwidth)].TopologyAwareCapacityQuantityList, len(tc.expectedCapacityQuantityList))

			assert.Equal(t, resp.AllocatableResources[string(consts.ResourceNetBandwidth)].TopologyAwareAllocatableQuantityList, tc.expectedAllocatableQuantityList)
			assert.Equal(t, resp.AllocatableResources[string(consts.ResourceNetBandwidth)].TopologyAwareCapacityQuantityList, tc.expectedCapacityQuantityList)
		} else {
			assert.Equal(t, float64(0), resp.AllocatableResources[string(consts.ResourceNetBandwidth)].AggregatedAllocatableQuantity)
			assert.Equal(t, float64(0), resp.AllocatableResources[string(consts.ResourceNetBandwidth)].AggregatedCapacityQuantity)
			assert.Len(t, resp.AllocatableResources[string(consts.ResourceNetBandwidth)].TopologyAwareAllocatableQuantityList, 0)
			assert.Len(t, resp.AllocatableResources[string(consts.ResourceNetBandwidth)].TopologyAwareCapacityQuantityList, 0)
		}
	}
}

func TestGetResourcePluginOptions(t *testing.T) {
	t.Parallel()

	policy := makeStaticPolicy(t, true)
	assert.NotNil(t, policy)

	expectedResp := &pluginapi.ResourcePluginOptions{
		PreStartRequired:      false,
		WithTopologyAlignment: true,
		NeedReconcile:         false,
	}

	resp, err := policy.GetResourcePluginOptions(context.TODO(), &pluginapi.Empty{})
	assert.NoError(t, err)
	assert.Equal(t, expectedResp, resp)
}

func TestPreStartContainer(t *testing.T) {
	t.Parallel()

	policy := makeStaticPolicy(t, true)
	assert.NotNil(t, policy)

	req := &pluginapi.PreStartContainerRequest{
		PodUid:        string(uuid.NewUUID()),
		PodNamespace:  "test-namespace",
		PodName:       "test-pod-name",
		ContainerName: "test-container-name",
	}

	_, err := policy.PreStartContainer(context.TODO(), req)
	assert.NoError(t, err)
}

func TestStaticPolicy_applyNetClass(t *testing.T) {
	t.Parallel()
	defer mockey.UnPatchAll()

	policy := makeStaticPolicy(t, true)
	assert.NotNil(t, policy)
	policy.CgroupV2Env = true
	policy.aliveCgroupID = make(map[uint64]time.Time)
	policy.metaServer.PodFetcher = &pod.PodFetcherStub{
		PodList: []*v1.Pod{
			{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod-name",
					Namespace: "test-namespace",
					UID:       "test-pod-uid",
					Annotations: map[string]string{
						consts.PodAnnotationNetClassKey: testSharedNetClsId,
					},
				},
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Name: "test-container-name",
						},
					},
				},
				Status: v1.PodStatus{
					ContainerStatuses: []v1.ContainerStatus{
						{
							Name:        "test-container-name",
							ContainerID: "test-container-id",
						},
					},
				},
			},
		},
	}
	policy.metaServer.ExternalManager = &external.DummyExternalManager{
		CgroupIDManager: &cgroupid.CgroupIDManagerStub{
			ContainerCGroupIDMap: map[string]map[string]uint64{
				"test-pod-uid": {
					"test-container-id": 314125,
				},
			},
		},
		NetworkManager: &network.NetworkManagerStub{
			NetClassMap: map[string]map[string]*common.NetClsData{
				"test-pod-uid": {
					"test-container-id": {
						ClassID:  0,
						CgroupID: 314125,
						Attributes: map[string]string{
							testNetClassIDResourceAllocationAnnotationKey: testSharedNetClsId,
						},
					},
				},
				"resource-allocation-test-pod-uid": {
					"test-container-id": {
						ClassID:  0,
						CgroupID: 242352,
						Attributes: map[string]string{
							testNetClassIDResourceAllocationAnnotationKey: testSharedNetClsId,
						},
					},
				},
			},
		},
	}

	policy.applyNetClassFunc = policy.metaServer.ExternalManager.ApplyNetClass

	mockey.Mock(common.IsContainerCgroupExist).To(func(podUID, containerID string) (bool, error) {
		return true, nil
	}).Build()

	policy.applyNetClass()
}
