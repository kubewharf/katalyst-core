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

package cpumetrics

import (
	"errors"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	pkgconsts "github.com/kubewharf/katalyst-core/pkg/consts"
	metrictypes "github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/types"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	utilmetric "github.com/kubewharf/katalyst-core/pkg/util/metric"
)

type scriptedMetricKey struct {
	cpu    int
	source string
}

type scriptedMetricResult struct {
	data utilmetric.MetricData
	err  error
}

type scriptedMetricsFetcher struct {
	metrictypes.MetricsFetcher

	mu        sync.Mutex
	sequences map[scriptedMetricKey][]scriptedMetricResult
	calls     map[scriptedMetricKey]int
	order     []scriptedMetricKey
}

func newScriptedMetricsFetcher(sequences map[scriptedMetricKey][]scriptedMetricResult) *scriptedMetricsFetcher {
	return &scriptedMetricsFetcher{
		sequences: sequences,
		calls:     make(map[scriptedMetricKey]int),
	}
}

func (f *scriptedMetricsFetcher) GetCPUMetric(cpu int, source string) (utilmetric.MetricData, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	key := scriptedMetricKey{cpu: cpu, source: source}
	f.order = append(f.order, key)
	call := f.calls[key]
	f.calls[key] = call + 1
	sequence := f.sequences[key]
	if call >= len(sequence) {
		return utilmetric.MetricData{}, errors.New("scripted metric unavailable")
	}
	return sequence[call].data, sequence[call].err
}

func (f *scriptedMetricsFetcher) callCount(cpu int, source string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[scriptedMetricKey{cpu: cpu, source: source}]
}

func (f *scriptedMetricsFetcher) totalCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	total := 0
	for _, calls := range f.calls {
		total += calls
	}
	return total
}

func (f *scriptedMetricsFetcher) callOrder() []scriptedMetricKey {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]scriptedMetricKey(nil), f.order...)
}

func scriptedSample(value float64, at time.Time) scriptedMetricResult {
	timestamp := at
	return scriptedMetricResult{
		data: utilmetric.MetricData{Value: value, Time: &timestamp},
	}
}

func TestSampleRun(t *testing.T) {
	t.Parallel()

	t1 := time.Unix(100, 0)
	t2 := time.Unix(200, 0)
	t3 := time.Unix(300, 0)

	t.Run("filters fetch errors nil timestamps and non-finite values while retaining zero", func(t *testing.T) {
		t.Parallel()

		fetcher := newScriptedMetricsFetcher(map[scriptedMetricKey][]scriptedMetricResult{
			{cpu: 0, source: pkgconsts.MetricCPUUsageRatio}: {scriptedSample(0, t1)},
			{cpu: 1, source: pkgconsts.MetricCPUUsageRatio}: {{
				data: utilmetric.MetricData{Value: 1, Time: &t1},
				err:  errors.New("fetch failed"),
			}},
			{cpu: 2, source: pkgconsts.MetricCPUUsageRatio}: {{
				data: utilmetric.MetricData{Value: 2, Time: &t1},
				err:  errors.New("expired"),
			}},
			{cpu: 3, source: pkgconsts.MetricCPUUsageRatio}: {{
				data: utilmetric.MetricData{Value: 3},
			}},
			{cpu: 4, source: pkgconsts.MetricCPUUsageRatio}: {scriptedSample(math.NaN(), t1)},
			{cpu: 5, source: pkgconsts.MetricCPUUsageRatio}: {scriptedSample(math.Inf(1), t1)},
			{cpu: 6, source: pkgconsts.MetricCPUUsageRatio}: {scriptedSample(math.Inf(-1), t1)},
		})

		cache := sampleRun(fetcher, machine.NewCPUSet(6, 5, 4, 3, 2, 1, 0))

		require.Equal(t, map[int]sample{
			0: {value: 0, at: t1},
		}, cache.single[pkgconsts.MetricCPUUsageRatio])
		for cpu := 0; cpu <= 6; cpu++ {
			require.Equal(t, 1, fetcher.callCount(cpu, pkgconsts.MetricCPUUsageRatio))
		}
	})

	t.Run("rereads a mixed source exactly once then keeps the latest final generation", func(t *testing.T) {
		t.Parallel()

		fetcher := newScriptedMetricsFetcher(map[scriptedMetricKey][]scriptedMetricResult{
			{cpu: 0, source: pkgconsts.MetricCPUUsageRatio}: {
				scriptedSample(10, t1),
				scriptedSample(20, t2),
			},
			{cpu: 1, source: pkgconsts.MetricCPUUsageRatio}: {
				scriptedSample(30, t2),
				scriptedSample(40, t3),
			},
		})

		cache := sampleRun(fetcher, machine.NewCPUSet(1, 0))

		require.Equal(t, map[int]sample{
			1: {value: 40, at: t3},
		}, cache.single[pkgconsts.MetricCPUUsageRatio])
		require.Equal(t, 2, fetcher.callCount(0, pkgconsts.MetricCPUUsageRatio))
		require.Equal(t, 2, fetcher.callCount(1, pkgconsts.MetricCPUUsageRatio))
		require.Equal(t, []scriptedMetricKey{
			{cpu: 0, source: pkgconsts.MetricCPUUsageRatio},
			{cpu: 1, source: pkgconsts.MetricCPUUsageRatio},
			{cpu: 0, source: pkgconsts.MetricCPUUsageRatio},
			{cpu: 1, source: pkgconsts.MetricCPUUsageRatio},
		}, fetcher.callOrder()[:4])
	})

	t.Run("pairs CPI CPU-locally and keeps the latest common generation after one reread", func(t *testing.T) {
		t.Parallel()

		fetcher := newScriptedMetricsFetcher(map[scriptedMetricKey][]scriptedMetricResult{
			{cpu: 0, source: pkgconsts.MetricCPUCycles}: {
				scriptedSample(100, t1),
				scriptedSample(200, t2),
			},
			{cpu: 0, source: pkgconsts.MetricCPUInstructions}: {
				scriptedSample(50, t1),
				scriptedSample(100, t2),
			},
			{cpu: 1, source: pkgconsts.MetricCPUCycles}: {
				scriptedSample(300, t1),
				scriptedSample(600, t3),
			},
			{cpu: 1, source: pkgconsts.MetricCPUInstructions}: {
				scriptedSample(100, t2),
				scriptedSample(200, t2),
			},
			{cpu: 2, source: pkgconsts.MetricCPUCycles}: {
				scriptedSample(400, t2),
				scriptedSample(900, t3),
			},
			{cpu: 2, source: pkgconsts.MetricCPUInstructions}: {
				scriptedSample(200, t2),
				scriptedSample(300, t3),
			},
		})

		cache := sampleRun(fetcher, machine.NewCPUSet(2, 1, 0))

		require.Equal(t, map[int]sample{
			2: {value: 900, at: t3},
		}, cache.cycles)
		require.Equal(t, map[int]sample{
			2: {value: 300, at: t3},
		}, cache.instructions)
		for cpu := 0; cpu <= 2; cpu++ {
			require.Equal(t, 2, fetcher.callCount(cpu, pkgconsts.MetricCPUCycles))
			require.Equal(t, 2, fetcher.callCount(cpu, pkgconsts.MetricCPUInstructions))
		}
	})

	t.Run("rereads all CPI pairs when a mismatched CPU is interleaved with an old complete pair", func(t *testing.T) {
		t.Parallel()

		fetcher := newScriptedMetricsFetcher(map[scriptedMetricKey][]scriptedMetricResult{
			{cpu: 0, source: pkgconsts.MetricCPUCycles}: {
				scriptedSample(200, t2),
				scriptedSample(300, t3),
			},
			{cpu: 0, source: pkgconsts.MetricCPUInstructions}: {
				scriptedSample(100, t3),
				scriptedSample(150, t3),
			},
			{cpu: 1, source: pkgconsts.MetricCPUCycles}: {
				scriptedSample(100, t1),
				scriptedSample(200, t2),
			},
			{cpu: 1, source: pkgconsts.MetricCPUInstructions}: {
				scriptedSample(50, t1),
				scriptedSample(100, t2),
			},
		})

		cache := sampleRun(fetcher, machine.NewCPUSet(0, 1))

		require.Equal(t, map[int]sample{
			0: {value: 300, at: t3},
		}, cache.cycles)
		require.Equal(t, map[int]sample{
			0: {value: 150, at: t3},
		}, cache.instructions)
		for cpu := 0; cpu <= 1; cpu++ {
			require.Equal(t, 2, fetcher.callCount(cpu, pkgconsts.MetricCPUCycles))
			require.Equal(t, 2, fetcher.callCount(cpu, pkgconsts.MetricCPUInstructions))
		}
	})
}

func TestAggregateSamples(t *testing.T) {
	t.Parallel()

	t1 := time.Unix(100, 0)

	t.Run("aggregates averages sums partial coverage zero and weighted CPI", func(t *testing.T) {
		t.Parallel()

		cache := runSamples{
			single: sourceCache{
				pkgconsts.MetricCPUUsageRatio: {
					0: {value: 0, at: t1},
					1: {value: 2, at: t1},
				},
				pkgconsts.MetricCPUIOWaitRatio: {
					1: {value: 0.4, at: t1},
				},
				pkgconsts.MetricCPUSchedwait: {
					0: {value: 10, at: t1},
					2: {value: 30, at: t1},
				},
				pkgconsts.MetricCPUIrqRatio: {
					0: {value: 0.2, at: t1},
					1: {value: 0.4, at: t1},
				},
				pkgconsts.MetricCPUL3Misses: {
					0: {value: 0, at: t1},
					1: {value: 5, at: t1},
				},
			},
			cycles: map[int]sample{
				0: {value: 100, at: t1},
				1: {value: 300, at: t1},
			},
			instructions: map[int]sample{
				0: {value: 50, at: t1},
				1: {value: 100, at: t1},
			},
		}

		values := aggregateSamples(cache, machine.NewCPUSet(0, 1))

		require.NotNil(t, values.cpuUsageRatio)
		require.InDelta(t, 1, *values.cpuUsageRatio, 1e-9)
		require.NotNil(t, values.ioWaitRatio)
		require.InDelta(t, 0.4, *values.ioWaitRatio, 1e-9)
		require.NotNil(t, values.schedWait)
		require.InDelta(t, 10, *values.schedWait, 1e-9)
		require.NotNil(t, values.irqRatio)
		require.InDelta(t, 0.3, *values.irqRatio, 1e-9)
		require.NotNil(t, values.l3Misses)
		require.InDelta(t, 5, *values.l3Misses, 1e-9)
		require.NotNil(t, values.cpi)
		require.InDelta(t, 400.0/150.0, *values.cpi, 1e-9)

		numaValues := aggregateSamples(cache, machine.NewCPUSet(1))
		require.NotNil(t, numaValues.cpuUsageRatio)
		require.InDelta(t, 2, *numaValues.cpuUsageRatio, 1e-9)
		require.Nil(t, numaValues.schedWait)
		require.NotNil(t, numaValues.l3Misses)
		require.InDelta(t, 5, *numaValues.l3Misses, 1e-9)
		require.NotNil(t, numaValues.cpi)
		require.InDelta(t, 3, *numaValues.cpi, 1e-9)
	})

	t.Run("returns nil fields when sources are absent or CPI denominator is not positive", func(t *testing.T) {
		t.Parallel()

		values := aggregateSamples(runSamples{
			single:       sourceCache{},
			cycles:       map[int]sample{0: {value: 10, at: t1}},
			instructions: map[int]sample{0: {value: 0, at: t1}},
		}, machine.NewCPUSet(0))

		require.Nil(t, values.cpuUsageRatio)
		require.Nil(t, values.ioWaitRatio)
		require.Nil(t, values.schedWait)
		require.Nil(t, values.irqRatio)
		require.Nil(t, values.cpi)
		require.Nil(t, values.l3Misses)
	})

	t.Run("averages MaxFloat64 samples without overflowing", func(t *testing.T) {
		t.Parallel()

		values := aggregateSamples(runSamples{
			single: sourceCache{
				pkgconsts.MetricCPUUsageRatio: {
					0: {value: math.MaxFloat64, at: t1},
					1: {value: math.MaxFloat64, at: t1},
				},
			},
		}, machine.NewCPUSet(0, 1))

		require.NotNil(t, values.cpuUsageRatio)
		require.Equal(t, math.MaxFloat64, *values.cpuUsageRatio)
	})

	t.Run("omits L3 misses when the sum overflows", func(t *testing.T) {
		t.Parallel()

		values := aggregateSamples(runSamples{
			single: sourceCache{
				pkgconsts.MetricCPUL3Misses: {
					0: {value: math.MaxFloat64, at: t1},
					1: {value: math.MaxFloat64, at: t1},
				},
			},
		}, machine.NewCPUSet(0, 1))

		require.Nil(t, values.l3Misses)
	})

	t.Run("normalizes CPI sums on a common scale before division", func(t *testing.T) {
		t.Parallel()

		values := aggregateSamples(runSamples{
			single: sourceCache{},
			cycles: map[int]sample{
				0: {value: math.MaxFloat64, at: t1},
				1: {value: math.MaxFloat64, at: t1},
			},
			instructions: map[int]sample{
				0: {value: math.MaxFloat64, at: t1},
				1: {value: math.MaxFloat64, at: t1},
			},
		}, machine.NewCPUSet(0, 1))

		require.NotNil(t, values.cpi)
		require.Equal(t, 1.0, *values.cpi)
	})

	t.Run("reuses one immutable cache for global and NUMA aggregation", func(t *testing.T) {
		t.Parallel()

		sequences := make(map[scriptedMetricKey][]scriptedMetricResult)
		sources := []string{
			pkgconsts.MetricCPUUsageRatio,
			pkgconsts.MetricCPUIOWaitRatio,
			pkgconsts.MetricCPUSchedwait,
			pkgconsts.MetricCPUIrqRatio,
			pkgconsts.MetricCPUL3Misses,
			pkgconsts.MetricCPUCycles,
			pkgconsts.MetricCPUInstructions,
		}
		for _, source := range sources {
			for cpu := 0; cpu < 2; cpu++ {
				sequences[scriptedMetricKey{cpu: cpu, source: source}] = []scriptedMetricResult{
					scriptedSample(float64(cpu+1), t1),
				}
			}
		}
		fetcher := newScriptedMetricsFetcher(sequences)
		cache := sampleRun(fetcher, machine.NewCPUSet(0, 1))
		callsAfterSampling := fetcher.totalCalls()

		global := aggregateSamples(cache, machine.NewCPUSet(0, 1))
		numa := aggregateSamples(cache, machine.NewCPUSet(1))

		require.NotNil(t, global.cpuUsageRatio)
		require.NotNil(t, numa.cpuUsageRatio)
		require.Equal(t, 14, callsAfterSampling)
		require.Equal(t, callsAfterSampling, fetcher.totalCalls())
	})
}
