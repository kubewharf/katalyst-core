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
	"context"
	"math"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pkg/errors"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	katalystapiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/memory/dynamicpolicy/memoryadvisor"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/helper"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/tmo"
	tmoconf "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/tmo"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	katalystcoreconsts "github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

const (
	TransparentMemoryOffloading = "transparent-memory-offloading"

	MetricMemoryOffloading = "memory_offloading"
)

const (
	InactiveProbe            = 0.1
	OffloadingSizeScaleCoeff = 1.05
	CacheMappedCoeff         = 2
)

const (
	DummyTMOBlockFnName             string = "dummy-tmo-block-fn"
	FromDynamicConfigTMOBlockFnName string = "tmo-block-func-from-dynamic-config"
)

// TMO policy funcs to calculate memory offloading size
var tmoPolicyFuncs sync.Map

// TMO block funcs to filter the containers required to disable TMO
var tmoBlockFuncs sync.Map

type TmoStats struct {
	obj                  string
	qosLevel             string
	memUsage             float64
	memInactive          float64
	memPsiAvg60          float64
	pgscan               float64
	pgsteal              float64
	refault              float64
	refaultActivate      float64
	cache                float64
	mapped               float64
	offloadingTargetSize float64
}

type TmoPolicyFn func(
	lastStats TmoStats,
	currStats TmoStats,
	conf *tmoconf.TMOConfigDetail,
	emitter metrics.MetricEmitter) (error, float64)

func psiPolicyFunc(lastStats TmoStats, currStats TmoStats, conf *tmoconf.TMOConfigDetail, emitter metrics.MetricEmitter) (error, float64) {
	if conf.PSIPolicyConf == nil {
		return errors.New("psi policy requires psi policy configuration"), 0
	}
	result := math.Max(0, 1-(currStats.memPsiAvg60)/(conf.PSIPolicyConf.PsiAvg60Threshold)) * conf.PSIPolicyConf.MaxProbe * currStats.memUsage

	general.InfoS("psi info", "obj", currStats.obj, "memPsiAvg60", currStats.memPsiAvg60, "psiAvg60Threshold",
		conf.PSIPolicyConf.PsiAvg60Threshold, "maxProbe", conf.PSIPolicyConf.MaxProbe, "memUsage", currStats.memUsage,
		"result", result)
	_ = emitter.StoreFloat64(MetricMemoryOffloading, result, metrics.MetricTypeNameRaw,
		metrics.MetricTag{Key: "policy", Val: string(v1alpha1.TMOPolicyNamePSI)},
		metrics.MetricTag{Key: "obj", Val: currStats.obj},
		metrics.MetricTag{Key: "qos_level", Val: currStats.qosLevel})
	return nil, result
}

func refaultPolicyFunc(lastStats TmoStats, currStats TmoStats, conf *tmoconf.TMOConfigDetail, emitter metrics.MetricEmitter) (error, float64) {
	if conf.RefaultPolicyConf == nil {
		return errors.New("refault policy requires refault policy configurations"), 0
	}
	pgscanDelta := currStats.pgscan - lastStats.pgscan
	pgstealDelta := currStats.pgsteal - lastStats.pgsteal
	refaultDelta := currStats.refaultActivate - lastStats.refaultActivate
	reclaimAccuracyRatio := 1.0
	reclaimScanEfficiencyRatio := 1.0
	if pgstealDelta > 0 && pgscanDelta > 0 {
		reclaimAccuracyRatio = 1 - refaultDelta/pgstealDelta
		reclaimScanEfficiencyRatio = pgstealDelta / pgscanDelta
	}

	var result float64
	if reclaimAccuracyRatio < conf.RefaultPolicyConf.ReclaimAccuracyTarget || reclaimScanEfficiencyRatio < conf.RefaultPolicyConf.ReclaimScanEfficiencyTarget {
		// Decrease offloading size if detecting the reclaim accuracy or scan efficiency is below the targets
		result = math.Max(0, lastStats.offloadingTargetSize*reclaimAccuracyRatio)
	} else {
		// Try to increase offloading size but make sure not exceed the max probe of memory usage and 10% of inactive memory when the target size of last round is relatively small,
		// which means reclaim accuracy and reclaim scan efficiency is low.
		result = math.Min(math.Max(lastStats.offloadingTargetSize*OffloadingSizeScaleCoeff, currStats.memInactive*InactiveProbe), currStats.memUsage*conf.RefaultPolicyConf.MaxProbe)
	}
	general.InfoS("refault info", "obj", currStats.obj, "reclaimAccuracyRatio", reclaimAccuracyRatio, "ReclaimAccuracyTarget", conf.RefaultPolicyConf.ReclaimAccuracyTarget,
		"reclaimScanEfficiencyRatio", reclaimScanEfficiencyRatio, "ReclaimScanEfficiencyTarget", conf.RefaultPolicyConf.ReclaimScanEfficiencyTarget,
		"refaultDelta", refaultDelta, "pgstealDelta", pgstealDelta, "pgscanDelta", pgscanDelta, "lastOffloadingTargetSize", general.FormatMemoryQuantity(lastStats.offloadingTargetSize),
		"result", general.FormatMemoryQuantity(result))
	_ = emitter.StoreFloat64(MetricMemoryOffloading, result, metrics.MetricTypeNameRaw,
		metrics.MetricTag{Key: "policy", Val: string(v1alpha1.TMOPolicyNameRefault)},
		metrics.MetricTag{Key: "obj", Val: currStats.obj},
		metrics.MetricTag{Key: "qos_level", Val: currStats.qosLevel})
	return nil, result
}

func integratedPolicyFunc(lastStats TmoStats, currStats TmoStats, conf *tmoconf.TMOConfigDetail, emitter metrics.MetricEmitter) (error, float64) {
	err, targetSizeFromPSI := psiPolicyFunc(lastStats, currStats, conf, emitter)
	if err != nil {
		return err, 0
	}
	err, targetSizeFromRefault := refaultPolicyFunc(lastStats, currStats, conf, emitter)
	if err != nil {
		return err, 0
	}
	result := math.Min(targetSizeFromPSI, targetSizeFromRefault)

	_ = emitter.StoreFloat64(MetricMemoryOffloading, result, metrics.MetricTypeNameRaw,
		metrics.MetricTag{Key: "policy", Val: string(v1alpha1.TMOPolicyNameIntegrated)},
		metrics.MetricTag{Key: "obj", Val: currStats.obj},
		metrics.MetricTag{Key: "qos_level", Val: currStats.qosLevel})
	return err, result
}

type TMOBlockFn func(ci *types.ContainerInfo, conf interface{}, dynamicConf interface{}) bool

func DummyTMOBlockFn(ci *types.ContainerInfo, conf interface{}, dynamicConf interface{}) bool {
	return false
}

func init() {
	RegisterTMOPolicyFunc(v1alpha1.TMOPolicyNamePSI, psiPolicyFunc)
	RegisterTMOPolicyFunc(v1alpha1.TMOPolicyNameRefault, refaultPolicyFunc)
	RegisterTMOPolicyFunc(v1alpha1.TMOPolicyNameIntegrated, integratedPolicyFunc)
	RegisterTMOBlockFunc(DummyTMOBlockFnName, DummyTMOBlockFn)
}

func RegisterTMOPolicyFunc(policyName v1alpha1.TMOPolicyName, tmoPolicyFn TmoPolicyFn) {
	tmoPolicyFuncs.Store(policyName, tmoPolicyFn)
}

func RegisterTMOBlockFunc(blockFnName string, blockFn TMOBlockFn) {
	tmoBlockFuncs.Store(blockFnName, blockFn)
}

func TMOBlockFnFromDynamicConfig(ci *types.ContainerInfo, extraConf interface{}, dynamicConf interface{}) bool {
	if conf, ok := dynamicConf.(*tmoconf.TMOBlockConfig); ok {
		if (conf.LabelsSelector != nil && conf.LabelsSelector.Matches(labels.Set(ci.Labels))) ||
			(conf.AnnotationsSelector != nil && conf.AnnotationsSelector.Matches(labels.Set(ci.Annotations))) {
			return true
		}
	}
	return false
}

type transparentMemoryOffloading struct {
	conf                *config.Configuration
	extraConf           interface{}
	mutex               sync.RWMutex
	metaReader          metacache.MetaReader
	metaServer          *metaserver.MetaServer
	emitter             metrics.MetricEmitter
	containerTmoEngines map[katalystcoreconsts.PodContainerName]TmoEngine
	cgpathTmoEngines    map[string]TmoEngine
}

type TmoEngine interface {
	GetContainerInfo() *types.ContainerInfo
	GetCgpath() string
	LoadConf(*tmo.TMOConfigDetail)
	GetConf() *tmo.TMOConfigDetail
	CalculateOffloadingTargetSize()
	GetOffloadingTargetSize() float64
}

type tmoEngineInstance struct {
	workingRounds        int64
	containerInfo        *types.ContainerInfo // only valid when this tmo engine is working on container
	cgpath               string
	metaServer           *metaserver.MetaServer
	emitter              metrics.MetricEmitter
	conf                 *tmo.TMOConfigDetail
	lastTime             time.Time
	lastStats            TmoStats
	offloadingTargetSize float64
}

func NewTmoEngineInstance(obj interface{}, metaServer *metaserver.MetaServer, emitter metrics.MetricEmitter, tmoConf *tmo.TransparentMemoryOffloadingConfiguration) *tmoEngineInstance {
	tmoEngine := &tmoEngineInstance{
		workingRounds: 0,
		metaServer:    metaServer,
		emitter:       emitter,
		conf:          tmo.NewTMOConfigDetail(tmoConf.DefaultConfigurations),
	}
	if path, ok := obj.(string); ok {
		tmoEngine.cgpath = path
	}
	if ci, ok := obj.(*types.ContainerInfo); ok {
		tmoEngine.containerInfo = ci
	}
	return tmoEngine
}

func (tmoEngine *tmoEngineInstance) GetConf() *tmo.TMOConfigDetail {
	return tmoEngine.conf
}

func (tmoEngine *tmoEngineInstance) getStats() (TmoStats, error) {
	tmoStats := &TmoStats{}
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
		tmoStats.memUsage = memUsage.Value
		tmoStats.memInactive = memInactiveFile.Value + memInactiveAnon.Value
		tmoStats.memPsiAvg60 = psiAvg60.Value
		tmoStats.pgsteal = pgsteal.Value
		tmoStats.pgscan = pgscan.Value
		tmoStats.refault = refault.Value
		tmoStats.refaultActivate = refaultActivate.Value
		tmoStats.cache = memCache.Value
		tmoStats.mapped = memMappedFile.Value
		tmoStats.offloadingTargetSize = tmoEngine.offloadingTargetSize
		general.Infof("Memory Usage of Cgroup %s, memUsage: %v, cache: %v, mapped: %v", tmoEngine.cgpath, memUsage.Value, memCache.Value, memMappedFile.Value)
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
		tmoStats.memUsage = memUsage.Value
		tmoStats.memInactive = memInactiveFile.Value + memInactiveAnon.Value
		tmoStats.memPsiAvg60 = psiAvg60.Value
		tmoStats.pgsteal = pgsteal.Value
		tmoStats.pgscan = pgscan.Value
		tmoStats.refault = refault.Value
		tmoStats.refaultActivate = refaultActivate.Value
		tmoStats.cache = memCache.Value
		tmoStats.mapped = memMappedFile.Value
		tmoStats.offloadingTargetSize = tmoEngine.offloadingTargetSize
		general.Infof("Memory Usage of Pod %v, Container %v, memUsage: %v, cache: %v, mapped: %v", podUID, containerName, memUsage.Value, memCache.Value, memMappedFile.Value)
		return nil
	}

	if tmoEngine.containerInfo == nil {
		err = getCgroupMetrics(tmoEngine.metaServer, tmoEngine.cgpath)
		tmoStats.obj = tmoEngine.cgpath
		tmoStats.qosLevel = tmoEngine.cgpath
	} else {
		err = getContainerMetrics(tmoEngine.metaServer, tmoEngine.containerInfo.PodUID, tmoEngine.containerInfo.ContainerName)
		tmoStats.obj = strings.Join([]string{tmoEngine.containerInfo.PodNamespace, tmoEngine.containerInfo.PodName}, "/")
		tmoStats.qosLevel = tmoEngine.containerInfo.QoSLevel
	}
	return *tmoStats, err
}

func (tmoEngine *tmoEngineInstance) GetOffloadingTargetSize() float64 {
	return tmoEngine.offloadingTargetSize
}

func (tmoEngine *tmoEngineInstance) GetContainerInfo() *types.ContainerInfo {
	return tmoEngine.containerInfo
}

func (tmoEngine *tmoEngineInstance) GetCgpath() string {
	return tmoEngine.cgpath
}

func (tmoEngine *tmoEngineInstance) LoadConf(detail *tmo.TMOConfigDetail) {
	tmoEngine.conf.EnableTMO = detail.EnableTMO
	tmoEngine.conf.EnableSwap = detail.EnableSwap
	tmoEngine.conf.Interval = detail.Interval
	tmoEngine.conf.PolicyName = detail.PolicyName
	if psiPolicyConfDynamic := detail.PSIPolicyConf; psiPolicyConfDynamic != nil {
		tmoEngine.conf.PSIPolicyConf.MaxProbe = psiPolicyConfDynamic.MaxProbe
		tmoEngine.conf.PSIPolicyConf.PsiAvg60Threshold = psiPolicyConfDynamic.PsiAvg60Threshold
	}
	if refaultPolicyConfDynamic := detail.RefaultPolicyConf; refaultPolicyConfDynamic != nil {
		tmoEngine.conf.RefaultPolicyConf.MaxProbe = refaultPolicyConfDynamic.MaxProbe
		tmoEngine.conf.RefaultPolicyConf.ReclaimAccuracyTarget = refaultPolicyConfDynamic.ReclaimAccuracyTarget
		tmoEngine.conf.RefaultPolicyConf.ReclaimScanEfficiencyTarget = refaultPolicyConfDynamic.ReclaimScanEfficiencyTarget
	}
}

func (tmoEngine *tmoEngineInstance) CalculateOffloadingTargetSize() {
	tmoEngine.offloadingTargetSize = 0
	if !tmoEngine.conf.EnableTMO {
		return
	}
	currTime := time.Now()
	if currTime.Sub(tmoEngine.lastTime) < tmoEngine.conf.Interval {
		tmoEngine.offloadingTargetSize = 0
		return
	}

	currStats, err := tmoEngine.getStats()
	if err != nil {
		general.Infof("Failed to get metrics %v", err)
		return
	}

	// TODO: get result from qrm to make sure last offloading action finished
	if fn, ok := tmoPolicyFuncs.Load(tmoEngine.conf.PolicyName); ok {
		if policyFunc, ok := fn.(TmoPolicyFn); ok {
			if !tmoEngine.conf.EnableSwap && currStats.cache < CacheMappedCoeff*currStats.mapped {
				general.Infof("Tmo obj: %s cache is close to mapped, skip reclaim", currStats.obj)
				tmoEngine.offloadingTargetSize = 0
				return
			}
			err, targetSize := policyFunc(tmoEngine.lastStats, currStats, tmoEngine.conf, tmoEngine.emitter)
			if err != nil {
				general.ErrorS(err, "Failed to calculate offloading memory size")
				return
			}

			cacheExceptMapped := currStats.cache - currStats.mapped
			general.InfoS("Handle targetSize from policy", "Tmo obj:", currStats.obj, "targetSize:", targetSize, "cacheExceptMapped", cacheExceptMapped)
			targetSize = math.Max(0, math.Min(cacheExceptMapped, targetSize))
			tmoEngine.offloadingTargetSize = targetSize
			currStats.offloadingTargetSize = targetSize
			tmoEngine.lastStats = currStats
			tmoEngine.lastTime = currTime
		}
	}
}

func NewTransparentMemoryOffloading(conf *config.Configuration, extraConfig interface{}, metaReader metacache.MetaReader, metaServer *metaserver.MetaServer, emitter metrics.MetricEmitter) MemoryAdvisorPlugin {
	return &transparentMemoryOffloading{
		conf:                conf,
		extraConf:           extraConfig,
		metaReader:          metaReader,
		metaServer:          metaServer,
		emitter:             emitter,
		containerTmoEngines: make(map[consts.PodContainerName]TmoEngine),
		cgpathTmoEngines:    make(map[string]TmoEngine),
	}
}

func (tmo *transparentMemoryOffloading) Reconcile(status *types.MemoryPressureStatus) error {
	if tmo.conf.GetDynamicConfiguration().BlockConfig != nil {
		RegisterTMOBlockFunc(FromDynamicConfigTMOBlockFnName, TMOBlockFnFromDynamicConfig)
	}
	podContainerNamesMap := make(map[katalystcoreconsts.PodContainerName]bool)
	podList, err := tmo.metaServer.GetPodList(context.Background(), native.PodIsActive)
	if err != nil {
		general.Infof("Failed to get pod list: %v", err)
		return err
	}

	for _, pod := range podList {
		if pod == nil {
			general.Infof("Get nil pod from meta server.")
			continue
		}
		qos, err := tmo.conf.QoSConfiguration.GetQoSLevel(pod, map[string]string{})
		if err != nil {
			general.Infof("Failed to get qos level for pod uid: %s", pod.UID)
			if helper.PodIsDaemonSet(pod) {
				qos = katalystapiconsts.PodAnnotationQoSLevelSystemCores
				general.Infof("DaemonSet pod %s is considered as system_cores qos level", pod.UID)
			}
		}
		for _, containerStatus := range pod.Status.ContainerStatuses {
			containerInfo := &types.ContainerInfo{
				PodUID:        string(pod.UID),
				PodName:       pod.Name,
				ContainerName: containerStatus.Name,
				Labels:        pod.Labels,
				Annotations:   pod.Annotations,
				QoSLevel:      qos,
			}
			podContainerName := native.GeneratePodContainerName(containerInfo.PodName, containerInfo.ContainerName)
			podContainerNamesMap[podContainerName] = true
			_, exist := tmo.containerTmoEngines[podContainerName]
			if !exist {
				tmo.containerTmoEngines[podContainerName] = NewTmoEngineInstance(containerInfo, tmo.metaServer, tmo.emitter, tmo.conf.GetDynamicConfiguration().TransparentMemoryOffloadingConfiguration)
			}
			// load QoSLevelConfig
			if helper.IsValidQosLevel(containerInfo.QoSLevel) {
				if tmoConfigDetail, exist := tmo.conf.GetDynamicConfiguration().QoSLevelConfigs[katalystapiconsts.QoSLevel(containerInfo.QoSLevel)]; exist {
					tmo.containerTmoEngines[podContainerName].LoadConf(tmoConfigDetail)
					general.Infof("Load QosLevel %s TMO config for podContainerName %s, enableTMO: %v, enableSwap: %v, interval: %v, policy: %v",
						containerInfo.QoSLevel, podContainerName,
						tmo.containerTmoEngines[podContainerName].GetConf().EnableTMO,
						tmo.containerTmoEngines[podContainerName].GetConf().EnableSwap,
						tmo.containerTmoEngines[podContainerName].GetConf().Interval,
						tmo.containerTmoEngines[podContainerName].GetConf().PolicyName)
				}
			}
			// load SPD conf if exists
			tmoIndicator := &v1alpha1.TransparentMemoryOffloadingIndicators{}
			isBaseline, err := tmo.metaServer.ServiceProfilingManager.ServiceExtendedIndicator(context.Background(), pod.ObjectMeta, tmoIndicator)
			if err != nil {
				general.Infof("Error occurred when load check baseline and load TransparentMemoryOffloadingIndicators, err : %v", err)
			} else if !isBaseline {
				tmoConfigDetail := tmo.containerTmoEngines[podContainerName].GetConf()
				if tmoIndicator.ConfigDetail != nil {
					tmoconf.ApplyTMOConfigDetail(tmoConfigDetail, *tmoIndicator.ConfigDetail)
					general.Infof("Load Service Level TMO config for podContainerName %s, enableTMO: %v, enableSwap: %v, interval: %v, policy: %v",
						podContainerName,
						tmo.containerTmoEngines[podContainerName].GetConf().EnableTMO,
						tmo.containerTmoEngines[podContainerName].GetConf().EnableSwap,
						tmo.containerTmoEngines[podContainerName].GetConf().Interval,
						tmo.containerTmoEngines[podContainerName].GetConf().PolicyName)
				}
			}

			// disable TMO if the Pod is numa exclusive and is not reclaimable
			enableReclaim, _ := helper.PodEnableReclaim(context.Background(), tmo.metaServer, containerInfo.PodUID, true)
			if containerInfo.IsNumaExclusive() && !enableReclaim {
				tmo.containerTmoEngines[podContainerName].GetConf().EnableTMO = false
				general.Infof("container with podContainerName: %s is required to disable TMO since it is not reclaimable", podContainerName)
			}

			// disable TMO if the container is in TMO block list
			funcs := make(map[string]TMOBlockFn)
			tmoBlockFuncs.Range(func(key, value interface{}) bool {
				funcs[key.(string)] = value.(TMOBlockFn)
				return true
			})
			for tmoBlockFnName, tmoBlockFn := range funcs {
				if tmoBlockFn(containerInfo, tmo.extraConf, tmo.conf.GetDynamicConfiguration().BlockConfig) {
					tmo.containerTmoEngines[podContainerName].GetConf().EnableTMO = false
					general.Infof("container with podContainerName: %s is required to disable TMO by TMOBlockFn: %s", podContainerName, tmoBlockFnName)
				}
			}

			general.Infof("Final TMO configs for podContainerName: %v, enableTMO: %v, enableSwap: %v, interval: %v, policy: %v", podContainerName,
				tmo.containerTmoEngines[podContainerName].GetConf().EnableTMO,
				tmo.containerTmoEngines[podContainerName].GetConf().EnableSwap,
				tmo.containerTmoEngines[podContainerName].GetConf().Interval,
				tmo.containerTmoEngines[podContainerName].GetConf().PolicyName)
		}
	}

	// update tmo config for specified cgroup paths
	for cgpath, tmoConfigDetail := range tmo.conf.GetDynamicConfiguration().TransparentMemoryOffloadingConfiguration.CgroupConfigs {
		general.Infof("Load Cgroup TMO config for specific cgroup path %v", cgpath)
		if _, exist := tmo.cgpathTmoEngines[cgpath]; !exist {
			tmo.cgpathTmoEngines[cgpath] = NewTmoEngineInstance(cgpath, tmo.metaServer, tmo.emitter, tmo.conf.GetDynamicConfiguration().TransparentMemoryOffloadingConfiguration)
		}
		tmo.cgpathTmoEngines[cgpath].LoadConf(tmoConfigDetail)
		general.Infof("TMO configs for cgroup: %v, enableTMO: %v, enableSwap: %v, interval: %v, policy: %v", cgpath,
			tmoConfigDetail.EnableTMO, tmoConfigDetail.EnableSwap, tmoConfigDetail.Interval, tmoConfigDetail.PolicyName)
	}

	// delete tmo engines for not existed containers
	for podContainerName := range tmo.containerTmoEngines {
		_, exist := podContainerNamesMap[podContainerName]
		if !exist {
			delete(tmo.containerTmoEngines, podContainerName)
		}
	}

	// delete tmo engines for not existed cgroups
	for cgpath := range tmo.cgpathTmoEngines {
		if _, exist := tmo.conf.GetDynamicConfiguration().CgroupConfigs[cgpath]; !exist {
			delete(tmo.cgpathTmoEngines, cgpath)
		}
	}

	// calculate memory offloading size for each container
	for podContainerName, tmoEngine := range tmo.containerTmoEngines {
		tmoEngine.CalculateOffloadingTargetSize()
		general.InfoS("Calculate target offloading size", "podContainer", podContainerName,
			"result", general.FormatMemoryQuantity(tmoEngine.GetOffloadingTargetSize()))
	}

	// calculate memory offloading size for each cgroups
	for cgpath, tmoEngine := range tmo.cgpathTmoEngines {
		tmoEngine.CalculateOffloadingTargetSize()
		general.InfoS("Calculate target offloading size", "groupPath", cgpath,
			"result", general.FormatMemoryQuantity(tmoEngine.GetOffloadingTargetSize()))
	}
	return nil
}

func (tmo *transparentMemoryOffloading) GetAdvices() types.InternalMemoryCalculationResult {
	result := types.InternalMemoryCalculationResult{
		ContainerEntries: make([]types.ContainerMemoryAdvices, 0),
		ExtraEntries:     make([]types.ExtraMemoryAdvices, 0),
	}
	tmo.mutex.RLock()
	defer tmo.mutex.RUnlock()
	for _, tmoEngine := range tmo.containerTmoEngines {
		if tmoEngine.GetOffloadingTargetSize() <= 0 {
			continue
		}
		enableSwap := consts.ControlKnobOFF
		if tmoEngine.GetConf().EnableSwap {
			enableSwap = consts.ControlKnobON
		}

		entry := types.ContainerMemoryAdvices{
			PodUID:        tmoEngine.GetContainerInfo().PodUID,
			ContainerName: tmoEngine.GetContainerInfo().ContainerName,
			Values: map[string]string{
				string(memoryadvisor.ControlKnobKeySwapMax):          enableSwap,
				string(memoryadvisor.ControlKnowKeyMemoryOffloading): strconv.FormatInt(int64(tmoEngine.GetOffloadingTargetSize()), 10),
			},
		}
		result.ContainerEntries = append(result.ContainerEntries, entry)
	}

	for cgpath, tmoEngine := range tmo.cgpathTmoEngines {
		if tmoEngine.GetOffloadingTargetSize() <= 0 {
			continue
		}
		enableSwap := consts.ControlKnobOFF
		if tmoEngine.GetConf().EnableSwap {
			enableSwap = consts.ControlKnobON
		}
		relativePath, err := filepath.Rel(common.CgroupFSMountPoint, cgpath)
		if err != nil {
			continue
		}
		relativePath = "/" + relativePath
		entry := types.ExtraMemoryAdvices{
			CgroupPath: relativePath,
			Values: map[string]string{
				string(memoryadvisor.ControlKnobKeySwapMax):          enableSwap,
				string(memoryadvisor.ControlKnowKeyMemoryOffloading): strconv.FormatInt(int64(tmoEngine.GetOffloadingTargetSize()), 10),
			},
		}
		result.ExtraEntries = append(result.ExtraEntries, entry)
	}

	return result
}
