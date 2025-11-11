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

package distributor

import (
	"reflect"
	"testing"
)

func Test_linearBoundedDistributor_Distribute(t *testing.T) {
	t.Parallel()
	type fields struct {
		min int
		max int
	}
	type args struct {
		total   int
		weights map[int]int
	}
	tests := []struct {
		name   string
		fields fields
		args   args
		want   map[int]int
	}{
		{
			name: "happy path",
			fields: fields{
				min: 1_000,
				max: 10_000,
			},
			args: args{
				total: 30_000,
				weights: map[int]int{
					0: 5_000,
					2: 5_000,
					5: 10_000,
				},
			},
			want: map[int]int{
				0: 7_500,
				2: 7_500,
				5: 10_000,
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			l := &linearBoundedDistributor{
				min: tt.fields.min,
				max: tt.fields.max,
			}
			if got := l.Distribute(tt.args.total, tt.args.weights); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Distribute() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_logarithmicBoundedDistributor_Distribute(t *testing.T) {
	t.Parallel()

	type fields struct {
		inner *linearBoundedDistributor
	}
	type args struct {
		total   int
		weights map[int]int
	}
	tests := []struct {
		name   string
		fields fields
		args   args
		want   map[int]int
	}{
		{
			name: "happy path",
			fields: fields{
				inner: &linearBoundedDistributor{},
			},
			args: args{
				total: 27_000,
				weights: map[int]int{
					8: 8192,
					9: 8192 * 2,
				},
			},
			want: map[int]int{
				8: 13_000,
				9: 14_000,
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			l := logarithmicBoundedDistributor{
				inner: tt.fields.inner,
			}
			if got := l.Distribute(tt.args.total, tt.args.weights); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Distribute() = %v, want %v", got, tt.want)
			}
		})
	}
}
