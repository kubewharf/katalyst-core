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

package monitor

import (
	"fmt"
	"sort"

	msrutils "github.com/kubewharf/katalyst-core/pkg/mbw/utils/msr"
	"github.com/kubewharf/katalyst-core/pkg/mbw/utils/rdt"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

type MB_CONTROL_ACTION int

const (
	BW_LEN                = 11
	MBA_COS_MAX           = 16 // AMD supports up to 16 cos per CCD, while Intel supports up to 16 cos per socket
	L3QOS_BW_CONTROL_BASE = 0xc0000200

	MEMORY_BANDWIDTH_THRESHOLD_PHYSICAL_NUMA = 140000 // fixed threshold 140GBps each physical numa node
	MEMORY_BANDWIDTH_INCREASE_STEP           = 10000  // the incremental rate each step
	MEMORY_BANDWIDTH_DECREASE_STEP           = 5000   // the decremental rate each step
	MEMORY_BANDWIDTH_INCREASE_MIN            = 256    // the minimum incremental value
	MEMORY_BANDWIDTH_DECREASE_MIN            = 512    // the minimum decremental value

	MEMORY_BANDWIDTH_PHYSICAL_NUMA_SWEETPOINT      = 0.95
	MEMORY_BANDWIDTH_PHYSICAL_NUMA_PAINPOINT       = 1.05
	MEMORY_BANDWIDTH_PHYSICAL_NUMA_UNTHROTTLEPOINT = 0.85

	INSTANCE_HIGH_PRIORITY = 1
	INSTANCE_LOW_PRIORITY  = 2

	MEMORY_BANDWIDTH_CONTROL_RAISE      MB_CONTROL_ACTION = 1
	MEMORY_BANDWIDTH_CONTROL_REDUCE     MB_CONTROL_ACTION = 2
	MEMORY_BANDWIDTH_CONTROL_UNTHROTTLE MB_CONTROL_ACTION = 3
)

func checkMSR(core uint32, msr int64, target uint64) error {
	ret, err := msrutils.ReadMSR(core, msr)
	if err != nil {
		return fmt.Errorf("failed to read msr - %v", err)
	}

	if ret != target {
		return fmt.Errorf("failed to set msr %d on core %d to the expected value %d from %d",
			msr, core, target, ret)
	}

	return nil
}

func writeMBAMSR(core uint32, msr int64, val uint64) error {
	if wErr := msrutils.WriteMSR(core, msr, val); wErr != nil {
		general.Errorf("failed to write %d to msr %d on core %d - %v", val, msr, core, wErr)
		return wErr
	}

	if cErr := checkMSR(core, msr, val); cErr != nil {
		general.Errorf("failed to bind pqr assoc - %v", cErr)
		return cErr
	}

	return nil
}

func bindCorePQRAssoc(core uint32, rmid, cos uint64) error {
	rmidBase := rmid & 0b1111111111
	cosBase := cos & 0b11111111111111111111111111111111
	target := (cosBase << 32) | rmidBase
	return writeMBAMSR(core, rdt.PQR_ASSOC, target)
}

// bind a set of cores to a qos doamin (represented by a cos) by setting each core's pqr_assoc register to the cos value
// when throtting these cores, just set any one's bwControl register to update the cos
func (m MBMonitor) BindCoresToCos(cos int, cores ...int) error {
	for _, core := range cores {
		// assign a unique rmid to each core
		// note: we can also bind all cores to the same rmid, then
		// each core's MB collected by rdt represents the whole cos domain (i.e. a CCD)
		if err := bindCorePQRAssoc(uint32(core), uint64(m.Controller.RMIDMap[core]), uint64(cos)); err != nil {
			return fmt.Errorf("faild to bind core %d to cos %d - %v", core, cos, err)
		}
	}

	return nil
}

// set the mba cos on a target die (i.e. a AMD CCD)
func (m MBMonitor) ConfigCCDMBACos(ccd, cos, ul int, max uint64) error {
	bwControl := L3QOS_BW_CONTROL_BASE + cos
	maskA := uint64((1 << BW_LEN) - 1)
	a := max * 8 / 1000 // bandwidth is expressed in 1/8GBps increments
	a1 := a & maskA
	b1 := ul & 1

	if a >= maskA {
		a1 = 0
		b1 = 1
		max = 0
	}

	shiftedB := b1 << BW_LEN
	targetVal := a1 | uint64(shiftedB)
	// use the first core on each CCD when throttling the CCD as a whole
	core := m.SysInfo.CCDMap[ccd][0]
	if err := writeMBAMSR(uint32(core), int64(bwControl), targetVal); err != nil {
		general.Errorf("failed to set core mba cos on core %d - %v", core, err)
		return err
	}

	return nil
}

// get the index of the first free cos from a CCD
func (m MBMonitor) PickFirstAvailableCos(ccd int) int {
	if ccd < 0 || ccd >= m.NumCCDs {
		general.Errorf("invalid ccd %d - out of range [0, %d]", ccd, m.NumCCDs-1)
		return -1
	}

	for i, entry := range m.Controller.CCDCosMap[ccd] {
		if !entry.Used {
			// return the index of the cos
			return i
		}
	}

	return -1
}

func (m MBMonitor) ResetMBACos() error {
	for i := 0; i < m.MachineInfo.NumCores; i++ {
		bindCorePQRAssoc(uint32(i), 0, 0)
	}

	var err error = nil
	for ccd := 0; ccd < m.NumCCDs; ccd++ {
		for cos := 0; cos < MBA_COS_MAX; cos++ {
			// use the first core on each CCD when throttling the CCD as a whole
			if err = m.ConfigCCDMBACos(ccd, cos, 1, 0); err != nil {
				return err
			}
		}
	}

	general.Infof("reset MBA cos on all cores")
	return err
}

func (m *MBMonitor) AddInstance(instances ...Instance) error {
	for _, ins := range instances {
		var err error = nil
		ins.CosTracking = make(map[int]int, m.SysInfo.NumCCDs)
		for node := range ins.Nodes {
			for _, ccd := range m.SysInfo.NumaMap[node] {
				cos := m.PickFirstAvailableCos(ccd)
				if cos == -1 {
					general.Errorf("failed to find a free cos on ccd %d and node %d for instance %s", ccd, node, ins.Name)
					err = fmt.Errorf("failed to find a free cos on ccd %d and node %d for instance %s", ccd, node, ins.Name)
					break
				}
				general.Infof("find a free cos %d on ccd %d for instance %s", cos, ccd, ins.Name)

				err = m.ConfigCCDMBACos(ccd, cos, 1, 0)
				if err != nil {
					break
				}

				// bind all cores on this CCD to one cos
				// this is the case that a socket container would occupy one or multiple CCDs on AMD
				// each core on a die uses a unique rmid
				err = m.BindCoresToCos(cos, m.SysInfo.CCDMap[ccd]...)
				if err != nil {
					break
				}

				m.Controller.CCDCosMap[ccd][cos].Used = true
				m.Controller.CCDCosMap[ccd][cos].InsName = ins.Name
				ins.CosTracking[ccd] = cos
			}

			if err == nil {
				general.Infof("init instance %s on node %d", ins.Name, node)
			}
		}

		if err != nil {
			general.Errorf("failed to add instance %s - %v", ins.Name, err)
		} else {
			m.Controller.Instances = append(m.Controller.Instances, ins)
			general.Infof("instance %s added successfully", ins.Name)
		}
	}

	return nil
}

func (m *MBMonitor) AdjustNumaMB(node int, avgMB, quota uint64, action MB_CONTROL_ACTION) error {
	m.MemoryBandwidth.PackageLocker.RLock()
	defer m.MemoryBandwidth.PackageLocker.RUnlock()

	ccdMB := m.MemoryBandwidth.Numas[node].Total / uint64(len(m.SysInfo.NumaMap[node]))
	// TODO: adjust the quota distribution based on CPU usage
	ccdQuota := quota / uint64(len(m.SysInfo.NumaMap[node]))

	if m.MemoryBandwidth.Numas[node].Total <=
		uint64(float64(avgMB)*MEMORY_BANDWIDTH_PHYSICAL_NUMA_SWEETPOINT) &&
		action != MEMORY_BANDWIDTH_CONTROL_UNTHROTTLE {
		action = MEMORY_BANDWIDTH_CONTROL_UNTHROTTLE
	}

	for _, ccd := range m.SysInfo.NumaMap[node] {
		instances := m.Controller.GetInstancesByCCD(ccd)
		if len(instances) == 0 {
			general.Infof("no instance running on ccd %d", ccd)
			continue
		}

		cos := instances[0].CosTracking[ccd]
		entry := m.Controller.CCDCosMap[ccd][cos]
		ul := 0
		// ingore the hybird deployment for now
		if entry.Used {
			switch action {
			case MEMORY_BANDWIDTH_CONTROL_RAISE:
				if entry.Cap == 0 {
					entry.Cap = ccdMB + ccdQuota
				} else {
					entry.Cap += ccdQuota
				}
			case MEMORY_BANDWIDTH_CONTROL_REDUCE:
				// workaround based on the experience: 1) MBA setting = (current_MB - quota) x 1.25
				if entry.Cap == 0 {
					entry.Cap = (ccdMB - ccdQuota) * 5 / 4
				} else {
					entry.Cap -= ccdQuota / 2
				}
			case MEMORY_BANDWIDTH_CONTROL_UNTHROTTLE:
				entry.Cap = 0
				ul = 1
			}

			err := m.ConfigCCDMBACos(ccd, cos, ul, entry.Cap)
			if err != nil {
				general.Errorf("failed to throttle ccd %d to the target MB %d from %d - %v", ccd, entry.Cap, ccdMB, err)
				return err
			}

			// reassign the entry because the myMap["key"] is not "addressable"
			m.Controller.CCDCosMap[ccd][cos] = entry

			if action == MEMORY_BANDWIDTH_CONTROL_REDUCE || action == MEMORY_BANDWIDTH_CONTROL_RAISE {
				m.Controller.NumaThrottled[node] = true
				general.Infof("adjust ccd %d to target throttling %d, current MB: %d", ccd, entry.Cap, ccdMB)
			} else {
				m.Controller.NumaThrottled[node] = false
				general.Infof("unthrottle ccd %d", ccd)
			}
		}
	}

	return nil
}

// AdjustMemoryBandwidth control the instances on a physical NUMA as follows:
// a. if the total MB on this physical NUMA beyonds the threshold:
// 1) throttle all low priority instances first and set their MB to the min directly;
// 2) throttle each high priority instances based on its real-time bandwidth,
// the more it consumes now the more intensive we'd throttle;
// b. if the total MB on this physical NUMA is below the threshold:
// 1) release the throttling of high priority instances first
// PS: we only support the case in which the CCDs align with Numas for now
func (m *MBMonitor) AdjustMemoryBandwidth() {
	if m.MonitorOnly {
		// no need to adjust the memory bandwidth with this option
		return
	}

	for i, p := range m.MemoryBandwidth.Packages {
		highPrisNumas, lowPrisNumas := getDifferentiatedNumas(m.Controller.Instances, m.SysInfo.PackageMap[i])
		if p.Total >= uint64(float64(m.Controller.MBThresholdPerNUMA)*m.Controller.PainPoint) {
			general.Infof("throttle the instances on package %d due to excessive mb consumption %d", i, p.Total)
			// mb throttling
			if len(highPrisNumas) > 0 {
				if len(lowPrisNumas) > 0 && !m.Controller.NumaLowPriThrottled[i] {
					// TODO: throttle the low priority instances first, and set their bandwidth to the min
					general.Infof("throttle the low priority instances on package %d first", i)
					m.Controller.NumaLowPriThrottled[i] = true
				} else {
					// throttle high priority instances if all low priority instances have been throttled already
					quota := uint64(float64(p.Total - m.Controller.MBThresholdPerNUMA))
					if quota < m.Controller.DecreaseStep {
						quota = m.Controller.DecreaseStep
					}
					general.Infof("throttle the high priority instances on package %d, %d MBps should be reduced", i, quota)

					// calculated the aggretated MB of all numas on this package
					var aggregatedMB uint64 = 0
					for _, node := range highPrisNumas {
						aggregatedMB += m.MemoryBandwidth.Numas[node].Total
					}

					if aggregatedMB > m.Controller.MBThresholdPerNUMA {
						aggregatedMB = m.Controller.MBThresholdPerNUMA
					}
					avgMB := aggregatedMB / uint64(len(highPrisNumas))

					// throttle the MB on high-throughput Numa first
					// TODO: consider the scenarios that multiple NUMAs belong to the same instance
					sortedHighPrisNumas := m.SortNumaDescend(highPrisNumas)
					for _, node := range sortedHighPrisNumas {
						weight := float64(m.MemoryBandwidth.Numas[node].Total) / float64(p.Total)
						deduction := uint64(float64(quota) * weight)
						if deduction <= MEMORY_BANDWIDTH_DECREASE_MIN {
							general.Infof("the deduction is too small to apply: %d", deduction)
							continue
						}

						if m.MemoryBandwidth.Numas[node].Total <= uint64(float64(avgMB)*MEMORY_BANDWIDTH_PHYSICAL_NUMA_SWEETPOINT) {
							// its consumption is too low to throttle
							general.Infof("its consumption is too low to throttle - total: %d, avgMB: %d, deduction: %d", m.MemoryBandwidth.Numas[node].Total, avgMB, deduction)
							continue
						}

						/*
							if deduction > quota {
								deduction = quota
								quota = 0
							} else {
								quota -= deduction
							}
						*/

						general.Infof("reduce the MB allocation of the high priority instance on numa %d by %d",
							node, deduction)
						// throttle the node
						if err := m.AdjustNumaMB(node, avgMB, deduction, MEMORY_BANDWIDTH_CONTROL_REDUCE); err == nil && !m.Controller.NumaThrottled[node] {
							m.Controller.NumaThrottled[node] = true
						}

						if quota <= 0 {
							break
						}
					}
				}
			}
			// no performance isolation needed if all instances are low-priority
		} else if p.Total <= uint64(float64(m.Controller.MBThresholdPerNUMA)*m.Controller.SweetPoint) {
			if p.Total <= uint64(float64(m.Controller.MBThresholdPerNUMA)*m.Controller.UnthrottlePoint) {
				// fully untrottle the numas
				for _, node := range highPrisNumas {
					if m.Controller.NumaThrottled[node] {
						if err := m.AdjustNumaMB(node, 0, 0, MEMORY_BANDWIDTH_CONTROL_UNTHROTTLE); err == nil {
							m.Controller.NumaThrottled[node] = false
							general.Infof("unthrottle the memory-bandwidth on package %d", node)
						}
					}
				}
			} else {
				// release throttling
				quota := uint64(float64(m.Controller.MBThresholdPerNUMA-p.Total) * 0.5)
				if quota < m.Controller.IncreaseStep {
					quota = m.Controller.IncreaseStep
				}

				// calculated the aggretated MB of all throttled numas on this package
				var aggregatedMBThrottled uint64 = 0
				for _, node := range highPrisNumas {
					if m.Controller.NumaThrottled[node] {
						aggregatedMBThrottled += m.MemoryBandwidth.Numas[node].Total
					}
				}

				if aggregatedMBThrottled < m.Controller.MBThresholdPerNUMA {
					aggregatedMBThrottled = m.Controller.MBThresholdPerNUMA
				}
				avgMB := aggregatedMBThrottled / uint64(len(highPrisNumas))

				// raise the cap of throttled numas proportionally
				// TODO: consider the scenarios that multiple NUMAs belong to the same instance
				sortedHighPrisNumas := m.SortNumaAscend(highPrisNumas)
				for _, node := range sortedHighPrisNumas {
					if m.Controller.NumaThrottled[node] {
						weight := float64(m.MemoryBandwidth.Numas[node].Total) / float64(p.Total)
						raise := uint64(float64(quota) * (1 - weight))
						if raise <= MEMORY_BANDWIDTH_INCREASE_MIN {
							general.Infof("the increase is too small to apply: %d", raise)
							continue
						}
						// increase the cap of the throttled node
						if err := m.AdjustNumaMB(node, avgMB, raise, MEMORY_BANDWIDTH_CONTROL_RAISE); err == nil {
							general.Infof("increase the MB allocation of the throttled instance on numa %d by %d",
								node, raise)
						}
					}
				}
			}
		}
	}
}

func (m MBMonitor) SortNumaDescend(numas []int) []int {
	nodes := make([]int, len(numas))
	_ = copy(nodes, numas)

	sort.SliceStable(nodes, func(i, j int) bool {
		// sort the numa node in the descending order of bandwidth
		return m.MemoryBandwidth.Numas[nodes[i]].Total > m.MemoryBandwidth.Numas[nodes[j]].Total
	})

	return nodes
}

func (m MBMonitor) SortNumaAscend(numas []int) []int {
	nodes := make([]int, len(numas))
	_ = copy(nodes, numas)

	sort.SliceStable(nodes, func(i, j int) bool {
		// sort the numa node in the ascending order of bandwidth
		return m.MemoryBandwidth.Numas[nodes[i]].Total < m.MemoryBandwidth.Numas[nodes[j]].Total
	})

	return nodes
}

func getDifferentiatedNumas(ins []Instance, numas []int) ([]int, []int) {
	highPriNumas := make([]int, 0)
	lowPriNumas := make([]int, 0)

	for i := range ins {
		for _, numa := range numas {
			if _, ok := ins[i].Nodes[numa]; ok {
				if ins[i].Priority == INSTANCE_LOW_PRIORITY {
					lowPriNumas = append(lowPriNumas, numa)
				} else if ins[i].Priority == INSTANCE_HIGH_PRIORITY {
					highPriNumas = append(highPriNumas, numa)
				}
			}
		}
	}

	return highPriNumas, lowPriNumas
}
