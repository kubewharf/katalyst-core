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
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"

	apiconfig "github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	memconsts "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/memory/consts"
	coreconfig "github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/config/agent"
	dynamicconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	dynamicqrm "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/adminqos/qrm"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	metaagent "github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	utilmetric "github.com/kubewharf/katalyst-core/pkg/util/metric"
)

var setMemTHPTestMu sync.Mutex

func makeTHPConf(defaultConfig apiconfig.THPMode, threshold int) (*coreconfig.Configuration, *dynamicconfig.DynamicAgentConfiguration) {
	dynamicConf := dynamicconfig.NewDynamicAgentConfiguration()
	dynamicConf.GetDynamicConfiguration().FragMemConfiguration.EnableFragMem = true
	dynamicConf.GetDynamicConfiguration().FragMemConfiguration.THPDefaultConfig = defaultConfig
	dynamicConf.GetDynamicConfiguration().FragMemConfiguration.THPHighOrderScoreThreshold = threshold

	return &coreconfig.Configuration{
		AgentConfiguration: &agent.AgentConfiguration{
			DynamicAgentConfiguration: dynamicConf,
		},
	}, dynamicConf
}

func makeFragMemCoreConf(enable bool, score int) (*coreconfig.Configuration, *dynamicconfig.DynamicAgentConfiguration) {
	dynamicConf := dynamicconfig.NewDynamicAgentConfiguration()
	dynamicConf.GetDynamicConfiguration().FragMemConfiguration.EnableFragMem = enable
	dynamicConf.GetDynamicConfiguration().FragMemConfiguration.MemFragScoreAsync = score

	return &coreconfig.Configuration{
		AgentConfiguration: &agent.AgentConfiguration{
			DynamicAgentConfiguration: dynamicConf,
		},
	}, dynamicConf
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
	conf, dynamicConf := makeFragMemCoreConf(false, 0)
	SetMemCompact(conf, nil, dynamicConf, nil, nil)

	conf, dynamicConf = makeFragMemCoreConf(true, 0)
	SetMemCompact(conf, nil, dynamicConf, metrics.DummyMetrics{}, nil)

	conf, dynamicConf = makeFragMemCoreConf(true, 0)
	SetMemCompact(conf, metrics.DummyMetrics{}, dynamicConf, metrics.DummyMetrics{}, nil)

	metaServer, err := makeMetaServer()
	assert.NoError(t, err)
	metaServer.PodFetcher = &pod.PodFetcherStub{PodList: []*v1.Pod{}}
	conf, dynamicConf = makeFragMemCoreConf(true, 0)
	SetMemCompact(conf, metrics.DummyMetrics{}, dynamicConf, metrics.DummyMetrics{}, metaServer)

	conf, dynamicConf = makeFragMemCoreConf(false, 0)
	SetMemCompact(conf, metrics.DummyMetrics{}, dynamicConf, metrics.DummyMetrics{}, metaServer)

	conf, dynamicConf = makeFragMemCoreConf(true, 80)
	SetMemCompact(conf, metrics.DummyMetrics{}, dynamicConf, metrics.DummyMetrics{}, metaServer)
}

func TestSetMemTHP(t *testing.T) {
	t.Parallel()

	// This test mutates package-level vars (thpEnabledPath/thpShmemEnabledPath), so serialize it.
	setMemTHPTestMu.Lock()
	defer setMemTHPTestMu.Unlock()

	general.RegisterReportCheck(memconsts.SetMemTHP, 0, general.HealthzCheckStateNotReady)

	oldDefragPath := thpDefragPath
	defragFile := createTempFile(t, "always defer defer+madvise madvise [never]\n")
	defer os.Remove(defragFile)
	defer func() { thpDefragPath = oldDefragPath }()
	thpDefragPath = defragFile

	conf, dynamicConf := makeFragMemCoreConf(false, 0)
	SetMemTHP(conf, nil, dynamicConf, nil, nil)

	// Both policies unset: leave defrag unchanged and skip THP mode tuning.
	conf, dynamicConf = makeTHPConf(apiconfig.THPMode(""), 85)
	SetMemTHP(conf, nil, dynamicConf, nil, nil)
	content, err := os.ReadFile(defragFile)
	assert.NoError(t, err)
	assert.Equal(t, "always defer defer+madvise madvise [never]\n", string(content))

	// THPDefaultConfig=never: fast-path to disable THP directly.
	oldEnabledPath := thpEnabledPath
	oldShmemPath := thpShmemEnabledPath
	f := createTempFile(t, "always [madvise] never\n")
	shmemFile := createTempFile(t, "always within_size [advise] never deny force\n")
	defer os.Remove(f)
	defer os.Remove(shmemFile)
	defer func() {
		thpEnabledPath = oldEnabledPath
		thpShmemEnabledPath = oldShmemPath
	}()
	thpEnabledPath = f
	thpShmemEnabledPath = shmemFile

	// An unset defrag policy must not access sysfs or prevent THP mode tuning.
	thpDefragPath = t.TempDir() + "/missing-defrag"
	conf, dynamicConf = makeTHPConf(apiconfig.THPModeNever, 85)
	SetMemTHP(conf, nil, dynamicConf, nil, nil)

	b, rerr := os.ReadFile(f)
	assert.NoError(t, rerr)
	assert.Equal(t, "never\n", string(b))
	b, rerr = os.ReadFile(shmemFile)
	assert.NoError(t, rerr)
	assert.Equal(t, "deny\n", string(b))

	res := general.GetRegisterReadinessCheckResult()
	check, ok := res[general.HealthzCheckName(memconsts.SetMemTHP)]
	assert.True(t, ok)
	assert.True(t, check.Ready)
}

func TestSetMemTHP_HotUpdateEnableFragMem(t *testing.T) {
	t.Parallel()

	setMemTHPTestMu.Lock()
	defer setMemTHPTestMu.Unlock()

	oldPath := thpEnabledPath
	oldShmemPath := thpShmemEnabledPath
	oldDefragPath := thpDefragPath
	defer func() {
		thpEnabledPath = oldPath
		thpShmemEnabledPath = oldShmemPath
		thpDefragPath = oldDefragPath
	}()

	thpFile := createTempFile(t, "always [madvise] never\n")
	shmemFile := createTempFile(t, "always within_size [advise] never deny force\n")
	defragFile := createTempFile(t, "[always] defer defer+madvise madvise never\n")
	defer os.Remove(thpFile)
	defer os.Remove(shmemFile)
	defer os.Remove(defragFile)
	thpEnabledPath = thpFile
	thpShmemEnabledPath = shmemFile
	thpDefragPath = defragFile

	conf, dynamicConf := makeFragMemCoreConf(false, 0)
	dynamicConf.GetDynamicConfiguration().FragMemConfiguration.THPDefaultConfig = apiconfig.THPModeNever
	dynamicConf.GetDynamicConfiguration().FragMemConfiguration.THPDefragPolicy = apiconfig.THPDefragPolicyDefer

	SetMemTHP(conf, nil, dynamicConf, nil, nil)

	content, err := os.ReadFile(thpFile)
	assert.NoError(t, err)
	assert.Equal(t, "always [madvise] never\n", string(content))
	content, err = os.ReadFile(defragFile)
	assert.NoError(t, err)
	assert.Equal(t, "[always] defer defer+madvise madvise never\n", string(content))

	updatedConf := dynamicconfig.NewConfiguration()
	updatedConf.FragMemConfiguration.EnableFragMem = true
	updatedConf.FragMemConfiguration.THPDefaultConfig = apiconfig.THPModeNever
	updatedConf.FragMemConfiguration.THPDefragPolicy = apiconfig.THPDefragPolicyDefer
	dynamicConf.SetDynamicConfiguration(updatedConf)

	SetMemTHP(conf, nil, dynamicConf, nil, nil)

	content, err = os.ReadFile(thpFile)
	assert.NoError(t, err)
	assert.Equal(t, "never\n", string(content))
	content, err = os.ReadFile(defragFile)
	assert.NoError(t, err)
	assert.Equal(t, "defer\n", string(content))

	// Removing the policy stops enforcing it, including after an external change.
	updatedConf = dynamicconfig.NewConfiguration()
	updatedConf.FragMemConfiguration.EnableFragMem = true
	dynamicConf.SetDynamicConfiguration(updatedConf)
	SetMemTHP(conf, nil, dynamicConf, nil, nil)
	content, err = os.ReadFile(defragFile)
	require.NoError(t, err)
	require.Equal(t, "defer\n", string(content))

	require.NoError(t, os.WriteFile(defragFile, []byte("always defer defer+madvise madvise [never]\n"), 0o644))
	SetMemTHP(conf, nil, dynamicConf, nil, nil)
	content, err = os.ReadFile(defragFile)
	require.NoError(t, err)
	require.Equal(t, "always defer defer+madvise madvise [never]\n", string(content))
}

func TestSetMemTHPDefragWithoutStaticTHP(t *testing.T) {
	t.Parallel()

	setMemTHPTestMu.Lock()
	defer setMemTHPTestMu.Unlock()

	oldEnabledPath := thpEnabledPath
	oldShmemPath := thpShmemEnabledPath
	oldDefragPath := thpDefragPath
	defer func() {
		thpEnabledPath = oldEnabledPath
		thpShmemEnabledPath = oldShmemPath
		thpDefragPath = oldDefragPath
	}()

	const initialTHP = "always [madvise] never\n"
	const initialShmem = "always within_size [advise] never deny force\n"
	const initialDefrag = "[always] defer defer+madvise madvise never\n"
	disabledStaticConfig := &dynamicqrm.THPStaticEnableConfiguration{
		Enable: false,
		THP:    apiconfig.THPModeAlways,
		THPShm: apiconfig.THPShmModeForce,
	}
	tests := []struct {
		name         string
		staticConfig *dynamicqrm.THPStaticEnableConfiguration
		defaultMode  apiconfig.THPMode
		policy       apiconfig.THPDefragPolicy
		wantDefrag   string
		wantTHP      string
		wantShmem    string
	}{
		{
			name:       "unset policy without static config",
			wantDefrag: initialDefrag,
			wantTHP:    initialTHP,
			wantShmem:  initialShmem,
		},
		{
			name:       "explicit defer without static config",
			policy:     apiconfig.THPDefragPolicyDefer,
			wantDefrag: "defer\n",
			wantTHP:    initialTHP,
			wantShmem:  initialShmem,
		},
		{
			name:       "custom policy without static config",
			policy:     apiconfig.THPDefragPolicyNever,
			wantDefrag: "never\n",
			wantTHP:    initialTHP,
			wantShmem:  initialShmem,
		},
		{
			name:         "unset policy with static config disabled",
			staticConfig: disabledStaticConfig,
			wantDefrag:   initialDefrag,
			wantTHP:      initialTHP,
			wantShmem:    initialShmem,
		},
		{
			name:         "custom policy with static config disabled",
			staticConfig: disabledStaticConfig,
			policy:       apiconfig.THPDefragPolicyDeferMadvise,
			wantDefrag:   "defer+madvise\n",
			wantTHP:      initialTHP,
			wantShmem:    initialShmem,
		},
		{
			name: "unset policy with static config enabled",
			staticConfig: &dynamicqrm.THPStaticEnableConfiguration{
				Enable: true,
				THP:    apiconfig.THPModeAlways,
				THPShm: apiconfig.THPShmModeForce,
			},
			wantDefrag: initialDefrag,
			wantTHP:    "always\n",
			wantShmem:  "force\n",
		},
		{
			name:         "unset policy and fragmentation-based tuning",
			staticConfig: disabledStaticConfig,
			defaultMode:  apiconfig.THPModeMadvise,
			wantDefrag:   initialDefrag,
			wantTHP:      "never\n",
			wantShmem:    "deny\n",
		},
		{
			name:         "defrag policy and fragmentation-based tuning",
			staticConfig: disabledStaticConfig,
			defaultMode:  apiconfig.THPModeMadvise,
			policy:       apiconfig.THPDefragPolicyNever,
			wantDefrag:   "never\n",
			wantTHP:      "never\n",
			wantShmem:    "deny\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			thpFile := createTempFile(t, initialTHP)
			shmemFile := createTempFile(t, initialShmem)
			defragFile := createTempFile(t, initialDefrag)
			defer os.Remove(thpFile)
			defer os.Remove(shmemFile)
			defer os.Remove(defragFile)
			thpEnabledPath = thpFile
			thpShmemEnabledPath = shmemFile
			thpDefragPath = defragFile

			conf, dynamicConf := makeTHPConf(tt.defaultMode, 85)
			fragMemConf := dynamicConf.GetDynamicConfiguration().FragMemConfiguration
			fragMemConf.THPStaticEnableConfig = tt.staticConfig
			fragMemConf.THPDefragPolicy = tt.policy

			metaServer, err := makeMetaServer()
			if !assert.NoError(t, err) {
				return
			}
			mf := metaServer.MetricsFetcher.(*metric.FakeMetricsFetcher)
			now := time.Now()
			mf.SetNumaMetric(0, consts.MetricMemFragHighOrderScoreNuma, utilmetric.MetricData{Value: 90, Time: &now})
			mf.SetNumaMetric(1, consts.MetricMemFragHighOrderScoreNuma, utilmetric.MetricData{Value: 10, Time: &now})

			SetMemTHP(conf, nil, dynamicConf, metrics.DummyMetrics{}, metaServer)

			for path, want := range map[string]string{
				defragFile: tt.wantDefrag,
				thpFile:    tt.wantTHP,
				shmemFile:  tt.wantShmem,
			} {
				content, err := os.ReadFile(path)
				assert.NoError(t, err)
				assert.Equal(t, want, string(content), path)
			}
		})
	}
}

func TestSetMemTHPStaticEnableConfig(t *testing.T) {
	t.Parallel()

	setMemTHPTestMu.Lock()
	defer setMemTHPTestMu.Unlock()

	oldEnabledPath := thpEnabledPath
	oldShmemPath := thpShmemEnabledPath
	oldDefragPath := thpDefragPath
	defer func() {
		thpEnabledPath = oldEnabledPath
		thpShmemEnabledPath = oldShmemPath
		thpDefragPath = oldDefragPath
	}()

	thpFile := createTempFile(t, "always [madvise] never\n")
	shmemFile := createTempFile(t, "always within_size [advise] never deny force\n")
	defragFile := createTempFile(t, "always [defer] defer+madvise madvise never\n")
	defer os.Remove(thpFile)
	defer os.Remove(shmemFile)
	defer os.Remove(defragFile)
	thpEnabledPath = thpFile
	thpShmemEnabledPath = shmemFile
	thpDefragPath = defragFile

	conf, dynamicConf := makeTHPConf(apiconfig.THPModeMadvise, 85)
	fragMemConf := dynamicConf.GetDynamicConfiguration().FragMemConfiguration
	fragMemConf.THPStaticEnableConfig = &dynamicqrm.THPStaticEnableConfiguration{
		Enable: true,
		THP:    apiconfig.THPModeAlways,
		THPShm: apiconfig.THPShmModeForce,
	}
	fragMemConf.THPDefragPolicy = apiconfig.THPDefragPolicyDeferMadvise

	SetMemTHP(conf, nil, dynamicConf, nil, nil)

	content, err := os.ReadFile(thpFile)
	assert.NoError(t, err)
	assert.Equal(t, "always\n", string(content))

	content, err = os.ReadFile(shmemFile)
	assert.NoError(t, err)
	assert.Equal(t, "force\n", string(content))

	content, err = os.ReadFile(defragFile)
	assert.NoError(t, err)
	assert.Equal(t, "defer+madvise\n", string(content))
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

	oldDefragPath := thpDefragPath
	defragFile := createTempFile(t, "always [defer] defer+madvise madvise never\n")
	defer os.Remove(defragFile)
	defer func() { thpDefragPath = oldDefragPath }()
	thpDefragPath = defragFile

	general.RegisterReportCheck(memconsts.SetMemTHP, 0, general.HealthzCheckStateNotReady)
	conf, dynamicConf := makeTHPConf(apiconfig.THPModeMadvise, 85)
	SetMemTHP(conf, nil, dynamicConf, nil, &metaserver.MetaServer{})
}

func TestSetMemTHPNilMetaServer(t *testing.T) {
	t.Parallel()

	setMemTHPTestMu.Lock()
	defer setMemTHPTestMu.Unlock()

	oldDefragPath := thpDefragPath
	defragFile := createTempFile(t, "always [defer] defer+madvise madvise never\n")
	defer os.Remove(defragFile)
	defer func() { thpDefragPath = oldDefragPath }()
	thpDefragPath = defragFile

	general.RegisterReportCheck(memconsts.SetMemTHP, 0, general.HealthzCheckStateNotReady)
	conf, dynamicConf := makeTHPConf(apiconfig.THPModeMadvise, 85)
	SetMemTHP(conf, nil, dynamicConf, metrics.DummyMetrics{}, nil)
}

func TestDoMemTHPDisable(t *testing.T) {
	t.Parallel()

	setMemTHPTestMu.Lock()
	defer setMemTHPTestMu.Unlock()

	oldEnabledPath := thpEnabledPath
	oldShmemPath := thpShmemEnabledPath
	defer func() {
		thpEnabledPath = oldEnabledPath
		thpShmemEnabledPath = oldShmemPath
	}()

	thpFile := createTempFile(t, "always [madvise] never\n")
	shmemFile := createTempFile(t, "always within_size [advise] never deny force\n")
	defer os.Remove(thpFile)
	defer os.Remove(shmemFile)
	thpEnabledPath = thpFile
	thpShmemEnabledPath = shmemFile

	metaServer, err := makeMetaServer()
	assert.NoError(t, err)
	mf := metaServer.MetricsFetcher.(*metric.FakeMetricsFetcher)
	now := time.Now()
	mf.SetNumaMetric(0, consts.MetricMemFragHighOrderScoreNuma, utilmetric.MetricData{Value: 90, Time: &now})
	mf.SetNumaMetric(1, consts.MetricMemFragHighOrderScoreNuma, utilmetric.MetricData{Value: 10, Time: &now})

	conf := &dynamicqrm.FragMemConfiguration{EnableFragMem: true, THPDefaultConfig: apiconfig.THPModeMadvise, THPHighOrderScoreThreshold: 85}
	err = doMemTHP(conf, metaServer, metrics.DummyMetrics{})
	assert.NoError(t, err)

	b, rerr := os.ReadFile(thpFile)
	assert.NoError(t, rerr)
	assert.Equal(t, "never\n", string(b))
	b, rerr = os.ReadFile(shmemFile)
	assert.NoError(t, rerr)
	assert.Equal(t, "deny\n", string(b))
}

func TestDoMemTHPEnable(t *testing.T) {
	t.Parallel()

	setMemTHPTestMu.Lock()
	defer setMemTHPTestMu.Unlock()

	oldEnabledPath := thpEnabledPath
	oldShmemPath := thpShmemEnabledPath
	defer func() {
		thpEnabledPath = oldEnabledPath
		thpShmemEnabledPath = oldShmemPath
	}()

	thpFile := createTempFile(t, "always madvise [never]\n")
	shmemFile := createTempFile(t, "always within_size advise [never] deny force\n")
	defer os.Remove(thpFile)
	defer os.Remove(shmemFile)
	thpEnabledPath = thpFile
	thpShmemEnabledPath = shmemFile

	metaServer, err := makeMetaServer()
	assert.NoError(t, err)
	mf := metaServer.MetricsFetcher.(*metric.FakeMetricsFetcher)
	now := time.Now()
	mf.SetNumaMetric(0, consts.MetricMemFragHighOrderScoreNuma, utilmetric.MetricData{Value: 10, Time: &now})
	mf.SetNumaMetric(1, consts.MetricMemFragHighOrderScoreNuma, utilmetric.MetricData{Value: 10, Time: &now})

	conf := &dynamicqrm.FragMemConfiguration{EnableFragMem: true, THPDefaultConfig: apiconfig.THPModeMadvise, THPHighOrderScoreThreshold: 85}
	err = doMemTHP(conf, metaServer, metrics.DummyMetrics{})
	assert.NoError(t, err)

	b, rerr := os.ReadFile(thpFile)
	assert.NoError(t, rerr)
	assert.Equal(t, "madvise\n", string(b))
	b, rerr = os.ReadFile(shmemFile)
	assert.NoError(t, rerr)
	assert.Equal(t, "advise\n", string(b))
}

func TestDoMemTHPEnableSkippedDueToMissingOrders(t *testing.T) {
	t.Parallel()

	setMemTHPTestMu.Lock()
	defer setMemTHPTestMu.Unlock()

	oldEnabledPath := thpEnabledPath
	oldShmemPath := thpShmemEnabledPath
	defer func() {
		thpEnabledPath = oldEnabledPath
		thpShmemEnabledPath = oldShmemPath
	}()

	thpFile := createTempFile(t, "never\n")
	shmemFile := createTempFile(t, "never\n")
	defer os.Remove(thpFile)
	defer os.Remove(shmemFile)
	thpEnabledPath = thpFile
	thpShmemEnabledPath = shmemFile

	metaServer, err := makeMetaServer()
	assert.NoError(t, err)
	mf := metaServer.MetricsFetcher.(*metric.FakeMetricsFetcher)
	now := time.Now()
	// Only set metric for NUMA 0. NUMA 1 is missing, so enable should be skipped.
	mf.SetNumaMetric(0, consts.MetricMemFragHighOrderScoreNuma, utilmetric.MetricData{Value: 10, Time: &now})

	conf := &dynamicqrm.FragMemConfiguration{EnableFragMem: true, THPDefaultConfig: apiconfig.THPModeMadvise, THPHighOrderScoreThreshold: 85}
	err = doMemTHP(conf, metaServer, metrics.DummyMetrics{})
	assert.NoError(t, err)

	b, rerr := os.ReadFile(thpFile)
	assert.NoError(t, rerr)
	assert.Equal(t, "never\n", string(b))
	b, rerr = os.ReadFile(shmemFile)
	assert.NoError(t, rerr)
	assert.Equal(t, "never\n", string(b))
}

func TestGetHighOrderThreshold(t *testing.T) {
	t.Parallel()

	assert.InDelta(t, 85.0, getHighOrderThreshold(nil), 1e-6)
	assert.InDelta(t, 85.0, getHighOrderThreshold(&dynamicqrm.FragMemConfiguration{THPDefaultConfig: apiconfig.THPModeMadvise, THPHighOrderScoreThreshold: 0}), 1e-6)
	assert.InDelta(t, 100.0, getHighOrderThreshold(&dynamicqrm.FragMemConfiguration{THPDefaultConfig: apiconfig.THPModeMadvise, THPHighOrderScoreThreshold: 150}), 1e-6)
	assert.InDelta(t, 1.0, getHighOrderThreshold(&dynamicqrm.FragMemConfiguration{THPDefaultConfig: apiconfig.THPModeMadvise, THPHighOrderScoreThreshold: 1}), 1e-6)
}

func TestGetStaticTHPModes(t *testing.T) {
	t.Parallel()

	enabled, thpMode, thpShmMode := getStaticTHPModes(nil)
	assert.False(t, enabled)
	assert.Empty(t, thpMode)
	assert.Empty(t, thpShmMode)

	enabled, thpMode, thpShmMode = getStaticTHPModes(&dynamicqrm.FragMemConfiguration{
		THPStaticEnableConfig: &dynamicqrm.THPStaticEnableConfiguration{Enable: true},
	})
	assert.True(t, enabled)
	assert.Equal(t, apiconfig.THPModeMadvise, thpMode)
	assert.Equal(t, apiconfig.THPShmModeAdvise, thpShmMode)

	enabled, thpMode, thpShmMode = getStaticTHPModes(&dynamicqrm.FragMemConfiguration{
		THPStaticEnableConfig: &dynamicqrm.THPStaticEnableConfiguration{
			Enable: true,
			THP:    apiconfig.THPModeNever,
			THPShm: apiconfig.THPShmModeWithinSize,
		},
	})
	assert.True(t, enabled)
	assert.Equal(t, apiconfig.THPModeNever, thpMode)
	assert.Equal(t, apiconfig.THPShmModeWithinSize, thpShmMode)
}

func TestDisableTHPAtPath(t *testing.T) {
	t.Parallel()

	setMemTHPTestMu.Lock()
	defer setMemTHPTestMu.Unlock()

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

	setMemTHPTestMu.Lock()
	defer setMemTHPTestMu.Unlock()

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

func TestEnableTHPMadviseAtPathRestoreMinFreeKbytes(t *testing.T) {
	t.Parallel()

	setMemTHPTestMu.Lock()
	defer setMemTHPTestMu.Unlock()

	oldEnabledPath := thpEnabledPath
	oldMinFreeKbytesPath := minFreeKbytesPath
	oldApplyTHPModeAtPath := applyTHPModeAtPath
	defer func() {
		thpEnabledPath = oldEnabledPath
		minFreeKbytesPath = oldMinFreeKbytesPath
		applyTHPModeAtPath = oldApplyTHPModeAtPath
	}()

	thpFile := createTempFile(t, "always madvise [never]\n")
	minFreeFile := createTempFile(t, "12345\n")
	defer os.Remove(thpFile)
	defer os.Remove(minFreeFile)

	thpEnabledPath = thpFile
	minFreeKbytesPath = minFreeFile
	applyTHPModeAtPath = func(path, mode string) error {
		if err := os.WriteFile(path, []byte(mode+"\n"), 0o644); err != nil {
			return err
		}
		return os.WriteFile(minFreeFile, []byte("54321\n"), 0o644)
	}

	err := setTHPModeAtPath(thpFile, "madvise")
	assert.NoError(t, err)

	b, rerr := os.ReadFile(thpFile)
	assert.NoError(t, rerr)
	assert.Equal(t, "madvise\n", string(b))

	b, rerr = os.ReadFile(minFreeFile)
	assert.NoError(t, rerr)
	assert.Equal(t, "12345\n", string(b))
}

func TestSetTHPModeAtPathNonMadviseDoesNotRestoreMinFreeKbytes(t *testing.T) {
	t.Parallel()

	setMemTHPTestMu.Lock()
	defer setMemTHPTestMu.Unlock()

	oldEnabledPath := thpEnabledPath
	oldMinFreeKbytesPath := minFreeKbytesPath
	oldApplyTHPModeAtPath := applyTHPModeAtPath
	defer func() {
		thpEnabledPath = oldEnabledPath
		minFreeKbytesPath = oldMinFreeKbytesPath
		applyTHPModeAtPath = oldApplyTHPModeAtPath
	}()

	thpFile := createTempFile(t, "always [madvise] never\n")
	minFreeFile := createTempFile(t, "12345\n")
	defer os.Remove(thpFile)
	defer os.Remove(minFreeFile)

	thpEnabledPath = thpFile
	minFreeKbytesPath = minFreeFile
	applyTHPModeAtPath = func(path, mode string) error {
		if err := os.WriteFile(path, []byte(mode+"\n"), 0o644); err != nil {
			return err
		}
		return os.WriteFile(minFreeFile, []byte("54321\n"), 0o644)
	}

	err := setTHPModeAtPath(thpFile, "never")
	assert.NoError(t, err)

	b, rerr := os.ReadFile(thpFile)
	assert.NoError(t, rerr)
	assert.Equal(t, "never\n", string(b))

	b, rerr = os.ReadFile(minFreeFile)
	assert.NoError(t, rerr)
	assert.Equal(t, "54321\n", string(b))
}

func TestEnableTHPAdviseAtPath(t *testing.T) {
	t.Parallel()

	setMemTHPTestMu.Lock()
	defer setMemTHPTestMu.Unlock()

	// already advise
	f1 := createTempFile(t, "always within_size [advise] never deny force\n")
	defer os.Remove(f1)
	err := setTHPModeAtPath(f1, "advise")
	assert.NoError(t, err)
	b, rerr := os.ReadFile(f1)
	assert.NoError(t, rerr)
	assert.Equal(t, "always within_size [advise] never deny force\n", string(b))

	// should set advise
	f2 := createTempFile(t, "always within_size advise [never] deny force\n")
	defer os.Remove(f2)
	err = setTHPModeAtPath(f2, "advise")
	assert.NoError(t, err)
	b, rerr = os.ReadFile(f2)
	assert.NoError(t, rerr)
	assert.Equal(t, "advise\n", string(b))
}

func TestDisableTHPDenyAtPath(t *testing.T) {
	t.Parallel()

	setMemTHPTestMu.Lock()
	defer setMemTHPTestMu.Unlock()

	// already deny
	f1 := createTempFile(t, "always within_size advise never [deny] force\n")
	defer os.Remove(f1)
	err := setTHPModeAtPath(f1, "deny")
	assert.NoError(t, err)
	b, rerr := os.ReadFile(f1)
	assert.NoError(t, rerr)
	assert.Equal(t, "always within_size advise never [deny] force\n", string(b))

	// should set deny
	f2 := createTempFile(t, "always within_size [advise] never deny force\n")
	defer os.Remove(f2)
	err = setTHPModeAtPath(f2, "deny")
	assert.NoError(t, err)
	b, rerr = os.ReadFile(f2)
	assert.NoError(t, rerr)
	assert.Equal(t, "deny\n", string(b))
}

func TestSetTHPModeAtPathSupportsAllShmemModes(t *testing.T) {
	t.Parallel()

	setMemTHPTestMu.Lock()
	defer setMemTHPTestMu.Unlock()

	for _, mode := range []apiconfig.THPShmMode{
		apiconfig.THPShmModeAlways,
		apiconfig.THPShmModeWithinSize,
		apiconfig.THPShmModeAdvise,
		apiconfig.THPShmModeNever,
		apiconfig.THPShmModeDeny,
		apiconfig.THPShmModeForce,
	} {
		initial := "always within_size advise [never] deny force\n"
		if mode == apiconfig.THPShmModeNever {
			initial = "always within_size [advise] never deny force\n"
		}
		f := createTempFile(t, initial)
		err := setTHPModeAtPath(f, mode)
		assert.NoError(t, err, string(mode))
		b, rerr := os.ReadFile(f)
		assert.NoError(t, rerr)
		assert.Equal(t, string(mode)+"\n", string(b))
		_ = os.Remove(f)
	}
}

func TestSetTHPModeAtPathIfExistsMissing(t *testing.T) {
	t.Parallel()

	err := setTHPModeAtPathIfExists("/tmp/path/does/not/exist/shmem_enabled", "advise")
	assert.NoError(t, err)
}

func TestSetTHPModeAtPathInvalid(t *testing.T) {
	t.Parallel()

	setMemTHPTestMu.Lock()
	defer setMemTHPTestMu.Unlock()

	for _, mode := range []string{"invalid", "defer", "defer+madvise"} {
		t.Run(mode, func(t *testing.T) {
			const initial = "always [madvise] never\n"
			f := createTempFile(t, initial)
			defer os.Remove(f)

			err := setTHPModeAtPath(f, mode)
			if assert.Error(t, err) {
				assert.Contains(t, err.Error(), "invalid THP mode")
			}
			content, err := os.ReadFile(f)
			assert.NoError(t, err)
			assert.Equal(t, initial, string(content))
		})
	}
}

func TestSetTHPDefragPolicyAtPath(t *testing.T) {
	t.Parallel()

	setMemTHPTestMu.Lock()
	defer setMemTHPTestMu.Unlock()

	const initial = "[always] defer defer+madvise madvise never\n"
	tests := []struct {
		name    string
		policy  apiconfig.THPDefragPolicy
		want    string
		wantErr bool
	}{
		{name: "already always", policy: apiconfig.THPDefragPolicyAlways, want: initial},
		{name: "defer", policy: apiconfig.THPDefragPolicyDefer, want: "defer\n"},
		{name: "defer madvise", policy: apiconfig.THPDefragPolicyDeferMadvise, want: "defer+madvise\n"},
		{name: "madvise", policy: apiconfig.THPDefragPolicyMadvise, want: "madvise\n"},
		{name: "never", policy: apiconfig.THPDefragPolicyNever, want: "never\n"},
		{name: "empty leaves current policy unchanged", want: initial},
		{name: "whitespace leaves current policy unchanged", policy: " \t\n", want: initial},
		{name: "normalize", policy: " Defer+Madvise ", want: "defer+madvise\n"},
		{name: "reject force", policy: "force", want: initial, wantErr: true},
		{name: "reject deny", policy: "deny", want: initial, wantErr: true},
		{name: "reject advise", policy: "advise", want: initial, wantErr: true},
		{name: "reject within_size", policy: "within_size", want: initial, wantErr: true},
		{name: "reject unknown policy", policy: "invalid", want: initial, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := createTempFile(t, initial)
			defer os.Remove(f)

			err := setTHPDefragPolicyAtPath(f, tt.policy)
			if tt.wantErr {
				if assert.Error(t, err) {
					assert.Contains(t, err.Error(), "invalid THP defrag policy")
				}
			} else {
				assert.NoError(t, err)
			}
			content, err := os.ReadFile(f)
			assert.NoError(t, err)
			assert.Equal(t, tt.want, string(content))
		})
	}
}

func TestSetTHPDefragPolicyAtPathUnsetDoesNotAccessSysfs(t *testing.T) {
	t.Parallel()

	setMemTHPTestMu.Lock()
	defer setMemTHPTestMu.Unlock()

	path := t.TempDir() + "/missing-defrag"
	for _, policy := range []apiconfig.THPDefragPolicy{"", " \t\n"} {
		require.NoError(t, setTHPDefragPolicyAtPath(path, policy))
		_, err := os.Stat(path)
		require.ErrorIs(t, err, os.ErrNotExist)
	}
}

func TestSetTHPDefragPolicyAtPathDoesNotRestoreMinFreeKbytes(t *testing.T) {
	t.Parallel()

	setMemTHPTestMu.Lock()
	defer setMemTHPTestMu.Unlock()

	oldMinFreeKbytesPath := minFreeKbytesPath
	oldApplyTHPModeAtPath := applyTHPModeAtPath
	defer func() {
		minFreeKbytesPath = oldMinFreeKbytesPath
		applyTHPModeAtPath = oldApplyTHPModeAtPath
	}()

	defragFile := createTempFile(t, "always [defer] defer+madvise madvise never\n")
	minFreeFile := createTempFile(t, "12345\n")
	defer os.Remove(defragFile)
	defer os.Remove(minFreeFile)
	minFreeKbytesPath = minFreeFile
	applyTHPModeAtPath = func(path, value string) error {
		if err := os.WriteFile(path, []byte(value+"\n"), 0o644); err != nil {
			return err
		}
		return os.WriteFile(minFreeFile, []byte("54321\n"), 0o644)
	}

	err := setTHPDefragPolicyAtPath(defragFile, apiconfig.THPDefragPolicyMadvise)
	assert.NoError(t, err)
	content, err := os.ReadFile(minFreeFile)
	assert.NoError(t, err)
	assert.Equal(t, "54321\n", string(content), "defrag writes must not restore vm.min_free_kbytes")
}

func TestWriteTHPSettingAtPathVerification(t *testing.T) {
	t.Parallel()

	setMemTHPTestMu.Lock()
	defer setMemTHPTestMu.Unlock()

	oldApply := applyTHPModeAtPath
	defer func() { applyTHPModeAtPath = oldApply }()

	const initial = "[always] defer defer+madvise madvise never\n"
	tests := []struct {
		name       string
		before     string
		after      string
		writeErr   error
		remove     bool
		wantWrites int
		wantErr    string
	}{
		{name: "bracketed selection", before: initial, after: "always [defer] defer+madvise madvise never\n", wantWrites: 1},
		{name: "bare value", before: initial, after: "defer\n", wantWrites: 1},
		{name: "bracketed idempotence", before: "always [defer] defer+madvise madvise never\n"},
		{name: "bare idempotence", before: "defer\n"},
		{name: "substring is not active target", before: "always defer [defer+madvise] madvise never\n", after: "always [defer] defer+madvise madvise never\n", wantWrites: 1},
		{name: "supported but inactive target", before: initial, after: initial, wantWrites: 1, wantErr: "verification failed"},
		{name: "active substring mismatch", before: initial, after: "always defer [defer+madvise] madvise never\n", wantWrites: 1, wantErr: "verification failed"},
		{name: "bare substring mismatch", before: initial, after: "defer+madvise\n", wantWrites: 1, wantErr: "verification failed"},
		{name: "empty read back", before: initial, wantWrites: 1, wantErr: "missing active THP setting"},
		{name: "whitespace read back", before: initial, after: " \t\n", wantWrites: 1, wantErr: "missing active THP setting"},
		{name: "missing selection", before: initial, after: "always defer defer+madvise madvise never\n", wantWrites: 1, wantErr: "missing active THP setting"},
		{name: "multiple selections", before: initial, after: "[always] [defer] never\n", wantWrites: 1, wantErr: "invalid THP setting content"},
		{name: "malformed brackets", before: initial, after: "always [defer never\n", wantWrites: 1, wantErr: "invalid THP setting content"},
		{name: "nested brackets", before: initial, after: "always [[defer]] never\n", wantWrites: 1, wantErr: "invalid THP setting content"},
		{name: "empty selection", before: initial, after: "always [] defer never\n", wantWrites: 1, wantErr: "invalid THP setting content"},
		{name: "read back error", before: initial, remove: true, wantWrites: 1, wantErr: "read back THP setting file"},
		{name: "write error", before: initial, writeErr: os.ErrPermission, wantWrites: 1, wantErr: "set THP setting failed"},
		{name: "empty initial content", wantErr: "missing active THP setting"},
		{name: "ambiguous initial selection", before: "[always] [defer] never\n", wantErr: "invalid THP setting content"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := createTempFile(t, tt.before)
			defer os.Remove(path)
			writes := 0
			applyTHPModeAtPath = func(gotPath, value string) error {
				writes++
				require.Equal(t, path, gotPath)
				require.Equal(t, "defer", value)
				if tt.writeErr != nil {
					return tt.writeErr
				}
				if tt.remove {
					return os.Remove(path)
				}
				return os.WriteFile(path, []byte(tt.after), 0o644)
			}

			err := writeTHPSettingAtPath(path, "defer")
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, tt.wantWrites, writes)
		})
	}
}

func TestSetTHPModeAtPathIfExistsReadBackMissing(t *testing.T) {
	t.Parallel()

	setMemTHPTestMu.Lock()
	defer setMemTHPTestMu.Unlock()

	oldApply := applyTHPModeAtPath
	defer func() { applyTHPModeAtPath = oldApply }()
	path := createTempFile(t, "always within_size advise [never] deny force\n")
	defer os.Remove(path)
	applyTHPModeAtPath = func(path, _ string) error {
		return os.Remove(path)
	}

	err := setTHPModeAtPathIfExists(path, apiconfig.THPShmModeAdvise)
	require.ErrorContains(t, err, "read back THP setting file")
	require.ErrorIs(t, err, os.ErrNotExist, "only paths absent before applying may be skipped")
}

func TestSetTHPModeAtPathVerificationFailureRestoresMinFreeKbytes(t *testing.T) {
	t.Parallel()

	setMemTHPTestMu.Lock()
	defer setMemTHPTestMu.Unlock()

	oldEnabledPath, oldMinFreePath, oldApply := thpEnabledPath, minFreeKbytesPath, applyTHPModeAtPath
	defer func() {
		thpEnabledPath, minFreeKbytesPath, applyTHPModeAtPath = oldEnabledPath, oldMinFreePath, oldApply
	}()
	thpFile := createTempFile(t, "always madvise [never]\n")
	minFreeFile := createTempFile(t, "12345\n")
	defer os.Remove(thpFile)
	defer os.Remove(minFreeFile)
	thpEnabledPath, minFreeKbytesPath = thpFile, minFreeFile

	for _, remove := range []bool{false, true} {
		require.NoError(t, os.WriteFile(thpFile, []byte("always madvise [never]\n"), 0o644))
		applyTHPModeAtPath = func(path, _ string) error {
			if err := os.WriteFile(minFreeFile, []byte("54321\n"), 0o644); err != nil {
				return err
			}
			if remove {
				return os.Remove(path)
			}
			return nil // Leave the old selection active to simulate an ineffective write.
		}

		require.Error(t, setTHPModeAtPath(thpFile, apiconfig.THPModeMadvise))
		content, err := os.ReadFile(minFreeFile)
		require.NoError(t, err)
		require.Equal(t, "12345\n", string(content))
	}
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
