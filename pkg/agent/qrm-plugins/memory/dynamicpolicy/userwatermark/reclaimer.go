package userwatermark

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/util/wait"

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
		reclaimConf:     userwatermark.NewReclaimConfigDetail(dynamicConf.GetDynamicConfiguration().UserWatermarkConfiguration.DefaultConfig),
		feedbackManager: NewFeedbackManager(),
	}

	reclaimer.cgroupPath = instanceInfo.CgroupPath
	if instanceInfo.ContainerInfo != nil {
		reclaimer.containerInfo = instanceInfo.ContainerInfo
	}

	return reclaimer
}

func (r *userWatermarkReclaimer) Run() {
	_ = wait.PollUntil(time.Duration(r.reclaimConf.ReclaimInterval)*time.Second, r.run, r.stopCh)
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

	// 3. determine if memory reclamation is necessary
	free := memLimit - memUsage
	if free >= highWatermark {
		general.Warningf("Memory watermark reclaimer skip cgroup %s due to free(%d) >= highWatermark(%d)", r.cgroupPath, free, highWatermark)
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

		general.Warningf("Memory reclaim failed, podName:%v containerName:%v cgroupPath: %s, result: %v", r.containerInfo.PodName, r.containerInfo.ContainerName, r.cgroupPath, result)
		time.Sleep(r.reclaimConf.BackoffDuration)
	} else {
		r.failedCount = 0
	}

	r.emitMetric(MetricNameUserWatermarkReclaimResult, 1, metrics.MetricTypeNameRaw,
		metrics.MetricTag{Key: MetricTagKeySuccess, Val: fmt.Sprintf("%v", result.Success)},
		metrics.MetricTag{Key: MetricTagKeyReason, Val: result.Reason},
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
	result := ReclaimResult{}
	general.InfofV(5, "Memory watermark reclaimer start to reclaim, reclaimInfo: %v", reclaimInfo)

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

	var reclaimed uint64
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
		free := memLimit - memUsage
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
		r.emitMetric(MetricNameUserWatermarkReclaimStats, 1, metrics.MetricTypeNameRaw,
			metrics.MetricTag{Key: MetricTagKeyMemoryFree, Val: fmt.Sprintf("%v", free)},
			metrics.MetricTag{Key: MetricTagKeyMemoryPSI, Val: fmt.Sprintf("%v", currReclaimStats.memPsiAvg60)},
			metrics.MetricTag{Key: MetricTagKeyReclaimRefault, Val: fmt.Sprintf("%v", currReclaimStats.refault)},
			metrics.MetricTag{Key: MetricTagKeyReclaimAccuracyRatio, Val: fmt.Sprintf("%v", reclaimAccuracyRatio)},
			metrics.MetricTag{Key: MetricTagKeyReclaimScanEfficiencyRatio, Val: fmt.Sprintf("%v", reclaimScanEfficiencyRatio)},
		)

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
		"reclaimTarget", reclaimInfo.ReclaimTarget, "highWatermark", reclaimInfo.HighWaterMark)

	return result, nil
}

func (r *userWatermarkReclaimer) GetConfig() *userwatermark.ReclaimConfigDetail {
	return r.reclaimConf
}

func (r *userWatermarkReclaimer) LoadConfig() {
	userWatermarkDynamicConf := r.dynamicConf.GetDynamicConfiguration().UserWatermarkConfiguration
	if userWatermarkDynamicConf == nil {
		general.Warningf("Get user watermark dynamic conf is nil")
		return
	}

	if r.containerInfo != nil && r.containerInfo.PodUID != "" {
		// load QoS level config
		if helper.IsValidQosLevel(r.containerInfo.QoSLevel) {
			if qosReclaimConfig, exist := userWatermarkDynamicConf.QoSLevelConfig[katalystapiconsts.QoSLevel(r.containerInfo.QoSLevel)]; exist {
				r.loadConfig(qosReclaimConfig)
			}
		}

		// load service config
		if serviceName, exist := r.containerInfo.Labels[r.serviceLabel]; exist {
			if serviceReclaimConfig, exist := userWatermarkDynamicConf.ServiceConfig[serviceName]; exist {
				r.loadConfig(serviceReclaimConfig)
			}
		}
		return
	}

	// load cgroup config
	if cgroupReclaimConfig, exist := r.dynamicConf.GetDynamicConfiguration().UserWatermarkConfiguration.CgroupConfig[r.cgroupPath]; exist {
		r.loadConfig(cgroupReclaimConfig)
	}
}

func (r *userWatermarkReclaimer) loadConfig(config *userwatermark.ReclaimConfigDetail) {
	if config == nil {
		general.Warningf("Load config detail failed, config is nil, podName:%v containerName:%v cgroupPath: %s qosLevel:%v",
			r.containerInfo.PodName, r.containerInfo.ContainerName, r.cgroupPath, r.containerInfo.QoSLevel)
		return
	}

	r.reclaimConf.EnableMemoryReclaim = config.EnableMemoryReclaim
	r.reclaimConf.ReclaimInterval = config.ReclaimInterval
	r.reclaimConf.SingleReclaimSize = config.SingleReclaimSize
	r.reclaimConf.ScaleFactor = config.ScaleFactor
	r.reclaimConf.BackoffDuration = config.BackoffDuration
	r.reclaimConf.FeedbackPolicy = config.FeedbackPolicy
	r.reclaimConf.ReclaimFailedThreshold = config.ReclaimFailedThreshold
	r.reclaimConf.FailureFreezePeriod = config.FailureFreezePeriod

	if config.PSIPolicyConf != nil {
		r.reclaimConf.PsiAvg60Threshold = config.PSIPolicyConf.PsiAvg60Threshold
	}
	if config.RefaultPolicyConf != nil {
		r.reclaimConf.ReclaimAccuracyTarget = config.RefaultPolicyConf.ReclaimAccuracyTarget
		r.reclaimConf.ReclaimScanEfficiencyTarget = config.RefaultPolicyConf.ReclaimScanEfficiencyTarget
	}

	general.InfofV(5, "Load config detail success, podName:%v containerName:%v cgroupPath: %s conf:%v",
		r.containerInfo.PodName, r.containerInfo.ContainerName, r.cgroupPath, r.reclaimConf)
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

func (r *userWatermarkReclaimer) emitMetric(metricName string, val int64, emitType metrics.MetricTypeName, tags ...metrics.MetricTag) {
	baseTags := []metrics.MetricTag{
		{Key: MetricTagKeyCGroupPath, Val: r.cgroupPath},
	}
	if r.containerInfo != nil {
		baseTags = append(baseTags, metrics.MetricTag{Key: MetricTagKeyPodName, Val: r.containerInfo.PodName})
		baseTags = append(baseTags, metrics.MetricTag{Key: MetricTagKeyContainerName, Val: r.containerInfo.ContainerName})
	}
	tags = append(baseTags, tags...)

	_ = r.emitter.StoreInt64(metricName, val, emitType, tags...)
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
