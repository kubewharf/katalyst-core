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
	"testing"
)

func TestSliceContains(t *testing.T) {
	t.Parallel()

	type args struct {
		list interface{}
		elem interface{}
	}
	type myType struct {
		name string
		id   int
	}
	tests := []struct {
		name string
		args args
		want bool
	}{
		{
			name: "int - true",
			args: args{
				list: []int{1, 2, 3},
				elem: 1,
			},
			want: true,
		},
		{
			name: "int - false",
			args: args{
				list: []int{1, 2, 3},
				elem: 4,
			},
			want: false,
		},
		{
			name: "string - true",
			args: args{
				list: []string{"1", "2", "3"},
				elem: "1",
			},
			want: true,
		},
		{
			name: "string - false",
			args: args{
				list: []string{"1", "2", "3"},
				elem: "4",
			},
			want: false,
		},
		{
			name: "string - false",
			args: args{
				list: []string{"1", "2", "3"},
				elem: 4,
			},
			want: false,
		},
		{
			name: "string - true",
			args: args{
				list: []string{"1", "2", "3"},
				elem: "1",
			},
			want: true,
		},
		{
			name: "myType - true",
			args: args{
				list: []myType{
					{
						name: "name",
						id:   1,
					},
					{
						name: "name",
						id:   2,
					},
				},
				elem: myType{
					name: "name",
					id:   2,
				},
			},
			want: true,
		},
		{
			name: "myType - false",
			args: args{
				list: []myType{
					{
						name: "name",
						id:   1,
					},
					{
						name: "name",
						id:   2,
					},
				},
				elem: myType{
					name: "name-1",
					id:   2,
				},
			},
			want: false,
		},
		{
			name: "list - nil",
			args: args{
				elem: 1,
			},
			want: false,
		},
		{
			name: "elem - nil",
			args: args{
				list: []int{1, 2, 3},
			},
			want: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := SliceContains(tt.args.list, tt.args.elem); got != tt.want {
				t.Errorf("SliceContains() = %v, want %v", got, tt.want)
			}
		})
	}
}
