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

package advisor

import (
	"reflect"
	"testing"

	"k8s.io/apimachinery/pkg/util/sets"
)

func Test_getSortedGroups(t *testing.T) {
	t.Parallel()
	type args struct {
		groups []string
	}
	tests := []struct {
		name string
		args args
		want []sets.String
	}{
		{
			name: "happy path",
			args: args{
				groups: []string{"reclaim", "dedicated", "unknown", "system", "share"},
			},
			want: []sets.String{
				sets.NewString("dedicated", "system"),
				sets.NewString("unknown"),
				sets.NewString("share"),
				sets.NewString("reclaim"),
			},
		},
		{
			name: "with shared sub groups",
			args: args{
				groups: []string{"shared-45", "dedicated", "shared-50", "shared-30"},
			},
			want: []sets.String{
				sets.NewString("dedicated"),
				sets.NewString("shared-50"),
				sets.NewString("shared-45"),
				sets.NewString("shared-30"),
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := getSortedGroups(tt.args.groups); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("getSortedGroups() = %v, want %v", got, tt.want)
			}
		})
	}
}
