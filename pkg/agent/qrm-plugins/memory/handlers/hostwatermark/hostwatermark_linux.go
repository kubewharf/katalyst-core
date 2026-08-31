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
	"fmt"

	"k8s.io/apimachinery/pkg/util/errors"

	memconsts "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/memory/consts"
	coreconfig "github.com/kubewharf/katalyst-core/pkg/config"
	dynamicconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	dynamicqrm "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/adminqos/qrm"
	coreconsts "github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/helper"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	procfsm "github.com/kubewharf/katalyst-core/pkg/util/procfs/manager"
)

// watermarkScaleFactorPath is the procfs path for vm.watermark_scale_factor.
// Declared as a var so tests can redirect it to a temp file.
var (
	watermarkScaleFactorPath = procfsm.VMWatermarkScaleFactorPath
	watermarkBoostFactorPath = procfsm.VMWatermarkBoostFactorPath
	extFragThresholdPath     = procfsm.VMExtFragThresholdPath
)

// SetHostWatermark tunes host vm watermark sysctls.
// Currently supports:
//   - /proc/sys/vm/watermark_scale_factor
//   - /proc/sys/vm/watermark_boost_factor
//   - /proc/sys/vm/ext_frag_threshold
//
// The target value is determined by the following precedence:
//  1. If VMWatermarkScaleFactor is explicitly configured, use it directly.
//  2. Otherwise, auto-calculate from ReservedKswapdWatermarkGB and NUMA memory stats.
//
// Regardless of the source, the target is always adjusted for huge pages via
// adjustWatermarkForHugePages. Clamping is only applied to auto-calculated values;
// explicitly configured values are respected as-is.
func SetHostWatermark(conf *coreconfig.Configuration,
	_ interface{}, dynamicConf *dynamicconfig.DynamicAgentConfiguration,
	emitter metrics.MetricEmitter, metaServer *metaserver.MetaServer,
) {
	general.Infof("called")

	var errList []error
	defer func() {
		_ = general.UpdateHealthzStateByError(memconsts.SetHostWatermark, errors.NewAggregate(errList))
	}()

	if conf == nil {
		general.Errorf("nil conf")
		return
	} else if emitter == nil {
		general.Errorf("nil emitter")
		return
	}

	hostWatermarkConf := getHostWatermarkConfiguration(dynamicConf)
	if hostWatermarkConf == nil {
		general.Infof("dynamic hostwatermark configuration not found")
		return
	}
	if !hostWatermarkConf.EnableHostWatermark {
		general.Infof("EnableHostWatermark disabled")
		return
	}
	general.Infof("EnableHostWatermark enabled")

	if err := setVMWatermarkScaleFactor(hostWatermarkConf, emitter, metaServer); err != nil {
		errList = append(errList, err)
	}

	if err := setVMWatermarkBoostFactor(hostWatermarkConf, emitter); err != nil {
		errList = append(errList, err)
	}

	if err := setVMExtFragThreshold(hostWatermarkConf, emitter); err != nil {
		errList = append(errList, err)
	}
}

func setVMWatermarkScaleFactor(conf *dynamicqrm.HostWatermarkConfiguration, emitter metrics.MetricEmitter, metaServer *metaserver.MetaServer) error {
	target, err := determineTargetWatermarkScaleFactor(conf, emitter, metaServer)
	if err != nil {
		general.Errorf("determine watermark_scale_factor failed: %v", err)
		return err
	} else if target == 0 {
		general.Infof("skip setting vm.watermark_scale_factor (no target specified)")
		return nil
	}

	target = adjustWatermarkForHugePages(target)

	if conf.VMWatermarkScaleFactor == 0 {
		target = clampWatermarkScaleFactor(target)
	}

	err = procfsm.ApplyVMWatermarkScaleFactorAtPath(watermarkScaleFactorPath, target)
	if err != nil {
		general.Errorf("set watermark_scale_factor failed: %v", err)
		return err
	}

	newVal, err := general.ReadInt64FromFile(watermarkScaleFactorPath)
	if err != nil {
		general.Errorf("read watermark_scale_factor after apply failed: %v", err)
		return err
	}
	_ = emitter.StoreInt64(metricNameVMWatermarkScaleFactor, newVal, metrics.MetricTypeNameRaw)

	return nil
}

// setVMWatermarkBoostFactor tunes vm.watermark_boost_factor. It allows the 0-value to be set explicitly, meaning no boost.
func setVMWatermarkBoostFactor(conf *dynamicqrm.HostWatermarkConfiguration, emitter metrics.MetricEmitter) error {
	if err := procfsm.ApplyVMWatermarkBoostFactorAtPath(watermarkBoostFactorPath, int64(conf.VMWatermarkBoostFactor)); err != nil {
		general.Errorf("set watermark_boost_factor failed: %v", err)
		return err
	}

	newVal, err := general.ReadInt64FromFile(watermarkBoostFactorPath)
	if err != nil {
		general.Errorf("read watermark_boost_factor after apply failed: %v", err)
		return err
	}
	_ = emitter.StoreInt64(metricNameVMWatermarkBoostFactor, newVal, metrics.MetricTypeNameRaw)

	return nil
}

// setVMExtFragThreshold tunes vm.extfrag_threshold. It allows the 0-value to be set explicitly, meaning allow compaction more easily.
func setVMExtFragThreshold(conf *dynamicqrm.HostWatermarkConfiguration, emitter metrics.MetricEmitter) error {
	if err := procfsm.ApplyVMExtFragThresholdAtPath(extFragThresholdPath, int64(conf.VMExtFragThreshold)); err != nil {
		general.Errorf("set extfrag_threshold failed: %v", err)
		return err
	}

	newVal, err := general.ReadInt64FromFile(extFragThresholdPath)
	if err != nil {
		general.Errorf("read extfrag_threshold after apply failed: %v", err)
		return err
	}
	_ = emitter.StoreInt64(metricNameVMExtFragThreshold, newVal, metrics.MetricTypeNameRaw)

	return nil
}

// clampWatermarkScaleFactor clamps target to [watermarkScaleFactorMin, watermarkScaleFactorMax].
func clampWatermarkScaleFactor(target int64) int64 {
	return int64(general.Clamp(float64(target), watermarkScaleFactorMin, watermarkScaleFactorMax))
}

func getHostWatermarkConfiguration(dynamicConf *dynamicconfig.DynamicAgentConfiguration) *dynamicqrm.HostWatermarkConfiguration {
	if dynamicConf == nil {
		return nil
	}
	conf := dynamicConf.GetDynamicConfiguration()
	if conf == nil {
		return nil
	}
	return conf.HostWatermarkConfiguration
}

// determineTargetWatermarkScaleFactor returns the target vm.watermark_scale_factor value.
// If VMWatermarkScaleFactor is explicitly configured, it is returned directly.
// Otherwise, the value is auto-calculated as ceil(reservedBytes / totalBytes * 10000)
// based on ReservedKswapdWatermarkGB and the first NUMA node's memory stats.
func determineTargetWatermarkScaleFactor(conf *dynamicqrm.HostWatermarkConfiguration, emitter metrics.MetricEmitter, metaServer *metaserver.MetaServer) (int64, error) {
	if conf == nil {
		return 0, nil
	}
	if conf.VMWatermarkScaleFactor != 0 {
		return int64(conf.VMWatermarkScaleFactor), nil
	}

	if conf.ReservedKswapdWatermarkGB == 0 {
		return 0, nil
	}

	if metaServer == nil {
		return 0, fmt.Errorf("metaServer is nil")
	} else if metaServer.MetricsFetcher == nil {
		return 0, fmt.Errorf("metaServer.MetricsFetcher is nil")
	} else if metaServer.CPUDetails == nil {
		return 0, fmt.Errorf("metaServer.CPUDetails is nil")
	}

	numaIDs := metaServer.CPUDetails.NUMANodes().ToSliceInt()
	if len(numaIDs) == 0 {
		return 0, fmt.Errorf("empty NUMA nodes")
	}
	numaID := numaIDs[0]

	totalWithTime, err := helper.GetNumaMetricWithTime(metaServer.MetricsFetcher, emitter, coreconsts.MetricMemTotalNuma, numaID)
	if err != nil {
		return 0, err
	}
	totalBytes := uint64(totalWithTime.Value)
	if totalBytes == 0 {
		return 0, fmt.Errorf("numa %d mem.total is 0", numaID)
	}

	reservedBytes := conf.ReservedKswapdWatermarkGB << 30
	scale := (reservedBytes*10000 + totalBytes - 1) / totalBytes
	if scale > 10000 {
		scale = 10000
	}
	if scale < 1 {
		scale = 1
	}

	general.Infof("auto-calc vm.watermark_scale_factor: numaID=%d total=%s reserved=%dGB scale=%d",
		numaID, general.FormatMemoryQuantity(float64(totalBytes)), conf.ReservedKswapdWatermarkGB, scale)
	return int64(scale), nil
}

// adjustWatermarkForHugePages scales down the watermark proportionally to account for
// memory occupied by huge pages. Since huge-page memory is not reclaimable by kswapd,
// the watermark should be based on the reclaimable portion: (MemTotal - HugePagesTotal * Hugepagesize).
// The adjusted value is: (MemTotal - HugePagesMem) * originalWatermark / MemTotal.
func adjustWatermarkForHugePages(originalWatermark int64) int64 {
	if originalWatermark <= 0 {
		return 0
	}

	newWatermark := originalWatermark

	memInfo, err := procfsm.GetMemInfo()
	if err != nil {
		general.Warningf("get mem info failed: %v", err)
		return newWatermark
	}

	if memInfo.MemTotal != nil && memInfo.HugePagesTotal != nil && memInfo.Hugepagesize != nil {
		total := *memInfo.MemTotal
		hugePages := (*memInfo.HugePagesTotal) * (*memInfo.Hugepagesize)
		newWatermark = int64((total - hugePages) * uint64(originalWatermark) / total)
		general.Infof("total=%d hugePages=%d originalWatermark=%d newWatermark=%d",
			total, hugePages, originalWatermark, newWatermark)
	} else {
		general.Infof("mem info incomplete, skip adjustment (memTotal=%v hugePagesTotal=%v hugepagesize=%v)",
			memInfo.MemTotal, memInfo.HugePagesTotal, memInfo.Hugepagesize)
	}

	return newWatermark
}
