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

package monitor

import (
	"reflect"
	"testing"
)

func TestGroupMBStats_NormalizeShareSubgroups(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                 string
		gms                  GroupMBStats
		wantStats            GroupMBStats
		wantIsSharedSubgroup bool
	}{
		{
			name: "happy path with shared subgroup",
			gms: map[string]GroupMB{
				"shared-30": map[int]MBInfo{},
				"/":         map[int]MBInfo{},
				"shared-60": map[int]MBInfo{},
			},
			wantStats: map[string]GroupMB{
				"share-30": map[int]MBInfo{},
				"/":        map[int]MBInfo{},
				"share-60": map[int]MBInfo{},
			},
			wantIsSharedSubgroup: true,
		},
		{
			name: "happy path without shared subgroup",
			gms: map[string]GroupMB{
				"share-30": map[int]MBInfo{},
				"/":        map[int]MBInfo{},
				"share":    map[int]MBInfo{},
			},
			wantStats: map[string]GroupMB{
				"share-30": map[int]MBInfo{},
				"/":        map[int]MBInfo{},
				"share":    map[int]MBInfo{},
			},
			wantIsSharedSubgroup: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gotStats, gotIsSharedSubgroup := tt.gms.NormalizeShareSubgroups()
			if !reflect.DeepEqual(gotStats, tt.wantStats) {
				t.Errorf("NormalizeShareSubgroups() gotStats = %v, want %v", gotStats, tt.wantStats)
			}
			if gotIsSharedSubgroup != tt.wantIsSharedSubgroup {
				t.Errorf("NormalizeShareSubgroups() gotIsSharedSubgroup = %v, want %v", gotIsSharedSubgroup, tt.wantIsSharedSubgroup)
			}
		})
	}
}
