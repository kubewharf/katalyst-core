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
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/kubewharf/katalyst-core/pkg/util/metric"

	katalystapiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/helper"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	dynamicconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/userwatermark"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	cgroupmgr "github.com/kubewharf/katalyst-core/pkg/util/cgroup/manager"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

type ReclaimInstance struct {
	ContainerInfo *types.ContainerInfo
	CgroupPath    string
}

type ReclaimInfo struct {
	CGroupPath    string
	LowWaterMark  uint64
	HighWaterMark uint64
	// ReclaimTarget is the target memory size that can be reclaimed. The default unit is byte.
	ReclaimTarget uint64
	// SingleReclaimMax is the max memory size that can be reclaimed in one reclaim cycle.The default unit is byte.
	SingleReclaimMax uint64
}

type ReclaimResult struct {
	Success       bool
	Reason        string
	ReclaimedSize uint64
}

type ReclaimStats struct {
	obj               string
	qosLevel          string
	memUsage          float64
	memInactive       float64
	memPsiAvg60       float64
	pgscan            float64
	pgsteal           float64
	refault           float64
	refaultActivate   float64
	cache             float64
	mapped            float64
	reclaimTargetSize float64
}

type Reclaimer interface {
	Run()
	Stop()
	LoadConfig()
	GetConfig() *userwatermark.ReclaimConfigDetail
	UpdateInstanceInfo(ReclaimInstance)
}

type userWatermarkReclaimer struct {
	mutex       sync.RWMutex
	stopCh      chan struct{}
	emitter     metrics.MetricEmitter
	metaServer  *metaserver.MetaServer
	dynamicConf *dynamicconfig.DynamicAgentConfiguration

	// absolute path of cgroup
	cgroupPath    string
	containerInfo *types.ContainerInfo
	serviceLabel  string
	reclaimConf   *userwatermark.ReclaimConfigDetail

	failedCount     uint64
	feedbackManager *FeedbackManager
}

// NewUserWatermarkReclaimer creates a new instance of userWatermarkReclaimer.
func NewUserWatermarkReclaimer(instanceInfo ReclaimInstance, metaServer *metaserver.MetaServer, emitter metrics.MetricEmitter, dynamicConf *dynamicconfig.DynamicAgentConfiguration) *userWatermarkReclaimer {
	reclaimer := &userWatermarkReclaimer{
		stopCh:      make(chan struct{}),
		emitter:     emitter,
		metaServer:  metaServer,
		dynamicConf: dynamicConf,

		serviceLabel:    dynamicConf.GetDynamicConfiguration().UserWatermarkConfiguration.ServiceLabel,
		containerInfo:   &types.ContainerInfo{},
		reclaimConf:     userwatermark.NewReclaimConfigDetail(userwatermark.NewUserWatermarkDefaultConfiguration()),
		feedbackManager: NewFeedbackManager(),
	}

	reclaimer.cgroupPath = instanceInfo.CgroupPath
	if instanceInfo.ContainerInfo != nil {
		reclaimer.containerInfo = instanceInfo.ContainerInfo
	}

	return reclaimer
}

func (r *userWatermarkReclaimer) Run() {
	general.Infof("UserWatermarkReclaimer started for cgroup %s", r.cgroupPath)
	for {
		// 1. Execute the run function
		done, err := r.run()
		if done || err != nil {
			general.Infof("UserWatermarkReclaimer run completed with done=%v, err=%v", done, err)
			return
		}

		// 2. Get the latest reclaim interval
		interval := r.reclaimConf.ReclaimInterval
		if interval <= 0 {
			interval = userwatermark.DefaultReclaimInterval
		}

		// 3. Wait for the next interval or until stop signal
		select {
		case <-time.After(time.Duration(interval) * time.Second):
			// Continue to next iteration
		case <-r.stopCh:
			general.Infof("UserWatermarkReclaimer stopped for cgroup %s", r.cgroupPath)
			return
		}
	}
}

func (r *userWatermarkReclaimer) Stop() {
	close(r.stopCh)
}

// The memory reclamation of the instance is performed in a loop through the following steps:
// 1. Load the latest configuration;
// 2. Check for frequent failures to avoid invalid recycling;
// 3. Determine if recycling is required (memory watermark not reached);
// 4. Perform a recycling and perform an appropriate amount of sleep based on the recovery state results.
func (r *userWatermarkReclaimer) run() (done bool, err error) {
	// 1. load the latest configuration
	r.LoadConfig()
	r.feedbackManager.UpdateFeedbackPolicy(r.reclaimConf.FeedbackPolicy)

	if !r.reclaimConf.EnableMemoryReclaim {
		general.Warningf("Memory reclamation is disabled for podcgroup %s", r.cgroupPath)
		return false, nil
	}

	// 2. check for frequent failures to avoid invalid recycling
	if r.failedCount >= r.reclaimConf.ReclaimFailedThreshold {
		general.Warningf("The number of memory watermark reclamation failures(%v) has reached a threshold(%v), freeze for %v seconds.",
			r.failedCount, r.reclaimConf.ReclaimFailedThreshold, r.reclaimConf.FailureFreezePeriod)
		time.Sleep(r.reclaimConf.FailureFreezePeriod)

		return false, nil
	}

	// get memory limit and usage
	memLimit, memUsage, err := GetCGroupMemoryLimitAndUsage(r.cgroupPath)
	general.InfofV(5, "Get cgroup %v memory limit and usage result: limit=%v, usage=%v, err: %v", r.cgroupPath, memLimit, memUsage, err)
	if err != nil {
		general.Warningf("Get cgroup(%s) memory limit and usage failed, err: %v", r.cgroupPath, err)
		return false, nil
	}
	if memLimit == 0 || memUsage >= memLimit {
		general.Warningf("Memory watermark reclaimer skip cgroup %s due to invalid limit(%d) or usage(%d)", r.cgroupPath, memLimit, memUsage)
		return false, nil
	}
	// calculate memory watermark
	mwc := NewMemoryWatermarkCalculator(r.cgroupPath, r.reclaimConf.ScaleFactor, r.reclaimConf.SingleReclaimFactor, r.reclaimConf.SingleReclaimSize)
	lowWatermark, highWatermark := mwc.GetWatermark(memLimit)
	general.InfofV(5, "Get object %v watermark, lowWatermark=%v, highWatermark=%v", r.cgroupPath, lowWatermark, highWatermark)

	// 3. determine if memory reclamation is necessary
	free := memLimit - memUsage
	if free >= lowWatermark {
		general.Warningf("Memory watermark reclaimer skip cgroup %s due to free(%d) >= lowWatermark(%d)", r.cgroupPath, free, lowWatermark)
		return false, nil
	}

	// warp the reclaim info
	memStats, err := GetCGroupMemoryStats(r.cgroupPath)
	if err != nil {
		general.Warningf("Get cgroup(%s) memory stats failed, err: %v", r.cgroupPath, err)
		return false, nil
	}
	reclaimableMax := mwc.GetReclaimMax(memStats)
	reclaimTarget := mwc.GetReclaimTarget(memLimit, memUsage, reclaimableMax)
	singleReclaimMax := mwc.GetReclaimSingleStepMax(memStats)
	reclaimInfo := &ReclaimInfo{
		CGroupPath:       r.cgroupPath,
		LowWaterMark:     lowWatermark,
		HighWaterMark:    highWatermark,
		ReclaimTarget:    reclaimTarget,
		SingleReclaimMax: singleReclaimMax,
	}

	// 4. perform memory reclamation and hibernate to a certain extent depending on the reclaim state
	result, err := r.Reclaim(reclaimInfo)
	if err != nil || !result.Success {
		r.failedCount++

		r.mutex.RLock()
		general.Warningf("Memory reclaim failed, podName:%v containerName:%v cgroupPath: %s, result: %+v", r.containerInfo.PodName, r.containerInfo.ContainerName, r.cgroupPath, result)
		r.mutex.RUnlock()
		time.Sleep(r.reclaimConf.BackoffDuration)
	} else {
		r.failedCount = 0
	}

	r.mutex.RLock()
	defer r.mutex.RUnlock()
	general.Infof("Object %v memory watermark reclaimer result: %+v, failedCount: %v", r.cgroupPath, result, r.failedCount)
	r.emitMetric(MetricNameUserWatermarkReclaimResult, 1, metrics.MetricTypeNameRaw,
		metrics.MetricTag{Key: MetricTagKeySuccess, Val: fmt.Sprintf("%v", result.Success)},
		metrics.MetricTag{Key: MetricTagKeyReason, Val: metric.MetricTagValueFormat(result.Reason)},
		metrics.MetricTag{Key: MetricTagKeyHighWaterMark, Val: fmt.Sprintf("%v", reclaimInfo.HighWaterMark)},
		metrics.MetricTag{Key: MetricTagKeyLowWaterMark, Val: fmt.Sprintf("%v", reclaimInfo.LowWaterMark)},
		metrics.MetricTag{Key: MetricTagKeyReclaimTarget, Val: fmt.Sprintf("%v", reclaimInfo.ReclaimTarget)},
		metrics.MetricTag{Key: MetricTagKeyReclaimedSize, Val: fmt.Sprintf("%v", result.ReclaimedSize)})

	r.emitMetric(MetricNameUserWatermarkReclaimFailedCount, int64(r.failedCount), metrics.MetricTypeNameRaw)

	return false, nil
}

// Reclaim performs memory reclamation based on the provided reclaimInfo.
// It returns a ReclaimResult indicating the success or failure of the reclamation operation,
// along with an error if any occurred.
func (r *userWatermarkReclaimer) Reclaim(reclaimInfo *ReclaimInfo) (ReclaimResult, error) {
	r.mutex.RLock()
	defer r.mutex.RUnlock()

	result := ReclaimResult{}
	general.InfofV(5, "Object %v memory watermark reclaimer start to reclaim, reclaimInfo: %+v", r.cgroupPath, reclaimInfo)

	originReclaimStats, err := r.getMemoryReclaimStats()
	if err != nil {
		result.Reason = fmt.Sprintf("Get memory reclaimStats failed: %v", err)
		return result, fmt.Errorf(result.Reason)
	}

	_, mems, err := cgroupmgr.GetEffectiveCPUSetWithAbsolutePath(r.cgroupPath)
	if err != nil {
		result.Reason = fmt.Sprintf("Get effective CPUSet with absolute path(%s) failed: %v", r.cgroupPath, err)
		return result, fmt.Errorf(result.Reason)
	}

	var reclaimed, free uint64
	remaining := reclaimInfo.ReclaimTarget
	start := time.Now()

	for remaining > 0 {
		step := general.MinUInt64(reclaimInfo.SingleReclaimMax, remaining)
		general.InfofV(5, "Memory watermark reclaimer ready to reclaim, size: %v", step)
		if err = cgroupmgr.MemoryOffloadingWithAbsolutePath(context.Background(), r.cgroupPath, int64(step), mems); err != nil {
			result.Reason = fmt.Sprintf("Memory offloading with absolute path(%s) failed, err: %v", r.cgroupPath, err)
			result.ReclaimedSize = reclaimed
			return result, fmt.Errorf(result.Reason)
		}
		reclaimed += step
		remaining -= step
		result.ReclaimedSize = reclaimed

		memLimit, memUsage, err := GetCGroupMemoryLimitAndUsage(r.cgroupPath)
		if err != nil {
			result.Reason = fmt.Sprintf("Get cgroup(%s) memory limit and usage failed, err: %v", r.cgroupPath, err)
			return result, fmt.Errorf(result.Reason)
		}
		free = memLimit - memUsage
		if reachedHighWatermark(free, reclaimInfo.HighWaterMark) {
			break
		}

		currReclaimStats, err := r.getMemoryReclaimStats()
		if err != nil {
			result.Reason = fmt.Sprintf("getMemoryReclaimStats failed: %v", err)
			return result, fmt.Errorf(result.Reason)
		}

		// reclaim state info metrics emit
		reclaimAccuracyRatio := getReclaimAccuracyRatio(originReclaimStats, currReclaimStats)
		reclaimScanEfficiencyRatio := getReclaimScanEfficiencyRatio(originReclaimStats, currReclaimStats)
		r.emitMetric(MetricNameUserWatermarkReclaimMemoryFree, int64(free), metrics.MetricTypeNameRaw)
		r.emitMetric(MetricNameUserWatermarkReclaimPSI, currReclaimStats.memPsiAvg60, metrics.MetricTypeNameRaw)
		r.emitMetric(MetricNameUserWatermarkReclaimRefault, currReclaimStats.refault, metrics.MetricTypeNameRaw)
		r.emitMetric(MetricNameUserWatermarkReclaimAccuracyRatio, reclaimAccuracyRatio, metrics.MetricTypeNameRaw)
		r.emitMetric(MetricNameUserWatermarkReclaimScanEfficiencyRatio, reclaimScanEfficiencyRatio, metrics.MetricTypeNameRaw)

		if feedbackResult, err := r.feedbackManager.FeedbackResult(originReclaimStats, currReclaimStats, r.reclaimConf, r.emitter); err != nil {
			result.Reason = fmt.Sprintf("get feedback result failed: %v", err)
			return result, fmt.Errorf(result.Reason)
		} else if feedbackResult.Abnormal {
			result.Reason = fmt.Sprintf("feedback indicates an anomaly: %v", feedbackResult.Reason)
			return result, fmt.Errorf(result.Reason)
		}
	}
	duration := time.Since(start)
	// update reclaim result
	result.Success = true

	r.emitMetric(MetricNameUserWatermarkReclaimCost, duration.Microseconds(), metrics.MetricTypeNameRaw,
		metrics.MetricTag{Key: MetricTagKeySuccess, Val: fmt.Sprintf("%v", result.Success)})
	general.InfoS("Memory watermark reclaim finished", "podName", r.containerInfo.PodName, "containerName",
		r.containerInfo.ContainerName, "cgroupPath", r.cgroupPath, "duration", duration, "reclaimedBytes", reclaimed,
		"reclaimTarget", reclaimInfo.ReclaimTarget, "highWatermark", reclaimInfo.HighWaterMark, "currentFree(Bytes)", free)

	return result, nil
}

func (r *userWatermarkReclaimer) GetConfig() *userwatermark.ReclaimConfigDetail {
	return r.reclaimConf
}

func (r *userWatermarkReclaimer) LoadConfig() {
	r.mutex.RLock()
	defer r.mutex.RUnlock()

	userWatermarkDynamicConf := r.dynamicConf.GetDynamicConfiguration().UserWatermarkConfiguration
	if userWatermarkDynamicConf == nil {
		general.Warningf("Get user watermark dynamic conf is nil")
		return
	}

	// get default config
	reclaimConfig := userwatermark.NewReclaimConfigDetail(userWatermarkDynamicConf.DefaultConfig)
	general.InfofV(5, "LoadConfig get object %v defaultConfig: %+v", r.cgroupPath, reclaimConfig)

	if r.containerInfo != nil && r.containerInfo.PodUID != "" {
		// get QoS level config
		if helper.IsValidQosLevel(r.containerInfo.QoSLevel) {
			if qosReclaimConfig, exist := userWatermarkDynamicConf.QoSLevelConfig[katalystapiconsts.QoSLevel(r.containerInfo.QoSLevel)]; exist {
				reclaimConfig = qosReclaimConfig
				general.InfofV(5, "LoadConfig get object %v qos(%v) reclaimConfig: %+v", r.cgroupPath, r.containerInfo.QoSLevel, reclaimConfig)
			}
		}

		// get service config
		if serviceName, exist := r.containerInfo.Labels[r.serviceLabel]; exist {
			if serviceReclaimConfig, exist := userWatermarkDynamicConf.ServiceConfig[serviceName]; exist {
				reclaimConfig = serviceReclaimConfig
				general.InfofV(5, "LoadConfig get object %v service(%v) reclaimConfig: %+v", r.cgroupPath, serviceName, reclaimConfig)
			}
		}
	}

	// get cgroup config
	if cgroupReclaimConfig, exist := r.dynamicConf.GetDynamicConfiguration().UserWatermarkConfiguration.CgroupConfig[r.cgroupPath]; exist {
		reclaimConfig = cgroupReclaimConfig
		general.InfofV(5, "LoadConfig get object %v cgroup reclaimConfig: %+v", r.cgroupPath, reclaimConfig)
	}

	// load reclaim config
	r.loadConfig(reclaimConfig)
}

func (r *userWatermarkReclaimer) loadConfig(config *userwatermark.ReclaimConfigDetail) {
	if config == nil {
		general.Warningf("Load config detail failed, config is nil, podName:%v containerName:%v cgroupPath: %s qosLevel:%v",
			r.containerInfo.PodName, r.containerInfo.ContainerName, r.cgroupPath, r.containerInfo.QoSLevel)
		return
	}
	general.InfofV(5, "loadConfig get object %v config: %+v", r.cgroupPath, config)

	r.reclaimConf.EnableMemoryReclaim = config.EnableMemoryReclaim
	if config.ReclaimInterval <= 0 {
		r.reclaimConf.ReclaimInterval = userwatermark.DefaultReconcileInterval
		general.Warningf("Load config detail failed, the ReclaimInterval(%v) is invalid, use default value %v", config.ReclaimInterval, userwatermark.DefaultReconcileInterval)
	} else {
		r.reclaimConf.ReclaimInterval = config.ReclaimInterval
	}

	if config.SingleReclaimSize <= 0 || config.SingleReclaimSize > 10*userwatermark.DefaultSingleReclaimSize {
		r.reclaimConf.SingleReclaimSize = userwatermark.DefaultSingleReclaimSize
		general.Warningf("Load config detail failed, the SingleReclaimSize(%v) is invalid, use default value %v", config.SingleReclaimSize, userwatermark.DefaultSingleReclaimSize)
	} else {
		r.reclaimConf.SingleReclaimSize = config.SingleReclaimSize
	}

	if config.SingleReclaimFactor <= 0 || config.SingleReclaimFactor >= 1 {
		r.reclaimConf.SingleReclaimFactor = userwatermark.DefaultSingleReclaimFactor
		general.Warningf("Load config detail failed, the SingleReclaimFactor(%v) is invalid, use default value %v", config.SingleReclaimFactor, userwatermark.DefaultSingleReclaimFactor)
	} else {
		r.reclaimConf.SingleReclaimFactor = config.SingleReclaimFactor
	}

	if config.ScaleFactor < 0 || config.ScaleFactor > 1000 {
		r.reclaimConf.ScaleFactor = userwatermark.DefaultScaleFactor
		general.Warningf("Load config detail failed, the ScaleFactor(%v) is invalid, use default value %v", config.ScaleFactor, userwatermark.DefaultScaleFactor)
	} else {
		r.reclaimConf.ScaleFactor = config.ScaleFactor
	}

	r.reclaimConf.BackoffDuration = config.BackoffDuration
	r.reclaimConf.FeedbackPolicy = config.FeedbackPolicy

	if config.ReclaimFailedThreshold <= 0 || config.ReclaimFailedThreshold > 5*userwatermark.DefaultReclaimFailedThreshold {
		r.reclaimConf.ReclaimFailedThreshold = userwatermark.DefaultReclaimFailedThreshold
		general.Warningf("Load config detail failed, the ReclaimFailedThreshold(%v) is invalid, use default value %v", config.ReclaimFailedThreshold, userwatermark.DefaultReclaimFailedThreshold)
	} else {
		r.reclaimConf.ReclaimFailedThreshold = config.ReclaimFailedThreshold
	}

	r.reclaimConf.FailureFreezePeriod = config.FailureFreezePeriod

	if config.PSIPolicyConf != nil {
		r.reclaimConf.PsiAvg60Threshold = config.PSIPolicyConf.PsiAvg60Threshold
	}
	if config.RefaultPolicyConf != nil {
		r.reclaimConf.ReclaimAccuracyTarget = config.RefaultPolicyConf.ReclaimAccuracyTarget
		r.reclaimConf.ReclaimScanEfficiencyTarget = config.RefaultPolicyConf.ReclaimScanEfficiencyTarget
	}

	general.InfofV(5, "Load config detail success, podName:%v containerName:%v cgroupPath: %s conf:%+v",
		r.containerInfo.PodName, r.containerInfo.ContainerName, r.cgroupPath, r.reclaimConf)
}

func (r *userWatermarkReclaimer) UpdateInstanceInfo(instance ReclaimInstance) {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	if instance.ContainerInfo != nil {
		r.containerInfo = instance.ContainerInfo
	}
}

func (r *userWatermarkReclaimer) getMemoryReclaimStats() (ReclaimStats, error) {
	reclaimStats := &ReclaimStats{}
	var err error
	getCgroupMetrics := func(metaserver *metaserver.MetaServer, absPath string) error {
		relativePath, err := filepath.Rel(common.CgroupFSMountPoint, absPath)
		if err != nil {
			return err
		}
		// make sure the relative path with prefix '/' has already been added to GeneralRelativeCgroupPaths,
		// otherwise the MalachiteMetricsProvisioner will not fetch and store the metrics for these cgroup paths.
		relativePath = "/" + relativePath
		psiAvg60, err := metaserver.GetCgroupMetric(relativePath, consts.MetricMemPsiAvg60Cgroup)
		if err != nil {
			return err
		}
		pgsteal, err := metaserver.GetCgroupMetric(relativePath, consts.MetricMemPgstealCgroup)
		if err != nil {
			return err
		}
		pgscan, err := metaserver.GetCgroupMetric(relativePath, consts.MetricMemPgscanCgroup)
		if err != nil {
			return err
		}
		refault, err := metaserver.GetCgroupMetric(relativePath, consts.MetricMemWorkingsetRefaultCgroup)
		if err != nil {
			return err
		}
		refaultActivate, err := metaserver.GetCgroupMetric(relativePath, consts.MetricMemWorkingsetActivateCgroup)
		if err != nil {
			return err
		}
		memUsage, err := metaserver.GetCgroupMetric(relativePath, consts.MetricMemUsageCgroup)
		if err != nil {
			return err
		}
		memInactiveAnon, err := metaserver.GetCgroupMetric(relativePath, consts.MetricMemInactiveAnonCgroup)
		if err != nil {
			return err
		}
		memInactiveFile, err := metaserver.GetCgroupMetric(relativePath, consts.MetricMemInactiveFileCgroup)
		if err != nil {
			return err
		}
		memCache, err := metaserver.GetCgroupMetric(relativePath, consts.MetricMemCacheCgroup)
		if err != nil {
			return err
		}
		memMappedFile, err := metaserver.GetCgroupMetric(relativePath, consts.MetricMemMappedCgroup)
		if err != nil {
			return err
		}
		reclaimStats.memUsage = memUsage.Value
		reclaimStats.memInactive = memInactiveFile.Value + memInactiveAnon.Value
		reclaimStats.memPsiAvg60 = psiAvg60.Value
		reclaimStats.pgsteal = pgsteal.Value
		reclaimStats.pgscan = pgscan.Value
		reclaimStats.refault = refault.Value
		reclaimStats.refaultActivate = refaultActivate.Value
		reclaimStats.cache = memCache.Value
		reclaimStats.mapped = memMappedFile.Value
		general.Infof("Memory Usage of Cgroup %s, memUsage: %v, cache: %v, mapped: %v", r.cgroupPath, memUsage.Value, memCache.Value, memMappedFile.Value)
		return nil
	}
	getContainerMetrics := func(metaserver *metaserver.MetaServer, podUID string, containerName string) error {
		psiAvg60, err := metaserver.GetContainerMetric(podUID, containerName, consts.MetricMemPsiAvg60Container)
		if err != nil {
			return err
		}
		pgsteal, err := metaserver.GetContainerMetric(podUID, containerName, consts.MetricMemPgstealContainer)
		if err != nil {
			return err
		}
		pgscan, err := metaserver.GetContainerMetric(podUID, containerName, consts.MetricMemPgscanContainer)
		if err != nil {
			return err
		}
		refault, err := metaserver.GetContainerMetric(podUID, containerName, consts.MetricMemWorkingsetRefaultContainer)
		if err != nil {
			return err
		}
		refaultActivate, err := metaserver.GetContainerMetric(podUID, containerName, consts.MetricMemWorkingsetActivateContainer)
		if err != nil {
			return err
		}
		memUsage, err := metaserver.GetContainerMetric(podUID, containerName, consts.MetricMemUsageContainer)
		if err != nil {
			return err
		}
		memInactiveAnon, err := metaserver.GetContainerMetric(podUID, containerName, consts.MetricMemInactiveAnonContainer)
		if err != nil {
			return err
		}
		memInactiveFile, err := metaserver.GetContainerMetric(podUID, containerName, consts.MetricMemInactiveFileContainer)
		if err != nil {
			return err
		}
		memCache, err := metaserver.GetContainerMetric(podUID, containerName, consts.MetricMemCacheContainer)
		if err != nil {
			return err
		}
		memMappedFile, err := metaserver.GetContainerMetric(podUID, containerName, consts.MetricMemMappedContainer)
		if err != nil {
			return err
		}
		reclaimStats.memUsage = memUsage.Value
		reclaimStats.memInactive = memInactiveFile.Value + memInactiveAnon.Value
		reclaimStats.memPsiAvg60 = psiAvg60.Value
		reclaimStats.pgsteal = pgsteal.Value
		reclaimStats.pgscan = pgscan.Value
		reclaimStats.refault = refault.Value
		reclaimStats.refaultActivate = refaultActivate.Value
		reclaimStats.cache = memCache.Value
		reclaimStats.mapped = memMappedFile.Value
		general.Infof("Memory Usage of Pod %v, Container %v, memUsage: %v, cache: %v, mapped: %v", podUID, containerName, memUsage.Value, memCache.Value, memMappedFile.Value)
		return nil
	}

	// if it's a container, get it from the container metric, otherwise from the cgroup metric
	if r.containerInfo != nil && r.containerInfo.PodUID != "" {
		err = getContainerMetrics(r.metaServer, r.containerInfo.PodUID, r.containerInfo.ContainerName)
		reclaimStats.obj = strings.Join([]string{r.containerInfo.PodNamespace, r.containerInfo.PodName}, "/")
		reclaimStats.qosLevel = r.containerInfo.QoSLevel
	} else {
		err = getCgroupMetrics(r.metaServer, r.cgroupPath)
		reclaimStats.obj = r.cgroupPath
		reclaimStats.qosLevel = r.cgroupPath
	}

	return *reclaimStats, err
}

func (r *userWatermarkReclaimer) emitMetric(metricName string, val interface{}, emitType metrics.MetricTypeName, tags ...metrics.MetricTag) {
	baseTags := []metrics.MetricTag{
		{Key: MetricTagKeyCGroupPath, Val: r.cgroupPath},
	}
	if r.containerInfo != nil {
		baseTags = append(baseTags, metrics.MetricTag{Key: MetricTagKeyQosLevel, Val: r.containerInfo.QoSLevel})
		baseTags = append(baseTags, metrics.MetricTag{Key: MetricTagKeyObjectName, Val: r.containerInfo.PodName})
		baseTags = append(baseTags, metrics.MetricTag{Key: MetricTagKeyContainerName, Val: r.containerInfo.ContainerName})
	}
	tags = append(baseTags, tags...)

	if val, ok := val.(int64); ok {
		_ = r.emitter.StoreInt64(metricName, val, emitType, tags...)
		return
	}
	if val, ok := val.(float64); ok {
		_ = r.emitter.StoreFloat64(metricName, val, emitType, tags...)
		return
	}
}

func GetContainerCgroupPath(podUID, containerId string) (string, error) {
	return common.GetContainerAbsCgroupPath(common.CgroupSubsysMemory, podUID, containerId)
}

func GetCGroupMemoryLimitAndUsage(cgroupPath string) (uint64, uint64, error) {
	ms, err := cgroupmgr.GetMemoryWithAbsolutePath(cgroupPath)
	if err != nil || ms == nil {
		return 0, 0, fmt.Errorf("failed to get memory usage for cgroup path %s: %v", cgroupPath, err)
	}

	return ms.Limit, ms.Usage, nil
}

func GetCGroupMemoryStats(cgroupPath string) (common.MemoryStats, error) {
	stats := common.MemoryStats{}
	ms, err := cgroupmgr.GetMemoryStatsWithAbsolutePath(cgroupPath)
	if err != nil || ms == nil {
		return common.MemoryStats{}, fmt.Errorf("failed to get memory usage for cgroup path %s: %v", cgroupPath, err)
	}

	stats.Limit = ms.Limit
	stats.Usage = ms.Usage
	stats.FileCache = ms.FileCache
	stats.ActiveFile = ms.ActiveFile
	stats.ActiveAnno = ms.ActiveAnno
	stats.InactiveFile = ms.InactiveFile
	stats.InactiveAnno = ms.InactiveAnno

	return stats, nil
}

func reachedHighWatermark(free, high uint64) bool {
	return high > 0 && free >= high
}
