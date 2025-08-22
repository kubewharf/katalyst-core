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
	"sort"
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

func TestIntSliceHasOverlap(t *testing.T) {
	t.Parallel()

	type args struct {
		a []int
		b []int
	}
	tests := []struct {
		name string
		args args
		want bool
	}{{
		name: "no overlap",
		args: args{
			a: []int{1, 2, 3},
			b: []int{4, 5, 6},
		},
		want: false,
	}, {
		name: "partial overlap",
		args: args{
			a: []int{1, 2, 3},
			b: []int{2, 3, 4},
		},
		want: true,
	}, {
		name: "full overlap",
		args: args{
			a: []int{1, 2, 3},
			b: []int{1, 2, 3},
		},
		want: true,
	}, {
		name: "one empty",
		args: args{
			a: []int{1, 2, 3},
			b: []int{},
		},
		want: false,
	}, {
		name: "both empty",
		args: args{
			a: []int{},
			b: []int{},
		},
		want: false,
	}}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := IntSliceHasOverlap(tt.args.a, tt.args.b); got != tt.want {
				t.Errorf("IntSliceHasOverlap() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGetSlicesIntersection(t *testing.T) {
	t.Parallel()

	type args struct {
		a []int64
		b []int64
	}
	tests := []struct {
		name string
		args args
		want []int64
	}{{
		name: "no intersection",
		args: args{
			a: []int64{1, 2, 3},
			b: []int64{4, 5, 6},
		},
		want: []int64{},
	}, {
		name: "partial intersection",
		args: args{
			a: []int64{1, 2, 3},
			b: []int64{2, 3, 4},
		},
		want: []int64{2, 3},
	}, {
		name: "full intersection",
		args: args{
			a: []int64{1, 2, 3},
			b: []int64{1, 2, 3},
		},
		want: []int64{1, 2, 3},
	}, {
		name: "one empty",
		args: args{
			a: []int64{1, 2, 3},
			b: []int64{},
		},
		want: []int64{},
	}, {
		name: "both empty",
		args: args{
			a: []int64{},
			b: []int64{},
		},
		want: []int64{},
	}}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := GetSlicesIntersection(tt.args.a, tt.args.b)
			// Sort the result to compare
			sort.Slice(got, func(i, j int) bool { return got[i] < got[j] })
			if !equalInt64Slices(got, tt.want) {
				t.Errorf("GetSlicesIntersection() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGetSlicesDiff(t *testing.T) {
	t.Parallel()

	type args struct {
		a []int64
		b []int64
	}
	tests := []struct {
		name string
		args args
		want []int64
	}{{
		name: "no difference",
		args: args{
			a: []int64{1, 2, 3},
			b: []int64{1, 2, 3},
		},
		want: []int64{},
	}, {
		name: "partial difference",
		args: args{
			a: []int64{1, 2, 3, 4},
			b: []int64{2, 3},
		},
		want: []int64{1, 4},
	}, {
		name: "complete difference",
		args: args{
			a: []int64{1, 2, 3},
			b: []int64{4, 5, 6},
		},
		want: []int64{1, 2, 3},
	}, {
		name: "b contains elements not in a",
		args: args{
			a: []int64{1, 2, 3},
			b: []int64{2, 3, 4, 5},
		},
		want: []int64{1},
	}, {
		name: "a is empty",
		args: args{
			a: []int64{},
			b: []int64{1, 2, 3},
		},
		want: []int64{},
	}}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := GetSlicesDiff(tt.args.a, tt.args.b)
			// Sort the result to compare
			sort.Slice(got, func(i, j int) bool { return got[i] < got[j] })
			if !equalInt64Slices(got, tt.want) {
				t.Errorf("GetSlicesDiff() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGetIntersectionOfTwoIntSlices(t *testing.T) {
	t.Parallel()

	type args struct {
		a []int
		b []int
	}
	tests := []struct {
		name string
		args args
		want []int
	}{{
		name: "no intersection",
		args: args{
			a: []int{1, 2, 3},
			b: []int{4, 5, 6},
		},
		want: []int{},
	}, {
		name: "partial intersection",
		args: args{
			a: []int{1, 2, 3},
			b: []int{2, 3, 4},
		},
		want: []int{2, 3},
	}, {
		name: "full intersection",
		args: args{
			a: []int{1, 2, 3},
			b: []int{1, 2, 3},
		},
		want: []int{1, 2, 3},
	}, {
		name: "one empty",
		args: args{
			a: []int{1, 2, 3},
			b: []int{},
		},
		want: []int{},
	}, {
		name: "both empty",
		args: args{
			a: []int{},
			b: []int{},
		},
		want: []int{},
	}}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := GetIntersectionOfTwoIntSlices(tt.args.a, tt.args.b)
			// Sort the result to compare
			sort.Ints(got)
			if !equalIntSlices(got, tt.want) {
				t.Errorf("GetIntersectionOfTwoIntSlices() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSortInt64Slice(t *testing.T) {
	t.Parallel()

	type args struct {
		x []int64
	}
	tests := []struct {
		name string
		args args
		want []int64
	}{{
		name: "empty slice",
		args: args{
			x: []int64{},
		},
		want: []int64{},
	}, {
		name: "already sorted",
		args: args{
			x: []int64{1, 2, 3},
		},
		want: []int64{1, 2, 3},
	}, {
		name: "reverse sorted",
		args: args{
			x: []int64{3, 2, 1},
		},
		want: []int64{1, 2, 3},
	}, {
		name: "random order",
		args: args{
			x: []int64{5, 2, 7, 1, 9},
		},
		want: []int64{1, 2, 5, 7, 9},
	}, {
		name: "with duplicates",
		args: args{
			x: []int64{3, 1, 4, 1, 5, 9, 2, 6},
		},
		want: []int64{1, 1, 2, 3, 4, 5, 6, 9},
	}}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Create a copy to avoid modifying the original slice
			xCopy := make([]int64, len(tt.args.x))
			copy(xCopy, tt.args.x)
			SortInt64Slice(xCopy)
			if !equalInt64Slices(xCopy, tt.want) {
				t.Errorf("SortInt64Slice() = %v, want %v", xCopy, tt.want)
			}
		})
	}
}

func TestConvertInt64SliceToIntSlice(t *testing.T) {
	t.Parallel()

	type args struct {
		a []int64
	}
	tests := []struct {
		name string
		args args
		want []int
	}{{
		name: "empty slice",
		args: args{
			a: []int64{},
		},
		want: []int{},
	}, {
		name: "positive numbers",
		args: args{
			a: []int64{1, 2, 3},
		},
		want: []int{1, 2, 3},
	}, {
		name: "negative numbers",
		args: args{
			a: []int64{-1, -2, -3},
		},
		want: []int{-1, -2, -3},
	}, {
		name: "large numbers",
		args: args{
			a: []int64{9223372036854775807}, // Max int64
		},
		want: []int{9223372036854775807},
	}}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := ConvertInt64SliceToIntSlice(tt.args.a)
			if !equalIntSlices(got, tt.want) {
				t.Errorf("ConvertInt64SliceToIntSlice() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestConvertIntSliceToBitmapString(t *testing.T) {
	t.Parallel()

	type args struct {
		nums []int64
	}
	tests := []struct {
		name    string
		args    args
		want    string
		wantErr bool
	}{{
		name: "empty slice",
		args: args{
			nums: []int64{},
		},
		want:    "",
		wantErr: false,
	}, {
		name: "single number",
		args: args{
			nums: []int64{5},
		},
		want:    "00000020",
		wantErr: false,
	}, {
		name: "multiple numbers",
		args: args{
			nums: []int64{0, 1, 5, 31},
		},
		want:    "80000023",
		wantErr: false,
	}, {
		name: "numbers across buckets",
		args: args{
			nums: []int64{31, 32},
		},
		want:    "00000001,80000000",
		wantErr: false,
	}, {
		name: "negative number",
		args: args{
			nums: []int64{-1},
		},
		want:    "",
		wantErr: true,
	}}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ConvertIntSliceToBitmapString(tt.args.nums)
			if (err != nil) != tt.wantErr {
				t.Errorf("ConvertIntSliceToBitmapString() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("ConvertIntSliceToBitmapString() = %v, want %v", got, tt.want)
			}
		})
	}
}

// Helper functions for comparing slices
func equalInt64Slices(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func equalIntSlices(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
