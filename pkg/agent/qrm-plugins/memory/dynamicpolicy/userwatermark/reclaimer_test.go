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

package userwatermark

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/bytedance/mockey"
	"github.com/stretchr/testify/assert"

	katalystapiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	dynamicconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	userwmconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/userwatermark"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	cgroupmgr "github.com/kubewharf/katalyst-core/pkg/util/cgroup/manager"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

var reclaimMutex = sync.Mutex{}

func newTestDynamicConf() *dynamicconfig.DynamicAgentConfiguration {
	// NewDynamicAgentConfiguration already initializes a non-nil
	// UserWatermarkConfiguration via NewConfiguration().
	return dynamicconfig.NewDynamicAgentConfiguration()
}

func TestNewUserWatermarkReclaimer_Defaults(t *testing.T) {
	t.Parallel()

	dynamicConf := newTestDynamicConf()
	cfg := dynamicConf.GetDynamicConfiguration()
	cfg.UserWatermarkConfiguration.ServiceLabel = "service-label"

	instance := ReclaimInstance{CgroupPath: "/sys/fs/cgroup/memory/test"}

	r := NewUserWatermarkReclaimer(instance, (*metaserver.MetaServer)(nil), metrics.DummyMetrics{}, dynamicConf)
	assert.NotNil(t, r)
	assert.Equal(t, "/sys/fs/cgroup/memory/test", r.cgroupPath)
	assert.Equal(t, "service-label", r.serviceLabel)
	assert.NotNil(t, r.containerInfo)
	assert.NotNil(t, r.reclaimConf)
	assert.NotNil(t, r.feedbackManager)
}

func TestNewUserWatermarkReclaimer_WithContainerInfo(t *testing.T) {
	t.Parallel()

	dynamicConf := newTestDynamicConf()
	cfg := dynamicConf.GetDynamicConfiguration()
	cfg.UserWatermarkConfiguration.ServiceLabel = "svc"

	cInfo := &types.ContainerInfo{
		PodUID:        "pod-uid-1",
		PodNamespace:  "default",
		PodName:       "pod-1",
		ContainerName: "c1",
		QoSLevel:      string(katalystapiconsts.QoSLevelSharedCores),
		Labels:        map[string]string{"svc": "service-a"},
	}

	instance := ReclaimInstance{
		ContainerInfo: cInfo,
		CgroupPath:    "/sys/fs/cgroup/memory/test",
	}

	r := NewUserWatermarkReclaimer(instance, (*metaserver.MetaServer)(nil), metrics.DummyMetrics{}, dynamicConf)
	assert.Equal(t, cInfo, r.containerInfo)
}

func TestUserWatermarkReclaimer_GetConfigAndLoadConfig_QoSAndService(t *testing.T) {
	t.Parallel()

	dynamicConf := newTestDynamicConf()
	cfg := dynamicConf.GetDynamicConfiguration()

	serviceLabel := "service-key"
	serviceName := "svc-a"
	cfg.UserWatermarkConfiguration.ServiceLabel = serviceLabel

	// prepare default & per-qos/service configs
	defaultConf := cfg.UserWatermarkConfiguration.DefaultConfig
	qosConf := userwmconfig.NewReclaimConfigDetail(defaultConf)
	qosConf.EnableMemoryReclaim = true
	qosConf.ScaleFactor = 200

	serviceConf := userwmconfig.NewReclaimConfigDetail(defaultConf)
	serviceConf.BackoffDuration = 10 * time.Second
	serviceConf.PSIPolicyConf = &userwmconfig.PSIPolicyConf{PsiAvg60Threshold: 0.5}
	serviceConf.RefaultPolicyConf = &userwmconfig.RefaultPolicyConf{
		ReclaimAccuracyTarget:       0.8,
		ReclaimScanEfficiencyTarget: 0.7,
	}

	cfg.UserWatermarkConfiguration.QoSLevelConfig[katalystapiconsts.QoSLevelSharedCores] = qosConf
	cfg.UserWatermarkConfiguration.ServiceConfig[serviceName] = serviceConf

	cInfo := &types.ContainerInfo{
		PodUID:        "pod-uid-1",
		PodNamespace:  "default",
		PodName:       "pod-1",
		ContainerName: "c1",
		QoSLevel:      string(katalystapiconsts.QoSLevelSharedCores),
		Labels:        map[string]string{serviceLabel: serviceName},
	}

	instance := ReclaimInstance{
		ContainerInfo: cInfo,
		CgroupPath:    "/sys/fs/cgroup/memory/test",
	}

	r := NewUserWatermarkReclaimer(instance, (*metaserver.MetaServer)(nil), metrics.DummyMetrics{}, dynamicConf)
	// Load QoS + service specific config: service-level config overrides qos-level
	r.LoadConfig()

	conf := r.GetConfig()
	assert.True(t, conf.EnableMemoryReclaim)
	// ScaleFactor should follow service config (which currently keeps default value).
	assert.Equal(t, serviceConf.ScaleFactor, conf.ScaleFactor)
	assert.Equal(t, serviceConf.BackoffDuration, conf.BackoffDuration)
	assert.Equal(t, serviceConf.PsiAvg60Threshold, conf.PsiAvg60Threshold)
	assert.Equal(t, serviceConf.RefaultPolicyConf.ReclaimAccuracyTarget, conf.ReclaimAccuracyTarget)
	assert.Equal(t, serviceConf.RefaultPolicyConf.ReclaimScanEfficiencyTarget, conf.ReclaimScanEfficiencyTarget)
}

func TestUserWatermarkReclaimer_LoadConfig_NilDynamicConf(t *testing.T) {
	t.Parallel()

	// construct a DynamicAgentConfiguration whose Configuration has nil UserWatermarkConfiguration
	dynamicConf := dynamicconfig.NewDynamicAgentConfiguration()
	dynamicConf.SetDynamicConfiguration(&dynamicconfig.Configuration{})

	// build reclaimer manually to avoid panic in NewUserWatermarkReclaimer
	defaultConf := userwmconfig.NewUserWatermarkDefaultConfiguration()
	r := &userWatermarkReclaimer{
		dynamicConf: dynamicConf,
		reclaimConf: userwmconfig.NewReclaimConfigDetail(defaultConf),
	}

	before := *r.reclaimConf

	r.LoadConfig()
	after := *r.reclaimConf

	assert.Equal(t, before, after, "LoadConfig should be noop when dynamic conf is nil")
}

func TestUserWatermarkReclaimer_LoadConfig_CgroupConfig(t *testing.T) {
	t.Parallel()

	dynamicConf := newTestDynamicConf()
	cfg := dynamicConf.GetDynamicConfiguration()

	cgroupPath := "/sys/fs/cgroup/memory/test-cgroup"
	defaultConf := cfg.UserWatermarkConfiguration.DefaultConfig
	cgConf := userwmconfig.NewReclaimConfigDetail(defaultConf)
	cgConf.ScaleFactor = 300
	cgConf.ReclaimFailedThreshold = 10

	cfg.UserWatermarkConfiguration.CgroupConfig[cgroupPath] = cgConf

	instance := ReclaimInstance{CgroupPath: cgroupPath}

	r := NewUserWatermarkReclaimer(instance, (*metaserver.MetaServer)(nil), metrics.DummyMetrics{}, dynamicConf)
	r.LoadConfig()

	conf := r.GetConfig()
	assert.Equal(t, uint64(300), conf.ScaleFactor)
	assert.Equal(t, uint64(10), conf.ReclaimFailedThreshold)
}

func TestUserWatermarkReclaimer_loadConfig_NilConfig(t *testing.T) {
	t.Parallel()

	dynamicConf := newTestDynamicConf()
	instance := ReclaimInstance{CgroupPath: "/sys/fs/cgroup/memory/test"}
	r := NewUserWatermarkReclaimer(instance, (*metaserver.MetaServer)(nil), metrics.DummyMetrics{}, dynamicConf)

	before := *r.reclaimConf
	r.loadConfig(nil)
	after := *r.reclaimConf

	assert.Equal(t, before, after)
}

func TestGetContainerCgroupPath_SuccessAndError(t *testing.T) {
	t.Parallel()

	t.Run("success", func(t *testing.T) {
		t.Parallel()
		reclaimMutex.Lock()
		defer reclaimMutex.Unlock()

		defer mockey.UnPatchAll()

		expected := "/sys/fs/cgroup/memory/pod/container"
		mockey.Mock(common.GetContainerAbsCgroupPath).Return(expected, nil).Build()

		path, err := GetContainerCgroupPath("pod", "container")
		assert.NoError(t, err)
		assert.Equal(t, expected, path)
	})

	t.Run("error", func(t *testing.T) {
		t.Parallel()
		reclaimMutex.Lock()
		defer reclaimMutex.Unlock()

		defer mockey.UnPatchAll()

		mockErr := fmt.Errorf("cgroup error")
		mockey.Mock(common.GetContainerAbsCgroupPath).Return("", mockErr).Build()

		path, err := GetContainerCgroupPath("pod", "container")
		assert.Error(t, err)
		assert.Empty(t, path)
	})
}

func TestGetCGroupMemoryLimitAndUsage(t *testing.T) {
	t.Parallel()

	t.Run("success", func(t *testing.T) {
		t.Parallel()
		reclaimMutex.Lock()
		defer reclaimMutex.Unlock()

		defer mockey.UnPatchAll()

		mockStats := &common.MemoryStats{Limit: 2048, Usage: 1024}
		mockey.Mock(cgroupmgr.GetMemoryWithAbsolutePath).Return(mockStats, nil).Build()

		limit, usage, err := GetCGroupMemoryLimitAndUsage("/sys/fs/cgroup/memory/test")
		assert.NoError(t, err)
		assert.Equal(t, uint64(2048), limit)
		assert.Equal(t, uint64(1024), usage)
	})

	t.Run("error", func(t *testing.T) {
		t.Parallel()
		reclaimMutex.Lock()
		defer reclaimMutex.Unlock()

		defer mockey.UnPatchAll()

		mockey.Mock(cgroupmgr.GetMemoryWithAbsolutePath).Return((*common.MemoryStats)(nil), fmt.Errorf("read error")).Build()

		limit, usage, err := GetCGroupMemoryLimitAndUsage("/sys/fs/cgroup/memory/test")
		assert.Error(t, err)
		assert.Equal(t, uint64(0), limit)
		assert.Equal(t, uint64(0), usage)
	})

	t.Run("nilStats", func(t *testing.T) {
		t.Parallel()
		reclaimMutex.Lock()
		defer reclaimMutex.Unlock()

		defer mockey.UnPatchAll()

		mockey.Mock(cgroupmgr.GetMemoryWithAbsolutePath).Return((*common.MemoryStats)(nil), nil).Build()

		limit, usage, err := GetCGroupMemoryLimitAndUsage("/sys/fs/cgroup/memory/test")
		assert.Error(t, err)
		assert.Equal(t, uint64(0), limit)
		assert.Equal(t, uint64(0), usage)
	})
}

func TestGetCGroupMemoryStats(t *testing.T) {
	t.Parallel()

	t.Run("success", func(t *testing.T) {
		t.Parallel()
		reclaimMutex.Lock()
		defer reclaimMutex.Unlock()

		defer mockey.UnPatchAll()

		mockStats := &common.MemoryStats{
			Limit:        4096,
			Usage:        2048,
			FileCache:    100,
			ActiveFile:   10,
			ActiveAnno:   20,
			InactiveFile: 30,
			InactiveAnno: 40,
		}
		mockey.Mock(cgroupmgr.GetMemoryStatsWithAbsolutePath).Return(mockStats, nil).Build()

		stats, err := GetCGroupMemoryStats("/sys/fs/cgroup/memory/test")
		assert.NoError(t, err)
		assert.Equal(t, uint64(4096), stats.Limit)
		assert.Equal(t, uint64(2048), stats.Usage)
		assert.Equal(t, mockStats.FileCache, stats.FileCache)
		assert.Equal(t, mockStats.ActiveFile, stats.ActiveFile)
		assert.Equal(t, mockStats.ActiveAnno, stats.ActiveAnno)
		assert.Equal(t, mockStats.InactiveFile, stats.InactiveFile)
		assert.Equal(t, mockStats.InactiveAnno, stats.InactiveAnno)
	})

	t.Run("error", func(t *testing.T) {
		t.Parallel()
		reclaimMutex.Lock()
		defer reclaimMutex.Unlock()

		defer mockey.UnPatchAll()

		mockey.Mock(cgroupmgr.GetMemoryStatsWithAbsolutePath).Return((*common.MemoryStats)(nil), fmt.Errorf("read error")).Build()

		stats, err := GetCGroupMemoryStats("/sys/fs/cgroup/memory/test")
		assert.Error(t, err)
		assert.Equal(t, uint64(0), stats.Limit)
		assert.Equal(t, uint64(0), stats.Usage)
		assert.Equal(t, uint64(0), stats.FileCache)
		assert.Equal(t, uint64(0), stats.ActiveFile)
		assert.Equal(t, uint64(0), stats.ActiveAnno)
		assert.Equal(t, uint64(0), stats.InactiveFile)
		assert.Equal(t, uint64(0), stats.InactiveAnno)
	})
}

func TestReachedHighWatermark(t *testing.T) {
	t.Parallel()

	assert.False(t, reachedHighWatermark(10, 0))
	assert.False(t, reachedHighWatermark(10, 20))
	assert.True(t, reachedHighWatermark(30, 10))
}

func TestUserWatermarkReclaimer_ReclaimSuccess(t *testing.T) {
	t.Parallel()
	calculatorMutex.Lock()
	defer calculatorMutex.Unlock()
	managerMutex.Lock()
	defer managerMutex.Unlock()
	reclaimMutex.Lock()
	defer reclaimMutex.Unlock()

	defer mockey.UnPatchAll()

	dynamicConf := newTestDynamicConf()
	defaultConf := dynamicConf.GetDynamicConfiguration().UserWatermarkConfiguration.DefaultConfig

	r := &userWatermarkReclaimer{
		stopCh:      make(chan struct{}),
		emitter:     metrics.DummyMetrics{},
		dynamicConf: dynamicConf,
		cgroupPath:  "/sys/fs/cgroup/memory/test",
		containerInfo: &types.ContainerInfo{
			PodName:       "pod-1",
			ContainerName: "c1",
		},
		reclaimConf:     userwmconfig.NewReclaimConfigDetail(defaultConf),
		feedbackManager: NewFeedbackManager(),
	}

	// origin and current reclaim stats
	callCount := 0
	mockey.Mock((*userWatermarkReclaimer).getMemoryReclaimStats).
		To(func(_ *userWatermarkReclaimer) (ReclaimStats, error) {
			callCount++
			switch callCount {
			case 1:
				return ReclaimStats{pgsteal: 100, pgscan: 200}, nil
			default:
				return ReclaimStats{pgsteal: 200, pgscan: 400, refaultActivate: 10}, nil
			}
		}).Build()

	mockey.Mock(cgroupmgr.GetEffectiveCPUSetWithAbsolutePath).
		Return(machine.NewCPUSet(0), machine.NewCPUSet(0), nil).Build()

	var offloadCalls []int64
	mockey.Mock(cgroupmgr.MemoryOffloadingWithAbsolutePath).
		To(func(_ context.Context, path string, nbytes int64, _ machine.CPUSet) error {
			offloadCalls = append(offloadCalls, nbytes)
			return nil
		}).Build()

	memCall := 0
	mockey.Mock(GetCGroupMemoryLimitAndUsage).
		To(func(_ string) (uint64, uint64, error) {
			memCall++
			switch memCall {
			case 1:
				return 200, 190, nil // free 10 < high watermark
			default:
				return 200, 150, nil // free 50 >= high watermark
			}
		}).Build()

	mockey.Mock((*FeedbackManager).FeedbackResult).
		Return(FeedbackResult{Abnormal: false}, nil).Build()

	reclaimInfo := &ReclaimInfo{
		CGroupPath:       r.cgroupPath,
		LowWaterMark:     10,
		HighWaterMark:    30,
		ReclaimTarget:    100,
		SingleReclaimMax: 60,
	}

	result, err := r.Reclaim(reclaimInfo)
	assert.NoError(t, err)
	assert.True(t, result.Success)
	assert.Equal(t, uint64(100), result.ReclaimedSize)
	assert.Equal(t, []int64{60, 40}, offloadCalls)
}

func TestUserWatermarkReclaimer_ReclaimGetStatsFailed(t *testing.T) {
	t.Parallel()
	reclaimMutex.Lock()
	defer reclaimMutex.Unlock()

	defer mockey.UnPatchAll()

	dynamicConf := newTestDynamicConf()
	defaultConf := dynamicConf.GetDynamicConfiguration().UserWatermarkConfiguration.DefaultConfig

	r := &userWatermarkReclaimer{
		stopCh:      make(chan struct{}),
		emitter:     metrics.DummyMetrics{},
		dynamicConf: dynamicConf,
		cgroupPath:  "/sys/fs/cgroup/memory/test",
		reclaimConf: userwmconfig.NewReclaimConfigDetail(defaultConf),
	}

	mockErr := fmt.Errorf("stats error")
	mockey.Mock((*userWatermarkReclaimer).getMemoryReclaimStats).
		Return(ReclaimStats{}, mockErr).Build()

	reclaimInfo := &ReclaimInfo{
		CGroupPath:       r.cgroupPath,
		LowWaterMark:     10,
		HighWaterMark:    30,
		ReclaimTarget:    64,
		SingleReclaimMax: 32,
	}

	result, err := r.Reclaim(reclaimInfo)
	assert.Error(t, err)
	assert.Contains(t, result.Reason, "Get memory reclaimStats failed")
	assert.Equal(t, uint64(0), result.ReclaimedSize)
}
