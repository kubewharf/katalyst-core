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

package datasource

import (
	"reflect"
	"testing"
)

func TestGetSamplesOverview(t *testing.T) {
	type args struct {
		timeSeries *TimeSeries
	}
	tests := []struct {
		name string
		args args
		want *SamplesOverview
	}{
		{
			name: "Empty time series",
			args: args{
				timeSeries: &TimeSeries{Samples: []Sample{}},
			},
			want: nil,
		},
		{
			name: "Non-empty time series",
			args: args{
				timeSeries: &TimeSeries{
					Samples: []Sample{
						{Value: 10, Timestamp: 1630838400},
						{Value: 20, Timestamp: 1630842000},
						{Value: 15, Timestamp: 1630845600},
					},
				},
			},
			want: &SamplesOverview{
				AvgValue:            15,
				MinValue:            10,
				MaxValue:            20,
				Percentile50thValue: 15,
				Percentile90thValue: 17.5,
				FirstTimestamp:      1630838400,
				LastTimestamp:       1630845600,
				Count:               3,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := GetSamplesOverview(tt.args.timeSeries); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("GetSamplesOverview() = %v, want %v", got, tt.want)
			}
		})
	}
}
