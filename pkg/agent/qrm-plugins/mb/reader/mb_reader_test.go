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
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/monitor"
	malachitetypes "github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/provisioner/malachite/types"
)

func Test_isDataFresh(t *testing.T) {
	t.Parallel()
	type args struct {
		epocElapsed int64
		now         time.Time
	}
	tests := []struct {
		name string
		args args
		want bool
	}{
		{
			name: "happy path",
			args: args{
				epocElapsed: time.Date(2025, 7, 18, 12, 29, 58, 0, time.UTC).Unix(),
				now:         time.Date(2025, 7, 18, 12, 30, 0, 0, time.UTC),
			},
			want: true,
		},
		{
			name: "stale",
			args: args{
				epocElapsed: time.Date(2025, 7, 18, 12, 29, 30, 0, time.UTC).Unix(),
				now:         time.Date(2025, 7, 18, 12, 30, 0, 0, time.UTC),
			},
			want: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := isDataFresh(tt.args.epocElapsed, tt.args.now); got != tt.want {
				t.Errorf("isDataFresh() = %v, want %v", got, tt.want)
			}
		})
	}
}

type mockFetcher struct {
	mock.Mock
}

func (m *mockFetcher) GetByStringIndex(metricName string) interface{} {
	args := m.Called(metricName)
	return args.Get(0)
}

func Test_metaServerMBReader_getMBData(t *testing.T) {
	t.Parallel()

	timestamp := time.Date(2025, 7, 18, 9, 10, 11, 0, time.UTC)
	rawCounter1 := &malachitetypes.MBData{
		MBBody: malachitetypes.MBGroupData{
			"root": {
				{
					CCDID:          1,
					MBLocalCounter: 11111 * 1024,
					MBTotalCounter: 11111 * 1024,
				},
			},
			"shared-50": {
				{
					CCDID:          2,
					MBLocalCounter: 11111 * 1024,
					MBTotalCounter: 11111 * 1024,
				},
			},
		},
		UpdateTime: timestamp.Add(-1 * time.Second).Unix(),
	}
	rawCounter2 := &malachitetypes.MBData{
		MBBody: malachitetypes.MBGroupData{
			"root": {
				{
					CCDID:          1,
					MBLocalCounter: 22222 * 1024,
					MBTotalCounter: 22222 * 1024,
				},
			},
			"shared-50": {
				{
					CCDID:          2,
					MBLocalCounter: 22222 * 1024,
					MBTotalCounter: 22222 * 1024,
				},
			},
		},
		UpdateTime: timestamp.Unix(),
	}
	mFetcher := new(mockFetcher)
	mFetcher.On("GetByStringIndex", "realtime.mb").Return(rawCounter2)

	type fields struct {
		metricFetcher interface {
			GetByStringIndex(metricName string) interface{}
		}
	}
	type args struct {
		now time.Time
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		want    *MBData
		wantErr bool
	}{
		{
			name: "happy path",
			fields: fields{
				metricFetcher: mFetcher,
			},
			args: args{
				now: timestamp,
			},
			want: &MBData{
				MBBody: monitor.GroupMBStats{
					"/": {
						1: {
							LocalMB:  10,
							RemoteMB: 0,
							TotalMB:  10,
						},
					},
					"shared-50": {
						2: {
							LocalMB:  10,
							RemoteMB: 0,
							TotalMB:  10,
						},
					},
				},
				UpdateTime: timestamp.Unix(),
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := &metaServerMBReader{
				metricFetcher:   tt.fields.metricFetcher,
				currCounterData: rawCounter1,
			}
			got, err := m.getMBData(tt.args.now)
			if (err != nil) != tt.wantErr {
				t.Errorf("getMBData() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("getMBData() got = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_calcRateData(t *testing.T) {
	t.Parallel()
	type args struct {
		newCounter malachitetypes.MBGroupData
		oldCounter malachitetypes.MBGroupData
		elspsed    time.Duration
	}
	tests := []struct {
		name    string
		args    args
		want    monitor.GroupMBStats
		wantErr bool
	}{
		{
			name: "happy path",
			args: args{
				oldCounter: malachitetypes.MBGroupData{
					"dedicated": {
						{
							CCDID:          5,
							MBLocalCounter: 2 * 1024 * 1024,
							MBTotalCounter: 3 * 1024 * 1024,
						},
					},
				},
				newCounter: malachitetypes.MBGroupData{
					"dedicated": {
						{
							CCDID:          5,
							MBLocalCounter: 7 * 1024 * 1024,
							MBTotalCounter: 10 * 1024 * 1024,
						},
					},
				},
				elspsed: 1 * time.Second,
			},
			want: monitor.GroupMBStats{
				"dedicated": {
					5: {
						LocalMB:  5,
						RemoteMB: 2,
						TotalMB:  7,
					},
				},
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := calcMBRate(tt.args.newCounter, tt.args.oldCounter, tt.args.elspsed)
			if (err != nil) != tt.wantErr {
				t.Errorf("calcRateData() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("calcRateData() got = %v, want %v", got, tt.want)
			}
		})
	}
}
