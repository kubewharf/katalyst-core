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

package fragmem

import (
	"io/ioutil"
	"os"
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

func TestSetSockMemLimit(t *testing.T) {
	t.Parallel()
	SetMemCompact(&coreconfig.Configuration{
		AgentConfiguration: &agent.AgentConfiguration{
			StaticAgentConfiguration: &configagent.StaticAgentConfiguration{
				QRMPluginsConfiguration: &qrm.QRMPluginsConfiguration{
					MemoryQRMPluginConfig: &qrm.MemoryQRMPluginConfig{
						FragMemOptions: qrm.FragMemOptions{
							EnableSettingFragMem: false,
						},
					},
				},
			},
		},
	}, nil, &dynamicconfig.DynamicAgentConfiguration{}, nil, nil)

	SetMemCompact(&coreconfig.Configuration{
		AgentConfiguration: &agent.AgentConfiguration{
			StaticAgentConfiguration: &configagent.StaticAgentConfiguration{
				QRMPluginsConfiguration: &qrm.QRMPluginsConfiguration{
					MemoryQRMPluginConfig: &qrm.MemoryQRMPluginConfig{
						FragMemOptions: qrm.FragMemOptions{
							EnableSettingFragMem: true,
						},
					},
				},
			},
		},
	}, nil, &dynamicconfig.DynamicAgentConfiguration{}, metrics.DummyMetrics{}, nil)

	SetMemCompact(&coreconfig.Configuration{
		AgentConfiguration: &agent.AgentConfiguration{
			StaticAgentConfiguration: &configagent.StaticAgentConfiguration{
				QRMPluginsConfiguration: &qrm.QRMPluginsConfiguration{
					MemoryQRMPluginConfig: &qrm.MemoryQRMPluginConfig{
						FragMemOptions: qrm.FragMemOptions{
							EnableSettingFragMem: true,
						},
					},
				},
			},
		},
	}, metrics.DummyMetrics{}, &dynamicconfig.DynamicAgentConfiguration{}, metrics.DummyMetrics{}, nil)

	metaServer, err := makeMetaServer()
	assert.NoError(t, err)
	metaServer.PodFetcher = &pod.PodFetcherStub{PodList: []*v1.Pod{}}
	SetMemCompact(&coreconfig.Configuration{
		AgentConfiguration: &agent.AgentConfiguration{
			StaticAgentConfiguration: &configagent.StaticAgentConfiguration{
				QRMPluginsConfiguration: &qrm.QRMPluginsConfiguration{
					MemoryQRMPluginConfig: &qrm.MemoryQRMPluginConfig{
						FragMemOptions: qrm.FragMemOptions{
							EnableSettingFragMem: true,
						},
					},
				},
			},
		},
	}, metrics.DummyMetrics{}, &dynamicconfig.DynamicAgentConfiguration{}, metrics.DummyMetrics{}, metaServer)

	SetMemCompact(&coreconfig.Configuration{
		AgentConfiguration: &agent.AgentConfiguration{
			StaticAgentConfiguration: &configagent.StaticAgentConfiguration{
				QRMPluginsConfiguration: &qrm.QRMPluginsConfiguration{
					MemoryQRMPluginConfig: &qrm.MemoryQRMPluginConfig{
						FragMemOptions: qrm.FragMemOptions{
							EnableSettingFragMem: false,
						},
					},
				},
			},
		},
	}, metrics.DummyMetrics{}, &dynamicconfig.DynamicAgentConfiguration{}, metrics.DummyMetrics{}, metaServer)

	SetMemCompact(&coreconfig.Configuration{
		AgentConfiguration: &agent.AgentConfiguration{
			StaticAgentConfiguration: &configagent.StaticAgentConfiguration{
				QRMPluginsConfiguration: &qrm.QRMPluginsConfiguration{
					MemoryQRMPluginConfig: &qrm.MemoryQRMPluginConfig{
						FragMemOptions: qrm.FragMemOptions{
							EnableSettingFragMem: true,
							SetMemFragScoreAsync: 800,
						},
					},
				},
			},
		},
	}, metrics.DummyMetrics{}, &dynamicconfig.DynamicAgentConfiguration{}, metrics.DummyMetrics{}, metaServer)
}

// Helper function to create a temporary file with given content
func createTempFile(t *testing.T, content string) string {
	file, err := ioutil.TempFile("", "testfile")
	assert.NoError(t, err)
	defer file.Close()

	_, err = file.WriteString(content)
	assert.NoError(t, err)

	return file.Name()
}

func TestCheckCompactionProactivenessDisabled(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		fileContent string
		expected    bool
	}{
		{"File does not exist", "", true},
		{"File content is empty", "", true},
		{"File content is zero", "0", true},
		{"File content is negative", "-1", true},
		{"File content is positive", "1", false},
		{"File content is not a number", "abc", true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			tt := tt // Reassign the loop variable
			t.Parallel()
			var filePath string
			if tt.fileContent != "" {
				filePath = createTempFile(t, tt.fileContent)
				defer os.Remove(filePath) // Clean up
			} else {
				filePath = "nonexistentfile"
			}

			result := checkCompactionProactivenessDisabled(filePath)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestGetNumaFragScore(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		fileContent    string
		expectedResult []nodeFragScoreInfo
		expectedError  bool
	}{
		{
			name: "Valid data",
			fileContent: `
Node 0, zone      Normal 0.000 0.014 0.056 0.560 0.849 0.887 0.894 0.904 0.918 0.939 0.962
Node 1, zone      Normal 0.000 0.017 0.061 0.114 0.355 0.703 0.806 0.864 0.901 0.920 0.968
`,
			expectedResult: []nodeFragScoreInfo{
				{Node: 0, Score: 939},
				{Node: 1, Score: 929},
			},
			expectedError: false,
		},
		{
			name:           "File not found",
			fileContent:    "",
			expectedResult: nil,
			expectedError:  true,
		},
		{
			name: "Invalid data format",
			fileContent: `
Node 0, zone      Normal 0.000 abc 0.056 0.560 0.849 0.887 0.894 0.904 0.918 0.939 0.962
`,
			expectedResult: nil,
			expectedError:  true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var filePath string
			if tt.fileContent != "" {
				tmpFile, err := os.CreateTemp("", "fragScoreFile")
				assert.NoError(t, err)
				defer os.Remove(tmpFile.Name())

				_, err = tmpFile.WriteString(tt.fileContent)
				assert.NoError(t, err)
				filePath = tmpFile.Name()
			} else {
				filePath = "nonexistentfile"
			}

			setHostMemCompact(100)

			result, err := GetNumaFragScore(filePath)
			if tt.expectedError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedResult, result)
			}
		})
	}
}
