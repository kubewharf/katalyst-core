/*
Copyright 2026 The Katalyst Authors.

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

package resource

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	hmadvisor "github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

type metricSample struct {
	value int64
	tags  map[string]string
}

type recordingMetricEmitter struct {
	sync.Mutex
	samples map[string]metricSample
}

func (r *recordingMetricEmitter) StoreInt64(
	key string, value int64, _ metrics.MetricTypeName, tags ...metrics.MetricTag,
) error {
	r.Lock()
	defer r.Unlock()
	tagMap := make(map[string]string, len(tags))
	for _, tag := range tags {
		tagMap[tag.Key] = tag.Val
	}
	r.samples[key] = metricSample{value: value, tags: tagMap}
	return nil
}

func (r *recordingMetricEmitter) StoreFloat64(
	string, float64, metrics.MetricTypeName, ...metrics.MetricTag,
) error {
	return nil
}

func (r *recordingMetricEmitter) WithTags(string, ...metrics.MetricTag) metrics.MetricEmitter {
	return r
}

func (r *recordingMetricEmitter) Run(context.Context) {}

type staticResourceAdvisor struct {
	subAdvisor hmadvisor.SubResourceAdvisor
}

func (s *staticResourceAdvisor) Run(context.Context) {}

func (s *staticResourceAdvisor) GetSubAdvisor(types.QoSResourceName) (hmadvisor.SubResourceAdvisor, error) {
	return s.subAdvisor, nil
}

type staticSubResourceAdvisor struct {
	total resource.Quantity
	numa  map[int]resource.Quantity
}

func (s *staticSubResourceAdvisor) Run(context.Context) {}

func (s *staticSubResourceAdvisor) UpdateAndGetAdvice(context.Context) (interface{}, error) {
	return nil, nil
}

func (s *staticSubResourceAdvisor) GetHeadroom() (resource.Quantity, map[int]resource.Quantity, error) {
	return s.total.DeepCopy(), copyQuantities(s.numa), nil
}

func copyQuantities(input map[int]resource.Quantity) map[int]resource.Quantity {
	output := make(map[int]resource.Quantity, len(input))
	for numaID, quantity := range input {
		output[numaID] = quantity.DeepCopy()
	}
	return output
}

func TestCPUNUMAResultApportioner(t *testing.T) {
	t.Parallel()

	current := map[int]resource.Quantity{
		0: resource.MustParse("10"),
		1: resource.MustParse("10"),
		2: resource.MustParse("10"),
		3: resource.MustParse("10"),
		4: resource.MustParse("10"),
		5: resource.MustParse("10"),
		6: resource.MustParse("8"),
		7: resource.MustParse("8"),
	}

	effective, allocations, err := newCPUNUMAResultApportioner(generateTestMetaServer(t))(
		resource.MustParse("64"), current)
	require.NoError(t, err)
	require.Equal(t, int64(64), effective.Value())
	require.Len(t, allocations, 8)
	// quantum=1: the target is distributed by largest-remainder without rounding
	// down to a whole-core multiple, so per-NUMA shares are not uniform.
	want := map[int]int64{0: 9, 1: 9, 2: 8, 3: 8, 4: 8, 5: 8, 6: 7, 7: 7}
	var sum int64
	for numaID := 0; numaID < 8; numaID++ {
		allocation := allocations[numaID]
		require.Equal(t, want[numaID], allocation.Value(), "numa %d", numaID)
		sum += allocation.Value()
	}
	require.Equal(t, int64(64), sum)
}

func TestCPUReservedHeadroomApportionmentIsAtomic(t *testing.T) {
	t.Parallel()

	numaHeadroom := map[int]resource.Quantity{
		0: resource.MustParse("10"),
		1: resource.MustParse("10"),
		2: resource.MustParse("10"),
		3: resource.MustParse("10"),
		4: resource.MustParse("10"),
		5: resource.MustParse("10"),
		6: resource.MustParse("8"),
		7: resource.MustParse("8"),
	}
	advisor := &staticResourceAdvisor{subAdvisor: &staticSubResourceAdvisor{
		total: resource.MustParse("76"),
		numa:  numaHeadroom,
	}}
	metaCache := newTestMetaCache(t)
	metaServer := generateTestMetaServer(t)
	manager := NewGenericHeadroomManager(
		v1.ResourceCPU,
		true,
		true,
		time.Second,
		advisor,
		metrics.DummyMetrics{},
		GenericSlidingWindowOptions{
			SlidingWindowTime: time.Second,
			MinStep:           resource.MustParse("0"),
			MaxStep:           resource.MustParse("1000"),
		},
		func() GenericReclaimOptions {
			return GenericReclaimOptions{
				EnableReclaim:                 true,
				ReservedResourceForReport:     resource.MustParse("12"),
				MinReclaimedResourceForReport: resource.MustParse("0"),
			}
		},
		metaServer,
		metaCache,
		WithNUMAResultApportioner(newCPUNUMAResultApportioner(metaServer)),
	)

	manager.sync(context.Background())

	require.NotNil(t, manager.lastReportResult)
	require.Equal(t, int64(64), manager.lastReportResult.Value())
	require.Len(t, manager.lastNUMAReportResult, 8)
	// quantum=1 distribution of target 64 (76 - 12 reserved) by largest-remainder.
	wantNUMA := map[int]int64{0: 9, 1: 9, 2: 8, 3: 8, 4: 8, 5: 8, 6: 7, 7: 7}
	var numaSum int64
	for numaID := 0; numaID < 8; numaID++ {
		numaValue := manager.lastNUMAReportResult[numaID]
		require.Equal(t, wantNUMA[numaID], numaValue.Value(), "numa %d", numaID)
		numaSum += numaValue.Value()
	}
	require.Equal(t, int64(64), numaSum)

	headroomInfo, ok := metaCache.GetHeadroomEntries(string(v1.ResourceCPU))
	require.True(t, ok)
	require.Equal(t, float64(64), headroomInfo.TotalHeadroom)
	var cacheNUMASum float64
	for _, value := range headroomInfo.NUMAHeadroom {
		cacheNUMASum += value
	}
	require.Equal(t, float64(64), cacheNUMASum)
}

func TestCPUHeadroomMinimumAndReserveSemantics(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		minimum   string
		wantTotal int64
		wantNUMA  map[int]int64
	}{
		{
			name:      "positive minimum is reported without core alignment",
			minimum:   "5",
			wantTotal: 5,
			wantNUMA:  map[int]int64{0: 3, 1: 2},
		},
		{
			name:      "zero minimum clamps reserve underflow to zero",
			minimum:   "0",
			wantTotal: 0,
			wantNUMA:  map[int]int64{0: 0, 1: 0},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			advisor := &staticResourceAdvisor{subAdvisor: &staticSubResourceAdvisor{
				total: resource.MustParse("3"),
				numa: map[int]resource.Quantity{
					0: resource.MustParse("4"),
					1: resource.MustParse("4"),
				},
			}}
			metaServer := generateTestMetaServer(t)
			manager := NewGenericHeadroomManager(
				v1.ResourceCPU,
				true,
				true,
				time.Second,
				advisor,
				metrics.DummyMetrics{},
				GenericSlidingWindowOptions{
					SlidingWindowTime: time.Second,
					MinStep:           resource.MustParse("0"),
					MaxStep:           resource.MustParse("1000"),
				},
				func() GenericReclaimOptions {
					return GenericReclaimOptions{
						EnableReclaim:                 true,
						ReservedResourceForReport:     resource.MustParse("5"),
						MinReclaimedResourceForReport: resource.MustParse(tt.minimum),
					}
				},
				metaServer,
				newTestMetaCache(t),
				WithNUMAResultApportioner(newCPUNUMAResultApportioner(metaServer)),
			)

			manager.sync(context.Background())

			require.NotNil(t, manager.lastReportResult)
			require.Equal(t, tt.wantTotal, manager.lastReportResult.Value())
			require.GreaterOrEqual(t, manager.lastReportResult.Value(), int64(0))
			var numaSum int64
			for numaID, want := range tt.wantNUMA {
				numaQuantity := manager.lastNUMAReportResult[numaID]
				got := numaQuantity.Value()
				require.Equal(t, want, got, "numa %d", numaID)
				require.GreaterOrEqual(t, got, int64(0), "numa %d", numaID)
				numaSum += got
			}
			require.Equal(t, manager.lastReportResult.Value(), numaSum)
		})
	}
}

func TestCPUApportionmentMetrics(t *testing.T) {
	t.Parallel()

	numaHeadroom := map[int]resource.Quantity{
		0: resource.MustParse("10"),
		1: resource.MustParse("10"),
		2: resource.MustParse("10"),
		3: resource.MustParse("10"),
		4: resource.MustParse("10"),
		5: resource.MustParse("10"),
		6: resource.MustParse("8"),
		7: resource.MustParse("8"),
	}
	advisor := &staticResourceAdvisor{subAdvisor: &staticSubResourceAdvisor{
		total: resource.MustParse("65.5"),
		numa:  numaHeadroom,
	}}
	emitter := &recordingMetricEmitter{samples: make(map[string]metricSample)}
	metaServer := generateTestMetaServer(t)
	manager := NewGenericHeadroomManager(
		v1.ResourceCPU,
		true,
		true,
		time.Second,
		advisor,
		emitter,
		GenericSlidingWindowOptions{
			SlidingWindowTime: time.Second,
			MinStep:           resource.MustParse("0"),
			MaxStep:           resource.MustParse("1000"),
		},
		func() GenericReclaimOptions {
			return GenericReclaimOptions{
				EnableReclaim:                 true,
				ReservedResourceForReport:     resource.MustParse("0"),
				MinReclaimedResourceForReport: resource.MustParse("0"),
			}
		},
		metaServer,
		newTestMetaCache(t),
		WithNUMAResultApportioner(newCPUNUMAResultApportioner(metaServer)),
	)

	manager.sync(context.Background())

	expectedTags := map[string]string{"component": "reporter", "resource": "cpu"}
	require.Equal(t, metricSample{value: 65, tags: expectedTags}, emitter.samples["headroom_apportion_requested"])
	require.Equal(t, metricSample{value: 65, tags: expectedTags}, emitter.samples["headroom_apportion_effective"])
	require.Equal(t, metricSample{value: 0, tags: expectedTags}, emitter.samples["headroom_apportion_alignment_loss"])
}
