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

package domain

import (
	"reflect"
	"testing"

	"k8s.io/apimachinery/pkg/util/sets"
)

func TestNewDomain(t *testing.T) {
	t.Parallel()
	type args struct {
		id              int
		ccds            sets.Int
		capacity        int
		ccdMax          int
		ccdMin          int
		ccdAlienMBLimit int
	}
	tests := []struct {
		name string
		args args
		want *Domain
	}{
		{
			name: "happy path",
			args: args{
				id:              1,
				ccds:            sets.NewInt(4, 5, 6, 7),
				capacity:        60_000,
				ccdMax:          35_000,
				ccdMin:          4_000,
				ccdAlienMBLimit: 5_000,
			},
			want: &Domain{
				ID:              1,
				CCDs:            sets.NewInt(4, 5, 6, 7),
				CapacityInMB:    60_000,
				ccdAlienMBLimit: 5_000,
				MaxMBPerCCD:     35_000,
				MinMBPerCCD:     4_000,
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := NewDomain(tt.args.id, tt.args.ccds, tt.args.capacity,
				tt.args.ccdMax, tt.args.ccdMin, tt.args.ccdAlienMBLimit); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("NewDomain() = %v, want %v", got, tt.want)
			}
		})
	}
}
