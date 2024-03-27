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

package iocost

import (
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"

	"github.com/kubewharf/katalyst-core/pkg/config"
	coreconfig "github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/config/agent"
	configagent "github.com/kubewharf/katalyst-core/pkg/config/agent"
	dynamicconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/qrm"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	metaagent "github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func makeMetaServer() (*metaserver.MetaServer, error) {
	server := &metaserver.MetaServer{
		MetaAgent: &metaagent.MetaAgent{},
	}

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 1, 2)
	if err != nil {
		return nil, err
	}

	server.KatalystMachineInfo = &machine.KatalystMachineInfo{
		CPUTopology: cpuTopology,
	}
	server.MetricsFetcher = metric.NewFakeMetricsFetcher(metrics.DummyMetrics{})
	return server, nil
}

func TestSetIOCost(t *testing.T) {
	t.Parallel()
	SetIOCost(nil,
		nil, &dynamicconfig.DynamicAgentConfiguration{}, nil, nil)

	SetIOCost(&coreconfig.Configuration{
		AgentConfiguration: &agent.AgentConfiguration{
			StaticAgentConfiguration: &configagent.StaticAgentConfiguration{
				QRMPluginsConfiguration: &qrm.QRMPluginsConfiguration{
					IOQRMPluginConfig: &qrm.IOQRMPluginConfig{
						IOCostOption: qrm.IOCostOption{
							EnableSettingIOCost: false,
						},
					},
				},
			},
		},
	}, nil, &dynamicconfig.DynamicAgentConfiguration{}, nil, nil)

	SetIOCost(&coreconfig.Configuration{
		AgentConfiguration: &agent.AgentConfiguration{
			StaticAgentConfiguration: &configagent.StaticAgentConfiguration{
				QRMPluginsConfiguration: &qrm.QRMPluginsConfiguration{
					IOQRMPluginConfig: &qrm.IOQRMPluginConfig{
						IOCostOption: qrm.IOCostOption{
							EnableSettingIOCost: false,
						},
					},
				},
			},
		},
	}, nil, &dynamicconfig.DynamicAgentConfiguration{}, metrics.DummyMetrics{}, nil)

	metaServer, err := makeMetaServer()
	assert.NoError(t, err)
	metaServer.PodFetcher = &pod.PodFetcherStub{PodList: []*v1.Pod{}}

	SetIOCost(&coreconfig.Configuration{
		AgentConfiguration: &agent.AgentConfiguration{
			StaticAgentConfiguration: &configagent.StaticAgentConfiguration{
				QRMPluginsConfiguration: &qrm.QRMPluginsConfiguration{
					IOQRMPluginConfig: &qrm.IOQRMPluginConfig{
						IOCostOption: qrm.IOCostOption{
							EnableSettingIOCost: false,
						},
					},
				},
			},
		},
	}, nil, &dynamicconfig.DynamicAgentConfiguration{}, metrics.DummyMetrics{}, metaServer)

	SetIOCost(&coreconfig.Configuration{
		AgentConfiguration: &agent.AgentConfiguration{
			StaticAgentConfiguration: &configagent.StaticAgentConfiguration{
				QRMPluginsConfiguration: &qrm.QRMPluginsConfiguration{
					IOQRMPluginConfig: &qrm.IOQRMPluginConfig{
						IOCostOption: qrm.IOCostOption{
							EnableSettingIOCost: true,
						},
					},
				},
			},
		},
	}, nil, &dynamicconfig.DynamicAgentConfiguration{}, metrics.DummyMetrics{}, metaServer)

	SetIOCost(&coreconfig.Configuration{
		AgentConfiguration: &agent.AgentConfiguration{
			StaticAgentConfiguration: &configagent.StaticAgentConfiguration{
				QRMPluginsConfiguration: &qrm.QRMPluginsConfiguration{
					IOQRMPluginConfig: &qrm.IOQRMPluginConfig{
						IOCostOption: qrm.IOCostOption{
							EnableSettingIOCost: true,
						},
					},
				},
			},
		},
	}, nil, &dynamicconfig.DynamicAgentConfiguration{}, metrics.DummyMetrics{}, metaServer)
}
func Test_disableIOCost(t *testing.T) {
	type args struct {
		conf *config.Configuration
	}
	tests := []struct {
		name string
		args args
	}{
		{
			name: "disableIOCost with EnableIOCostControl: false",
			args: args{
				conf: &config.Configuration{
					AgentConfiguration: &agent.AgentConfiguration{
						StaticAgentConfiguration: &configagent.StaticAgentConfiguration{
							QRMPluginsConfiguration: &qrm.QRMPluginsConfiguration{
								IOQRMPluginConfig: &qrm.IOQRMPluginConfig{
									IOCostOption: qrm.IOCostOption{
										EnableSettingIOCost: false,
									},
								},
							},
						},
					},
				},
			},
		},
		{
			name: "disableIOCost with EnableIOCostControl: true",
			args: args{
				conf: &config.Configuration{
					AgentConfiguration: &agent.AgentConfiguration{
						StaticAgentConfiguration: &configagent.StaticAgentConfiguration{
							QRMPluginsConfiguration: &qrm.QRMPluginsConfiguration{
								IOQRMPluginConfig: &qrm.IOQRMPluginConfig{
									IOCostOption: qrm.IOCostOption{
										EnableSettingIOCost: true,
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
		t.Run(tt.name, func(t *testing.T) {
			disableIOCost(tt.args.conf)
		})
	}
}

func Test_applyIOCostConfig(t *testing.T) {
	type args struct {
		conf    *config.Configuration
		emitter metrics.MetricEmitter
	}

	jsonContent := `{
        "default": {
        "ctrl_mode": "user",
        "enable": 1,
        "read_latency_percent": 95,
        "read_latency_us": 150000,
        "vrate_max": 150,
        "vrate_min": 100,
        "write_latency_percent": 95,
        "write_latency_us": 240000
    }
	}`

	// Create a temporary file
	tempFile, err := ioutil.TempFile("", "test.json")
	if err != nil {
		fmt.Println("Error creating temporary file:", err)
		return
	}
	defer os.Remove(tempFile.Name()) // Defer removing the temporary file

	// Write the JSON content to the temporary file
	if _, err := tempFile.WriteString(jsonContent); err != nil {
		fmt.Println("Error writing to temporary file:", err)
		return
	}

	absPath, err := filepath.Abs(tempFile.Name())
	if err != nil {
		fmt.Println("Error obtaining absolute path:", err)
		return
	}

	tests := []struct {
		name string
		args args
	}{
		{
			name: "applyIOCostConfig with EnableIOCostControl: false",
			args: args{
				conf: &config.Configuration{
					AgentConfiguration: &agent.AgentConfiguration{
						StaticAgentConfiguration: &configagent.StaticAgentConfiguration{
							QRMPluginsConfiguration: &qrm.QRMPluginsConfiguration{
								IOQRMPluginConfig: &qrm.IOQRMPluginConfig{
									IOCostOption: qrm.IOCostOption{
										EnableSettingIOCost: false,
									},
								},
							},
						},
					},
				},
			},
		},
		{
			name: "applyIOCostConfig without config file",
			args: args{
				conf: &config.Configuration{
					AgentConfiguration: &agent.AgentConfiguration{
						StaticAgentConfiguration: &configagent.StaticAgentConfiguration{
							QRMPluginsConfiguration: &qrm.QRMPluginsConfiguration{
								IOQRMPluginConfig: &qrm.IOQRMPluginConfig{
									IOCostOption: qrm.IOCostOption{
										EnableSettingIOCost: true,
									},
								},
							},
						}},
				},
			},
		},
		{
			name: "applyIOCostConfig with config file",
			args: args{
				conf: &config.Configuration{
					AgentConfiguration: &agent.AgentConfiguration{
						StaticAgentConfiguration: &configagent.StaticAgentConfiguration{
							QRMPluginsConfiguration: &qrm.QRMPluginsConfiguration{
								IOQRMPluginConfig: &qrm.IOQRMPluginConfig{
									IOCostOption: qrm.IOCostOption{
										EnableSettingIOCost:   true,
										IOCostModelConfigFile: "/tmp/fakeFile1",
										IOCostQoSConfigFile:   "/tmp/fakeFile2",
									},
								},
							},
						},
					},
				},
			},
		},

		{
			name: "applyIOCostConfig with real qos config file",
			args: args{
				conf: &config.Configuration{
					AgentConfiguration: &agent.AgentConfiguration{
						StaticAgentConfiguration: &configagent.StaticAgentConfiguration{
							QRMPluginsConfiguration: &qrm.QRMPluginsConfiguration{
								IOQRMPluginConfig: &qrm.IOQRMPluginConfig{
									IOCostOption: qrm.IOCostOption{
										EnableSettingIOCost:   true,
										IOCostModelConfigFile: "/tmp/fakeFile1",
										IOCostQoSConfigFile:   absPath,
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
		t.Run(tt.name, func(t *testing.T) {
			applyIOCostConfig(tt.args.conf, tt.args.emitter)
		})
	}
}

func Test_reportDevicesIOCostVrate(t *testing.T) {
	type args struct {
		emitter metrics.MetricEmitter
	}
	tests := []struct {
		name string
		args args
	}{
		{
			name: "reportDevicesIOCostVrate with nil emitter",
			args: args{
				emitter: nil,
			},
		},
		{
			name: "reportDevicesIOCostVrate with fake emitter",
			args: args{
				emitter: metrics.DummyMetrics{},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reportDevicesIOCostVrate(tt.args.emitter)
		})
	}
}

func Test_applyIOCostModel(t *testing.T) {
	type args struct {
		ioCostModelConfigs map[DevModel]*common.IOCostModelData
		devsIDToModel      map[string]DevModel
	}
	tests := []struct {
		name string
		args args
	}{
		{
			name: "applyIOCostModel with fake data",
			args: args{
				ioCostModelConfigs: map[DevModel]*common.IOCostModelData{
					DevModelDefault: {
						CtrlMode: common.IOCostCtrlModeAuto,
					},
				},
				devsIDToModel: map[string]DevModel{
					"fakeID": DevModelDefault,
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			applyIOCostModel(tt.args.ioCostModelConfigs, tt.args.devsIDToModel)
		})
	}
}
