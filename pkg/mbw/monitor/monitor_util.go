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

// this file is extension of mbw monitor
// it implements the needed adaptor utilities (besides FakeNumaConfigured, Init & GlobalStats) for sampled metrics retrievals

// GetMemoryBandwidthOfNUMAs is the extension utility as part of adaptor to MB metric sampler
func (m MBMonitor) GetMemoryBandwidthOfNUMAs() []NumaMB {
	m.MemoryBandwidth.CoreLocker.RLock()
	defer m.MemoryBandwidth.CoreLocker.RUnlock()

	// ignore the result at the 1st sec
	if m.MemoryBandwidth.Cores[0].LRMB_Delta == 0 {
		return nil
	}
	return m.MemoryBandwidth.Numas
}

// GetMemoryBandwidthOfPackages is the extension utility as part of adaptor to MB metric sampler
func (m MBMonitor) GetMemoryBandwidthOfPackages() []PackageMB {
	m.MemoryBandwidth.PackageLocker.RLock()
	defer m.MemoryBandwidth.PackageLocker.RUnlock()

	// ignore the result at the 1st sec
	if m.MemoryBandwidth.Packages[0].RMB_Delta == 0 {
		return nil
	}
	return m.MemoryBandwidth.Packages
}

// GetPackageNUMA is part of adaptor to MB metric sampler
// it returns (read-only) package-numa map
func (m MBMonitor) GetPackageNUMA() map[int][]int {
	return m.PackageMap
}

// GetNUMACCD is part of adaptor to MB metric sampler
// it returns (read-only) numa-ccd map
func (m MBMonitor) GetNUMACCD() map[int][]int {
	return m.NumaMap
}

// GetCCDL3Latency is part of adaptor to MB metric sampler
// it returns all the ccd L3 latency [ns]
func (m MBMonitor) GetCCDL3Latency() []float64 {
	m.MemoryLatency.CCDLocker.RLock()
	defer m.MemoryLatency.CCDLocker.RUnlock()

	// ignore the result at the 1st sec
	if m.MemoryLatency.L3Latency[0].L3PMCLatency == 0 {
		return nil
	}

	ccdCount := len(m.MemoryLatency.L3Latency)
	result := make([]float64, ccdCount)
	for ccd := 0; ccd < ccdCount; ccd++ {
		result[ccd] = m.MemoryLatency.L3Latency[ccd].L3PMCLatency
	}

	return result
}
