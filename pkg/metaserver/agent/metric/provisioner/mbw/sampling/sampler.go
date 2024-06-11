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

package sampling

import (
	"context"
	"fmt"
	"time"

	"github.com/kubewharf/katalyst-core/pkg/mbw/monitor"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	utilmetric "github.com/kubewharf/katalyst-core/pkg/util/metric"
)

const probeInterval = time.Second * 1

type Sampler interface {
	Startup(ctx context.Context) error
	Sample(context.Context)
}

type mbwSampler struct {
	monitor *monitor.MBMonitor

	metricStore *utilmetric.MetricStore
	emitter     metrics.MetricEmitter
}

func (m mbwSampler) Startup(ctx context.Context) error {
	if err := m.monitor.Init(); err != nil {
		return err
	}

	return m.monitor.GlobalStats(ctx, uint64(probeInterval.Milliseconds()))

}

func (m mbwSampler) Sample(ctx context.Context) {
	now := time.Now()

	if m.monitor.MemoryBandwidth.Cores[0].LRMB_Delta != 0 {
		// ignore the result at the 1st sec
		m.monitor.MemoryBandwidth.CoreLocker.RLock()
		defer m.monitor.MemoryBandwidth.CoreLocker.RUnlock()

		// write per-NUMA memory-bandwidth
		for i, numa := range m.monitor.MemoryBandwidth.Numas {
			m.metricStore.SetNumaMetric(i,
				"mb-mbps",
				utilmetric.MetricData{Value: float64(numa.Total), Time: &now})
		}

	}

	if m.monitor.MemoryBandwidth.Packages[0].RMB_Delta != 0 {
		// ignore the result at the 1st sec
		m.monitor.MemoryBandwidth.PackageLocker.RLock()
		defer m.monitor.MemoryBandwidth.PackageLocker.RUnlock()

		// write per-package memory-bandwidth - Read & Write Bandwidth [r/w ratio]
		for i, pkg := range m.monitor.MemoryBandwidth.Packages {
			fmt.Printf("package %d: %d [%.0f], ", i, pkg.Total, float64(pkg.RMB_Delta)/float64(pkg.WMB_Delta))
			m.metricStore.SetPackageMetric(i, "rw-mbps",
				utilmetric.MetricData{Value: float64(pkg.Total), Time: &now})
			m.metricStore.SetPackageMetric(i, "rw-ratio",
				utilmetric.MetricData{Value: float64(pkg.RMB_Delta) / float64(pkg.WMB_Delta), Time: &now})
		}

	}

	if m.monitor.MemoryLatency.L3Latency[0].L3PMCLatency != 0 {
		m.monitor.MemoryLatency.CCDLocker.RLock()
		defer m.monitor.MemoryLatency.CCDLocker.RUnlock()

		fmt.Println("L3PMC - Memory Access Latency [ns]")
		// print avg memory access latency for each package
		for i := 0; i < len(m.monitor.PackageMap); i++ {
			// todo: verify it has right logic?????
			for _, node := range m.monitor.PackageMap[i] {
				latency := 0.0
				for _, ccd := range m.monitor.NumaMap[node] {
					latency += m.monitor.MemoryLatency.L3Latency[ccd].L3PMCLatency
				}
				fmt.Printf("node %d: %.1f, ", node, latency/float64(len(m.monitor.NumaMap[0])))
			}
		}

		fmt.Println()
	}
}

func New(monitor *monitor.MBMonitor, metricStore *utilmetric.MetricStore, emitter metrics.MetricEmitter) Sampler {
	return &mbwSampler{
		monitor:     monitor,
		metricStore: metricStore,
		emitter:     emitter,
	}
}
