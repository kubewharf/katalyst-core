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

	"github.com/bytedance/mockey"
	"github.com/prometheus/procfs"
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
	procm "github.com/kubewharf/katalyst-core/pkg/util/procfs/manager"
)

var hostWatermarkTestMu sync.Mutex

var (
	defaultMemTotal      uint64 = 1000
	defaultHugePageSize  uint64 = 10
	defaultHugePageTotal uint64 = 10
)

func makeNormalMemInfo() procfs.Meminfo {
	mi := procfs.Meminfo{
		MemTotal:       &defaultMemTotal,
		Hugepagesize:   &defaultHugePageSize,
		HugePagesTotal: &defaultHugePageTotal,
	}
	return mi
}

func makeNilMemInfo() procfs.Meminfo {
	return procfs.Meminfo{}
}

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

	hostWatermarkTestMu.Lock()
	defer hostWatermarkTestMu.Unlock()
	mockey.PatchConvey("test determine target watermark scale factor with direct_value", t, func() {
		mockey.Mock(procm.GetMemInfo).IncludeCurrentGoRoutine().Return(makeNilMemInfo(), nil).Build()

		target, err := determineTargetWatermarkScaleFactor(conf, metrics.DummyMetrics{}, nil)
		require.NoError(t, err)
		require.Equal(t, int64(123), target)
	})
}

func TestDetermineTargetWatermarkScaleFactor_AutoCalc_10G_100G(t *testing.T) {
	t.Parallel()

	server := makeTestMetaServerWithNumaTotal(t, 0, 100<<30)
	conf := makeTestCoreConf(0, 10)

	hostWatermarkTestMu.Lock()
	defer hostWatermarkTestMu.Unlock()
	mockey.PatchConvey("test determine target watermark scale factor with auto_calc_10G/100G", t, func() {
		mockey.Mock(procm.GetMemInfo).IncludeCurrentGoRoutine().Return(makeNilMemInfo(), nil).Build()

		target, err := determineTargetWatermarkScaleFactor(conf, metrics.DummyMetrics{}, server)
		require.NoError(t, err)
		require.Equal(t, int64(1000), target)
	})
}

func TestDetermineTargetWatermarkScaleFactor_AutoCalc_ZeroTotal(t *testing.T) {
	t.Parallel()

	server := makeTestMetaServerWithNumaTotal(t, 0, 0)
	conf := makeTestCoreConf(0, 10)

	hostWatermarkTestMu.Lock()
	defer hostWatermarkTestMu.Unlock()
	mockey.PatchConvey("test determine target watermark scale factor with auto_calc_zero_total", t, func() {
		mockey.Mock(procm.GetMemInfo).IncludeCurrentGoRoutine().Return(makeNilMemInfo(), nil).Build()

		_, err := determineTargetWatermarkScaleFactor(conf, metrics.DummyMetrics{}, server)
		require.Error(t, err)
	})
}

func TestDetermineTargetWatermarkScaleFactor_AutoCalc_NilMetaServer(t *testing.T) {
	t.Parallel()

	conf := makeTestCoreConf(0, 10)

	hostWatermarkTestMu.Lock()
	defer hostWatermarkTestMu.Unlock()
	mockey.PatchConvey("test determine target watermark scale factor with auto_calc_nil_meta_server", t, func() {
		mockey.Mock(procm.GetMemInfo).IncludeCurrentGoRoutine().Return(makeNilMemInfo(), nil).Build()

		_, err := determineTargetWatermarkScaleFactor(conf, metrics.DummyMetrics{}, nil)
		require.Error(t, err)
	})
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

	hostWatermarkTestMu.Lock()
	defer hostWatermarkTestMu.Unlock()
	mockey.PatchConvey("test set host watermark with no_target", t, func() {
		mockey.Mock(procm.GetMemInfo).IncludeCurrentGoRoutine().Return(makeNilMemInfo(), nil).Build()

		conf := makeTestCoreConf(0, 0)
		SetHostWatermark(conf, nil, nil, metrics.DummyMetrics{}, nil)
	})
}

func TestSetHostWatermark_SetScaleFactor_WritesFile(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	targetFile := filepath.Join(tmpDir, "watermark_scale_factor")
	require.NoError(t, os.WriteFile(targetFile, []byte("100\n"), 0o644))

	var oldPath string
	hostWatermarkTestMu.Lock()
	defer func() {
		watermarkScaleFactorPath = oldPath
		hostWatermarkTestMu.Unlock()
	}()

	mockey.PatchConvey("test set host watermark with write_file", t, func() {
		oldPath = watermarkScaleFactorPath
		watermarkScaleFactorPath = targetFile
		mockey.Mock(procm.GetMemInfo).IncludeCurrentGoRoutine().Return(makeNilMemInfo(), nil).Build()

		conf := makeTestCoreConf(200, 0)
		SetHostWatermark(conf, nil, nil, metrics.DummyMetrics{}, nil)

		val, err := general.ReadInt64FromFile(targetFile)
		require.NoError(t, err)
		require.Equal(t, int64(200), val)
	})
}

func TestSetHostWatermark_SetScaleFactor_NoClamp(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	targetFile := filepath.Join(tmpDir, "watermark_scale_factor")
	require.NoError(t, os.WriteFile(targetFile, []byte("100\n"), 0o644))

	var oldPath string
	hostWatermarkTestMu.Lock()
	defer func() {
		watermarkScaleFactorPath = oldPath
		hostWatermarkTestMu.Unlock()
	}()

	mockey.PatchConvey("test set host watermark with no_clamp", t, func() {
		oldPath = watermarkScaleFactorPath
		watermarkScaleFactorPath = targetFile
		mockey.Mock(procm.GetMemInfo).IncludeCurrentGoRoutine().Return(makeNilMemInfo(), nil).Build()

		conf := makeTestCoreConf(1, 0)
		SetHostWatermark(conf, nil, nil, metrics.DummyMetrics{}, nil)

		val, err := general.ReadInt64FromFile(targetFile)
		require.NoError(t, err)
		require.Equal(t, int64(1), val)
	})
}

func TestSetHostWatermark_AutoCalc_ClampMin(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	targetFile := filepath.Join(tmpDir, "watermark_scale_factor")
	require.NoError(t, os.WriteFile(targetFile, []byte("100\n"), 0o644))

	var oldPath string
	hostWatermarkTestMu.Lock()
	defer func() {
		watermarkScaleFactorPath = oldPath
		hostWatermarkTestMu.Unlock()
	}()

	mockey.PatchConvey("test set host watermark with auto_calc_clamp_min", t, func() {
		oldPath = watermarkScaleFactorPath
		watermarkScaleFactorPath = targetFile
		mockey.Mock(procm.GetMemInfo).IncludeCurrentGoRoutine().Return(makeNilMemInfo(), nil).Build()

		// total=10000GB, reserved=1GB => raw scale=1, should be clamped to watermarkScaleFactorMin(10)
		server := makeTestMetaServerWithNumaTotal(t, 0, 10000<<30)
		conf := makeTestCoreConf(0, 1)
		SetHostWatermark(conf, nil, nil, metrics.DummyMetrics{}, server)

		val, err := general.ReadInt64FromFile(targetFile)
		require.NoError(t, err)
		require.Equal(t, int64(10), val)
	})
}

func TestSetHostWatermark_AutoCalc_ClampMax(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	targetFile := filepath.Join(tmpDir, "watermark_scale_factor")
	require.NoError(t, os.WriteFile(targetFile, []byte("100\n"), 0o644))

	var oldPath string
	hostWatermarkTestMu.Lock()
	defer func() {
		watermarkScaleFactorPath = oldPath
		hostWatermarkTestMu.Unlock()
	}()

	mockey.PatchConvey("test set host watermark with auto_calc_clamp_min", t, func() {
		oldPath = watermarkScaleFactorPath
		watermarkScaleFactorPath = targetFile
		mockey.Mock(procm.GetMemInfo).IncludeCurrentGoRoutine().Return(makeNilMemInfo(), nil).Build()

		// total=10GB, reserved=10GB => raw scale=10000, should be clamped to watermarkScaleFactorMax(500)
		server := makeTestMetaServerWithNumaTotal(t, 0, 10<<30)
		conf := makeTestCoreConf(0, 10)
		SetHostWatermark(conf, nil, nil, metrics.DummyMetrics{}, server)

		val, err := general.ReadInt64FromFile(targetFile)
		require.NoError(t, err)
		require.Equal(t, int64(500), val)
	})
}

func TestAdjustWatermarkForHugePages_Unchanged(t *testing.T) {
	t.Parallel()

	hostWatermarkTestMu.Lock()
	defer hostWatermarkTestMu.Unlock()

	mockey.PatchConvey("test adjust watermark for huge pages with unchanged", t, func() {
		mockey.Mock(procm.GetMemInfo).IncludeCurrentGoRoutine().Return(makeNilMemInfo(), nil).Build()

		var originalWatermark uint64 = 50
		newWatermark := adjustWatermarkForHugePages(originalWatermark)
		require.Equal(t, originalWatermark, newWatermark)
	})
}

func TestAdjustWatermarkForHugePages(t *testing.T) {
	t.Parallel()

	hostWatermarkTestMu.Lock()
	defer hostWatermarkTestMu.Unlock()

	mockey.PatchConvey("test adjust watermark for huge pages", t, func() {
		mockey.Mock(procm.GetMemInfo).IncludeCurrentGoRoutine().Return(makeNormalMemInfo(), nil).Build()

		var originalWatermark uint64 = 500
		var expectedWatermark uint64 = 450
		newWatermark := adjustWatermarkForHugePages(originalWatermark)
		require.Equal(t, expectedWatermark, newWatermark)
	})
}
