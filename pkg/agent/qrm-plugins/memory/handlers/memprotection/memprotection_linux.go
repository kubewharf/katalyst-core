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

package memprotection

import (
	"context"
	"fmt"
	"strconv"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	coreconfig "github.com/kubewharf/katalyst-core/pkg/config"
	dynamicconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	cgroupcm "github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	cgroupmgr "github.com/kubewharf/katalyst-core/pkg/util/cgroup/manager"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

func calculatedBestSoftLimit(memUsage, memFileInactive, userSoftLimit uint64) uint64 {
	if memFileInactive > memUsage {
		return 0
	}
	minSoftLimit := memUsage - memFileInactive + cgroupMemory64M
	maxSoftLimit := memUsage + cgroupMemory128M
	softLimit := uint64(general.Clamp(float64(userSoftLimit), float64(minSoftLimit), float64(maxSoftLimit)))
	return softLimit
}

func getUserSpecifiedMemoryProtectionInBytes(memLimit, memUsage, ratio uint64) uint64 {
	if ratio > 100 || ratio <= 0 {
		general.Infof("Bad ratio %v", ratio)
		return 0
	}

	maxLimit := memLimit
	if memLimit >= cgroupMemoryUnlimited {
		maxLimit = memUsage + cgroupMemory128M
	}
	softLimit := uint64(float64(maxLimit) / 100.0 * float64(ratio))
	softLimit = general.AlignToPageSize(softLimit)
	return softLimit
}

func calculateMemSoftLimit(relCgPath string, ratio uint64) (uint64, error) {
	/*
	 * I hope to protect cgroup from System-Thrashing(insufficient hot file memory)
	 * during mem_cgroup_soft_limit_reclaim() through memory.low.
	 */
	// Step1, get cgroup memory.limit, memory.usage, inactive-file-memory.
	memStat, err := cgroupmgr.GetMemoryWithRelativePath(relCgPath)
	if err != nil {
		general.Warningf("GetMemoryWithRelativePath failed with err: %v", err)
		return 0, err
	}

	// Step2, Reserve a certain ratio of file memory for high-QoS cgroups.
	userSoftLimit := getUserSpecifiedMemoryProtectionInBytes(memStat.Limit, memStat.Usage, ratio)
	if userSoftLimit == 0 {
		general.Warningf("getUserSpecifiedMemoryProtectionBytes return 0")
		return 0, fmt.Errorf("getUserSpecifiedMemoryProtectionBytes return 0")
	}

	// Step3, I don't want to hurt existing hot file-memory.
	// If the reserve file memory is not sufficient for current hot file-memory,
	// then the final memory.low will be based on current hot file-memory.
	softLimit := calculatedBestSoftLimit(memStat.Usage, memStat.FileInactive, userSoftLimit)

	return softLimit, nil
}

func applyMemSoftLimitCgroupLevelConfig(conf *coreconfig.Configuration,
	emitter metrics.MetricEmitter) {
	if conf.MemSoftLimitCgroupLevelConfigFile == "" {
		general.Errorf("MemSoftLimitCgroupLevelConfigFile isn't configured")
		return
	}

	memSoftLimitCgroupLevelConfigs := make(map[string]uint64)
	err := general.LoadJsonConfig(conf.MemSoftLimitCgroupLevelConfigFile, &memSoftLimitCgroupLevelConfigs)
	if err != nil {
		general.Errorf("load MemSoftLimitCgroupLevelConfigFile failed with error: %v", err)
		return
	}

	for relCgPath, ratio := range memSoftLimitCgroupLevelConfigs {
		softLimit, err := calculateMemSoftLimit(relCgPath, ratio)
		if err != nil {
			general.Errorf("calculateMemSoftLimit for relativeCgPath: %s failed with error: %v",
				relCgPath, err)
			continue
		}

		// OK. Set the value for memory.low.
		var data *cgroupcm.MemoryData
		data = &cgroupcm.MemoryData{SoftLimitInBytes: int64(softLimit)}
		if err := cgroupmgr.ApplyMemoryWithRelativePath(relCgPath, data); err != nil {
			general.Warningf("ApplyMemoryWithRelativePath failed, cgpath=%v, err=%v", relCgPath, err)
			continue
		}

		_ = emitter.StoreInt64(metricNameMemLow, int64(softLimit), metrics.MetricTypeNameRaw,
			metrics.ConvertMapToTags(map[string]string{
				"path": relCgPath,
			})...)
	}
}

func applyMemSoftLimitQoSLevelConfig(conf *coreconfig.Configuration,
	emitter metrics.MetricEmitter, metaServer *metaserver.MetaServer) {
	if conf.MemSoftLimitQoSLevelConfigFile == "" {
		general.Infof("no MemSoftLimitQoSLevelConfigFile found")
		return
	}

	var extraControlKnobConfigs commonstate.ExtraControlKnobConfigs
	if err := general.LoadJsonConfig(conf.MemSoftLimitQoSLevelConfigFile, &extraControlKnobConfigs); err != nil {
		general.Errorf("MemSoftLimitQoSLevelConfigFile load failed:%v", err)
		return
	}
	ctx := context.Background()
	podList, err := metaServer.GetPodList(ctx, native.PodIsActive)
	if err != nil {
		general.Infof("get pod list failed: %v", err)
		return
	}

	for _, pod := range podList {
		if pod == nil {
			general.Warningf("get nil pod from metaServer")
			continue
		}
		if conf.QoSConfiguration == nil {
			continue
		}
		qosConfig := conf.QoSConfiguration
		qosLevel, err := qosConfig.GetQoSLevelForPod(pod)
		if err != nil {
			general.Warningf("GetQoSLevelForPod failed:%v", err)
			continue
		}
		qosLevelDefaultValue, ok := extraControlKnobConfigs[controlKnobKeyMemSoftLimit].QoSLevelToDefaultValue[qosLevel]
		if !ok {
			continue
		}

		ratio, err := strconv.Atoi(qosLevelDefaultValue)
		if err != nil {
			general.Infof("Atoi failed with err: %v", err)
			continue
		}

		for _, containerStatus := range pod.Status.ContainerStatuses {
			podUID, containerID := string(pod.UID), native.TrimContainerIDPrefix(containerStatus.ContainerID)
			relCgPath, err := cgroupcm.GetContainerRelativeCgroupPath(podUID, containerID)
			if err != nil {
				general.Warningf("GetContainerRelativeCgroupPath failed, pod=%v, container=%v, err=%v", podUID, containerID, err)
				continue
			}

			softLimit, err := calculateMemSoftLimit(relCgPath, uint64(ratio))
			if err != nil {
				general.Errorf("calculateMemSoftLimit for relativeCgPath: %s failed with error: %v",
					relCgPath, err)
				continue
			}

			// OK. Set the value for memory.low.
			var data *cgroupcm.MemoryData
			data = &cgroupcm.MemoryData{SoftLimitInBytes: int64(softLimit)}
			if err := cgroupmgr.ApplyMemoryWithRelativePath(relCgPath, data); err != nil {
				general.Warningf("ApplyMemoryWithRelativePath failed, cgpath=%v, err=%v", relCgPath, err)
				continue
			}

			_ = emitter.StoreInt64(metricNameMemLow, int64(softLimit), metrics.MetricTypeNameRaw,
				metrics.ConvertMapToTags(map[string]string{
					"podUID":      podUID,
					"containerID": containerID,
				})...)
		}
	}
}

func MemProtectionTaskFunc(conf *coreconfig.Configuration,
	_ interface{}, _ *dynamicconfig.DynamicAgentConfiguration,
	emitter metrics.MetricEmitter, metaServer *metaserver.MetaServer) {
	general.Infof("called")

	if conf == nil {
		general.Errorf("nil extraConf")
		return
	} else if emitter == nil {
		general.Errorf("nil emitter")
		return
	} else if metaServer == nil {
		general.Errorf("nil metaServer")
		return
	}

	// SettingMemProtection featuregate.
	if !conf.EnableSettingMemProtection {
		general.Infof("EnableSettingMemProtection disabled")
		return
	}

	// checking qos-level memory.low configuration.
	if len(conf.MemSoftLimitQoSLevelConfigFile) > 0 {
		applyMemSoftLimitQoSLevelConfig(conf, emitter, metaServer)
	}

	// checking cgroup-level memory.low configuration.
	if len(conf.MemSoftLimitCgroupLevelConfigFile) > 0 {
		applyMemSoftLimitCgroupLevelConfig(conf, emitter)
	}
}
