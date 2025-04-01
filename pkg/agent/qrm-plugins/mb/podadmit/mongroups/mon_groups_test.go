package mongroups

import (
	"testing"

	"github.com/stretchr/testify/assert"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/config/agent"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/qrm"
)

func TestManager_Allocate(t *testing.T) {
	cases := []struct {
		name       string
		policy     string
		expectResp *pluginapi.ResourceAllocationResponse
	}{
		{
			name:   "empty policy",
			policy: "",
			expectResp: &pluginapi.ResourceAllocationResponse{
				AllocationResult: &pluginapi.ResourceAllocation{
					ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{
						"memory": {
							Annotations: map[string]string{},
						},
					},
				},
			},
		}, {
			name:   "inject policy",
			policy: `{"enabled-closids": "dedicated,shared-30"}`,
			expectResp: &pluginapi.ResourceAllocationResponse{
				AllocationResult: &pluginapi.ResourceAllocation{
					ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{
						"memory": {
							Annotations: map[string]string{
								util.AnnotationMonGroupsEnabledClosIDs: "dedicated,shared-30",
							},
						},
					},
				},
			},
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			conf := &config.Configuration{
				AgentConfiguration: &agent.AgentConfiguration{
					StaticAgentConfiguration: &agent.StaticAgentConfiguration{
						QRMPluginsConfiguration: &qrm.QRMPluginsConfiguration{
							MBQRMPluginConfig: &qrm.MBQRMPluginConfig{
								MonGroupsPolicy: tt.policy,
							},
						},
					},
				},
			}
			mgr := NewManager(conf)
			resp := &pluginapi.ResourceAllocationResponse{
				AllocationResult: &pluginapi.ResourceAllocation{
					ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{
						"memory": {
							Annotations: map[string]string{},
						},
					},
				},
			}
			mgr.Allocate(nil, resp, "", nil)
			assert.Equal(t, tt.expectResp, resp, "allocate resp not equal")
		})
	}
}
