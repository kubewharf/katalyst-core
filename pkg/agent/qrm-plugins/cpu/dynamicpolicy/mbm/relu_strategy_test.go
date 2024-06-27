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

package mbm

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
)

func Test_distributeInGroup(t *testing.T) {
	t.Parallel()
	type args struct {
		target    float64
		fairBar   float64
		groupUses map[int]float64
	}
	tests := []struct {
		name string
		args args
		want map[int]float64
	}{
		{
			name: "happy path of single",
			args: args{
				target:    6,
				fairBar:   10,
				groupUses: map[int]float64{8: 15.1},
			},
			want: map[int]float64{8: 6},
		},
		{
			name: "happy path of evens",
			args: args{
				target:    10,
				fairBar:   50,
				groupUses: map[int]float64{0: 70, 1: 70},
			},
			want: map[int]float64{0: 5, 1: 5},
		},
		{
			name: "happy path of odds",
			args: args{
				target:    25,
				fairBar:   50.5,
				groupUses: map[int]float64{3: 120, 4: 40.6},
			},
			want: map[int]float64{3: 25},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := distributeInGroup(tt.args.target, tt.args.fairBar, tt.args.groupUses); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("distributeInGroup() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_calcShares(t *testing.T) {
	t.Parallel()
	type args struct {
		targetDeduction float64
		currentUses     []map[int]float64
	}
	tests := []struct {
		name string
		args args
		want []map[int]float64
	}{
		{
			name: "2 groups with one composite both noisy",
			args: args{
				targetDeduction: 40,
				currentUses:     []map[int]float64{{4: 100}, {3: 150, 5: 80}},
			},
			want: []map[int]float64{{4: 3.33333333333333}, {3: 36.66666666666667}},
		},
		{
			name: "2 groups with one composite quiet",
			args: args{
				targetDeduction: 40,
				currentUses:     []map[int]float64{{2: 160}, {0: 170, 1: 10}},
			},
			want: []map[int]float64{{2: 40}, nil},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := calcShares(tt.args.targetDeduction, tt.args.currentUses); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("calcShares() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_getFairAverage(t *testing.T) {
	t.Parallel()
	type args struct {
		useGroups []map[int]float64
		excessive float64
	}
	tests := []struct {
		name string
		args args
		want float64
	}{
		{
			name: "happy path",
			args: args{
				useGroups: []map[int]float64{{1: 13}, {2: 10, 0: 18}},
				excessive: 7.7,
			},
			want: 11.1,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := getFairAverage(tt.args.useGroups, tt.args.excessive); got != tt.want {
				t.Errorf("getFairAverage() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_calcSharesInThrottleds(t *testing.T) {
	t.Parallel()
	type args struct {
		target       float64
		throttledMBs map[int]float64
	}
	tests := []struct {
		name string
		args args
		want map[int]float64
	}{
		{
			name: "happy path",
			args: args{
				target: 15.0,
				throttledMBs: map[int]float64{
					1: 50,
					4: 100,
				},
			},
			want: map[int]float64{1: 10, 4: 5},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equalf(t, tt.want, calcSharesInThrottleds(tt.args.target, tt.args.throttledMBs), "calcSharesInThrottleds(%v, %v)", tt.args.target, tt.args.throttledMBs)
		})
	}
}
