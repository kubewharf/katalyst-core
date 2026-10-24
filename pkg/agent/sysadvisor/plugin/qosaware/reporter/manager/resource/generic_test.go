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

package resource

import (
	"context"
	"errors"
	"io/ioutil"
	"testing"
	"time"

	info "github.com/google/cadvisor/info/v1"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	hmadvisor "github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/spd"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	metricspool "github.com/kubewharf/katalyst-core/pkg/metrics/metrics-pool"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func generateTestMetaServer(t *testing.T) *metaserver.MetaServer {
	cpuTopology, err := machine.GenerateDummyCPUTopology(96, 2, 2)
	require.NoError(t, err)
	return &metaserver.MetaServer{
		MetaAgent: &agent.MetaAgent{
			KatalystMachineInfo: &machine.KatalystMachineInfo{
				MachineInfo: &info.MachineInfo{
					NumCores:       96,
					MemoryCapacity: 500 << 30,
				},
				CPUTopology: cpuTopology,
			},
		},
		ServiceProfilingManager: &spd.DummyServiceProfilingManager{},
	}
}

func generateMachineConfig(t *testing.T) *config.Configuration {
	testConfiguration, err := options.NewOptions().Config()
	require.NoError(t, err)
	require.NotNil(t, testConfiguration)

	tmpStateDir, err := ioutil.TempDir("", "sys-advisor-test")
	require.NoError(t, err)
	testConfiguration.GenericSysAdvisorConfiguration.StateFileDirectory = tmpStateDir

	return testConfiguration
}

func newTestMetaCache(t *testing.T) *metacache.MetaCacheImp {
	metaCache, err := metacache.NewMetaCacheImp(generateMachineConfig(t), metricspool.DummyMetricsEmitterPool{}, metric.NewFakeMetricsFetcher(metrics.DummyMetrics{}))
	require.NoError(t, err)
	require.NotNil(t, metaCache)
	return metaCache
}

func TestNewGenericHeadroomManager(t *testing.T) {
	t.Parallel()

	type args struct {
		name                  v1.ResourceName
		useMilliValue         bool
		reportMillValue       bool
		syncPeriod            time.Duration
		headroomAdvisor       hmadvisor.ResourceAdvisor
		emitter               metrics.MetricEmitter
		slidingWindowOptions  GenericSlidingWindowOptions
		getReclaimOptionsFunc GetGenericReclaimOptionsFunc
		metaServer            *metaserver.MetaServer
	}

	tests := []struct {
		name string
		args args
	}{
		{
			name: "test for cpu",
			args: args{
				name:            v1.ResourceCPU,
				useMilliValue:   true,
				syncPeriod:      30 * time.Second,
				headroomAdvisor: hmadvisor.NewResourceAdvisorStub(),
				emitter:         metrics.DummyMetrics{},
				slidingWindowOptions: GenericSlidingWindowOptions{
					SlidingWindowTime: 2 * time.Minute,
					MinStep:           resource.MustParse("0.3"),
					MaxStep:           resource.MustParse("4"),
				},
				getReclaimOptionsFunc: func() GenericReclaimOptions {
					return GenericReclaimOptions{
						EnableReclaim:                 true,
						ReservedResourceForReport:     resource.MustParse("10"),
						MinReclaimedResourceForReport: resource.MustParse("4"),
					}
				},
				metaServer: generateTestMetaServer(t),
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mgr := NewGenericHeadroomManager(tt.args.name, tt.args.useMilliValue, tt.args.reportMillValue,
				tt.args.syncPeriod, tt.args.headroomAdvisor, tt.args.emitter,
				tt.args.slidingWindowOptions, tt.args.getReclaimOptionsFunc,
				tt.args.metaServer, newTestMetaCache(t))
			mgr.newSlidingWindow()
		})
	}
}

func TestGenericHeadroomManager_Allocatable(t *testing.T) {
	t.Parallel()

	r := hmadvisor.NewResourceAdvisorStub()
	reclaimOptions := GenericReclaimOptions{
		EnableReclaim:                 true,
		ReservedResourceForReport:     resource.MustParse("10"),
		MinReclaimedResourceForReport: resource.MustParse("4"),
	}
	m := NewGenericHeadroomManager(v1.ResourceCPU, true, false,
		30*time.Millisecond, r, metrics.DummyMetrics{},
		GenericSlidingWindowOptions{
			SlidingWindowTime: 180 * time.Millisecond,
			MinStep:           resource.MustParse("0.3"),
			MaxStep:           resource.MustParse("4"),
		},
		func() GenericReclaimOptions {
			return reclaimOptions
		},
		generateTestMetaServer(t),
		newTestMetaCache(t),
	)
	go m.Run(context.Background())

	var (
		err         error
		allocatable resource.Quantity
	)

	// first get allocatable with notFound error return
	_, err = m.GetAllocatable()
	require.Error(t, err)

	// set headroom to 20 and sleep 30ms to sync but not enough sample,
	// so return notFound error also
	r.SetHeadroom(v1.ResourceCPU, resource.MustParse("20"))
	time.Sleep(30 * time.Millisecond)
	_, err = m.GetAllocatable()
	require.Error(t, err)

	// wait 180ms which has enough sample in window, so return allocatable with reserve
	time.Sleep(180 * time.Millisecond)
	allocatable, err = m.GetAllocatable()
	require.NoError(t, err)
	require.Equal(t, int64(10000), allocatable.MilliValue())

	// update reclaim options to disable reclaim, return zero next getting allocatable
	reclaimOptions.EnableReclaim = false
	m.sync(context.Background())
	allocatable, err = m.GetAllocatable()
	require.NoError(t, err)
	require.Equal(t, int64(0), allocatable.MilliValue())

	reclaimOptions.EnableReclaim = true
	reclaimOptions.MinReclaimedResourceForReport = resource.MustParse("100")
	m.sync(context.Background())
	capacity, err := m.GetCapacity()
	require.NoError(t, err)
	require.Equal(t, int64(100000), capacity.MilliValue())
}

func TestGenericHeadroomManager_ApportionFailurePreservesState(t *testing.T) {
	t.Parallel()

	manager, metaCache := newAtomicUpdateTestManager(t, func(
		target resource.Quantity, current map[int]resource.Quantity,
	) (resource.Quantity, map[int]resource.Quantity, error) {
		return target, current, nil
	})
	manager.sync(context.Background())

	previousTotal := manager.lastReportResult.DeepCopy()
	previousNUMA := copyQuantities(manager.lastNUMAReportResult)
	previousCache, ok := metaCache.GetHeadroomEntries(string(v1.ResourceCPU))
	require.True(t, ok)

	manager.numaResultApportioner = func(
		resource.Quantity, map[int]resource.Quantity,
	) (resource.Quantity, map[int]resource.Quantity, error) {
		return resource.Quantity{}, nil, errors.New("apportion failure")
	}
	manager.sync(context.Background())

	require.Equal(t, previousTotal.String(), manager.lastReportResult.String())
	require.Equal(t, quantityStrings(previousNUMA), quantityStrings(manager.lastNUMAReportResult))
	currentCache, ok := metaCache.GetHeadroomEntries(string(v1.ResourceCPU))
	require.True(t, ok)
	require.Equal(t, previousCache, currentCache)
}

func TestGenericHeadroomManager_InvalidApportionmentPreservesState(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		effective   string
		allocations map[int]resource.Quantity
	}{
		{
			name:      "negative effective",
			effective: "-1",
			allocations: map[int]resource.Quantity{
				0: resource.MustParse("0"),
				1: resource.MustParse("0"),
			},
		},
		{
			name:      "negative allocation",
			effective: "10",
			allocations: map[int]resource.Quantity{
				0: resource.MustParse("-1"),
				1: resource.MustParse("11"),
			},
		},
		{
			name:      "effective exceeds requested",
			effective: "11",
			allocations: map[int]resource.Quantity{
				0: resource.MustParse("6"),
				1: resource.MustParse("5"),
			},
		},
		{
			name:      "allocation exceeds numa limit",
			effective: "10",
			allocations: map[int]resource.Quantity{
				0: resource.MustParse("7"),
				1: resource.MustParse("3"),
			},
		},
		{
			name:      "missing numa key",
			effective: "6",
			allocations: map[int]resource.Quantity{
				0: resource.MustParse("6"),
			},
		},
		{
			name:      "allocation sum mismatches effective",
			effective: "10",
			allocations: map[int]resource.Quantity{
				0: resource.MustParse("6"),
				1: resource.MustParse("3"),
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			manager, metaCache := newAtomicUpdateTestManager(t, func(
				target resource.Quantity, current map[int]resource.Quantity,
			) (resource.Quantity, map[int]resource.Quantity, error) {
				return target, current, nil
			})
			manager.sync(context.Background())

			previousTotal := manager.lastReportResult.DeepCopy()
			previousNUMA := copyQuantities(manager.lastNUMAReportResult)
			previousCache, ok := metaCache.GetHeadroomEntries(string(v1.ResourceCPU))
			require.True(t, ok)

			manager.numaResultApportioner = func(
				resource.Quantity, map[int]resource.Quantity,
			) (resource.Quantity, map[int]resource.Quantity, error) {
				return resource.MustParse(tt.effective), tt.allocations, nil
			}
			manager.sync(context.Background())

			require.Equal(t, previousTotal.String(), manager.lastReportResult.String())
			require.Equal(t, quantityStrings(previousNUMA), quantityStrings(manager.lastNUMAReportResult))
			currentCache, ok := metaCache.GetHeadroomEntries(string(v1.ResourceCPU))
			require.True(t, ok)
			require.Equal(t, previousCache, currentCache)
		})
	}
}

func TestGenericHeadroomManager_ApportionerCannotMutateValidationBaseline(t *testing.T) {
	t.Parallel()

	manager, metaCache := newAtomicUpdateTestManager(t, func(
		target resource.Quantity, current map[int]resource.Quantity,
	) (resource.Quantity, map[int]resource.Quantity, error) {
		return target, current, nil
	})
	manager.sync(context.Background())

	previousTotal := manager.lastReportResult.DeepCopy()
	previousNUMA := copyQuantities(manager.lastNUMAReportResult)
	previousCache, ok := metaCache.GetHeadroomEntries(string(v1.ResourceCPU))
	require.True(t, ok)

	manager.numaResultApportioner = func(
		target resource.Quantity, current map[int]resource.Quantity,
	) (resource.Quantity, map[int]resource.Quantity, error) {
		delete(current, 1)
		current[0] = target.DeepCopy()
		current[2] = resource.Quantity{}
		return target, current, nil
	}
	manager.sync(context.Background())

	require.Equal(t, previousTotal.String(), manager.lastReportResult.String())
	require.Equal(t, quantityStrings(previousNUMA), quantityStrings(manager.lastNUMAReportResult))
	currentCache, ok := metaCache.GetHeadroomEntries(string(v1.ResourceCPU))
	require.True(t, ok)
	require.Equal(t, previousCache, currentCache)
}

func TestGenericHeadroomManager_QuantityIsolation(t *testing.T) {
	t.Parallel()

	t.Run("strategy output cannot mutate committed state", func(t *testing.T) {
		t.Parallel()

		var retained map[int]resource.Quantity
		manager, _ := newAtomicUpdateTestManager(t, func(
			target resource.Quantity, current map[int]resource.Quantity,
		) (resource.Quantity, map[int]resource.Quantity, error) {
			retained = current
			return target, current, nil
		})
		manager.sync(context.Background())

		quantity := retained[0]
		quantity.Set(1)
		retained[0] = resource.MustParse("2")

		require.Equal(t, map[int]string{0: "6", 1: "4"}, quantityStrings(manager.lastNUMAReportResult))
	})

	t.Run("getter results cannot mutate committed state", func(t *testing.T) {
		t.Parallel()

		total := resource.MustParse("123456789012345678901234567890")
		numa := resource.MustParse("223456789012345678901234567890")
		manager := &GenericHeadroomManager{
			resourceName: v1.ResourceCPU,
			reportResultTransformer: func(quantity resource.Quantity) resource.Quantity {
				return quantity
			},
			lastReportResult:     &total,
			lastNUMAReportResult: map[int]resource.Quantity{0: numa},
		}

		returnedTotal, err := manager.GetAllocatable()
		require.NoError(t, err)
		returnedNUMA, err := manager.GetNumaAllocatable()
		require.NoError(t, err)

		returnedTotal.Add(resource.MustParse("123456789012345678901234567890"))
		quantity := returnedNUMA[0]
		quantity.Add(resource.MustParse("123456789012345678901234567890"))
		returnedNUMA[0] = quantity

		currentTotal, err := manager.GetAllocatable()
		require.NoError(t, err)
		currentNUMA, err := manager.GetNumaAllocatable()
		require.NoError(t, err)
		require.Zero(t, currentTotal.Cmp(resource.MustParse("123456789012345678901234567890")))
		currentNUMA0 := currentNUMA[0]
		require.Zero(t, currentNUMA0.Cmp(resource.MustParse("223456789012345678901234567890")))
	})
}

func TestGenericHeadroomManager_DefaultMemoryNUMARatioIsUnchanged(t *testing.T) {
	t.Parallel()

	advisor := &staticResourceAdvisor{subAdvisor: &staticSubResourceAdvisor{
		total: resource.MustParse("7"),
		numa: map[int]resource.Quantity{
			0: resource.MustParse("4"),
			1: resource.MustParse("3"),
		},
	}}
	manager := NewGenericHeadroomManager(
		v1.ResourceMemory,
		false,
		false,
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
				ReservedResourceForReport:     resource.MustParse("2"),
				MinReclaimedResourceForReport: resource.MustParse("0"),
			}
		},
		generateTestMetaServer(t),
		newTestMetaCache(t),
	)

	manager.sync(context.Background())

	require.NotNil(t, manager.lastReportResult)
	require.Equal(t, int64(5), manager.lastReportResult.Value())
	require.Equal(t, map[int]string{0: "2", 1: "2"}, quantityStrings(manager.lastNUMAReportResult))
}

func newAtomicUpdateTestManager(t *testing.T, apportioner NUMAResultApportioner) (
	*GenericHeadroomManager, *metacache.MetaCacheImp,
) {
	t.Helper()

	advisor := &staticResourceAdvisor{subAdvisor: &staticSubResourceAdvisor{
		total: resource.MustParse("10"),
		numa: map[int]resource.Quantity{
			0: resource.MustParse("6"),
			1: resource.MustParse("4"),
		},
	}}
	metaCache := newTestMetaCache(t)
	manager := NewGenericHeadroomManager(
		v1.ResourceCPU,
		true,
		false,
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
				ReservedResourceForReport:     resource.MustParse("0"),
				MinReclaimedResourceForReport: resource.MustParse("0"),
			}
		},
		generateTestMetaServer(t),
		metaCache,
		WithNUMAResultApportioner(apportioner),
	)
	return manager, metaCache
}

func quantityStrings(input map[int]resource.Quantity) map[int]string {
	output := make(map[int]string, len(input))
	for numaID, quantity := range input {
		output[numaID] = quantity.String()
	}
	return output
}
