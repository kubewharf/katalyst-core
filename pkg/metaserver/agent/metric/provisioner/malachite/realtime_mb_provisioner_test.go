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

type mockMBDataGetter struct {
	mock.Mock
}

func (m *mockMBDataGetter) GetMBData() (*malachitetypes.MBData, error) {
	args := m.Called()
	return args.Get(0).(*malachitetypes.MBData), args.Error(1)
}

func TestMalachiteRealtimeMBMetricsProvisioner_Run(t *testing.T) {
	t.Parallel()

	mockDataGetter := new(mockMBDataGetter)
	mockDataGetter.On("GetMBData").Return(&malachitetypes.MBData{
		MBBody:     malachitetypes.MBGroupData{},
		UpdateTime: 111,
	}, nil)

	type fields struct {
		malachiteClient             mbDataGetter
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
				malachiteClient: mockDataGetter,
				MalachiteMetricsProvisioner: &MalachiteMetricsProvisioner{
					metricStore:  utilmetric.NewMetricStore(),
					emitter:      &metrics.DummyMetrics{},
					machineInfo:  nil,
					cpuToNumaMap: nil,
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
			m := &MalachiteRealtimeMBMetricsProvisioner{
				malachiteClient:             tt.fields.malachiteClient,
				MalachiteMetricsProvisioner: tt.fields.MalachiteMetricsProvisioner,
			}
			m.Run(tt.args.ctx)
		})
	}
}
