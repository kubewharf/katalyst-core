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

package mbdomain

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func Test_isIncubatedBy(t *testing.T) {
	t.Parallel()
	type args struct {
		graduateTime time.Time
		by           time.Time
	}
	tests := []struct {
		name string
		args args
		want bool
	}{
		{
			name: "happy path",
			args: args{
				graduateTime: time.Date(2024, 10, 18, 14, 0, 0, 0, time.UTC),
				by:           time.Date(2024, 10, 18, 13, 0, 0, 0, time.UTC),
			},
			want: true,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equalf(t, tt.want, isIncubatedBy(tt.args.graduateTime, tt.args.by), "isIncubatedBy(%v, %v)", tt.args.graduateTime, tt.args.by)
		})
	}
}
