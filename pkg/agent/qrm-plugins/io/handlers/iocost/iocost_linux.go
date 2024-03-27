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

package iocost

import (
	"fmt"
	"path/filepath"
	"strconv"
	"sync"

	"github.com/kubewharf/katalyst-core/pkg/config"
	coreconfig "github.com/kubewharf/katalyst-core/pkg/config"
	dynamicconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	cgcommon "github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	"github.com/kubewharf/katalyst-core/pkg/util/cgroup/manager"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

var (
	initializeOnce   sync.Once
	ioCgroupRootPath = cgcommon.GetCgroupRootPath(cgcommon.CgroupSubsysIO)
)

func applyIOCostModel(ioCostModelConfigs map[DevModel]*common.IOCostModelData, devsIDToModel map[string]DevModel) {
	curDevIDToIOCostModelData, err := manager.GetIOCostModelWithAbsolutePath(ioCgroupRootPath)
	if err != nil {
		general.Errorf("GetIOCostModelWithAbsolutePath failed with error: %v", err)
		return
	}

	for devID, devModel := range devsIDToModel {
		var expectedModelData, curModelData *common.IOCostModelData

		if ioCostModelConfigs[devModel] != nil {
			expectedModelData = ioCostModelConfigs[devModel]
		} else {
			expectedModelData = ioCostModelConfigs[DevModelDefault]
		}

		if expectedModelData == nil {
			general.Errorf("there is no expected io cost Model Data for devID: %s, devModel: %s", devID, devModel)
			continue
		}

		curModelData = curDevIDToIOCostModelData[devID]
		if curModelData == nil {
			general.Errorf("there is no current io cost Model Data for devID: %s, devModel: %s", devID, devModel)
			continue
		}

		if (*curModelData) != (*expectedModelData) {
			err = manager.ApplyIOCostModelWithAbsolutePath(ioCgroupRootPath, devID, expectedModelData)
			if err != nil {
				general.Errorf("ApplyIOCostModelWithAbsolutePath for devID: %s, devModel: %s, failed with error: %v",
					devID, devModel, err)
			} else {
				general.Errorf("ApplyIOCostModelWithAbsolutePath for devID: %s, devModel: %s successfully",
					devID, devModel)
			}
		} else {
			general.Infof("modelData isn't changed, skip ApplyIOCostModelWithAbsolutePath for devID: %s, devModel: %s ",
				devID, devModel)
		}
	}
}

func reportDevicesIOCostVrate(emitter metrics.MetricEmitter) {
	devIDToIOStat, err := manager.GetIOStatWithAbsolutePath(ioCgroupRootPath)
	if err != nil {
		general.Errorf("GetIOStatWithAbsolutePath failed with error: %v", err)
		return
	}

	for devID, ioStat := range devIDToIOStat {
		if valueStr, found := ioStat[IOStatMetricCostVrate]; found {
			valueFloat64, err := strconv.ParseFloat(valueStr, 64)
			if err != nil {
				general.Errorf("%s value: %s is invalid for devID: %s",
					IOStatMetricCostVrate, valueStr, devID)
				continue
			}

			devName, found, err := getDeviceNameFromID(devID)
			if err != nil {
				general.Errorf("getDeviceNameFromID: %s failed with error: %v",
					devID, err)
				continue
			} else if !found {
				general.Errorf("no device name found for device id: %s", devID)
				continue
			}

			_ = emitter.StoreFloat64(MetricNameIOCostVrate, valueFloat64,
				metrics.MetricTypeNameRaw, metrics.MetricTag{
					Key: "device_name",
					Val: devName,
				})
		}
	}
}

func disableIOCost(conf *config.Configuration) {
	if !cgcommon.CheckCgroup2UnifiedMode() {
		return
	}

	devIDToIOCostQoSData, err := manager.GetIOCostQoSWithAbsolutePath(ioCgroupRootPath)
	if err != nil {
		general.Errorf("GetIOCostQoSWithAbsolutePath failed with error: %v in Init", err)
	}

	disabledIOCostQoSData := &cgcommon.IOCostQoSData{Enable: 0}
	for devID, ioCostQoSData := range devIDToIOCostQoSData {
		if ioCostQoSData == nil {
			general.Warningf("nil ioCostQoSData")
			continue
		} else if ioCostQoSData.Enable == 0 {
			general.Warningf("devID: %s ioCostQoS is already disabled", devID)
			continue
		}

		err = manager.ApplyIOCostQoSWithAbsolutePath(ioCgroupRootPath, devID, disabledIOCostQoSData)
		if err != nil {
			general.Errorf("ApplyIOCostQoSWithAbsolutePath for devID: %s, failed with error: %v", devID, err)
		} else {
			general.Infof("disable ioCostQoS for devID: %s successfully", devID)
		}
	}
}

func applyIOCostQoSWithDefault(
	ioCostQoSConfigs map[DevModel]*common.IOCostQoSData,
	devsIDToModel map[string]DevModel,
) {
	for devID := range devsIDToModel {

		// checking device type: isHDD?
		devName, found, err := getDeviceNameFromID(devID)
		if err != nil {
			general.Errorf("getDeviceNameFromID: %s failed with error: %v", devID, err)
			continue
		} else if !found {
			general.Errorf("no device name found for device id: %s", devID)
			continue
		}

		rotationalFile := filepath.Clean(fmt.Sprintf(queueRotationalFilePattern, devName))
		deviceType, err := getDeviceType(devName, rotationalFile)
		if err != nil {
			general.Errorf("checking device %v failed, error:%v", devName, err)
			continue
		}

		var defaultConfig DevModel
		switch deviceType {
		case HDD:
			defaultConfig = DevModelDefaultHDD
		case SSD:
			defaultConfig = DevModelDefault
		case Unknown:
			general.Errorf("for now, only HDD/SSD were supported, device:%v.", devName)
			continue
		}

		expectedQoSData := ioCostQoSConfigs[defaultConfig]
		if expectedQoSData == nil {
			general.Errorf("there is no default io cost QoS Data for devID: %s", devID)
			continue
		}
		err = manager.ApplyIOCostQoSWithAbsolutePath(ioCgroupRootPath, devID, expectedQoSData)
		if err != nil {
			general.Errorf("ApplyIOCostQoSWithAbsolutePath for devID: %s, failed with error: %v",
				devID, err)
		}
	}
}

func applyIOCostConfig(conf *config.Configuration, emitter metrics.MetricEmitter) {
	if !conf.EnableSettingIOCost {
		general.Infof("IOCostControl disabled, skip applyIOCostConfig")
		return
	} else if conf.IOCostQoSConfigFile == "" || conf.IOCostModelConfigFile == "" {
		general.Errorf("IOCostQoSConfigFile or IOCostQoSConfigFile not configured")
		return
	}

	ioCostQoSConfigs := make(map[DevModel]*common.IOCostQoSData)
	err := general.LoadJsonConfig(conf.IOCostQoSConfigFile, &ioCostQoSConfigs)

	if err != nil {
		general.Errorf("load IOCostQoSConfigs failed with error: %v", err)
		return
	}

	ioCostModelConfigs := make(map[DevModel]*common.IOCostModelData)
	err = general.LoadJsonConfig(conf.IOCostModelConfigFile, &ioCostModelConfigs)

	if err != nil {
		general.Errorf("load IOCostModelConfigs failed with error: %v", err)
		return
	}

	var targetDeviceNames []string

	targetDeviceNames, err = getAllDeviceNames()

	if err != nil {
		general.Errorf("get targetDevices with error: %v", err)
		return
	}

	general.Infof("targetDeviceNames: %+v to apply io cost configurations", targetDeviceNames)

	if len(targetDeviceNames) == 0 {
		general.Warningf("empty targetDeviceNames")
		return
	}

	devsIDToModel, err := getDevicesIdToModel(targetDeviceNames)

	if err != nil {
		general.Errorf("getDevicesIdToModel failed with error: %v", err)
		return
	}

	applyIOCostQoSWithDefault(ioCostQoSConfigs, devsIDToModel)
	applyIOCostModel(ioCostModelConfigs, devsIDToModel)
}

func SetIOCost(conf *coreconfig.Configuration,
	_ interface{},
	_ *dynamicconfig.DynamicAgentConfiguration,
	emitter metrics.MetricEmitter,
	metaServer *metaserver.MetaServer) {
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

	// EnableSettingIOCost featuregate.
	if !conf.EnableSettingIOCost {
		general.Infof("SetIOCost disabled.")
		// If EnableSettingIOCost was disabled, we should never enable io.cost.
		initializeOnce.Do(func() {
			disableIOCost(conf)
		})
		return
	}

	if !cgcommon.CheckCgroup2UnifiedMode() {
		general.Infof("not in cgv2 environment, skip IOAsyncTaskFunc")
		return
	}

	initializeOnce.Do(func() {
		disableIOCost(conf)
		applyIOCostConfig(conf, emitter)
	})

	reportDevicesIOCostVrate(emitter)
}
