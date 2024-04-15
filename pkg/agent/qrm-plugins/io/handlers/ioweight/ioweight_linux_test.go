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

package ioweight

import (
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	coreconfig "github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/config/agent"
	configagent "github.com/kubewharf/katalyst-core/pkg/config/agent"
	dynamicconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/qrm"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
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

func TestIOWeightTaskFunc(t *testing.T) {
	t.Parallel()

	IOWeightTaskFunc(nil,
		nil, &dynamicconfig.DynamicAgentConfiguration{}, nil, nil)

	IOWeightTaskFunc(&coreconfig.Configuration{
		AgentConfiguration: &agent.AgentConfiguration{
			StaticAgentConfiguration: &configagent.StaticAgentConfiguration{
				QRMPluginsConfiguration: &qrm.QRMPluginsConfiguration{
					IOQRMPluginConfig: &qrm.IOQRMPluginConfig{
						IOWeightOption: qrm.IOWeightOption{
							EnableSettingIOWeight: false,
						},
					},
				},
			},
		},
	}, nil, &dynamicconfig.DynamicAgentConfiguration{}, nil, nil)

	IOWeightTaskFunc(&coreconfig.Configuration{
		AgentConfiguration: &agent.AgentConfiguration{
			StaticAgentConfiguration: &configagent.StaticAgentConfiguration{
				QRMPluginsConfiguration: &qrm.QRMPluginsConfiguration{
					IOQRMPluginConfig: &qrm.IOQRMPluginConfig{
						IOWeightOption: qrm.IOWeightOption{
							EnableSettingIOWeight: true,
						},
					},
				},
			},
		},
	}, nil, &dynamicconfig.DynamicAgentConfiguration{}, metrics.DummyMetrics{}, nil)

	metaServer, err := makeMetaServer()
	assert.NoError(t, err)
	metaServer.PodFetcher = &pod.PodFetcherStub{PodList: []*v1.Pod{}}

	IOWeightTaskFunc(&coreconfig.Configuration{
		AgentConfiguration: &agent.AgentConfiguration{
			StaticAgentConfiguration: &configagent.StaticAgentConfiguration{
				QRMPluginsConfiguration: &qrm.QRMPluginsConfiguration{
					IOQRMPluginConfig: &qrm.IOQRMPluginConfig{
						IOWeightOption: qrm.IOWeightOption{
							EnableSettingIOWeight: true,
						},
					},
				},
			},
		},
	}, nil, &dynamicconfig.DynamicAgentConfiguration{}, metrics.DummyMetrics{}, metaServer)

	IOWeightTaskFunc(&coreconfig.Configuration{
		AgentConfiguration: &agent.AgentConfiguration{
			StaticAgentConfiguration: &configagent.StaticAgentConfiguration{
				QRMPluginsConfiguration: &qrm.QRMPluginsConfiguration{
					IOQRMPluginConfig: &qrm.IOQRMPluginConfig{
						IOWeightOption: qrm.IOWeightOption{
							EnableSettingIOWeight: true,
						},
					},
				},
			},
		},
	}, metrics.DummyMetrics{}, &dynamicconfig.DynamicAgentConfiguration{}, metrics.DummyMetrics{}, metaServer)

	normalPod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			UID:  "normalPod",
			Name: "normalPod",
		},
		Spec: v1.PodSpec{
			Containers: []v1.Container{
				{
					Name: "c",
				},
			},
		},
	}

	metaServer.PodFetcher = &pod.PodFetcherStub{PodList: []*v1.Pod{normalPod}}

	IOWeightTaskFunc(&coreconfig.Configuration{
		AgentConfiguration: &agent.AgentConfiguration{
			StaticAgentConfiguration: &configagent.StaticAgentConfiguration{
				QRMPluginsConfiguration: &qrm.QRMPluginsConfiguration{
					IOQRMPluginConfig: &qrm.IOQRMPluginConfig{
						IOWeightOption: qrm.IOWeightOption{
							EnableSettingIOWeight: true,
						},
					},
				},
			},
		},
	}, metrics.DummyMetrics{}, &dynamicconfig.DynamicAgentConfiguration{}, metrics.DummyMetrics{}, metaServer)

	IOWeightTaskFunc(&coreconfig.Configuration{
		AgentConfiguration: &agent.AgentConfiguration{
			StaticAgentConfiguration: &configagent.StaticAgentConfiguration{
				QRMPluginsConfiguration: &qrm.QRMPluginsConfiguration{
					IOQRMPluginConfig: &qrm.IOQRMPluginConfig{
						IOWeightOption: qrm.IOWeightOption{
							EnableSettingIOWeight: false,
						},
					},
				},
			},
		},
	}, metrics.DummyMetrics{}, &dynamicconfig.DynamicAgentConfiguration{}, metrics.DummyMetrics{}, metaServer)

	IOWeightTaskFunc(&coreconfig.Configuration{
		AgentConfiguration: &agent.AgentConfiguration{
			StaticAgentConfiguration: &configagent.StaticAgentConfiguration{
				QRMPluginsConfiguration: &qrm.QRMPluginsConfiguration{
					IOQRMPluginConfig: &qrm.IOQRMPluginConfig{
						IOWeightOption: qrm.IOWeightOption{
							EnableSettingIOWeight:         true,
							IOWeightQoSLevelConfigFile:    "fake",
							IOWeightCgroupLevelConfigFile: "fake",
						},
					},
				},
			},
		},
	}, metrics.DummyMetrics{}, &dynamicconfig.DynamicAgentConfiguration{}, metrics.DummyMetrics{}, metaServer)

	applyIOWeightCgroupLevelConfig(&coreconfig.Configuration{
		AgentConfiguration: &agent.AgentConfiguration{
			StaticAgentConfiguration: &configagent.StaticAgentConfiguration{
				QRMPluginsConfiguration: &qrm.QRMPluginsConfiguration{
					IOQRMPluginConfig: &qrm.IOQRMPluginConfig{
						IOWeightOption: qrm.IOWeightOption{
							EnableSettingIOWeight:         true,
							IOWeightCgroupLevelConfigFile: "",
						},
					},
				},
			},
		},
	}, metrics.DummyMetrics{})

	applyIOWeightCgroupLevelConfig(&coreconfig.Configuration{
		AgentConfiguration: &agent.AgentConfiguration{
			StaticAgentConfiguration: &configagent.StaticAgentConfiguration{
				QRMPluginsConfiguration: &qrm.QRMPluginsConfiguration{
					IOQRMPluginConfig: &qrm.IOQRMPluginConfig{
						IOWeightOption: qrm.IOWeightOption{
							EnableSettingIOWeight:         true,
							IOWeightCgroupLevelConfigFile: "fake",
						},
					},
				},
			},
		},
	}, metrics.DummyMetrics{})

	applyIOWeightQoSLevelConfig(&coreconfig.Configuration{
		AgentConfiguration: &agent.AgentConfiguration{
			StaticAgentConfiguration: &configagent.StaticAgentConfiguration{
				QRMPluginsConfiguration: &qrm.QRMPluginsConfiguration{
					IOQRMPluginConfig: &qrm.IOQRMPluginConfig{
						IOWeightOption: qrm.IOWeightOption{
							EnableSettingIOWeight:      true,
							IOWeightQoSLevelConfigFile: "",
						},
					},
				},
			},
		},
	}, metrics.DummyMetrics{}, metaServer)

	applyIOWeightQoSLevelConfig(&coreconfig.Configuration{
		AgentConfiguration: &agent.AgentConfiguration{
			StaticAgentConfiguration: &configagent.StaticAgentConfiguration{
				QRMPluginsConfiguration: &qrm.QRMPluginsConfiguration{
					IOQRMPluginConfig: &qrm.IOQRMPluginConfig{
						IOWeightOption: qrm.IOWeightOption{
							EnableSettingIOWeight:      true,
							IOWeightQoSLevelConfigFile: "fake",
						},
					},
				},
			},
		},
	}, metrics.DummyMetrics{}, metaServer)

	metaServerEmpty, err := makeMetaServer()
	assert.NoError(t, err)
	metaServerEmpty.PodFetcher = &pod.PodFetcherStub{PodList: []*v1.Pod{}}

	jsonContent := `{
        "io_weight": {
        "control_knob_info": {
            "cgroup_subsys_name": "io",
            "cgroup_version_to_iface_name": {
                "v1": "",
                "v2": "io.weight"
            },
            "control_knob_value": "100",
            "oci_property_name": ""
        },
        "pod_explicitly_annotation_key": "IOWeightValue",
        "qos_level_to_default_value": {
            "dedicated_cores": "500",
            "shared_cores": "500"
        }
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

	jsonContent2 := `{
    "fake": 200,
    "fake2": 300
	}`

	// Create a temporary file
	tempFile2, err := ioutil.TempFile("", "test2.json")
	if err != nil {
		fmt.Println("Error creating temporary file:", err)
		return
	}
	defer os.Remove(tempFile2.Name()) // Defer removing the temporary file

	// Write the JSON content to the temporary file
	if _, err := tempFile2.WriteString(jsonContent2); err != nil {
		fmt.Println("Error writing to temporary file:", err)
		return
	}

	absPathCgroup, err := filepath.Abs(tempFile2.Name())
	if err != nil {
		fmt.Println("Error obtaining absolute path:", err)
		return
	}

	applyIOWeightCgroupLevelConfig(&coreconfig.Configuration{
		AgentConfiguration: &agent.AgentConfiguration{
			StaticAgentConfiguration: &configagent.StaticAgentConfiguration{
				QRMPluginsConfiguration: &qrm.QRMPluginsConfiguration{
					IOQRMPluginConfig: &qrm.IOQRMPluginConfig{
						IOWeightOption: qrm.IOWeightOption{
							EnableSettingIOWeight:         true,
							IOWeightCgroupLevelConfigFile: absPathCgroup,
						},
					},
				},
			},
		},
	}, metrics.DummyMetrics{})

	applyIOWeightQoSLevelConfig(&coreconfig.Configuration{
		AgentConfiguration: &agent.AgentConfiguration{
			StaticAgentConfiguration: &configagent.StaticAgentConfiguration{
				QRMPluginsConfiguration: &qrm.QRMPluginsConfiguration{
					IOQRMPluginConfig: &qrm.IOQRMPluginConfig{
						IOWeightOption: qrm.IOWeightOption{
							EnableSettingIOWeight:      true,
							IOWeightQoSLevelConfigFile: absPath,
						},
					},
				},
			},
		},
		GenericConfiguration: &generic.GenericConfiguration{
			QoSConfiguration: nil,
		},
	}, metrics.DummyMetrics{}, metaServer)

	applyIOWeightQoSLevelConfig(&coreconfig.Configuration{
		AgentConfiguration: &agent.AgentConfiguration{
			StaticAgentConfiguration: &configagent.StaticAgentConfiguration{
				QRMPluginsConfiguration: &qrm.QRMPluginsConfiguration{
					IOQRMPluginConfig: &qrm.IOQRMPluginConfig{
						IOWeightOption: qrm.IOWeightOption{
							EnableSettingIOWeight:      true,
							IOWeightQoSLevelConfigFile: absPath,
						},
					},
				},
			},
		},
		GenericConfiguration: &generic.GenericConfiguration{
			QoSConfiguration: nil,
		},
	}, metrics.DummyMetrics{}, metaServerEmpty)
}

func TestApplyPodIOWeight(t *testing.T) {
	t.Parallel()

	pod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			UID:       "test-uid",
			Namespace: "default",
			Name:      "test-pod",
		},
	}

	defaultDevID := "test-dev-id"
	qosLevelDefaultValue := "500"

	err := applyPodIOWeight(pod, defaultDevID, qosLevelDefaultValue)
	assert.Error(t, err)

	err = applyPodIOWeight(pod, defaultDevID, "test")
	assert.Error(t, err)
}
