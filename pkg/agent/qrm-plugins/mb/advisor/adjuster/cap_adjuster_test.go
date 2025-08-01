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

package adjuster

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/mock"
)

type mockInner struct {
	mock.Mock
}

func (m *mockInner) AdjustOutgoingTargets(targets []int, currents []int) []int {
	args := m.Called(targets, currents)
	return args.Get(0).([]int)
}

func Test_capAdjuster_AdjustOutgoingTargets(t *testing.T) {
	t.Parallel()

	mInner := new(mockInner)
	mInner.On("AdjustOutgoingTargets", []int{10_000, 10_000}, []int{20_000, 5_000}).Return([]int{5_000, 20_000})

	type fields struct {
		inner Adjuster
	}
	type args struct {
		targets  []int
		currents []int
	}
	tests := []struct {
		name   string
		fields fields
		args   args
		want   []int
	}{
		{
			name: "happy path",
			fields: fields{
				inner: mInner,
			},
			args: args{
				targets:  []int{10_000, 10_000},
				currents: []int{20_000, 5_000},
			},
			want: []int{5_000, 10_000},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := &capAdjuster{
				inner: tt.fields.inner,
			}
			if got := c.AdjustOutgoingTargets(tt.args.targets, tt.args.currents); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("AdjustOutgoingTargets() = %v, want %v", got, tt.want)
			}
		})
	}
}
