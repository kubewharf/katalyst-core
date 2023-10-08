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

package malachite

// for those metrics need extra calculation logic,
// we will put them in a separate file here
import (
	"time"

	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/malachite/types"
	"github.com/kubewharf/katalyst-core/pkg/util/metric"
)

// processContainerMemBandwidth handles memory bandwidth (read/write) rate in a period while,
// and it will need the previously collected data to do this
func (m *MalachiteMetricsFetcher) processContainerMemBandwidth(podUID, containerName string, cgStats *types.MalachiteCgroupInfo, lastUpdateTimeInSec float64) {
	var (
		lastOCRReadDRAMs, _ = m.metricStore.GetContainerMetric(podUID, containerName, consts.MetricOCRReadDRAMsContainer)
		lastIMCWrites, _    = m.metricStore.GetContainerMetric(podUID, containerName, consts.MetricIMCWriteContainer)
		lastStoreAllIns, _  = m.metricStore.GetContainerMetric(podUID, containerName, consts.MetricStoreAllInsContainer)
		lastStoreIns, _     = m.metricStore.GetContainerMetric(podUID, containerName, consts.MetricStoreInsContainer)
	)

	var curOCRReadDRAMs, curIMCWrites, curStoreAllIns, curStoreIns, curUpdateTimeInSec float64
	if cgStats.CgroupType == "V1" {
		curOCRReadDRAMs = float64(cgStats.V1.Cpu.OCRReadDRAMs)
		curIMCWrites = float64(cgStats.V1.Cpu.IMCWrites)
		curStoreAllIns = float64(cgStats.V1.Cpu.StoreAllInstructions)
		curStoreIns = float64(cgStats.V1.Cpu.StoreInstructions)
		curUpdateTimeInSec = float64(cgStats.V1.Cpu.UpdateTime)
	} else if cgStats.CgroupType == "V2" {
		curOCRReadDRAMs = float64(cgStats.V2.Cpu.OCRReadDRAMs)
		curIMCWrites = float64(cgStats.V2.Cpu.IMCWrites)
		curStoreAllIns = float64(cgStats.V2.Cpu.StoreAllInstructions)
		curStoreIns = float64(cgStats.V2.Cpu.StoreInstructions)
		curUpdateTimeInSec = float64(cgStats.V2.Cpu.UpdateTime)
	}

	// read bandwidth
	m.setContainerRateMetric(podUID, containerName, consts.MetricMemBandwidthReadContainer,
		func() float64 {
			// read megabyte
			return (curOCRReadDRAMs - lastOCRReadDRAMs.Value) * 64 / (1024 * 1024)
		},
		int64(lastUpdateTimeInSec), int64(curUpdateTimeInSec))

	// write bandwidth
	m.setContainerRateMetric(podUID, containerName, consts.MetricMemBandwidthWriteContainer,
		func() float64 {
			// write megabyte
			return (curStoreIns - lastStoreIns.Value) * (curIMCWrites - lastIMCWrites.Value) * 64 / (curStoreAllIns - lastStoreAllIns.Value) / (1024 * 1024)
		},
		int64(lastUpdateTimeInSec), int64(curUpdateTimeInSec))
}

// setContainerRateMetric is used to set rate metric in container level.
// This method will check if the metric is really updated, and decide weather to update metric in metricStore.
// The method could help avoid lots of meaningless "zero" value.
func (m *MalachiteMetricsFetcher) setContainerRateMetric(podUID, containerName, targetMetricName string, deltaValueFunc func() float64, lastUpdateTime, curUpdateTime int64) {
	timeDeltaInSec := curUpdateTime - lastUpdateTime
	if lastUpdateTime == 0 || timeDeltaInSec <= 0 {
		// Return directly when the following situations happen:
		// 1. lastUpdateTime == 0, which means no previous data.
		// 2. timeDeltaInSec == 0, which means the metric is not updated,
		//	this is originated from the sampling lag between katalyst-core and malachite(data source)
		// 3. timeDeltaInSec < 0, this is illegal and unlikely to happen.
		return
	}

	// TODO this will duplicate "updateTime" a lot.
	// But to my knowledge, the cost could be acceptable.
	updateTime := time.Unix(curUpdateTime, 0)
	m.metricStore.SetContainerMetric(podUID, containerName, targetMetricName,
		metric.MetricData{Value: deltaValueFunc() / float64(timeDeltaInSec), Time: &updateTime})
}
