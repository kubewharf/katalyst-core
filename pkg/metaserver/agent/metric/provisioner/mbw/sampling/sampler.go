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
	"errors"
	"time"

	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	utilmetric "github.com/kubewharf/katalyst-core/pkg/util/metric"
)

const probeInterval = time.Second * 1

type Sampler interface {
	Startup(ctx context.Context) error
	Sample(context.Context)
}

type mbwSampler struct {
	monitor     MBMonitorAdaptor
	metricStore *utilmetric.MetricStore
	emitter     metrics.MetricEmitter
}

func (m mbwSampler) Startup(ctx context.Context) error {
	if !m.monitor.FakeNumaConfigured() {
		return errors.New("not fake numa; memory bandwidth management not applicable")
	}

	if err := m.monitor.Init(); err != nil {
		return err
	}

	return m.monitor.GlobalStats(ctx, uint64(probeInterval.Milliseconds()))
}

func (m mbwSampler) Sample(ctx context.Context) {
	now := time.Now()

	// write per-NUMA memory-bandwidth
	for i, numaMB := range m.monitor.GetMemoryBandwidthOfNUMAs() {
		m.metricStore.SetNumaMetric(i, consts.MetricMemBandwidthFinerNuma,
			utilmetric.MetricData{Value: float64(numaMB.Total), Time: &now})
	}

	// write per-package memory-bandwidth - Read & Write Bandwidth [r/w ratio]
	for i, packageMB := range m.monitor.GetMemoryBandwidthOfPackages() {
		m.metricStore.SetPackageMetric(i, consts.MetricMemBandwidthRWPackage,
			utilmetric.MetricData{Value: float64(packageMB.Total), Time: &now})
		m.metricStore.SetPackageMetric(i, consts.MetricMemBandwidthRWRatioPackage,
			utilmetric.MetricData{Value: float64(packageMB.RMB_Delta) / float64(packageMB.WMB_Delta), Time: &now})
	}

	// write avg L3 memory access latency for each numa
	// L3PMC - Memory Access Latency [ns]
	packageNUMA := m.monitor.GetPackageNUMA()
	// we need package-numa count for estimated value per numa
	numaInPackage := float64(len(packageNUMA[0]))
	numaCCD := m.monitor.GetNUMACCD()
	ccdL3Latency := m.monitor.GetCCDL3Latency()
	// todo: more stringent sanity check for valid ccd latency data
	if len(ccdL3Latency) > 0 {
		for i := 0; i < len(packageNUMA); i++ {
			for _, node := range packageNUMA[i] {
				latency := averageLatency(numaInPackage, numaCCD[node], ccdL3Latency)
				m.metricStore.SetNumaMetric(node, consts.MetricMemL3PMCNuma,
					utilmetric.MetricData{Value: latency, Time: &now})
			}
		}
	}
}

func averageLatency(count float64, ccds []int, ccdL3Latency []float64) float64 {
	latency := 0.0
	for _, ccd := range ccds {
		latency += ccdL3Latency[ccd]
	}
	return latency / count
}

func New(monitor MBMonitorAdaptor, metricStore *utilmetric.MetricStore, emitter metrics.MetricEmitter) Sampler {
	return &mbwSampler{
		monitor:     monitor,
		metricStore: metricStore,
		emitter:     emitter,
	}
}
