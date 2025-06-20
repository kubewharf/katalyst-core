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

import (
	"context"
	"testing"

	"github.com/stretchr/testify/mock"

	malachitetypes "github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/provisioner/malachite/types"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	utilmetric "github.com/kubewharf/katalyst-core/pkg/util/metric"
)

type mockSysFreqDataGetter struct {
	mock.Mock
}

func (m *mockSysFreqDataGetter) GetSysFreqData() (*malachitetypes.SysFreqData, error) {
	args := m.Called()
	return args.Get(0).(*malachitetypes.SysFreqData), args.Error(1)
}

func TestRealtimeFreqMetricsProvisioner_Run(t *testing.T) {
	t.Parallel()

	mockClient := new(mockSysFreqDataGetter)
	mockClient.On("GetSysFreqData").Return(
		&malachitetypes.SysFreqData{
			SysFreq: malachitetypes.SysFreq{
				CPUFreq: []malachitetypes.CurFreq{
					{ScalingCurFreqKHZ: 1_234_000},
				},
				UpdateTime: 77777,
			},
		},
		nil,
	)

	type fields struct {
		dataGetter                  *mockSysFreqDataGetter
		MalachiteMetricsProvisioner *MalachiteMetricsProvisioner
	}
	type args struct {
		ctx context.Context
	}
	tests := []struct {
		name   string
		fields fields
		args   args
	}{
		{
			name: "happy path",
			fields: fields{
				dataGetter: mockClient,
				MalachiteMetricsProvisioner: &MalachiteMetricsProvisioner{
					emitter:     &metrics.DummyMetrics{},
					metricStore: utilmetric.NewMetricStore(),
				},
			},
			args: args{
				ctx: context.TODO(),
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := &RealtimeFreqMetricsProvisioner{
				dataGetter:                  tt.fields.dataGetter,
				MalachiteMetricsProvisioner: tt.fields.MalachiteMetricsProvisioner,
			}
			m.Run(tt.args.ctx)
			tt.fields.dataGetter.AssertExpectations(t)
		})
	}
}
