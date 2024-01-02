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
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/provisioner/malachite/types"
	"github.com/kubewharf/katalyst-core/pkg/util/metric"
)

// processContainerMemBandwidth handles memory bandwidth (read/write) rate in a period while,
// and it will need the previously collected data to do this
func (m *MalachiteMetricsProvisioner) processContainerMemBandwidth(podUID, containerName string, cgStats *types.MalachiteCgroupInfo, lastUpdateTimeInSec float64) {
	var (
		lastOCRReadDRAMsMetric, _ = m.metricStore.GetContainerMetric(podUID, containerName, consts.MetricOCRReadDRAMsContainer)
		lastIMCWritesMetric, _    = m.metricStore.GetContainerMetric(podUID, containerName, consts.MetricIMCWriteContainer)
		lastStoreAllInsMetric, _  = m.metricStore.GetContainerMetric(podUID, containerName, consts.MetricStoreAllInsContainer)
		lastStoreInsMetric, _     = m.metricStore.GetContainerMetric(podUID, containerName, consts.MetricStoreInsContainer)

		// those value are uint64 type from source
		lastOCRReadDRAMs = uint64(lastOCRReadDRAMsMetric.Value)
		lastIMCWrites    = uint64(lastIMCWritesMetric.Value)
		lastStoreAllIns  = uint64(lastStoreAllInsMetric.Value)
		lastStoreIns     = uint64(lastStoreInsMetric.Value)
	)

	var (
		curOCRReadDRAMs, curIMCWrites, curStoreAllIns, curStoreIns uint64
		curUpdateTimeInSec                                         float64
	)

	if cgStats.CgroupType == "V1" {
		curOCRReadDRAMs = cgStats.V1.Cpu.OcrReadDrams
		curIMCWrites = cgStats.V1.Cpu.ImcWrites
		curStoreAllIns = cgStats.V1.Cpu.StoreAllIns
		curStoreIns = cgStats.V1.Cpu.StoreIns
		curUpdateTimeInSec = float64(cgStats.V1.Cpu.UpdateTime)
	} else if cgStats.CgroupType == "V2" {
		curOCRReadDRAMs = cgStats.V2.Cpu.OcrReadDrams
		curIMCWrites = cgStats.V2.Cpu.ImcWrites
		curStoreAllIns = cgStats.V2.Cpu.StoreAllIns
		curStoreIns = cgStats.V2.Cpu.StoreIns
		curUpdateTimeInSec = float64(cgStats.V2.Cpu.UpdateTime)
	} else {
		return
	}

	// read bandwidth
	m.setContainerRateMetric(podUID, containerName, consts.MetricMemBandwidthReadContainer,
		func() float64 {
			// read megabyte
			return float64(uint64CounterDelta(lastOCRReadDRAMs, curOCRReadDRAMs)) * 64 / (1024 * 1024)
		},
		int64(lastUpdateTimeInSec), int64(curUpdateTimeInSec))

	// write bandwidth
	m.setContainerRateMetric(podUID, containerName, consts.MetricMemBandwidthWriteContainer,
		func() float64 {
			storeAllInsInc := uint64CounterDelta(lastStoreAllIns, curStoreAllIns)
			if storeAllInsInc == 0 {
				return 0
			}

			storeInsInc := uint64CounterDelta(lastStoreIns, curStoreIns)
			imcWritesInc := uint64CounterDelta(lastIMCWrites, curIMCWrites)

			// write megabyte
			return float64(storeInsInc) / float64(storeAllInsInc) / (1024 * 1024) * float64(imcWritesInc) * 64
		},
		int64(lastUpdateTimeInSec), int64(curUpdateTimeInSec))
}

// processContainerCPURelevantRate is used to calculate some container cpu-relevant rates.
// this would be executed before setting the latest values into metricStore.
func (m *MalachiteMetricsProvisioner) processContainerCPURelevantRate(podUID, containerName string, cgStats *types.MalachiteCgroupInfo, lastUpdateTimeInSec float64) {
	lastMetricValueFn := func(metricName string) float64 {
		lastMetric, _ := m.metricStore.GetContainerMetric(podUID, containerName, metricName)
		return lastMetric.Value
	}

	var (
		lastCPUIns       = uint64(lastMetricValueFn(consts.MetricCPUInstructionsContainer))
		lastCPUCycles    = uint64(lastMetricValueFn(consts.MetricCPUCyclesContainer))
		lastCPUNRTht     = uint64(lastMetricValueFn(consts.MetricCPUNrThrottledContainer))
		lastCPUNRPeriod  = uint64(lastMetricValueFn(consts.MetricCPUNrPeriodContainer))
		lastThrottleTime = uint64(lastMetricValueFn(consts.MetricCPUThrottledTimeContainer))
		lastL3CacheMiss  = uint64(lastMetricValueFn(consts.MetricCPUL3CacheMissContainer))

		curCPUIns, curCPUCycles, curCPUNRTht, curCPUNRPeriod, curCPUThrottleTime, curL3CacheMiss uint64

		curUpdateTime int64
	)

	if cgStats.CgroupType == "V1" {
		curCPUIns = cgStats.V1.Cpu.Instructions
		curCPUCycles = cgStats.V1.Cpu.Cycles
		curCPUNRTht = cgStats.V1.Cpu.CPUNrThrottled
		curCPUNRPeriod = cgStats.V1.Cpu.CPUNrPeriods
		curCPUThrottleTime = cgStats.V1.Cpu.CPUThrottledTime / 1000
		if cgStats.V1.Cpu.L3Misses > 0 {
			curL3CacheMiss = cgStats.V1.Cpu.L3Misses
		} else if cgStats.V1.Cpu.OcrReadDrams > 0 {
			curL3CacheMiss = cgStats.V1.Cpu.OcrReadDrams
		}
		curUpdateTime = cgStats.V1.Cpu.UpdateTime
	} else if cgStats.CgroupType == "V2" {
		curCPUIns = cgStats.V2.Cpu.Instructions
		curCPUCycles = cgStats.V2.Cpu.Cycles
		curCPUNRTht = cgStats.V2.Cpu.CPUStats.NrThrottled
		curCPUNRPeriod = cgStats.V2.Cpu.CPUStats.NrPeriods
		curCPUThrottleTime = cgStats.V2.Cpu.CPUStats.ThrottledUsec
		if cgStats.V2.Cpu.L3Misses > 0 {
			curL3CacheMiss = cgStats.V2.Cpu.L3Misses
		} else if cgStats.V2.Cpu.OcrReadDrams > 0 {
			curL3CacheMiss = cgStats.V2.Cpu.OcrReadDrams
		}
		curUpdateTime = cgStats.V2.Cpu.UpdateTime
	} else {
		return
	}
	m.setContainerRateMetric(podUID, containerName, consts.MetricCPUInstructionsRateContainer, func() float64 {
		return float64(uint64CounterDelta(lastCPUIns, curCPUIns))
	}, int64(lastUpdateTimeInSec), curUpdateTime)
	m.setContainerRateMetric(podUID, containerName, consts.MetricCPUCyclesRateContainer, func() float64 {
		return float64(uint64CounterDelta(lastCPUCycles, curCPUCycles))
	}, int64(lastUpdateTimeInSec), curUpdateTime)
	m.setContainerRateMetric(podUID, containerName, consts.MetricCPUNrThrottledRateContainer, func() float64 {
		return float64(uint64CounterDelta(lastCPUNRTht, curCPUNRTht))
	}, int64(lastUpdateTimeInSec), curUpdateTime)
	m.setContainerRateMetric(podUID, containerName, consts.MetricCPUNrPeriodRateContainer, func() float64 {
		return float64(uint64CounterDelta(lastCPUNRPeriod, curCPUNRPeriod))
	}, int64(lastUpdateTimeInSec), curUpdateTime)
	m.setContainerRateMetric(podUID, containerName, consts.MetricCPUThrottledTimeRateContainer, func() float64 {
		return float64(uint64CounterDelta(lastThrottleTime, curCPUThrottleTime))
	}, int64(lastUpdateTimeInSec), curUpdateTime)
	m.setContainerRateMetric(podUID, containerName, consts.MetricCPUL3CacheMissRateContainer, func() float64 {
		return float64(uint64CounterDelta(lastL3CacheMiss, curL3CacheMiss))
	}, int64(lastUpdateTimeInSec), curUpdateTime)
}

func (m *MalachiteMetricsProvisioner) processContainerMemRelevantRate(podUID, containerName string, cgStats *types.MalachiteCgroupInfo, lastUpdateTimeInSec float64) {
	lastMetricValueFn := func(metricName string) float64 {
		lastMetric, _ := m.metricStore.GetContainerMetric(podUID, containerName, metricName)
		return lastMetric.Value
	}

	var (
		lastPGFault    = uint64(lastMetricValueFn(consts.MetricMemPgfaultContainer))
		lastPGMajFault = uint64(lastMetricValueFn(consts.MetricMemPgmajfaultContainer))
		lastOOMCnt     = uint64(lastMetricValueFn(consts.MetricMemOomContainer))

		curPGFault, curPGMajFault, curOOMCnt uint64

		curUpdateTime int64
	)

	if cgStats.CgroupType == "V1" {
		curPGFault = cgStats.V1.Memory.Pgfault
		curPGMajFault = cgStats.V1.Memory.Pgmajfault
		curOOMCnt = cgStats.V1.Memory.BpfMemStat.OomCnt
		curUpdateTime = cgStats.V1.Memory.UpdateTime
	} else if cgStats.CgroupType == "V2" {
		curPGFault = cgStats.V2.Memory.MemStats.Pgmajfault
		curPGMajFault = cgStats.V2.Memory.MemStats.Pgmajfault
		curOOMCnt = cgStats.V2.Memory.BpfMemStat.OomCnt
		curUpdateTime = cgStats.V2.Memory.UpdateTime
	} else {
		return
	}

	m.setContainerRateMetric(podUID, containerName, consts.MetricMemPgfaultRateContainer, func() float64 {
		return float64(uint64CounterDelta(lastPGFault, curPGFault))
	}, int64(lastUpdateTimeInSec), curUpdateTime)
	m.setContainerRateMetric(podUID, containerName, consts.MetricMemPgmajfaultRateContainer, func() float64 {
		return float64(uint64CounterDelta(lastPGMajFault, curPGMajFault))
	}, int64(lastUpdateTimeInSec), curUpdateTime)
	m.setContainerRateMetric(podUID, containerName, consts.MetricMemOomRateContainer, func() float64 {
		return float64(uint64CounterDelta(lastOOMCnt, curOOMCnt))
	}, int64(lastUpdateTimeInSec), curUpdateTime)
}

func (m *MalachiteMetricsProvisioner) processContainerNetRelevantRate(podUID, containerName string, cgStats *types.MalachiteCgroupInfo, lastUpdateTimeInSec float64) {
	lastMetricValueFn := func(metricName string) float64 {
		lastMetric, _ := m.metricStore.GetContainerMetric(podUID, containerName, metricName)
		return lastMetric.Value
	}

	var (
		lastNetTCPRx      = uint64(lastMetricValueFn(consts.MetricNetTcpRecvPacketsContainer))
		lastNetTCPTx      = uint64(lastMetricValueFn(consts.MetricNetTcpSendPacketsContainer))
		lastNetTCPRxBytes = uint64(lastMetricValueFn(consts.MetricNetTcpRecvBytesContainer))
		lastNetTCPTxBytes = uint64(lastMetricValueFn(consts.MetricNetTcpSendBytesContainer))

		netData *types.NetClsCgData
	)

	if cgStats.V1 != nil {
		netData = cgStats.V1.NetCls
	} else if cgStats.V2 != nil {
		netData = cgStats.V2.NetCls
	} else {
		return
	}

	curUpdateTime := netData.UpdateTime
	_curUpdateTime := time.Unix(curUpdateTime, 0)
	updateTimeDiff := float64(curUpdateTime) - lastUpdateTimeInSec
	if updateTimeDiff > 0 {
		m.setContainerRateMetric(podUID, containerName, consts.MetricNetTcpSendBPSContainer, func() float64 {
			return float64(uint64CounterDelta(lastNetTCPTxBytes, netData.BpfNetData.NetTCPTxBytes))
		}, int64(lastUpdateTimeInSec), curUpdateTime)
		m.setContainerRateMetric(podUID, containerName, consts.MetricNetTcpRecvBPSContainer, func() float64 {
			return float64(uint64CounterDelta(lastNetTCPRxBytes, netData.BpfNetData.NetTCPRxBytes))
		}, int64(lastUpdateTimeInSec), curUpdateTime)
		m.setContainerRateMetric(podUID, containerName, consts.MetricNetTcpSendPpsContainer, func() float64 {
			return float64(uint64CounterDelta(lastNetTCPTx, netData.BpfNetData.NetTCPTx))
		}, int64(lastUpdateTimeInSec), curUpdateTime)
		m.setContainerRateMetric(podUID, containerName, consts.MetricNetTcpRecvPpsContainer, func() float64 {
			return float64(uint64CounterDelta(lastNetTCPRx, netData.BpfNetData.NetTCPRx))
		}, int64(lastUpdateTimeInSec), curUpdateTime)
	} else {
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricNetTcpSendBPSContainer, metric.MetricData{
			Value: float64(uint64CounterDelta(netData.OldBpfNetData.NetTCPTxBytes, netData.BpfNetData.NetTCPTxBytes)) / defaultMetricUpdateInterval,
			Time:  &_curUpdateTime,
		})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricNetTcpRecvBPSContainer, metric.MetricData{
			Value: float64(uint64CounterDelta(netData.OldBpfNetData.NetTCPRxBytes, netData.BpfNetData.NetTCPRxBytes)) / defaultMetricUpdateInterval,
			Time:  &_curUpdateTime,
		})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricNetTcpSendPpsContainer, metric.MetricData{
			Value: float64(uint64CounterDelta(netData.OldBpfNetData.NetTCPTx, netData.BpfNetData.NetTCPTx)) / defaultMetricUpdateInterval,
			Time:  &_curUpdateTime,
		})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricNetTcpRecvPpsContainer, metric.MetricData{
			Value: float64(uint64CounterDelta(netData.OldBpfNetData.NetTCPRx, netData.BpfNetData.NetTCPRx)) / defaultMetricUpdateInterval,
			Time:  &_curUpdateTime,
		})
	}
}

// setContainerRateMetric is used to set rate metric in container level.
// This method will check if the metric is really updated, and decide weather to update metric in metricStore.
// The method could help avoid lots of meaningless "zero" value.
func (m *MalachiteMetricsProvisioner) setContainerRateMetric(podUID, containerName, targetMetricName string, deltaValueFunc func() float64, lastUpdateTime, curUpdateTime int64) {
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

// uint64CounterDelta calculate the delta between two uint64 counters
// Sometimes the counter value would go beyond the MaxUint64. In that case,
// negative counter delta would happen, and the data is not incorrect.
func uint64CounterDelta(previous, current uint64) uint64 {
	if current >= previous {
		return current - previous
	}

	// Return 0 when previous > current, because we may not be able to make sure
	// the upper bound for each counter.
	return 0
}
