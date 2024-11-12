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

package reader

import (
	"context"
	"testing"
	"time"

	utilmetric "github.com/kubewharf/katalyst-core/pkg/util/metric"
)

func Test_metricStorePowerReader_get(t *testing.T) {
	t.Parallel()

	setTime := time.Date(2024, 11, 11, 8, 30, 10, 0, time.UTC)
	dummyMetricStore := utilmetric.NewMetricStore()
	dummyMetricStore.SetNodeMetric("total.power.used.watts", utilmetric.MetricData{
		Value: 999,
		Time:  &setTime,
	})

	type fields struct {
		metricStore *utilmetric.MetricStore
	}
	type args struct {
		ctx context.Context
		now time.Time
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		want    int
		wantErr bool
	}{
		{
			name: "happy path",
			fields: fields{
				metricStore: dummyMetricStore,
			},
			args: args{
				ctx: context.TODO(),
				now: time.Date(2024, 11, 11, 8, 30, 11, 0, time.UTC),
			},
			want:    999,
			wantErr: false,
		},
		{
			name: "stale data",
			fields: fields{
				metricStore: dummyMetricStore,
			},
			args: args{
				ctx: context.TODO(),
				now: time.Date(2024, 11, 11, 8, 30, 13, 0, time.UTC),
			},
			want:    0,
			wantErr: true,
		},
		{
			name: "no such metric in store",
			fields: fields{
				metricStore: utilmetric.NewMetricStore(),
			},
			args: args{
				ctx: context.TODO(),
				now: time.Date(2024, 11, 11, 8, 30, 13, 0, time.UTC),
			},
			want:    0,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := NewMetricStorePowerReader(tt.fields.metricStore).(*metricStorePowerReader)
			got, err := m.get(tt.args.ctx, tt.args.now)
			if (err != nil) != tt.wantErr {
				t.Errorf("get() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("get() got = %v, want %v", got, tt.want)
			}
		})
	}
}
