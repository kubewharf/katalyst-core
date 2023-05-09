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
	"context"
	"fmt"
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
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

func generateTestConfiguration(t *testing.T) *config.Configuration {
	testConfiguration, err := options.NewOptions().Config()
	require.NoError(t, err)
	require.NotNil(t, testConfiguration)
	return testConfiguration
}

func makeMetaServer() *metaserver.MetaServer {
	return &metaserver.MetaServer{
		MetaAgent: &metaserveragent.MetaAgent{},
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

func makeDynamicPolicy(t *testing.T) *DynamicPolicy {
	agentCtx := makeTestGenericContext(t)
	wrappedEmitter := agentCtx.EmitterPool.GetDefaultMetricsEmitter().WithTags(NetworkResourcePluginPolicyNameDynamic, metrics.MetricTag{
		Key: util.QRMPluginPolicyTagName,
		Val: NetworkResourcePluginPolicyNameDynamic,
	})

	return &DynamicPolicy{
		qosConfig:   generateTestConfiguration(t).QoSConfiguration,
		emitter:     wrappedEmitter,
		metaServer:  agentCtx.MetaServer,
		stopCh:      make(chan struct{}),
		name:        fmt.Sprintf("%s_%s", "qrm_network_plugin", NetworkResourcePluginPolicyNameDynamic),
		netClassMap: make(map[string]uint32),
	}
}

func TestNewDynamicPolicy(t *testing.T) {
	neetToRun, policy, err := NewDynamicPolicy(makeTestGenericContext(t), generateTestConfiguration(t), nil, NetworkResourcePluginPolicyNameDynamic)
	assert.NoError(t, err)
	assert.NotNil(t, policy)
	assert.True(t, neetToRun)

	return
}

func TestRemovePod(t *testing.T) {
	policy := makeDynamicPolicy(t)
	assert.NotNil(t, policy)

	req := &pluginapi.RemovePodRequest{
		PodUid: string(uuid.NewUUID()),
	}

	_, err := policy.RemovePod(context.TODO(), req)
	assert.NoError(t, err)

	return
}

func TestAllocate(t *testing.T) {
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
				ResourceName:     ResourceNameNetwork,
				ResourceRequests: make(map[string]float64),
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
				},
			},
			expectedResp: &pluginapi.ResourceAllocationResponse{
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_INIT,
				ContainerIndex: 0,
				ResourceName:   ResourceNameNetwork,
				AllocationResult: &pluginapi.ResourceAllocation{
					ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{
						ResourceNameNetwork: {},
					},
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
			description: "req for shared_cores main container",
			req: &pluginapi.ResourceRequest{
				PodUid:           string(uuid.NewUUID()),
				PodNamespace:     testName,
				PodName:          testName,
				ContainerName:    testName,
				ContainerType:    pluginapi.ContainerType_MAIN,
				ContainerIndex:   0,
				ResourceName:     ResourceNameNetwork,
				ResourceRequests: make(map[string]float64),
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
				},
			},
			expectedResp: &pluginapi.ResourceAllocationResponse{
				PodNamespace:   testName,
				PodName:        testName,
				ContainerName:  testName,
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   ResourceNameNetwork,
				AllocationResult: &pluginapi.ResourceAllocation{
					ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{
						ResourceNameNetwork: {},
					},
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
			description: "req for reclaimed_cores main container",
			req: &pluginapi.ResourceRequest{
				PodUid:           string(uuid.NewUUID()),
				PodNamespace:     testName,
				PodName:          testName,
				ContainerName:    testName,
				ContainerType:    pluginapi.ContainerType_MAIN,
				ContainerIndex:   0,
				ResourceName:     ResourceNameNetwork,
				ResourceRequests: make(map[string]float64),
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
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
				ResourceName:   ResourceNameNetwork,
				AllocationResult: &pluginapi.ResourceAllocation{
					ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{
						ResourceNameNetwork: {},
					},
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
				},
			},
		},
		{
			description: "req for dedicated_cores main container",
			req: &pluginapi.ResourceRequest{
				PodUid:           string(uuid.NewUUID()),
				PodNamespace:     testName,
				PodName:          testName,
				ContainerName:    testName,
				ContainerType:    pluginapi.ContainerType_MAIN,
				ContainerIndex:   0,
				ResourceName:     ResourceNameNetwork,
				ResourceRequests: make(map[string]float64),
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
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
				ResourceName:   ResourceNameNetwork,
				AllocationResult: &pluginapi.ResourceAllocation{
					ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{
						ResourceNameNetwork: {},
					},
				},
				Labels: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
			},
		},
	}

	for _, tc := range testCases {
		dynamicPolicy := makeDynamicPolicy(t)

		resp, err := dynamicPolicy.Allocate(context.Background(), tc.req)
		assert.NoError(t, err)
		assert.NotNil(t, resp)

		tc.expectedResp.PodUid = tc.req.PodUid
		assert.Equal(t, tc.expectedResp, resp)
	}
}

func TestGetNetClassID(t *testing.T) {
	dynamicPolicy := makeDynamicPolicy(t)
	dynamicPolicy.netClassMap = map[string]uint32{
		consts.PodAnnotationQoSLevelReclaimedCores: 10,
		consts.PodAnnotationQoSLevelSharedCores:    20,
		consts.PodAnnotationQoSLevelDedicatedCores: 30,
		consts.PodAnnotationQoSLevelSystemCores:    70,
	}
	dynamicPolicy.podLevelNetClassAnnoKey = consts.PodAnnotationNetClassKey

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
						consts.PodAnnotationQoSLevelKey:       consts.PodAnnotationQoSLevelSharedCores,
						dynamicPolicy.podLevelNetClassAnnoKey: fmt.Sprintf("%d", 100),
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
		gotClassID, err := dynamicPolicy.getNetClassID(tc.pod, dynamicPolicy.podLevelNetClassAnnoKey)
		assert.NoError(t, err)
		assert.Equal(t, tc.expectedClassID, gotClassID)
	}
}

func TestName(t *testing.T) {
	policy := makeDynamicPolicy(t)
	assert.NotNil(t, policy)

	assert.Equal(t, "qrm_network_plugin_dynamic", policy.Name())
}

func TestResourceName(t *testing.T) {
	policy := makeDynamicPolicy(t)
	assert.NotNil(t, policy)

	assert.Equal(t, ResourceNameNetwork, policy.ResourceName())
}

func TestGetTopologyHints(t *testing.T) {
	policy := makeDynamicPolicy(t)
	assert.NotNil(t, policy)

	testName := "test"

	req := &pluginapi.ResourceRequest{
		PodUid:           string(uuid.NewUUID()),
		PodNamespace:     testName,
		PodName:          testName,
		ContainerName:    testName,
		ContainerType:    pluginapi.ContainerType_MAIN,
		ContainerIndex:   0,
		ResourceName:     ResourceNameNetwork,
		ResourceRequests: make(map[string]float64),
		Labels: map[string]string{
			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
		},
		Annotations: map[string]string{
			consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
		},
	}

	_, err := policy.GetTopologyHints(context.TODO(), req)
	assert.NoError(t, err)
}

func TestGetResourcesAllocation(t *testing.T) {
	policy := makeDynamicPolicy(t)
	assert.NotNil(t, policy)

	_, err := policy.GetResourcesAllocation(context.TODO(), &pluginapi.GetResourcesAllocationRequest{})
	assert.NoError(t, err)
}

func TestGetTopologyAwareResources(t *testing.T) {
	policy := makeDynamicPolicy(t)
	assert.NotNil(t, policy)

	req := &pluginapi.GetTopologyAwareResourcesRequest{
		PodUid:        string(uuid.NewUUID()),
		ContainerName: "test-container-name",
	}

	_, err := policy.GetTopologyAwareResources(context.TODO(), req)
	assert.NoError(t, err)
}

func TestGetTopologyAwareAllocatableResources(t *testing.T) {
	policy := makeDynamicPolicy(t)
	assert.NotNil(t, policy)

	_, err := policy.GetTopologyAwareAllocatableResources(context.TODO(), &pluginapi.GetTopologyAwareAllocatableResourcesRequest{})
	assert.NoError(t, err)
}

func TestGetResourcePluginOptions(t *testing.T) {
	policy := makeDynamicPolicy(t)
	assert.NotNil(t, policy)

	expectedResp := &pluginapi.ResourcePluginOptions{
		PreStartRequired:      false,
		WithTopologyAlignment: false,
		NeedReconcile:         false,
	}

	resp, err := policy.GetResourcePluginOptions(context.TODO(), &pluginapi.Empty{})
	assert.NoError(t, err)
	assert.Equal(t, expectedResp, resp)
}

func TestPreStartContainer(t *testing.T) {
	policy := makeDynamicPolicy(t)
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
