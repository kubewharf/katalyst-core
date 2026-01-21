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
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
		lastPGFault               = uint64(lastMetricValueFn(consts.MetricMemPgfaultContainer))
		lastPGMajFault            = uint64(lastMetricValueFn(consts.MetricMemPgmajfaultContainer))
		lastOOMCnt                = uint64(lastMetricValueFn(consts.MetricMemOomContainer))
		lastKswapdSteal           = uint64(lastMetricValueFn(consts.MetricMemKswapdstealContainer))
		lastKswapdScan            = uint64(lastMetricValueFn(consts.MetricMemKswapdScanContainer))
		lastDirectSteal           = uint64(lastMetricValueFn(consts.MetricMemDirectStealContainer))
		lastDirectScan            = uint64(lastMetricValueFn(consts.MetricMemDirectScanContainer))
		lastPgsteal               = uint64(lastMetricValueFn(consts.MetricMemPgstealContainer))
		lastPgscan                = uint64(lastMetricValueFn(consts.MetricMemPgscanContainer))
		lastWorkingsetRefaultAnon = uint64(lastMetricValueFn(consts.MetricMemWorkingsetRefaultAnonContainer))
		lastWorkingsetRefaultFile = uint64(lastMetricValueFn(consts.MetricMemWorkingsetRefaultFileContainer))
		lastWorkingsetRefault     = uint64(lastMetricValueFn(consts.MetricMemWorkingsetRefaultContainer))
		lastProactiveReclaim      = uint64(lastMetricValueFn(consts.MetricMemProactiveReclaimContainer))

		curPGFault, curPGMajFault, curOOMCnt, curKswapdsteal, curKswapdscan, curDirectSteal, curDirectScan, curPgsteal, curPgscan, curWorkingsetRefaultAnon, curWorkingsetRefaultFile, curWorkingsetRefault, curProactiveReclaim uint64

		curUpdateTime int64
	)

	if cgStats.CgroupType == "V1" {
		curPGFault = cgStats.V1.Memory.Pgfault
		curPGMajFault = cgStats.V1.Memory.Pgmajfault
		curOOMCnt = cgStats.V1.Memory.BpfMemStat.OomCnt
		curUpdateTime = cgStats.V1.Memory.UpdateTime
		curKswapdsteal = cgStats.V1.Memory.KswapdSteal
		// mem.KswapdSteal
	} else if cgStats.CgroupType == "V2" {
		curPGFault = cgStats.V2.Memory.MemStats.Pgfault
		curPGMajFault = cgStats.V2.Memory.MemStats.Pgmajfault
		curOOMCnt = cgStats.V2.Memory.BpfMemStat.OomCnt
		curUpdateTime = cgStats.V2.Memory.UpdateTime
		curKswapdsteal = cgStats.V2.Memory.MemStats.PgstealKswapd
		curKswapdscan = cgStats.V2.Memory.MemStats.PgscanKswapd
		curDirectSteal = cgStats.V2.Memory.MemStats.PgstealDirect
		curDirectScan = cgStats.V2.Memory.MemStats.PgscanDirect
		curPgsteal = cgStats.V2.Memory.MemStats.Pgsteal
		curPgscan = cgStats.V2.Memory.MemStats.Pgscan
		curWorkingsetRefaultAnon = cgStats.V2.Memory.MemStats.WorkingsetRefaultAnon
		curWorkingsetRefaultFile = cgStats.V2.Memory.MemStats.WorkingsetRefaultFile
		curWorkingsetRefault = cgStats.V2.Memory.MemStats.WorkingsetRefault
		curProactiveReclaim = cgStats.V2.Memory.BpfMemStat.MemReclaimSettingSum
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

	m.setContainerRateMetric(podUID, containerName, consts.MetricMemKswapdstealRateContainer, func() float64 {
		return float64(uint64CounterDelta(lastKswapdSteal, curKswapdsteal))
	}, int64(lastUpdateTimeInSec), curUpdateTime)
	m.setContainerRateMetric(podUID, containerName, consts.MetricMemKswapdScanRateContainer, func() float64 {
		return float64(uint64CounterDelta(lastKswapdScan, curKswapdscan))
	}, int64(lastUpdateTimeInSec), curUpdateTime)
	m.setContainerRateMetric(podUID, containerName, consts.MetricMemDirectStealRateContainer, func() float64 {
		return float64(uint64CounterDelta(lastDirectSteal, curDirectSteal))
	}, int64(lastUpdateTimeInSec), curUpdateTime)
	m.setContainerRateMetric(podUID, containerName, consts.MetricMemDirectScanRateContainer, func() float64 {
		return float64(uint64CounterDelta(lastDirectScan, curDirectScan))
	}, int64(lastUpdateTimeInSec), curUpdateTime)
	m.setContainerRateMetric(podUID, containerName, consts.MetricMemPgstealRateContainer, func() float64 {
		return float64(uint64CounterDelta(lastPgsteal, curPgsteal))
	}, int64(lastUpdateTimeInSec), curUpdateTime)
	m.setContainerRateMetric(podUID, containerName, consts.MetricMemPgscanRateContainer, func() float64 {
		return float64(uint64CounterDelta(lastPgscan, curPgscan))
	}, int64(lastUpdateTimeInSec), curUpdateTime)
	m.setContainerRateMetric(podUID, containerName, consts.MetricMemWorkingsetRefaultRateContainer, func() float64 {
		return float64(uint64CounterDelta(lastWorkingsetRefault, curWorkingsetRefault))
	}, int64(lastUpdateTimeInSec), curUpdateTime)
	m.setContainerRateMetric(podUID, containerName, consts.MetricMemWorkingsetRefaultAnonRateContainer, func() float64 {
		return float64(uint64CounterDelta(lastWorkingsetRefaultAnon, curWorkingsetRefaultAnon))
	}, int64(lastUpdateTimeInSec), curUpdateTime)
	m.setContainerRateMetric(podUID, containerName, consts.MetricMemWorkingsetRefaultFileRateContainer, func() float64 {
		return float64(uint64CounterDelta(lastWorkingsetRefaultFile, curWorkingsetRefaultFile))
	}, int64(lastUpdateTimeInSec), curUpdateTime)
	m.setContainerRateMetric(podUID, containerName, consts.MetricMemProactiveReclaimRateContainer, func() float64 {
		return float64(uint64CounterDelta(lastProactiveReclaim, curProactiveReclaim))
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

// setContainerMbmTotalMetric calcuate the total memory bandwidth usage of a container
func (m *MalachiteMetricsProvisioner) setContainerMbmTotalMetric(podUID, containerName string, resctrlData types.MbmbandData, updateTime *time.Time) {
	// 1. get previous data
	oldResctrlData, ok := m.metricStore.GetByStringIndex(consts.MetricResctrlDataContainer).(types.MbmbandData)
	if !ok || len(oldResctrlData.Mbm) == 0 || len(resctrlData.Mbm) == 0 {
		// No previous or current data, store current for next round and return
		m.metricStore.SetByStringIndex(consts.MetricResctrlDataContainer, resctrlData)
		return
	}

	// 2. calculate the time interval
	prevMetric, err := m.metricStore.GetContainerMetric(podUID, containerName, consts.MetricMbmTotalPsContainer)
	if err != nil || prevMetric.Time == nil {
		// if no previous metric, store current for next round and return
		m.metricStore.SetByStringIndex(consts.MetricResctrlDataContainer, resctrlData)
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricMbmTotalPsContainer,
			metric.MetricData{Value: 0, Time: updateTime})
		m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricMbmlocalPsContainer,
			metric.MetricData{Value: 0, Time: updateTime})
		return
	}
	timeInterval := updateTime.Sub(*prevMetric.Time).Seconds()
	// if previous metric time is after the current update time, do not update
	if timeInterval <= 0 {
		return
	}
	var totalMbmBytesPS, totalLocalBytesPS float64
	for _, l3Mon := range resctrlData.Mbm {
		l3CacheID := l3Mon.ID

		var oldL3Mon *types.MBMItem
		for _, item := range oldResctrlData.Mbm {
			if item.ID == l3CacheID {
				oldL3Mon = &item
				break
			}
		}

		if oldL3Mon == nil {
			continue
		}

		var totalBytesPS float64
		if l3Mon.MBMTotalBytes > oldL3Mon.MBMTotalBytes && oldL3Mon.MBMTotalBytes != 0 {
			totalBytesPS = float64(l3Mon.MBMTotalBytes-oldL3Mon.MBMTotalBytes) / timeInterval
			if totalBytesPS > consts.MaxMBGBps {
				continue
			}
		}
		totalMbmBytesPS += totalBytesPS

		// Calculate local bandwidth
		var localBytesPS float64
		if l3Mon.MBMLocalBytes > oldL3Mon.MBMLocalBytes && oldL3Mon.MBMLocalBytes != 0 {
			localBytesPS = float64(l3Mon.MBMLocalBytes-oldL3Mon.MBMLocalBytes) / timeInterval
			if localBytesPS > consts.MaxMBGBps {
				continue
			}
		}
		totalLocalBytesPS += localBytesPS
		cpuCodeName := m.metricStore.GetByStringIndex(consts.MetricCPUCodeName)
		if codeName, ok := cpuCodeName.(string); ok {
			if strings.Contains(codeName, consts.AMDGenoaArch) {
				// Notice: data adjustments needed due to genoa hardware bug
				totalLocalBytesPS += localBytesPS / 3 * 2
			}
		}
	}
	m.metricStore.SetByStringIndex(consts.MetricResctrlDataContainer, resctrlData)
	m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricMbmTotalPsContainer,
		metric.MetricData{Value: totalMbmBytesPS, Time: updateTime})
	m.metricStore.SetContainerMetric(podUID, containerName, consts.MetricMbmlocalPsContainer,
		metric.MetricData{Value: totalLocalBytesPS, Time: updateTime})
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

func getNumaIDByL3CacheID(l3ID int, cpuPath, nodePath string) (int, error) {
	files, err := ioutil.ReadDir(cpuPath)
	if err != nil {
		return -1, fmt.Errorf("failed to read CPU directory: %v", err)
	}

	for _, file := range files {
		if !strings.HasPrefix(file.Name(), "cpu") {
			continue
		}

		cpuID, err := strconv.Atoi(strings.TrimPrefix(file.Name(), "cpu"))
		if err != nil {
			continue
		}

		cacheIDPath := filepath.Join(cpuPath, file.Name(), consts.SystemL3CacheSubPath)
		cacheIDBytes, err := os.ReadFile(cacheIDPath)
		if err != nil {
			continue
		}

		cacheID, err := strconv.Atoi(strings.TrimSpace(string(cacheIDBytes)))
		if err != nil {
			continue
		}

		if cacheID == l3ID {
			nodeFiles, err := ioutil.ReadDir(nodePath)
			if err != nil {
				return -1, fmt.Errorf("failed to read NUMA node directory: %v", err)
			}

			for _, nodeFile := range nodeFiles {
				if !strings.HasPrefix(nodeFile.Name(), "node") {
					continue
				}

				numaID, err := strconv.Atoi(strings.TrimPrefix(nodeFile.Name(), "node"))
				if err != nil {
					continue
				}

				cpuListPath := filepath.Join(nodePath, nodeFile.Name(), "cpulist")
				cpuListBytes, err := os.ReadFile(cpuListPath)
				if err != nil {
					continue
				}
				cpuList := strings.TrimSpace(string(cpuListBytes))

				if cpuInList(cpuID, cpuList) {
					return numaID, nil
				}
			}

			return -1, fmt.Errorf("NUMA ID not found for CPU %d", cpuID)
		}
	}

	return -1, fmt.Errorf("no matching NUMA ID found for L3 Cache ID: %d", l3ID)
}

func cpuInList(cpu int, cpuList string) bool {
	ranges := strings.Split(cpuList, ",")
	for _, r := range ranges {
		parts := strings.Split(r, "-")
		if len(parts) == 1 {
			if p, err := strconv.Atoi(parts[0]); err == nil && p == cpu {
				return true
			}
		} else if len(parts) == 2 {
			start, err1 := strconv.Atoi(parts[0])
			end, err2 := strconv.Atoi(parts[1])
			if err1 == nil && err2 == nil && cpu >= start && cpu <= end {
				return true
			}
		}
	}
	return false
}

func getCCDMaxMemBandwidth(cpuCode string) uint64 {
	platform, err := metric.GetCPUPlatform(cpuCode)
	if err != nil {
		return 0
	}

	ccdCount := metric.GetPlatformCCDCount(platform)
	socketBW := metric.GetPlatformSocketBandwidth(platform)
	return socketBW / uint64(ccdCount)
}

// findOldL3Cache: Find previous L3 cache data by ID
// returns pointer to previous L3Mon struct for given ID, or nil if not found.
func findOldL3Cache(oldL3Mon *types.L3Monitor, id int) *types.L3Mon {
	if oldL3Mon == nil || len(oldL3Mon.L3Mon) == 0 {
		return nil
	}
	for i := range oldL3Mon.L3Mon {
		if oldL3Mon.L3Mon[i].ID == id {
			return &oldL3Mon.L3Mon[i]
		}
	}
	return nil
}

// calcBytesPerSec: Calculate bytes per second for total/local MBM
// Returns the per-second bandwidth between two counters, or 0 if invalid.
func calcBytesPerSec(current, previous uint64, interval uint64) uint64 {
	if current > previous && previous != 0 && interval > 0 {
		return (current - previous) / interval
	}
	return 0
}

// clampMBMDelta: Clamp MBM delta to MaxMBMStep if overflow detected
func clampMBMDelta(current, previous, oldValue uint64, maxDiff, maxStep uint64) uint64 {
	if current > previous && current-previous > maxDiff {
		return oldValue + maxStep
	}
	return current
}

// getNumaAndMaxBandwidth: Get NUMA ID and max bandwidth for L3 cache
func getNumaAndMaxBandwidth(l3ID int, cpuCodeName string) (int, uint64) {
	numaID, _ := getNumaIDByL3CacheID(l3ID, consts.SystemCpuDir, consts.SystemNodeDir)
	maxBytesPS := getCCDMaxMemBandwidth(cpuCodeName)
	if maxBytesPS == 0 {
		maxBytesPS = consts.MaxMBGBps
	}
	return numaID, maxBytesPS
}

// aggregateNUMABytesPS: Aggregate per-NUMA stats from L3 cache stats
func aggregateNUMABytesPS(l3BytesPS map[int]types.L3CacheBytesPS, cpuCodeName string) map[int]types.L3CacheBytesPS {
	numaBytesPS := make(map[int]types.L3CacheBytesPS)
	for _, mbmStat := range l3BytesPS {
		numaBandwidthStats, exist := numaBytesPS[mbmStat.NumaID]
		if !exist {
			numaBandwidthStats = types.L3CacheBytesPS{NumaID: mbmStat.NumaID}
		}
		numaBandwidthStats.MbmTotalBytesPS += mbmStat.MbmTotalBytesPS
		numaBandwidthStats.MbmLocalBytesPS += mbmStat.MbmLocalBytesPS
		numaBandwidthStats.MbmVictimBytesPS += mbmStat.MbmVictimBytesPS
		numaBandwidthStats.MBMMaxBytesPS = mbmStat.MBMMaxBytesPS
		// AMD-specific aggregation
		if strings.Contains(cpuCodeName, consts.AMDMilanArch) {
			numaBandwidthStats.MbmTotalBytesPS += mbmStat.MbmVictimBytesPS
		}
		if strings.Contains(cpuCodeName, consts.AMDGenoaArch) {
			// Notice: data adjustments needed due to genoa hardware bug
			numaBandwidthStats.MbmTotalBytesPS += mbmStat.MbmLocalBytesPS / 3 * 2
		}
		numaBytesPS[mbmStat.NumaID] = numaBandwidthStats
	}
	return numaBytesPS
}
