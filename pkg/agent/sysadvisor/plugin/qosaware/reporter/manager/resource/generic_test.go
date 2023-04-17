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
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/reporter/manager/broker"
	hmadvisor "github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

func TestNewGenericHeadroomManager(t *testing.T) {
	type args struct {
		name                  v1.ResourceName
		useMilliValue         bool
		reportMillValue       bool
		syncPeriod            time.Duration
		broker                broker.Broker
		headroomAdvisor       hmadvisor.ResourceAdvisor
		emitter               metrics.MetricEmitter
		slidingWindowOptions  GenericSlidingWindowOptions
		getReclaimOptionsFunc GetGenericReclaimOptionsFunc
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
				broker:          broker.NewNoneBroker(nil, nil, nil),
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
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			NewGenericHeadroomManager(tt.args.name, tt.args.useMilliValue, tt.args.reportMillValue,
				tt.args.syncPeriod, tt.args.broker, tt.args.headroomAdvisor, tt.args.emitter,
				tt.args.slidingWindowOptions, tt.args.getReclaimOptionsFunc)
		})
	}
}

func TestGenericHeadroomManager_Allocatable(t *testing.T) {
	r := hmadvisor.NewResourceAdvisorStub()
	reclaimOptions := GenericReclaimOptions{
		EnableReclaim:                 true,
		ReservedResourceForReport:     resource.MustParse("10"),
		MinReclaimedResourceForReport: resource.MustParse("4"),
	}
	m := NewGenericHeadroomManager(v1.ResourceCPU, true, false, 30*time.Millisecond,
		broker.NewNoneBroker(nil, nil, nil), r, metrics.DummyMetrics{},
		GenericSlidingWindowOptions{
			SlidingWindowTime: 180 * time.Millisecond,
			MinStep:           resource.MustParse("0.3"),
			MaxStep:           resource.MustParse("4"),
		},
		func() GenericReclaimOptions {
			return reclaimOptions
		},
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
