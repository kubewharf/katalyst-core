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

package plugin

import (
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/bytedance/mockey"
	"github.com/stretchr/testify/assert"

	v1alpha1 "github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/memory/dynamicpolicy/memoryadvisor"
	tmoconf "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/tmo"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	agentmetaserver "github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	metric "github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	utilmetric "github.com/kubewharf/katalyst-core/pkg/util/metric"
)

var transparentMemoryOffloadingTestMutex sync.Mutex

type mockDirEntry struct {
	name  string
	isDir bool
}

func (m mockDirEntry) Name() string               { return m.name }
func (m mockDirEntry) IsDir() bool                { return m.isDir }
func (m mockDirEntry) Type() os.FileMode          { return 0 }
func (m mockDirEntry) Info() (os.FileInfo, error) { return nil, nil }

func TestTransparentMemoryOffloading_GetAdvices_DyingMemcgReclaim(t *testing.T) {
	t.Parallel()
	transparentMemoryOffloadingTestMutex.Lock()
	defer transparentMemoryOffloadingTestMutex.Unlock()
	defer mockey.UnPatchAll()

	tmo := &transparentMemoryOffloading{
		enableDyingMemcgReclaim: true,
	}

	mockey.Mock(os.ReadDir).IncludeCurrentGoRoutine().Return([]os.DirEntry{
		mockDirEntry{name: "offline-besteffort-0", isDir: true},
		mockDirEntry{name: "offline-besteffort-1", isDir: true},
		mockDirEntry{name: "burstable", isDir: true},
	}, nil).Build()

	result := tmo.GetAdvices()
	assert.Len(t, result.ExtraEntries, 3)

	expectedPaths := map[string]struct{}{
		"/sys/fs/cgroup/" + memoryadvisor.OnlineBurstableCgroupPath:                    {},
		"/sys/fs/cgroup/" + memoryadvisor.KubePodsCgroupPath + "/offline-besteffort-0": {},
		"/sys/fs/cgroup/" + memoryadvisor.KubePodsCgroupPath + "/offline-besteffort-1": {},
	}

	for _, entry := range result.ExtraEntries {
		assert.Equal(t, consts.ControlKnobON, entry.Values[string(memoryadvisor.ControlKnowKeyDyingMemcgReclaim)])
		_, ok := expectedPaths[entry.CgroupPath]
		assert.True(t, ok)
		delete(expectedPaths, entry.CgroupPath)
	}
	assert.Empty(t, expectedPaths)
}

func TestTransparentMemoryOffloading_GetAdvices_DyingMemcgReclaimInterval(t *testing.T) {
	t.Parallel()
	transparentMemoryOffloadingTestMutex.Lock()
	defer transparentMemoryOffloadingTestMutex.Unlock()
	defer mockey.UnPatchAll()

	tmo := &transparentMemoryOffloading{
		lastDyingCGReclaimTime:  time.Now(),
		enableDyingMemcgReclaim: true,
	}

	mockey.Mock(os.ReadDir).IncludeCurrentGoRoutine().Return([]os.DirEntry{
		mockDirEntry{name: "offline-besteffort-0", isDir: true},
	}, nil).Build()

	result := tmo.GetAdvices()
	assert.Len(t, result.ExtraEntries, 0)
}

func TestTransparentMemoryOffloading_GetAdvices_DyingMemcgReclaimReadDirError(t *testing.T) {
	t.Parallel()
	transparentMemoryOffloadingTestMutex.Lock()
	defer transparentMemoryOffloadingTestMutex.Unlock()
	defer mockey.UnPatchAll()

	tmo := &transparentMemoryOffloading{
		enableDyingMemcgReclaim: true,
	}

	mockey.Mock(os.ReadDir).IncludeCurrentGoRoutine().Return(nil, errors.New("read failed")).Build()

	result := tmo.GetAdvices()
	assert.Len(t, result.ExtraEntries, 0)
}

func TestTmoEngineCalculateOffloadingTargetSize_SkipWhenCacheCloseToMapped(t *testing.T) {
	t.Parallel()
	transparentMemoryOffloadingTestMutex.Lock()
	defer transparentMemoryOffloadingTestMutex.Unlock()
	defer mockey.UnPatchAll()

	RegisterTMOPolicyFunc(v1alpha1.TMOPolicyNamePSI, psiPolicyFunc)
	defer RegisterTMOPolicyFunc(v1alpha1.TMOPolicyNamePSI, psiPolicyFunc)
	RegisterTMOPolicyFunc(v1alpha1.TMOPolicyNamePSI,
		func(lastStats TmoStats, currStats TmoStats, conf *tmoconf.TMOConfigDetail, emitter metrics.MetricEmitter) (error, float64) {
			return nil, 128
		})

	engine := &tmoEngineInstance{
		conf: &tmoconf.TMOConfigDetail{
			EnableTMO:  true,
			EnableSwap: false,
			Interval:   0,
			PolicyName: v1alpha1.TMOPolicyNamePSI,
		},
		lastTime: time.Now().Add(-time.Second),
	}

	mockey.Mock((*tmoEngineInstance).getStats).IncludeCurrentGoRoutine().Return(TmoStats{
		cache:  100,
		mapped: 60,
	}, nil).Build()

	engine.CalculateOffloadingTargetSize()
	assert.Equal(t, 0.0, engine.GetOffloadingTargetSize())
}

func TestTmoEngineCalculateOffloadingTargetSize_ClampByReservedInactiveFile(t *testing.T) {
	t.Parallel()
	transparentMemoryOffloadingTestMutex.Lock()
	defer transparentMemoryOffloadingTestMutex.Unlock()
	defer mockey.UnPatchAll()

	RegisterTMOPolicyFunc(v1alpha1.TMOPolicyNamePSI, psiPolicyFunc)
	defer RegisterTMOPolicyFunc(v1alpha1.TMOPolicyNamePSI, psiPolicyFunc)
	RegisterTMOPolicyFunc(v1alpha1.TMOPolicyNamePSI,
		func(lastStats TmoStats, currStats TmoStats, conf *tmoconf.TMOConfigDetail, emitter metrics.MetricEmitter) (error, float64) {
			return nil, 128
		})

	engine := &tmoEngineInstance{
		conf: &tmoconf.TMOConfigDetail{
			EnableTMO:            true,
			EnableSwap:           false,
			ReservedInactiveFile: 100,
			Interval:             0,
			PolicyName:           v1alpha1.TMOPolicyNamePSI,
		},
		lastTime: time.Now().Add(-time.Second),
	}

	mockey.Mock((*tmoEngineInstance).getStats).IncludeCurrentGoRoutine().Return(TmoStats{
		memInactiveFile: 350,
		cache:           1000,
		mapped:          100,
	}, nil).Build()

	engine.CalculateOffloadingTargetSize()
	assert.Equal(t, 50.0, engine.GetOffloadingTargetSize())
}

func TestTmoEngineCalculateOffloadingTargetSize_ClampToZeroByReservedInactiveFile(t *testing.T) {
	t.Parallel()
	transparentMemoryOffloadingTestMutex.Lock()
	defer transparentMemoryOffloadingTestMutex.Unlock()
	defer mockey.UnPatchAll()

	RegisterTMOPolicyFunc(v1alpha1.TMOPolicyNamePSI, psiPolicyFunc)
	defer RegisterTMOPolicyFunc(v1alpha1.TMOPolicyNamePSI, psiPolicyFunc)
	RegisterTMOPolicyFunc(v1alpha1.TMOPolicyNamePSI,
		func(lastStats TmoStats, currStats TmoStats, conf *tmoconf.TMOConfigDetail, emitter metrics.MetricEmitter) (error, float64) {
			return nil, 128
		})

	engine := &tmoEngineInstance{
		conf: &tmoconf.TMOConfigDetail{
			EnableTMO:            true,
			EnableSwap:           false,
			ReservedInactiveFile: 100,
			Interval:             0,
			PolicyName:           v1alpha1.TMOPolicyNamePSI,
		},
		lastTime: time.Now().Add(-time.Second),
	}

	mockey.Mock((*tmoEngineInstance).getStats).IncludeCurrentGoRoutine().Return(TmoStats{
		memInactiveFile: 280,
		cache:           1000,
		mapped:          100,
	}, nil).Build()

	engine.CalculateOffloadingTargetSize()
	assert.Equal(t, 0.0, engine.GetOffloadingTargetSize())
}

func TestTmoEngineCalculateOffloadingTargetSize_SkipReservedInactiveFileClampWhenSwapEnabled(t *testing.T) {
	t.Parallel()
	transparentMemoryOffloadingTestMutex.Lock()
	defer transparentMemoryOffloadingTestMutex.Unlock()
	defer mockey.UnPatchAll()

	RegisterTMOPolicyFunc(v1alpha1.TMOPolicyNamePSI, psiPolicyFunc)
	defer RegisterTMOPolicyFunc(v1alpha1.TMOPolicyNamePSI, psiPolicyFunc)
	RegisterTMOPolicyFunc(v1alpha1.TMOPolicyNamePSI,
		func(lastStats TmoStats, currStats TmoStats, conf *tmoconf.TMOConfigDetail, emitter metrics.MetricEmitter) (error, float64) {
			return nil, 128
		})

	engine := &tmoEngineInstance{
		conf: &tmoconf.TMOConfigDetail{
			EnableTMO:            true,
			EnableSwap:           true,
			ReservedInactiveFile: 100,
			Interval:             0,
			PolicyName:           v1alpha1.TMOPolicyNamePSI,
		},
		lastTime: time.Now().Add(-time.Second),
	}

	mockey.Mock((*tmoEngineInstance).getStats).IncludeCurrentGoRoutine().Return(TmoStats{
		memInactiveFile: 280,
		cache:           1000,
		mapped:          100,
	}, nil).Build()

	engine.CalculateOffloadingTargetSize()
	assert.Equal(t, 128.0, engine.GetOffloadingTargetSize())
}

func TestTmoEngineCalculateOffloadingTargetSize_SkipWhenSwapFreeLow(t *testing.T) {
	t.Parallel()
	transparentMemoryOffloadingTestMutex.Lock()
	defer transparentMemoryOffloadingTestMutex.Unlock()
	defer mockey.UnPatchAll()

	RegisterTMOPolicyFunc(v1alpha1.TMOPolicyNamePSI, psiPolicyFunc)
	defer RegisterTMOPolicyFunc(v1alpha1.TMOPolicyNamePSI, psiPolicyFunc)
	RegisterTMOPolicyFunc(v1alpha1.TMOPolicyNamePSI,
		func(lastStats TmoStats, currStats TmoStats, conf *tmoconf.TMOConfigDetail, emitter metrics.MetricEmitter) (error, float64) {
			return nil, 128
		})

	now := time.Now()
	fetcher := metric.NewFakeMetricsFetcher(metrics.DummyMetrics{}).(*metric.FakeMetricsFetcher)
	fetcher.SetNodeMetric(consts.MetricMemSwapTotalSystem, utilmetric.MetricData{Value: 1000, Time: &now})
	fetcher.SetNodeMetric(consts.MetricMemSwapFreeSystem, utilmetric.MetricData{Value: 100, Time: &now})

	engine := &tmoEngineInstance{
		metaServer: &metaserver.MetaServer{MetaAgent: &agentmetaserver.MetaAgent{MetricsFetcher: fetcher}},
		conf: &tmoconf.TMOConfigDetail{
			EnableTMO:              true,
			EnableSwap:             true,
			SwapFreeThresholdRatio: 0.1,
			Interval:               0,
			PolicyName:             v1alpha1.TMOPolicyNamePSI,
		},
		lastTime: time.Now().Add(-time.Second),
	}

	mockey.Mock((*tmoEngineInstance).getStats).IncludeCurrentGoRoutine().Return(TmoStats{
		cache:  1000,
		mapped: 100,
	}, nil).Build()

	engine.CalculateOffloadingTargetSize()
	assert.Equal(t, 0.0, engine.GetOffloadingTargetSize())
}

func TestTmoEngineCalculateOffloadingTargetSize_DoNotSkipWhenSwapFreeEnough(t *testing.T) {
	t.Parallel()
	transparentMemoryOffloadingTestMutex.Lock()
	defer transparentMemoryOffloadingTestMutex.Unlock()
	defer mockey.UnPatchAll()

	RegisterTMOPolicyFunc(v1alpha1.TMOPolicyNamePSI, psiPolicyFunc)
	defer RegisterTMOPolicyFunc(v1alpha1.TMOPolicyNamePSI, psiPolicyFunc)
	RegisterTMOPolicyFunc(v1alpha1.TMOPolicyNamePSI,
		func(lastStats TmoStats, currStats TmoStats, conf *tmoconf.TMOConfigDetail, emitter metrics.MetricEmitter) (error, float64) {
			return nil, 128
		})

	now := time.Now()
	fetcher := metric.NewFakeMetricsFetcher(metrics.DummyMetrics{}).(*metric.FakeMetricsFetcher)
	fetcher.SetNodeMetric(consts.MetricMemSwapTotalSystem, utilmetric.MetricData{Value: 1000, Time: &now})
	fetcher.SetNodeMetric(consts.MetricMemSwapFreeSystem, utilmetric.MetricData{Value: 200, Time: &now})

	engine := &tmoEngineInstance{
		metaServer: &metaserver.MetaServer{MetaAgent: &agentmetaserver.MetaAgent{MetricsFetcher: fetcher}},
		conf: &tmoconf.TMOConfigDetail{
			EnableTMO:              true,
			EnableSwap:             true,
			SwapFreeThresholdRatio: 0.1,
			Interval:               0,
			PolicyName:             v1alpha1.TMOPolicyNamePSI,
		},
		lastTime: time.Now().Add(-time.Second),
	}

	mockey.Mock((*tmoEngineInstance).getStats).IncludeCurrentGoRoutine().Return(TmoStats{
		cache:  1000,
		mapped: 100,
	}, nil).Build()

	engine.CalculateOffloadingTargetSize()
	assert.Equal(t, 128.0, engine.GetOffloadingTargetSize())
}
