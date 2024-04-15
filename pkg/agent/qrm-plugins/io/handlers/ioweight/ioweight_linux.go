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

package ioweight

import (
	"context"
	"fmt"
	"strconv"

	v1 "k8s.io/api/core/v1"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	coreconfig "github.com/kubewharf/katalyst-core/pkg/config"
	dynamicconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	cgroupmgr "github.com/kubewharf/katalyst-core/pkg/util/cgroup/manager"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

func applyIOWeightCgroupLevelConfig(conf *coreconfig.Configuration, emitter metrics.MetricEmitter) {
	if conf.IOWeightCgroupLevelConfigFile == "" {
		general.Errorf("IOWeightCgroupLevelConfigFile isn't configured")
		return
	}

	ioWightCgroupLevelConfigs := make(map[string]uint64)
	err := general.LoadJsonConfig(conf.IOWeightCgroupLevelConfigFile, &ioWightCgroupLevelConfigs)
	if err != nil {
		general.Errorf("load IOWeightCgroupLevelConfig failed with error: %v", err)
		return
	}

	for relativeCgPath, weight := range ioWightCgroupLevelConfigs {
		err := cgroupmgr.ApplyIOWeightWithRelativePath(relativeCgPath, defaultDevID, weight)
		if err != nil {
			general.Errorf("ApplyIOWeightWithRelativePath for devID: %s in relativeCgPath: %s failed with error: %v",
				defaultDevID, relativeCgPath, err)
		} else {
			general.Infof("ApplyIOWeightWithRelativePath for devID: %s, weight: %d in relativeCgPath: %s successfully",
				defaultDevID, weight, relativeCgPath)
			_ = emitter.StoreInt64(metricNameIOWeight, int64(weight), metrics.MetricTypeNameRaw,
				metrics.ConvertMapToTags(map[string]string{
					"cgPath": relativeCgPath,
				})...)

		}
	}
}

func applyPodIOWeight(pod *v1.Pod, defaultDevID, qosLevelDefaultValue string) error {
	ioWeightValue, err := strconv.ParseInt(qosLevelDefaultValue, 10, 64)
	if err != nil {
		return fmt.Errorf("strconv.ParseInt failed, string=%v, err=%v", qosLevelDefaultValue, err)
	}

	podAbsCGPath, err := common.GetPodAbsCgroupPath(common.CgroupSubsysIO, string(pod.UID))
	if err != nil {
		return fmt.Errorf("GetPodAbsCgroupPath for pod: %s/%s failed with error: %v", pod.Namespace, pod.Name, err)
	}

	err = cgroupmgr.ApplyIOWeightWithAbsolutePath(podAbsCGPath, defaultDevID, uint64(ioWeightValue))
	if err != nil {
		return fmt.Errorf("ApplyIOWeightWithAbsolutePath for pod: %s/%s failed with error: %v", pod.Namespace, pod.Name, err)
	}

	general.Infof("ApplyIOWeightWithRelativePath for pod: %s/%s, weight: %d successfully", pod.Namespace, pod.Name, ioWeightValue)
	return nil
}

func applyIOWeightQoSLevelConfig(conf *coreconfig.Configuration,
	emitter metrics.MetricEmitter, metaServer *metaserver.MetaServer,
) {
	if conf.IOWeightQoSLevelConfigFile == "" {
		general.Infof("no IOWeightQoSLevelConfigFile found")
		return
	}

	var extraControlKnobConfigs commonstate.ExtraControlKnobConfigs
	if err := general.LoadJsonConfig(conf.IOWeightQoSLevelConfigFile, &extraControlKnobConfigs); err != nil {
		general.Errorf("IOWeightQoSLevelConfigFile load failed:%v", err)
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
		qosLevelDefaultValue, ok := extraControlKnobConfigs[controlKnobKeyIOWeight].QoSLevelToDefaultValue[qosLevel]
		if !ok {
			continue
		}

		// setup pod level.
		err = applyPodIOWeight(pod, defaultDevID, qosLevelDefaultValue)
		if err != nil {
			general.Errorf("Failed to apply IO weight for pod %s/%s: %v", pod.Namespace, pod.Name, err)
			continue
		}

		// setup contaienr level.
		for _, containerStatus := range pod.Status.ContainerStatuses {
			podUID, containerID := string(pod.UID), native.TrimContainerIDPrefix(containerStatus.ContainerID)
			err := cgroupmgr.ApplyUnifiedDataForContainer(podUID, containerID, extraControlKnobConfigs[controlKnobKeyIOWeight].CgroupSubsysName, cgroupIOWeightName, qosLevelDefaultValue)
			if err != nil {
				general.Warningf("ApplyUnifiedDataForContainer failed:%v", err)
				continue
			}

			ioWeightValue, err := strconv.ParseInt(qosLevelDefaultValue, 10, 64)
			if err != nil {
				general.Warningf("strconv.ParseInt failed, string=%v, err=%v", qosLevelDefaultValue, err)
				continue
			}

			_ = emitter.StoreInt64(metricNameIOWeight, ioWeightValue, metrics.MetricTypeNameRaw,
				metrics.ConvertMapToTags(map[string]string{
					"podUID":      podUID,
					"containerID": containerID,
				})...)
		}
	}
}

func IOWeightTaskFunc(conf *coreconfig.Configuration,
	_ interface{}, _ *dynamicconfig.DynamicAgentConfiguration,
	emitter metrics.MetricEmitter, metaServer *metaserver.MetaServer,
) {
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

	// SettingIOWeight featuregate.
	if !conf.EnableSettingIOWeight {
		general.Infof("EnableSettingIOWeight disabled")
		return
	}

	if !common.CheckCgroup2UnifiedMode() {
		general.Infof("skip IOWeightTaskFunc in cg1 env")
		return
	}

	// checking qos-level io.weight configuration.
	if len(conf.IOWeightQoSLevelConfigFile) > 0 {
		applyIOWeightQoSLevelConfig(conf, emitter, metaServer)
	}

	// checking cgroup-level io.weight configuration.
	if len(conf.IOWeightCgroupLevelConfigFile) > 0 {
		applyIOWeightCgroupLevelConfig(conf, emitter)
	}
}
