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

package cpuweight

import (
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"

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

func TestSetCPUWeight(t *testing.T) {
	t.Parallel()

	SetCPUWeight(&coreconfig.Configuration{
		AgentConfiguration: &agent.AgentConfiguration{
			StaticAgentConfiguration: &configagent.StaticAgentConfiguration{
				QRMPluginsConfiguration: &qrm.QRMPluginsConfiguration{
					CPUQRMPluginConfig: &qrm.CPUQRMPluginConfig{
						CPUWeightOptions: qrm.CPUWeightOptions{
							EnableSettingCPUWeight: false,
						},
					},
				},
			},
		},
	}, nil, &dynamicconfig.DynamicAgentConfiguration{}, nil, nil)

	SetCPUWeight(&coreconfig.Configuration{
		AgentConfiguration: &agent.AgentConfiguration{
			StaticAgentConfiguration: &configagent.StaticAgentConfiguration{
				QRMPluginsConfiguration: &qrm.QRMPluginsConfiguration{
					CPUQRMPluginConfig: &qrm.CPUQRMPluginConfig{
						CPUWeightOptions: qrm.CPUWeightOptions{
							EnableSettingCPUWeight: true,
						},
					},
				},
			},
		},
	}, nil, &dynamicconfig.DynamicAgentConfiguration{}, nil, nil)

	metaServer, err := makeMetaServer()
	assert.NoError(t, err)
	metaServer.PodFetcher = &pod.PodFetcherStub{PodList: []*v1.Pod{}}
	SetCPUWeight(&coreconfig.Configuration{
		AgentConfiguration: &agent.AgentConfiguration{
			StaticAgentConfiguration: &configagent.StaticAgentConfiguration{
				QRMPluginsConfiguration: &qrm.QRMPluginsConfiguration{
					CPUQRMPluginConfig: &qrm.CPUQRMPluginConfig{
						CPUWeightOptions: qrm.CPUWeightOptions{
							EnableSettingCPUWeight: true,
						},
					},
				},
			},
		},
	}, metrics.DummyMetrics{}, &dynamicconfig.DynamicAgentConfiguration{}, metrics.DummyMetrics{}, metaServer)

	SetCPUWeight(&coreconfig.Configuration{
		AgentConfiguration: &agent.AgentConfiguration{
			StaticAgentConfiguration: &configagent.StaticAgentConfiguration{
				QRMPluginsConfiguration: &qrm.QRMPluginsConfiguration{
					CPUQRMPluginConfig: &qrm.CPUQRMPluginConfig{
						CPUWeightOptions: qrm.CPUWeightOptions{
							EnableSettingCPUWeight: true,
						},
					},
				},
			},
		},
	}, nil, &dynamicconfig.DynamicAgentConfiguration{}, metrics.DummyMetrics{}, metaServer)

	applyCPUWeightCgroupLevelConfig(&coreconfig.Configuration{
		AgentConfiguration: &agent.AgentConfiguration{
			StaticAgentConfiguration: &configagent.StaticAgentConfiguration{
				QRMPluginsConfiguration: &qrm.QRMPluginsConfiguration{
					CPUQRMPluginConfig: &qrm.CPUQRMPluginConfig{
						CPUWeightOptions: qrm.CPUWeightOptions{
							EnableSettingCPUWeight: true,
						},
					},
				},
			},
		},
	}, metrics.DummyMetrics{})

	applyCPUWeightCgroupLevelConfig(&coreconfig.Configuration{
		AgentConfiguration: &agent.AgentConfiguration{
			StaticAgentConfiguration: &configagent.StaticAgentConfiguration{
				QRMPluginsConfiguration: &qrm.QRMPluginsConfiguration{
					CPUQRMPluginConfig: &qrm.CPUQRMPluginConfig{
						CPUWeightOptions: qrm.CPUWeightOptions{
							EnableSettingCPUWeight: true,
							CPUWeightConfigFile:    "fake",
						},
					},
				},
			},
		},
	}, metrics.DummyMetrics{})

	// Create a temporary file
	tempFile, err := ioutil.TempFile("", "cpuweight.json")
	if err != nil {
		fmt.Println("Error creating temporary file:", err)
		return
	}
	defer os.Remove(tempFile.Name()) // Defer removing the temporary file

	// Write the JSON content to the temporary file
	jsonContent := `{
			    "fake": 200,
			    "fake2": 400
			        }`

	if _, err := tempFile.WriteString(jsonContent); err != nil {
		fmt.Println("Error writing to temporary file:", err)
		return
	}
	absPathCgroup, err := filepath.Abs(tempFile.Name())
	if err != nil {
		fmt.Println("Error obtaining absolute path:", err)
		return
	}

	applyCPUWeightCgroupLevelConfig(&coreconfig.Configuration{
		AgentConfiguration: &agent.AgentConfiguration{
			StaticAgentConfiguration: &configagent.StaticAgentConfiguration{
				QRMPluginsConfiguration: &qrm.QRMPluginsConfiguration{
					CPUQRMPluginConfig: &qrm.CPUQRMPluginConfig{
						CPUWeightOptions: qrm.CPUWeightOptions{
							EnableSettingCPUWeight: true,
							CPUWeightConfigFile:    absPathCgroup,
						},
					},
				},
			},
		},
	}, metrics.DummyMetrics{})
}
