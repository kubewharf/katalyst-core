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

package policy

import (
	"context"
	"testing"

	"github.com/stretchr/testify/mock"
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/advisor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/allocator"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/domain"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/monitor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/plan"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/reader"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

type mockAdvisor struct {
	mock.Mock
}

func (ma *mockAdvisor) GetPlan(ctx context.Context, domainsMon *monitor.DomainStats) (*plan.MBPlan, error) {
	args := ma.Called(ctx, domainsMon)
	return args.Get(0).(*plan.MBPlan), args.Error(1)
}

type mockPlanAlloctor struct {
	mock.Mock
}

func (mp *mockPlanAlloctor) Allocate(ctx context.Context, plan *plan.MBPlan) error {
	args := mp.Called(ctx, plan)
	return args.Error(0)
}

type mockReader struct {
	mock.Mock
}

func (mr *mockReader) GetMBData() (*reader.MBData, error) {
	args := mr.Called()
	return args.Get(0).(*reader.MBData), args.Error(1)
}

func TestMBPlugin_run(t *testing.T) {
	t.Parallel()

	dummyPlan := &plan.MBPlan{}

	mReader := new(mockReader)
	mReader.On("GetMBData").Return(&reader.MBData{
		MBBody: monitor.GroupMBStats{
			"/": {
				0: {
					LocalMB:  888,
					RemoteMB: 222,
					TotalMB:  1110,
				},
				1: {
					LocalMB:  777,
					RemoteMB: 333,
					TotalMB:  1110,
				},
			},
		},
		UpdateTime: 0,
	}, nil)

	mAdvisor := new(mockAdvisor)
	mAdvisor.On("GetPlan", mock.Anything, mock.Anything).Return(dummyPlan, nil)

	mPlanAllocator := new(mockPlanAlloctor)
	mPlanAllocator.On("Allocate", mock.Anything, dummyPlan).Return(nil)

	type fields struct {
		chStop        chan struct{}
		emitter       metrics.MetricEmitter
		ccdToDomain   map[int]int
		xDomGroups    sets.String
		domains       domain.Domains
		reader        reader.MBReader
		advisor       advisor.Advisor
		planAllocator allocator.PlanAllocator
	}
	tests := []struct {
		name   string
		fields fields
	}{
		{
			name: "happy path",
			fields: fields{
				emitter:       &metrics.DummyMetrics{},
				ccdToDomain:   map[int]int{0: 0, 1: 0},
				xDomGroups:    nil,
				domains:       nil,
				reader:        mReader,
				advisor:       mAdvisor,
				planAllocator: mPlanAllocator,
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := &MBPlugin{
				chStop:        tt.fields.chStop,
				emitter:       tt.fields.emitter,
				ccdToDomain:   tt.fields.ccdToDomain,
				xDomGroups:    tt.fields.xDomGroups,
				domains:       tt.fields.domains,
				reader:        tt.fields.reader,
				advisor:       tt.fields.advisor,
				planAllocator: tt.fields.planAllocator,
			}
			m.run()
			mock.AssertExpectationsForObjects(t, tt.fields.advisor)
			mock.AssertExpectationsForObjects(t, tt.fields.planAllocator)
		})
	}
}
