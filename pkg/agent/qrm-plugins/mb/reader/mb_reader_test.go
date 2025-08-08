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
	mFetcher := new(mockFetcher)
	mFetcher.On("GetByStringIndex", "realtime.mb").Return(
		&malachitetypes.MBData{
			MBBody: map[string]malachitetypes.MBGroupData{
				"/": {
					0: {
						MBLocal:  222,
						MBRemote: 100,
					},
				},
			},
			UpdateTime: timestamp.Unix(),
		},
	)

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
		want    *malachitetypes.MBData
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
			want: &malachitetypes.MBData{
				MBBody: map[string]malachitetypes.MBGroupData{
					"/": {
						0: {
							MBLocal:  222,
							MBRemote: 100,
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
				metricFetcher: tt.fields.metricFetcher,
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
