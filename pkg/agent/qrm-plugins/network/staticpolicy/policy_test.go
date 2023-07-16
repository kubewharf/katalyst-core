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
	"net"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/uuid"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	katalyst_base "github.com/kubewharf/katalyst-core/cmd/base"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/agent"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	metaserveragent "github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/external"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
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

	testEth0Name               = "eth0"
	testEth0AffinitiveNUMANode = 0
	testEth0NSAbsolutePath     = ""
	testEth0NSName             = ""

	testEth2Name               = "eth2"
	testEth2AffinitiveNUMANode = 2
	testEth2NSAbsolutePath     = "/var/run/ns2"
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

func makeStaticPolicy(t *testing.T) *StaticPolicy {
	agentCtx := makeTestGenericContext(t)
	wrappedEmitter := agentCtx.EmitterPool.GetDefaultMetricsEmitter().WithTags(NetworkResourcePluginPolicyNameStatic, metrics.MetricTag{
		Key: util.QRMPluginPolicyTagName,
		Val: NetworkResourcePluginPolicyNameStatic,
	})

	return &StaticPolicy{
		qosConfig:  generateTestConfiguration(t).QoSConfiguration,
		emitter:    wrappedEmitter,
		metaServer: agentCtx.MetaServer,
		stopCh:     make(chan struct{}),
		name:       fmt.Sprintf("%s_%s", "qrm_network_plugin", NetworkResourcePluginPolicyNameStatic),
		qosLevelToNetClassMap: map[string]uint32{
			consts.PodAnnotationQoSLevelSharedCores:    testDefaultSharedNetClsId,
			consts.PodAnnotationQoSLevelReclaimedCores: testDefaultReclaimedNetClsId,
			consts.PodAnnotationQoSLevelDedicatedCores: testDefaultDedicatedNetClsId,
		},
		agentCtx:                                        agentCtx,
		nics:                                            makeNICs(),
		podLevelNetClassAnnoKey:                         consts.PodAnnotationNetClassKey,
		podLevelNetAttributesAnnoKeys:                   []string{},
		ipv4ResourceAllocationAnnotationKey:             testIPv4ResourceAllocationAnnotationKey,
		ipv6ResourceAllocationAnnotationKey:             testIPv6ResourceAllocationAnnotationKey,
		netNSPathResourceAllocationAnnotationKey:        testNetNSPathResourceAllocationAnnotationKey,
		netInterfaceNameResourceAllocationAnnotationKey: testNetInterfaceNameResourceAllocationAnnotationKey,
		netClassIDResourceAllocationAnnotationKey:       testNetClassIDResourceAllocationAnnotationKey,
		netBandwidthResourceAllocationAnnotationKey:     testNetBandwidthResourceAllocationAnnotationKey,
	}
}

func makeNICs() []machine.InterfaceInfo {
	v4 := net.ParseIP(testEth0IPv4)
	v6 := net.ParseIP(testEth2IPv6)

	return []machine.InterfaceInfo{
		{
			Iface:    testEth0Name,
			NumaNode: testEth0AffinitiveNUMANode,
			Enable:   true,
			Addr: &machine.IfaceAddr{
				IPV4: []*net.IP{&v4},
			},
			NSAbsolutePath: testEth0NSAbsolutePath,
			NSName:         testEth0NSName,
		},
		{
			Iface:    testEth2Name,
			NumaNode: testEth2AffinitiveNUMANode,
			Enable:   true,
			Addr: &machine.IfaceAddr{
				IPV6: []*net.IP{&v6},
			},
			NSAbsolutePath: testEth2NSAbsolutePath,
			NSName:         testEth2NSName,
		},
	}
}

func TestNewStaticPolicy(t *testing.T) {
	t.Parallel()

	neetToRun, policy, err := NewStaticPolicy(makeTestGenericContext(t), generateTestConfiguration(t), nil, NetworkResourcePluginPolicyNameStatic)
	assert.NoError(t, err)
	assert.NotNil(t, policy)
	assert.True(t, neetToRun)

	return
}

func TestRemovePod(t *testing.T) {
	t.Parallel()

	policy := makeStaticPolicy(t)
	assert.NotNil(t, policy)

	req := &pluginapi.RemovePodRequest{
		PodUid: string(uuid.NewUUID()),
	}

	_, err := policy.RemovePod(context.TODO(), req)
	assert.NoError(t, err)

	return
}

func TestAllocate(t *testing.T) {
	t.Parallel()

	testName := "test"

	testCases := []struct {
		description  string
		req          *pluginapi.ResourceRequest
		expectedResp *pluginapi.ResourceAllocationResponse
	}{
		{
			description: "req for init container",
			req: &pluginapi.ResourceRequest{
				PodUid:           string(uuid.NewUUID()),
				PodNamespace:     testName,
				PodName:          testName,
				ContainerName:    testName,
				ContainerType:    pluginapi.ContainerType_INIT,
				ContainerIndex:   0,
				ResourceName:     string(consts.ResourceNetBandwidth),
				ResourceRequests: make(map[string]float64),
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
				ResourceRequests: make(map[string]float64),
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
							IsNodeResource:    false,
							IsScalarResource:  true,
							AllocatedQuantity: 0,
							Annotations: map[string]string{
								testIPv4ResourceAllocationAnnotationKey:             testEth0IPv4,
								testIPv6ResourceAllocationAnnotationKey:             "",
								testNetInterfaceNameResourceAllocationAnnotationKey: testEth0Name,
								testNetClassIDResourceAllocationAnnotationKey:       testSharedNetClsId,
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
				ResourceRequests: make(map[string]float64),
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
							IsNodeResource:    false,
							IsScalarResource:  true,
							AllocatedQuantity: 0,
							Annotations: map[string]string{
								testIPv4ResourceAllocationAnnotationKey:             "",
								testIPv6ResourceAllocationAnnotationKey:             testEth2IPv6,
								testNetNSPathResourceAllocationAnnotationKey:        testEth2NSAbsolutePath,
								testNetInterfaceNameResourceAllocationAnnotationKey: testEth2Name,
								testNetClassIDResourceAllocationAnnotationKey:       testReclaimedNetClsId,
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
				ResourceRequests: make(map[string]float64),
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
							IsNodeResource:    false,
							IsScalarResource:  true,
							AllocatedQuantity: 0,
							Annotations: map[string]string{
								testIPv4ResourceAllocationAnnotationKey:             testEth0IPv4,
								testIPv6ResourceAllocationAnnotationKey:             "",
								testNetInterfaceNameResourceAllocationAnnotationKey: testEth0Name,
								testNetClassIDResourceAllocationAnnotationKey:       fmt.Sprintf("%d", testDefaultDedicatedNetClsId),
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
	}

	for _, tc := range testCases {
		staticPolicy := makeStaticPolicy(t)

		resp, err := staticPolicy.Allocate(context.Background(), tc.req)
		assert.NoError(t, err)
		assert.NotNil(t, resp)

		tc.expectedResp.PodUid = tc.req.PodUid
		t.Logf("expect: %v", tc.expectedResp.AllocationResult)
		t.Logf("actucal: %v", resp.AllocationResult)
		assert.Equalf(t, tc.expectedResp, resp, "failed in test case: %s", tc.description)
	}
}

func TestGetNetClassID(t *testing.T) {
	t.Parallel()

	staticPolicy := makeStaticPolicy(t)
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
			expectedClassID: 100,
		},
	}

	for _, tc := range testCases {
		gotClassID, err := staticPolicy.getNetClassID(tc.pod.Annotations, staticPolicy.podLevelNetClassAnnoKey)
		assert.NoError(t, err)
		assert.Equal(t, tc.expectedClassID, gotClassID)
	}
}

func TestName(t *testing.T) {
	t.Parallel()

	policy := makeStaticPolicy(t)
	assert.NotNil(t, policy)

	assert.Equal(t, "qrm_network_plugin_static", policy.Name())
}

func TestResourceName(t *testing.T) {
	t.Parallel()

	policy := makeStaticPolicy(t)
	assert.NotNil(t, policy)

	assert.Equal(t, string(consts.ResourceNetBandwidth), policy.ResourceName())
}

func TestGetTopologyHints(t *testing.T) {
	t.Parallel()

	testName := "test"

	testCases := []struct {
		description  string
		req          *pluginapi.ResourceRequest
		expectedResp *pluginapi.ResourceHintsResponse
	}{
		{
			description: "req for init container",
			req: &pluginapi.ResourceRequest{
				PodUid:           string(uuid.NewUUID()),
				PodNamespace:     testName,
				PodName:          testName,
				ContainerName:    testName,
				ContainerType:    pluginapi.ContainerType_INIT,
				ContainerIndex:   0,
				ResourceName:     string(consts.ResourceNetBandwidth),
				ResourceRequests: make(map[string]float64),
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
				PodUid:           string(uuid.NewUUID()),
				PodNamespace:     testName,
				PodName:          testName,
				ContainerName:    testName,
				ContainerType:    pluginapi.ContainerType_MAIN,
				ContainerIndex:   0,
				ResourceName:     string(consts.ResourceNetBandwidth),
				ResourceRequests: make(map[string]float64),
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
			description: "req for reclaimed_cores main container with not host netns preference",
			req: &pluginapi.ResourceRequest{
				PodUid:           string(uuid.NewUUID()),
				PodNamespace:     testName,
				PodName:          testName,
				ContainerName:    testName,
				ContainerType:    pluginapi.ContainerType_MAIN,
				ContainerIndex:   0,
				ResourceName:     string(consts.ResourceNetBandwidth),
				ResourceRequests: make(map[string]float64),
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
				PodUid:           string(uuid.NewUUID()),
				PodNamespace:     testName,
				PodName:          testName,
				ContainerName:    testName,
				ContainerType:    pluginapi.ContainerType_MAIN,
				ContainerIndex:   0,
				ResourceName:     string(consts.ResourceNetBandwidth),
				ResourceRequests: make(map[string]float64),
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
		staticPolicy := makeStaticPolicy(t)

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
}

func TestGetResourcesAllocation(t *testing.T) {
	t.Parallel()

	policy := makeStaticPolicy(t)
	assert.NotNil(t, policy)

	_, err := policy.GetResourcesAllocation(context.TODO(), &pluginapi.GetResourcesAllocationRequest{})
	assert.NoError(t, err)
}

func TestGetTopologyAwareResources(t *testing.T) {
	t.Parallel()

	policy := makeStaticPolicy(t)
	assert.NotNil(t, policy)

	req := &pluginapi.GetTopologyAwareResourcesRequest{
		PodUid:        string(uuid.NewUUID()),
		ContainerName: "test-container-name",
	}

	_, err := policy.GetTopologyAwareResources(context.TODO(), req)
	assert.NoError(t, err)
}

func TestGetTopologyAwareAllocatableResources(t *testing.T) {
	t.Parallel()

	policy := makeStaticPolicy(t)
	assert.NotNil(t, policy)

	_, err := policy.GetTopologyAwareAllocatableResources(context.TODO(), &pluginapi.GetTopologyAwareAllocatableResourcesRequest{})
	assert.NoError(t, err)
}

func TestGetResourcePluginOptions(t *testing.T) {
	t.Parallel()

	policy := makeStaticPolicy(t)
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

	policy := makeStaticPolicy(t)
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
