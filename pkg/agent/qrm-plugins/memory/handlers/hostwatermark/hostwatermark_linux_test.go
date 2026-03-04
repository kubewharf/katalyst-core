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

package hostwatermark

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	coreconfig "github.com/kubewharf/katalyst-core/pkg/config"
	configagent "github.com/kubewharf/katalyst-core/pkg/config/agent"
	configqrm "github.com/kubewharf/katalyst-core/pkg/config/agent/qrm"
	coreconsts "github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	metaagent "github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	metametric "github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	utilmetric "github.com/kubewharf/katalyst-core/pkg/util/metric"
)

var hostWatermarkTestMu sync.Mutex

func makeTestCoreConf(watermarkScaleFactor int, reservedGB uint64) *coreconfig.Configuration {
	return &coreconfig.Configuration{
		AgentConfiguration: &configagent.AgentConfiguration{
			StaticAgentConfiguration: &configagent.StaticAgentConfiguration{
				QRMPluginsConfiguration: &configqrm.QRMPluginsConfiguration{
					MemoryQRMPluginConfig: &configqrm.MemoryQRMPluginConfig{
						HostWatermarkQRMPluginConfig: configqrm.HostWatermarkQRMPluginConfig{
							SetVMWatermarkScaleFactor:  watermarkScaleFactor,
							ReservedKswapdWatermarkGB:  reservedGB,
							EnableSettingHostWatermark: true,
						},
					},
				},
			},
		},
	}
}

func makeTestMetaServerWithNumaTotal(t *testing.T, numaID int, totalBytes uint64) *metaserver.MetaServer {
	t.Helper()

	cpuTopology, err := machine.GenerateDummyCPUTopology(4, 1, 1)
	require.NoError(t, err)

	server := &metaserver.MetaServer{MetaAgent: &metaagent.MetaAgent{}}
	server.KatalystMachineInfo = &machine.KatalystMachineInfo{CPUTopology: cpuTopology}

	fetcher := metametric.NewFakeMetricsFetcher(metrics.DummyMetrics{})
	f, ok := fetcher.(*metametric.FakeMetricsFetcher)
	require.True(t, ok)
	server.MetricsFetcher = fetcher

	f.SetNumaMetric(numaID, coreconsts.MetricMemTotalNuma, utilmetric.MetricData{Value: float64(totalBytes)})
	return server
}

func TestDetermineTargetWatermarkScaleFactor_DirectValue(t *testing.T) {
	t.Parallel()

	conf := makeTestCoreConf(123, 10)

	target, err := determineTargetWatermarkScaleFactor(conf, metrics.DummyMetrics{}, nil)
	require.NoError(t, err)
	require.Equal(t, int64(123), target)
}

func TestDetermineTargetWatermarkScaleFactor_AutoCalc_10G_100G(t *testing.T) {
	t.Parallel()

	server := makeTestMetaServerWithNumaTotal(t, 0, 100<<30)
	conf := makeTestCoreConf(0, 10)

	target, err := determineTargetWatermarkScaleFactor(conf, metrics.DummyMetrics{}, server)
	require.NoError(t, err)
	require.Equal(t, int64(1000), target)
}

func TestDetermineTargetWatermarkScaleFactor_AutoCalc_ZeroTotal(t *testing.T) {
	t.Parallel()

	server := makeTestMetaServerWithNumaTotal(t, 0, 0)
	conf := makeTestCoreConf(0, 10)

	_, err := determineTargetWatermarkScaleFactor(conf, metrics.DummyMetrics{}, server)
	require.Error(t, err)
}

func TestDetermineTargetWatermarkScaleFactor_AutoCalc_NilMetaServer(t *testing.T) {
	t.Parallel()

	conf := makeTestCoreConf(0, 10)

	_, err := determineTargetWatermarkScaleFactor(conf, metrics.DummyMetrics{}, nil)
	require.Error(t, err)
}

func TestClampWatermarkScaleFactor(t *testing.T) {
	t.Parallel()

	require.Equal(t, int64(10), clampWatermarkScaleFactor(1))
	require.Equal(t, int64(10), clampWatermarkScaleFactor(10))
	require.Equal(t, int64(500), clampWatermarkScaleFactor(500))
	require.Equal(t, int64(500), clampWatermarkScaleFactor(1000))
	require.Equal(t, int64(500), clampWatermarkScaleFactor(5000))
}

func TestSetHostWatermark_NoTarget_Skipped(t *testing.T) {
	t.Parallel()

	conf := makeTestCoreConf(0, 0)
	SetHostWatermark(conf, nil, nil, metrics.DummyMetrics{}, nil)
}

func TestSetHostWatermark_SetScaleFactor_WritesFile(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	targetFile := filepath.Join(tmpDir, "watermark_scale_factor")
	require.NoError(t, os.WriteFile(targetFile, []byte("100\n"), 0o644))

	hostWatermarkTestMu.Lock()
	oldPath := watermarkScaleFactorPath
	watermarkScaleFactorPath = targetFile
	defer func() {
		watermarkScaleFactorPath = oldPath
		hostWatermarkTestMu.Unlock()
	}()

	conf := makeTestCoreConf(200, 0)
	SetHostWatermark(conf, nil, nil, metrics.DummyMetrics{}, nil)

	val, err := general.ReadInt64FromFile(targetFile)
	require.NoError(t, err)
	require.Equal(t, int64(200), val)
}

func TestSetHostWatermark_SetScaleFactor_NoClamp(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	targetFile := filepath.Join(tmpDir, "watermark_scale_factor")
	require.NoError(t, os.WriteFile(targetFile, []byte("100\n"), 0o644))

	hostWatermarkTestMu.Lock()
	oldPath := watermarkScaleFactorPath
	watermarkScaleFactorPath = targetFile
	defer func() {
		watermarkScaleFactorPath = oldPath
		hostWatermarkTestMu.Unlock()
	}()

	conf := makeTestCoreConf(1, 0)
	SetHostWatermark(conf, nil, nil, metrics.DummyMetrics{}, nil)

	val, err := general.ReadInt64FromFile(targetFile)
	require.NoError(t, err)
	require.Equal(t, int64(1), val)
}

func TestSetHostWatermark_AutoCalc_ClampMin(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	targetFile := filepath.Join(tmpDir, "watermark_scale_factor")
	require.NoError(t, os.WriteFile(targetFile, []byte("100\n"), 0o644))

	hostWatermarkTestMu.Lock()
	oldPath := watermarkScaleFactorPath
	watermarkScaleFactorPath = targetFile
	defer func() {
		watermarkScaleFactorPath = oldPath
		hostWatermarkTestMu.Unlock()
	}()

	// total=10000GB, reserved=1GB => raw scale=1, should be clamped to watermarkScaleFactorMin(10)
	server := makeTestMetaServerWithNumaTotal(t, 0, 10000<<30)
	conf := makeTestCoreConf(0, 1)
	SetHostWatermark(conf, nil, nil, metrics.DummyMetrics{}, server)

	val, err := general.ReadInt64FromFile(targetFile)
	require.NoError(t, err)
	require.Equal(t, int64(10), val)
}

func TestSetHostWatermark_AutoCalc_ClampMax(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	targetFile := filepath.Join(tmpDir, "watermark_scale_factor")
	require.NoError(t, os.WriteFile(targetFile, []byte("100\n"), 0o644))

	hostWatermarkTestMu.Lock()
	oldPath := watermarkScaleFactorPath
	watermarkScaleFactorPath = targetFile
	defer func() {
		watermarkScaleFactorPath = oldPath
		hostWatermarkTestMu.Unlock()
	}()

	// total=10GB, reserved=10GB => raw scale=10000, should be clamped to watermarkScaleFactorMax(500)
	server := makeTestMetaServerWithNumaTotal(t, 0, 10<<30)
	conf := makeTestCoreConf(0, 10)
	SetHostWatermark(conf, nil, nil, metrics.DummyMetrics{}, server)

	val, err := general.ReadInt64FromFile(targetFile)
	require.NoError(t, err)
	require.Equal(t, int64(500), val)
}
