//go:build linux
// +build linux

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

package dirtymem

import (
	"testing"

	"github.com/stretchr/testify/assert"

	coreconfig "github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/config/agent"
	configagent "github.com/kubewharf/katalyst-core/pkg/config/agent"
	dynamicconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/qrm"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

func TestSetDirtyMem(t *testing.T) {
	t.Parallel()
	SetDirtyMem(&coreconfig.Configuration{
		AgentConfiguration: &agent.AgentConfiguration{
			StaticAgentConfiguration: &configagent.StaticAgentConfiguration{
				QRMPluginsConfiguration: &qrm.QRMPluginsConfiguration{
					IOQRMPluginConfig: &qrm.IOQRMPluginConfig{
						WritebackThrottlingOption: qrm.WritebackThrottlingOption{
							EnableSettingWBT: false,
						},
					},
				},
			},
		},
	}, nil, &dynamicconfig.DynamicAgentConfiguration{}, nil, nil)

	SetDirtyMem(&coreconfig.Configuration{
		AgentConfiguration: &agent.AgentConfiguration{
			StaticAgentConfiguration: &configagent.StaticAgentConfiguration{
				QRMPluginsConfiguration: &qrm.QRMPluginsConfiguration{
					IOQRMPluginConfig: &qrm.IOQRMPluginConfig{
						WritebackThrottlingOption: qrm.WritebackThrottlingOption{
							EnableSettingWBT: true,
						},
					},
				},
			},
		},
	}, nil, &dynamicconfig.DynamicAgentConfiguration{}, metrics.DummyMetrics{}, nil)
}

func TestGetWBTValueForDiskType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		diskType    int
		config      *coreconfig.Configuration
		expectedWBT int
		shouldApply bool
	}{
		{
			name:     "HDD with valid WBT",
			diskType: consts.DiskTypeHDD,
			config: &coreconfig.Configuration{
				AgentConfiguration: &agent.AgentConfiguration{
					StaticAgentConfiguration: &configagent.StaticAgentConfiguration{
						QRMPluginsConfiguration: &qrm.QRMPluginsConfiguration{
							IOQRMPluginConfig: &qrm.IOQRMPluginConfig{
								WritebackThrottlingOption: qrm.WritebackThrottlingOption{
									WBTValueHDD: 100,
								},
							},
						},
					},
				},
			},
			expectedWBT: 100,
			shouldApply: true,
		},
		{
			name:     "HDD with WBT set to -1",
			diskType: consts.DiskTypeHDD,
			config: &coreconfig.Configuration{
				AgentConfiguration: &agent.AgentConfiguration{
					StaticAgentConfiguration: &configagent.StaticAgentConfiguration{
						QRMPluginsConfiguration: &qrm.QRMPluginsConfiguration{
							IOQRMPluginConfig: &qrm.IOQRMPluginConfig{
								WritebackThrottlingOption: qrm.WritebackThrottlingOption{
									WBTValueHDD: -1,
								},
							},
						},
					},
				},
			},
			expectedWBT: 0,
			shouldApply: false,
		},
		{
			name:     "SSD with valid WBT",
			diskType: consts.DiskTypeSSD,
			config: &coreconfig.Configuration{
				AgentConfiguration: &agent.AgentConfiguration{
					StaticAgentConfiguration: &configagent.StaticAgentConfiguration{
						QRMPluginsConfiguration: &qrm.QRMPluginsConfiguration{
							IOQRMPluginConfig: &qrm.IOQRMPluginConfig{
								WritebackThrottlingOption: qrm.WritebackThrottlingOption{
									WBTValueSSD: 200,
								},
							},
						},
					},
				},
			},
			expectedWBT: 200,
			shouldApply: true,
		},
		{
			name:     "SSD with invalid WBT -1",
			diskType: consts.DiskTypeSSD,
			config: &coreconfig.Configuration{
				AgentConfiguration: &agent.AgentConfiguration{
					StaticAgentConfiguration: &configagent.StaticAgentConfiguration{
						QRMPluginsConfiguration: &qrm.QRMPluginsConfiguration{
							IOQRMPluginConfig: &qrm.IOQRMPluginConfig{
								WritebackThrottlingOption: qrm.WritebackThrottlingOption{
									WBTValueSSD: -1,
								},
							},
						},
					},
				},
			},
			expectedWBT: 0,
			shouldApply: false,
		},
		{
			name:     "NVME with valid WBT",
			diskType: consts.DiskTypeNVME,
			config: &coreconfig.Configuration{
				AgentConfiguration: &agent.AgentConfiguration{
					StaticAgentConfiguration: &configagent.StaticAgentConfiguration{
						QRMPluginsConfiguration: &qrm.QRMPluginsConfiguration{
							IOQRMPluginConfig: &qrm.IOQRMPluginConfig{
								WritebackThrottlingOption: qrm.WritebackThrottlingOption{
									WBTValueNVME: 300,
								},
							},
						},
					},
				},
			},
			expectedWBT: 300,
			shouldApply: true,
		},
		{
			name:     "NVME with valid WBT -1",
			diskType: consts.DiskTypeNVME,
			config: &coreconfig.Configuration{
				AgentConfiguration: &agent.AgentConfiguration{
					StaticAgentConfiguration: &configagent.StaticAgentConfiguration{
						QRMPluginsConfiguration: &qrm.QRMPluginsConfiguration{
							IOQRMPluginConfig: &qrm.IOQRMPluginConfig{
								WritebackThrottlingOption: qrm.WritebackThrottlingOption{
									WBTValueNVME: -1,
								},
							},
						},
					},
				},
			},
			expectedWBT: 0,
			shouldApply: false,
		},
		{
			name:     "VIRTIO with valid WBT",
			diskType: consts.DiskTypeVIRTIO,
			config: &coreconfig.Configuration{
				AgentConfiguration: &agent.AgentConfiguration{
					StaticAgentConfiguration: &configagent.StaticAgentConfiguration{
						QRMPluginsConfiguration: &qrm.QRMPluginsConfiguration{
							IOQRMPluginConfig: &qrm.IOQRMPluginConfig{
								WritebackThrottlingOption: qrm.WritebackThrottlingOption{
									WBTValueVIRTIO: 400,
								},
							},
						},
					},
				},
			},
			expectedWBT: 400,
			shouldApply: true,
		},
		{
			name:     "VIRIIO with valid WBT -1",
			diskType: consts.DiskTypeVIRTIO,
			config: &coreconfig.Configuration{
				AgentConfiguration: &agent.AgentConfiguration{
					StaticAgentConfiguration: &configagent.StaticAgentConfiguration{
						QRMPluginsConfiguration: &qrm.QRMPluginsConfiguration{
							IOQRMPluginConfig: &qrm.IOQRMPluginConfig{
								WritebackThrottlingOption: qrm.WritebackThrottlingOption{
									WBTValueVIRTIO: -1,
								},
							},
						},
					},
				},
			},
			expectedWBT: 0,
			shouldApply: false,
		},
		{
			name:        "Unknown disk type",
			diskType:    consts.DiskTypeUnknown,
			config:      &coreconfig.Configuration{},
			expectedWBT: 0,
			shouldApply: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			wbtValue, shouldApply := getWBTValueForDiskType(tt.diskType, tt.config)
			assert.Equal(t, tt.expectedWBT, wbtValue)
			assert.Equal(t, tt.shouldApply, shouldApply)
		})
	}
}
