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

package baseplugin

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/uuid"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	katalyst_base "github.com/kubewharf/katalyst-core/cmd/base"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/agent"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/state"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

func generateTestConfiguration(t *testing.T) *config.Configuration {
	conf := config.NewConfiguration()
	tmpDir := t.TempDir()
	conf.QRMPluginSocketDirs = []string{tmpDir}
	conf.CheckpointManagerDir = tmpDir

	return conf
}

func generateTestGenericContext(t *testing.T, conf *config.Configuration) *agent.GenericContext {
	genericCtx, err := katalyst_base.GenerateFakeGenericContext([]runtime.Object{})
	if err != nil {
		t.Fatalf("unable to generate test generic context: %v", err)
	}

	metaServer, err := metaserver.NewMetaServer(genericCtx.Client, metrics.DummyMetrics{}, conf)
	if err != nil {
		t.Fatalf("unable to generate test meta server: %v", err)
	}

	agentCtx := &agent.GenericContext{
		GenericContext: genericCtx,
		MetaServer:     metaServer,
		PluginManager:  nil,
	}

	agentCtx.MetaServer = metaServer
	return agentCtx
}

func TestBasePlugin_PackAllocationResponse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		req            *pluginapi.ResourceRequest
		allocationInfo *state.AllocationInfo
		annotations    map[string]string
		resourceName   string
		expectedResp   *pluginapi.ResourceAllocationResponse
		expectedErr    bool
	}{
		{
			name: "nil allocation info",
			req: &pluginapi.ResourceRequest{
				PodUid:         string(uuid.NewUUID()),
				PodNamespace:   "test",
				PodName:        "test",
				ContainerName:  "test",
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
			},
			resourceName: "test-resource",
			expectedErr:  true,
		},
		{
			name: "nil request",
			allocationInfo: &state.AllocationInfo{
				AllocatedAllocation: state.Allocation{
					Quantity:  4,
					NUMANodes: []int{0, 1},
				},
			},
			resourceName: "test-resource",
			expectedErr:  true,
		},
		{
			name: "basic case",
			req: &pluginapi.ResourceRequest{
				PodUid:         "test-uid",
				PodNamespace:   "test",
				PodName:        "test",
				ContainerName:  "test",
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				Hint: &pluginapi.TopologyHint{
					Nodes: []uint64{0, 1},
				},
				ResourceName: "test-resource",
			},
			allocationInfo: &state.AllocationInfo{
				AllocatedAllocation: state.Allocation{
					Quantity:  4,
					NUMANodes: []int{0, 1},
				},
			},
			annotations: map[string]string{
				"test-key": "test-value",
			},
			resourceName: "test-resource",
			expectedResp: &pluginapi.ResourceAllocationResponse{
				PodUid:         "test-uid",
				PodNamespace:   "test",
				PodName:        "test",
				ContainerName:  "test",
				ContainerType:  pluginapi.ContainerType_MAIN,
				ContainerIndex: 0,
				ResourceName:   "test-resource",
				AllocationResult: &pluginapi.ResourceAllocation{
					ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{
						"test-resource": {
							IsNodeResource:    true,
							IsScalarResource:  true,
							AllocatedQuantity: 4,
							Annotations: map[string]string{
								"test-key": "test-value",
							},
							ResourceHints: &pluginapi.ListOfTopologyHints{
								Hints: []*pluginapi.TopologyHint{
									{
										Nodes: []uint64{0, 1},
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

			conf := generateTestConfiguration(t)
			agentCtx := generateTestGenericContext(t, conf)
			basePlugin, err := NewBasePlugin(agentCtx, conf, metrics.DummyMetrics{})
			assert.NoError(t, err)

			resp, err := basePlugin.PackAllocationResponse(tt.req, tt.allocationInfo, tt.annotations, tt.resourceName)
			if tt.expectedErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedResp, resp)
			}
		})
	}
}
