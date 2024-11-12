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
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	malachitetypes "github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/provisioner/malachite/types"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	utilmetric "github.com/kubewharf/katalyst-core/pkg/util/metric"
)

func TestMalachiteRealtimeMetricsProvisioner_processSystemPowerData(t *testing.T) {
	t.Parallel()

	testTimestamp := time.Date(2024, 11, 12, 0, 0, 0, 0, time.Local)
	testUnixTime := testTimestamp.Unix()

	type fields struct {
		MalachiteMetricsProvisioner *MalachiteMetricsProvisioner
	}
	type args struct {
		data *malachitetypes.PowerData
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		want    utilmetric.MetricData
		wantErr assert.ErrorAssertionFunc
	}{
		{
			name: "happy path",
			fields: fields{
				MalachiteMetricsProvisioner: &MalachiteMetricsProvisioner{
					metricStore: utilmetric.NewMetricStore(),
				},
			},
			args: args{
				data: &malachitetypes.PowerData{
					Sensors: malachitetypes.SensorData{
						TotalPowerWatt: 123,
						UpdateTime:     testUnixTime,
					},
				},
			},
			want: utilmetric.MetricData{
				Value: float64(123),
				Time:  &testTimestamp,
			},
			wantErr: assert.NoError,
		},
		{
			name: "negative case of nil input",
			fields: fields{
				MalachiteMetricsProvisioner: &MalachiteMetricsProvisioner{
					metricStore: utilmetric.NewMetricStore(),
				},
			},
			args: args{
				data: nil,
			},
			wantErr: assert.Error,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := &MalachiteRealtimeMetricsProvisioner{
				MalachiteMetricsProvisioner: tt.fields.MalachiteMetricsProvisioner,
			}
			m.processSystemPowerData(tt.args.data)
			saved, err := tt.fields.MalachiteMetricsProvisioner.metricStore.GetNodeMetric("total.power.used.watts")
			if !tt.wantErr(t, err) {
				return
			}
			assert.Equal(t, tt.want, saved)
		})
	}
}

type mockPowerDataGetter struct {
	mock.Mock
}

func (m *mockPowerDataGetter) GetPowerData() (*malachitetypes.PowerData, error) {
	args := m.Called()
	return args.Get(0).(*malachitetypes.PowerData), args.Error(1)
}

func TestMalachiteRealtimeMetricsProvisioner_Run(t *testing.T) {
	t.Parallel()

	mockPowerDataClient := new(mockPowerDataGetter)
	mockPowerDataClient.On("GetPowerData").Return(
		&malachitetypes.PowerData{Sensors: malachitetypes.SensorData{TotalPowerWatt: 345, UpdateTime: 77777}},
		nil,
	)

	type fields struct {
		malachiteClient             *mockPowerDataGetter
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
				malachiteClient: mockPowerDataClient,
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
			m := &MalachiteRealtimeMetricsProvisioner{
				malachiteClient:             tt.fields.malachiteClient,
				MalachiteMetricsProvisioner: tt.fields.MalachiteMetricsProvisioner,
			}
			m.Run(tt.args.ctx)
			tt.fields.malachiteClient.AssertExpectations(t)
		})
	}
}
