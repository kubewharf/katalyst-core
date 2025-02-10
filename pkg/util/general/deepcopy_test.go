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

package general

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDeepCopyIntToIntMap(t *testing.T) {
	t.Parallel()
	type args struct {
		origin map[int]int
	}
	tests := []struct {
		name string
		args args
		want map[int]int
	}{
		{
			name: "deep copy int to int map normally",
			args: args{
				origin: map[int]int{1: 1},
			},
			want: map[int]int{
				1: 1,
			},
		},
		{
			name: "deep copy nil int to int map",
			args: args{
				origin: nil,
			},
			want: nil,
		},
	}
	for _, tt := range tests {
		curTT := tt
		t.Run(curTT.name, func(t *testing.T) {
			t.Parallel()
			if got := DeepCopyIntToIntMap(curTT.args.origin); !reflect.DeepEqual(got, curTT.want) {
				t.Errorf("DeepCopyIntToIntMap() = %v, want %v", got, curTT.want)
			}
		})
	}
}

func TestDeepCopyIntToStringMap(t *testing.T) {
	t.Parallel()
	type args struct {
		origin map[int]string
	}
	tests := []struct {
		name string
		args args
		want map[int]string
	}{
		{
			name: "deep copy int to string map normally",
			args: args{
				origin: map[int]string{1: "1"},
			},
			want: map[int]string{
				1: "1",
			},
		},
		{
			name: "deep copy nil int to int map",
			args: args{
				origin: nil,
			},
			want: nil,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equalf(t, tt.want, DeepCopyIntToStringMap(tt.args.origin), "DeepCopyIntToStringMap(%v)", tt.args.origin)
		})
	}
}
