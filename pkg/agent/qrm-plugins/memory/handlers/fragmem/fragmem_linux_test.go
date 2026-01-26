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
	"encoding/json"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"

	memconsts "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/memory/consts"
	coreconfig "github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/config/agent"
	configagent "github.com/kubewharf/katalyst-core/pkg/config/agent"
	dynamicconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/qrm"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	metaagent "github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric"
	malachiteclient "github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/provisioner/malachite/client"
	malachitetypes "github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/provisioner/malachite/types"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

var setMemTHPTestMu sync.Mutex

func makeTHPConf(defaultConfig string, threshold int) *coreconfig.Configuration {
	return &coreconfig.Configuration{
		AgentConfiguration: &agent.AgentConfiguration{
			StaticAgentConfiguration: &configagent.StaticAgentConfiguration{
				QRMPluginsConfiguration: &qrm.QRMPluginsConfiguration{
					MemoryQRMPluginConfig: &qrm.MemoryQRMPluginConfig{
						FragMemOptions: qrm.FragMemOptions{
							EnableSettingFragMem:       true,
							THPDefaultConfig:           defaultConfig,
							THPHighOrderScoreThreshold: threshold,
						},
					},
				},
			},
		},
	}
}

func newSystemMemoryServer(t *testing.T, data *malachitetypes.SystemMemoryData) *httptest.Server {
	t.Helper()

	rsp := &malachitetypes.MalachiteSystemMemoryResponse{
		Status: 0,
		Data:   *data,
	}
	b, err := json.Marshal(rsp)
	assert.NoError(t, err)

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(b)
	}))
}

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

func TestSetMemCompact(t *testing.T) {
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
							SetMemFragScoreAsync: 80,
						},
					},
				},
			},
		},
	}, metrics.DummyMetrics{}, &dynamicconfig.DynamicAgentConfiguration{}, metrics.DummyMetrics{}, metaServer)
}

func TestSetMemTHP(t *testing.T) {
	t.Parallel()

	// This test mutates package-level vars (thpEnabledPath), so serialize it.
	setMemTHPTestMu.Lock()
	defer setMemTHPTestMu.Unlock()

	general.RegisterReportCheck(memconsts.SetMemTHP, 0, general.HealthzCheckStateNotReady)

	SetMemTHP(&coreconfig.Configuration{
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

	// THPDefaultConfig empty: skip THP tuning entirely.
	SetMemTHP(&coreconfig.Configuration{
		AgentConfiguration: &agent.AgentConfiguration{
			StaticAgentConfiguration: &configagent.StaticAgentConfiguration{
				QRMPluginsConfiguration: &qrm.QRMPluginsConfiguration{
					MemoryQRMPluginConfig: &qrm.MemoryQRMPluginConfig{
						FragMemOptions: qrm.FragMemOptions{
							EnableSettingFragMem: true,
							THPDefaultConfig:     "",
						},
					},
				},
			},
		},
	}, nil, &dynamicconfig.DynamicAgentConfiguration{}, nil, nil)

	// THPDefaultConfig=never: fast-path to disable THP directly.
	oldPath := thpEnabledPath
	f := createTempFile(t, "always [madvise] never\n")
	defer os.Remove(f)
	defer func() { thpEnabledPath = oldPath }()
	thpEnabledPath = f

	SetMemTHP(&coreconfig.Configuration{
		AgentConfiguration: &agent.AgentConfiguration{
			StaticAgentConfiguration: &configagent.StaticAgentConfiguration{
				QRMPluginsConfiguration: &qrm.QRMPluginsConfiguration{
					MemoryQRMPluginConfig: &qrm.MemoryQRMPluginConfig{
						FragMemOptions: qrm.FragMemOptions{
							EnableSettingFragMem: true,
							THPDefaultConfig:     "never",
						},
					},
				},
			},
		},
	}, nil, &dynamicconfig.DynamicAgentConfiguration{}, nil, nil)

	b, rerr := os.ReadFile(f)
	assert.NoError(t, rerr)
	assert.Equal(t, "never\n", string(b))

	res := general.GetRegisterReadinessCheckResult()
	check, ok := res[general.HealthzCheckName(memconsts.SetMemTHP)]
	assert.True(t, ok)
	assert.True(t, check.Ready)
}

func TestSetMemTHPNilConf(t *testing.T) {
	t.Parallel()

	setMemTHPTestMu.Lock()
	defer setMemTHPTestMu.Unlock()

	general.RegisterReportCheck(memconsts.SetMemTHP, 0, general.HealthzCheckStateNotReady)
	SetMemTHP(nil, nil, &dynamicconfig.DynamicAgentConfiguration{}, nil, nil)
}

func TestSetMemTHPNilEmitter(t *testing.T) {
	t.Parallel()

	setMemTHPTestMu.Lock()
	defer setMemTHPTestMu.Unlock()

	general.RegisterReportCheck(memconsts.SetMemTHP, 0, general.HealthzCheckStateNotReady)
	conf := makeTHPConf("madvise", 85)
	SetMemTHP(conf, nil, &dynamicconfig.DynamicAgentConfiguration{}, nil, &metaserver.MetaServer{})
}

func TestSetMemTHPNilMetaServer(t *testing.T) {
	t.Parallel()

	setMemTHPTestMu.Lock()
	defer setMemTHPTestMu.Unlock()

	general.RegisterReportCheck(memconsts.SetMemTHP, 0, general.HealthzCheckStateNotReady)
	conf := makeTHPConf("madvise", 85)
	SetMemTHP(conf, nil, &dynamicconfig.DynamicAgentConfiguration{}, metrics.DummyMetrics{}, nil)
}

func TestDoMemTHPDisable(t *testing.T) {
	t.Parallel()

	setMemTHPTestMu.Lock()
	defer setMemTHPTestMu.Unlock()

	oldPath := thpEnabledPath
	oldNewClient := newMalachiteClient
	defer func() {
		thpEnabledPath = oldPath
		newMalachiteClient = oldNewClient
	}()

	thpFile := createTempFile(t, "always [madvise] never\n")
	defer os.Remove(thpFile)
	thpEnabledPath = thpFile

	server := newSystemMemoryServer(t, &malachitetypes.SystemMemoryData{
		ExtFrag: []malachitetypes.ExtFrag{{
			ID:             0,
			MemOrderScores: []malachitetypes.MemOrderScore{{Order: 9, Score: 90}, {Order: 10, Score: 90}},
		}},
		UpdateTime: time.Now().Unix(),
	})
	defer server.Close()

	newMalachiteClient = func(fetcher pod.PodFetcher, emitter metrics.MetricEmitter) *malachiteclient.MalachiteClient {
		c := malachiteclient.NewMalachiteClient(fetcher, emitter)
		c.SetURL(map[string]string{malachiteclient.SystemMemoryResource: server.URL})
		return c
	}

	err := doMemTHP(makeTHPConf("madvise", 85), &metaserver.MetaServer{MetaAgent: &metaagent.MetaAgent{PodFetcher: &pod.PodFetcherStub{}}}, metrics.DummyMetrics{})
	assert.NoError(t, err)

	b, rerr := os.ReadFile(thpFile)
	assert.NoError(t, rerr)
	assert.Equal(t, "never\n", string(b))
}

func TestDoMemTHPEnable(t *testing.T) {
	t.Parallel()

	setMemTHPTestMu.Lock()
	defer setMemTHPTestMu.Unlock()

	oldPath := thpEnabledPath
	oldNewClient := newMalachiteClient
	defer func() {
		thpEnabledPath = oldPath
		newMalachiteClient = oldNewClient
	}()

	thpFile := createTempFile(t, "always madvise [never]\n")
	defer os.Remove(thpFile)
	thpEnabledPath = thpFile

	server := newSystemMemoryServer(t, &malachitetypes.SystemMemoryData{
		ExtFrag: []malachitetypes.ExtFrag{{
			ID:             0,
			MemOrderScores: []malachitetypes.MemOrderScore{{Order: 9, Score: 10}, {Order: 10, Score: 10}},
		}},
		UpdateTime: time.Now().Unix(),
	})
	defer server.Close()

	newMalachiteClient = func(fetcher pod.PodFetcher, emitter metrics.MetricEmitter) *malachiteclient.MalachiteClient {
		c := malachiteclient.NewMalachiteClient(fetcher, emitter)
		c.SetURL(map[string]string{malachiteclient.SystemMemoryResource: server.URL})
		return c
	}

	err := doMemTHP(makeTHPConf("madvise", 85), &metaserver.MetaServer{MetaAgent: &metaagent.MetaAgent{PodFetcher: &pod.PodFetcherStub{}}}, metrics.DummyMetrics{})
	assert.NoError(t, err)

	b, rerr := os.ReadFile(thpFile)
	assert.NoError(t, rerr)
	assert.Equal(t, "madvise\n", string(b))
}

func TestDoMemTHPEnableSkippedDueToMissingOrders(t *testing.T) {
	t.Parallel()

	setMemTHPTestMu.Lock()
	defer setMemTHPTestMu.Unlock()

	oldPath := thpEnabledPath
	oldNewClient := newMalachiteClient
	defer func() {
		thpEnabledPath = oldPath
		newMalachiteClient = oldNewClient
	}()

	thpFile := createTempFile(t, "never\n")
	defer os.Remove(thpFile)
	thpEnabledPath = thpFile

	server := newSystemMemoryServer(t, &malachitetypes.SystemMemoryData{
		ExtFrag: []malachitetypes.ExtFrag{{
			ID:             0,
			MemOrderScores: []malachitetypes.MemOrderScore{{Order: 9, Score: 10}}, // missing order 10
		}},
		UpdateTime: time.Now().Unix(),
	})
	defer server.Close()

	newMalachiteClient = func(fetcher pod.PodFetcher, emitter metrics.MetricEmitter) *malachiteclient.MalachiteClient {
		c := malachiteclient.NewMalachiteClient(fetcher, emitter)
		c.SetURL(map[string]string{malachiteclient.SystemMemoryResource: server.URL})
		return c
	}

	err := doMemTHP(makeTHPConf("madvise", 85), &metaserver.MetaServer{MetaAgent: &metaagent.MetaAgent{PodFetcher: &pod.PodFetcherStub{}}}, metrics.DummyMetrics{})
	assert.NoError(t, err)

	b, rerr := os.ReadFile(thpFile)
	assert.NoError(t, rerr)
	assert.Equal(t, "never\n", string(b))
}

func TestGetHighOrderThreshold(t *testing.T) {
	t.Parallel()

	assert.InDelta(t, 85.0, getHighOrderThreshold(nil), 1e-6)
	assert.InDelta(t, 85.0, getHighOrderThreshold(makeTHPConf("madvise", 0)), 1e-6)
	assert.InDelta(t, 100.0, getHighOrderThreshold(makeTHPConf("madvise", 150)), 1e-6)
	assert.InDelta(t, 1.0, getHighOrderThreshold(makeTHPConf("madvise", 1)), 1e-6)
}

func TestCalcHighOrderScore(t *testing.T) {
	t.Parallel()

	score, missing, ok := calcHighOrderScore(
		[]malachitetypes.MemOrderScore{{Order: 10, Score: 100}, {Order: 9, Score: 80}},
	)
	assert.True(t, ok)
	assert.Nil(t, missing)
	assert.InDelta(t, 90.0, score, 1e-6)

	_, missing, ok = calcHighOrderScore([]malachitetypes.MemOrderScore{{Order: 9, Score: 100}})
	assert.False(t, ok)
	assert.Equal(t, []int{10}, missing)

	_, _, ok = calcHighOrderScore(nil)
	assert.False(t, ok)
}

func TestDisableTHPAtPath(t *testing.T) {
	t.Parallel()

	// already disabled
	f1 := createTempFile(t, "always madvise [never]\n")
	defer os.Remove(f1)
	err := setTHPModeAtPath(f1, "never")
	assert.NoError(t, err)
	b, rerr := os.ReadFile(f1)
	assert.NoError(t, rerr)
	assert.Equal(t, "always madvise [never]\n", string(b))

	// should disable
	f2 := createTempFile(t, "always [madvise] never\n")
	defer os.Remove(f2)
	err = setTHPModeAtPath(f2, "never")
	assert.NoError(t, err)
	b, rerr = os.ReadFile(f2)
	assert.NoError(t, rerr)
	assert.Equal(t, "never\n", string(b))
}

func TestEnableTHPMadviseAtPath(t *testing.T) {
	t.Parallel()

	// already madvise
	f1 := createTempFile(t, "always [madvise] never\n")
	defer os.Remove(f1)
	err := setTHPModeAtPath(f1, "madvise")
	assert.NoError(t, err)
	b, rerr := os.ReadFile(f1)
	assert.NoError(t, rerr)
	assert.Equal(t, "always [madvise] never\n", string(b))

	// should set madvise
	f2 := createTempFile(t, "always madvise [never]\n")
	defer os.Remove(f2)
	err = setTHPModeAtPath(f2, "madvise")
	assert.NoError(t, err)
	b, rerr = os.ReadFile(f2)
	assert.NoError(t, rerr)
	assert.Equal(t, "madvise\n", string(b))
}

func TestSetTHPModeAtPathInvalid(t *testing.T) {
	t.Parallel()

	f := createTempFile(t, "always [madvise] never\n")
	defer os.Remove(f)
	err := setTHPModeAtPath(f, "invalid")
	assert.Error(t, err)
}

func TestDecideTHPDecision(t *testing.T) {
	t.Parallel()

	threshold := 100.0
	assert.Equal(t, thpDecisionDisable, decideTHPDecision(100.1, threshold))
	assert.Equal(t, thpDecisionNone, decideTHPDecision(100.0, threshold))
	assert.Equal(t, thpDecisionNone, decideTHPDecision(95.0, threshold))
	assert.Equal(t, thpDecisionEnable, decideTHPDecision(89.9, threshold)) // < 100*0.9
	assert.Equal(t, thpDecisionNone, decideTHPDecision(90.0, threshold))   // == 100*0.9
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

func TestSetDelayTimes(t *testing.T) {
	t.Parallel()
	// Test case 1: Set delayTimes to a positive value
	SetDelayTimes(10)
	assert.Equal(t, 10, GetDelayTimes(), "Expected delayTimes to be 10")

	// Test case 2: Set delayTimes to zero
	SetDelayTimes(0)
	assert.Equal(t, 0, GetDelayTimes(), "Expected delayTimes to be 0")

	// Test case 3: Set delayTimes to a negative value
	SetDelayTimes(-5)
	assert.Equal(t, -5, GetDelayTimes(), "Expected delayTimes to be -5")
}

func TestSetHostCompact(t *testing.T) {
	t.Parallel()
	setHostMemCompact(25535)
}
