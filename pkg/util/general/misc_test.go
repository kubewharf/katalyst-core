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
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func TestParseLinuxListFormat(t *testing.T) {
	t.Parallel()

	type args struct {
		listStr string
	}
	tests := []struct {
		name    string
		args    args
		want    []int64
		wantErr bool
	}{
		{
			name: "empty string",
			args: args{
				listStr: "",
			},
			want:    nil,
			wantErr: false,
		},
		{
			name: "single number",
			args: args{
				listStr: "5",
			},
			want:    []int64{5},
			wantErr: false,
		},
		{
			name: "multiple numbers",
			args: args{
				listStr: "1,3,5",
			},
			want:    []int64{1, 3, 5},
			wantErr: false,
		},
		{
			name: "range",
			args: args{
				listStr: "1-3",
			},
			want:    []int64{1, 2, 3},
			wantErr: false,
		},
		{
			name: "mixed",
			args: args{
				listStr: "1-3,5,7-9",
			},
			want:    []int64{1, 2, 3, 5, 7, 8, 9},
			wantErr: false,
		},
		{
			name: "invalid format",
			args: args{
				listStr: "1-3-5",
			},
			want:    nil,
			wantErr: true,
		},
		{
			name: "invalid number",
			args: args{
				listStr: "1,a,3",
			},
			want:    nil,
			wantErr: true,
		},
		{
			name: "start >= end",
			args: args{
				listStr: "3-1",
			},
			want:    nil,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseLinuxListFormat(tt.args.listStr)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseLinuxListFormat() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !equalInt64Slices(got, tt.want) {
				t.Errorf("ParseLinuxListFormat() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestParseLinuxListFormatFromFile(t *testing.T) {
	t.Parallel()
	// Create a temporary file for testing
	tempDir := t.TempDir()
	fileName := filepath.Join(tempDir, "test.txt")

	type args struct {
		filePath string
		content  string
	}
	tests := []struct {
		name    string
		args    args
		want    []int64
		wantErr bool
	}{
		{
			name: "empty file",
			args: args{
				filePath: fileName + "-1",
				content:  "",
			},
			want:    nil,
			wantErr: true,
		},
		{
			name: "valid content",
			args: args{
				filePath: fileName + "-2",
				content:  "1-3,5,7-9",
			},
			want:    []int64{1, 2, 3, 5, 7, 8, 9},
			wantErr: false,
		},
		{
			name: "non-existent file",
			args: args{
				filePath: filepath.Join(tempDir, "non-existent.txt"),
				content:  "",
			},
			want:    nil,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		tt := tt

		writeFile := func() error {
			if tt.args.content != "" {
				return os.WriteFile(tt.args.filePath, []byte(tt.args.content), 0o644)
			}
			return nil
		}

		if err := writeFile(); err != nil {
			t.Fatalf("Failed to write test file: %v", err)
		}

		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseLinuxListFormatFromFile(tt.args.filePath)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseLinuxListFormatFromFile() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !equalInt64Slices(got, tt.want) {
				t.Errorf("ParseLinuxListFormatFromFile() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestConvertLinuxListToString(t *testing.T) {
	t.Parallel()

	type args struct {
		numbers []int64
	}
	tests := []struct {
		name string
		args args
		want string
	}{{
		name: "empty slice",
		args: args{
			numbers: []int64{},
		},
		want: "",
	}, {
		name: "single number",
		args: args{
			numbers: []int64{5},
		},
		want: "5",
	}, {
		name: "multiple numbers",
		args: args{
			numbers: []int64{1, 3, 5},
		},
		want: "1,3,5",
	}, {
		name: "consecutive numbers",
		args: args{
			numbers: []int64{1, 2, 3},
		},
		want: "1-3",
	}, {
		name: "mixed",
		args: args{
			numbers: []int64{1, 2, 3, 5, 7, 8, 9},
		},
		want: "1-3,5,7-9",
	}, {
		name: "unsorted numbers",
		args: args{
			numbers: []int64{3, 1, 5, 2, 4},
		},
		want: "1-5",
	}}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := ConvertLinuxListToString(tt.args.numbers); got != tt.want {
				t.Errorf("ConvertLinuxListToString() = %v, want %v", got, tt.want)
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

func TestReadLines(t *testing.T) {
	t.Parallel()

	// Create a temporary file for testing
	tempDir := t.TempDir()
	fileName := filepath.Join(tempDir, "test.txt")

	type args struct {
		filePath string
		content  string
	}
	tests := []struct {
		name    string
		args    args
		want    []string
		wantErr bool
	}{{
		name: "empty file",
		args: args{
			filePath: fileName + "-1",
			content:  "",
		},
		want:    []string{},
		wantErr: true,
	}, {
		name: "single line",
		args: args{
			filePath: fileName + "-2",
			content:  "hello world",
		},
		want:    []string{"hello world"},
		wantErr: false,
	}, {
		name: "multiple lines",
		args: args{
			filePath: fileName + "-3",
			content:  "line1\nline2\nline3",
		},
		want:    []string{"line1", "line2", "line3"},
		wantErr: false,
	}, {
		name: "non-existent file",
		args: args{
			filePath: filepath.Join(tempDir, "non-existent.txt"),
			content:  "",
		},
		want:    nil,
		wantErr: true,
	}}

	for _, tt := range tests {
		tt := tt
		wantErr := tt.wantErr

		writeFile := func() error {
			if !wantErr && tt.args.content != "" {
				return os.WriteFile(tt.args.filePath, []byte(tt.args.content), 0o644)
			}
			return nil
		}

		if err := writeFile(); err != nil {
			t.Fatalf("Failed to write test file: %v", err)
		}

		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ReadLines(tt.args.filePath)
			if (err != nil) != tt.wantErr {
				t.Errorf("ReadLines() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !equalStringSlices(got, tt.want) {
				t.Errorf("ReadLines() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestReadInt64FromFile(t *testing.T) {
	t.Parallel()

	// Create a temporary file for testing
	tempDir := t.TempDir()
	fileName := filepath.Join(tempDir, "test.txt")

	type args struct {
		filePath string
		content  string
	}
	tests := []struct {
		name    string
		args    args
		want    int64
		wantErr bool
	}{{
		name: "empty file",
		args: args{
			filePath: fileName + "-1",
			content:  "",
		},
		want:    -1,
		wantErr: true,
	}, {
		name: "valid number",
		args: args{
			filePath: fileName + "-2",
			content:  "123",
		},
		want:    123,
		wantErr: false,
	}, {
		name: "negative number",
		args: args{
			filePath: fileName + "-3",
			content:  "-456",
		},
		want:    -456,
		wantErr: false,
	}, {
		name: "invalid number",
		args: args{
			filePath: fileName + "-4",
			content:  "abc",
		},
		want:    -1,
		wantErr: true,
	}, {
		name: "non-existent file",
		args: args{
			filePath: filepath.Join(tempDir, "non-existent.txt"),
			content:  "",
		},
		want:    -1,
		wantErr: true,
	}}

	for _, tt := range tests {
		tt := tt

		writeFile := func() error {
			if tt.args.content != "" {
				return os.WriteFile(tt.args.filePath, []byte(tt.args.content), 0o644)
			}
			return nil
		}

		if err := writeFile(); err != nil {
			t.Fatalf("Failed to write test file: %v", err)
		}

		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ReadInt64FromFile(tt.args.filePath)
			if (err != nil) != tt.wantErr {
				t.Errorf("ReadInt64FromFile() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("ReadInt64FromFile() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestReadUint64FromFile(t *testing.T) {
	t.Parallel()

	// Create a temporary file for testing
	tempDir := t.TempDir()
	fileName := filepath.Join(tempDir, "test.txt")

	type args struct {
		filePath string
		content  string
	}
	tests := []struct {
		name    string
		args    args
		want    uint64
		wantErr bool
	}{{
		name: "valid number",
		args: args{
			filePath: fileName + "-1",
			content:  "123",
		},
		want:    123,
		wantErr: false,
	}, {
		name: "large number",
		args: args{
			filePath: fileName + "-2",
			content:  "18446744073709551615",
		},
		want:    18446744073709551615,
		wantErr: false,
	}, {
		name: "invalid number",
		args: args{
			filePath: fileName + "-3",
			content:  "abc",
		},
		want:    0,
		wantErr: true,
	}, {
		name: "negative number",
		args: args{
			filePath: fileName + "-4",
			content:  "-456",
		},
		want:    0,
		wantErr: true,
	}, {
		name: "empty file",
		args: args{
			filePath: fileName + "-5",
			content:  "",
		},
		want:    0,
		wantErr: true,
	}, {
		name: "non-existent file",
		args: args{
			filePath: filepath.Join(tempDir, "non-existent.txt"),
			content:  "",
		},
		want:    0,
		wantErr: true,
	}}

	for _, tt := range tests {
		tt := tt

		writeFile := func() error {
			if tt.args.content != "" {
				return os.WriteFile(tt.args.filePath, []byte(tt.args.content), 0o644)
			}
			return nil
		}

		if err := writeFile(); err != nil {
			t.Fatalf("Failed to write test file: %v", err)
		}

		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ReadUint64FromFile(tt.args.filePath)
			if (err != nil) != tt.wantErr {
				t.Errorf("ReadUint64FromFile() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("ReadUint64FromFile() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGetFileInode(t *testing.T) {
	t.Parallel()

	// Create a temporary file for testing
	tempDir := t.TempDir()
	fileName := filepath.Join(tempDir, "test.txt")
	if err := os.WriteFile(fileName, []byte("content"), 0o644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	type args struct {
		file string
	}
	tests := []struct {
		name    string
		args    args
		want    uint64
		wantErr bool
	}{{
		name: "valid file",
		args: args{
			file: fileName,
		},
		want:    0, // We can't know the exact inode, but we expect it to be non-zero
		wantErr: false,
	}, {
		name: "non-existent file",
		args: args{
			file: filepath.Join(tempDir, "non-existent.txt"),
		},
		want:    0,
		wantErr: true,
	}}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := GetFileInode(tt.args.file)
			if (err != nil) != tt.wantErr {
				t.Errorf("GetFileInode() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.name == "valid file" {
				if got == 0 {
					t.Error("GetFileInode() returned 0 for valid file")
				}
			} else if got != tt.want {
				t.Errorf("GetFileInode() = %v, want %v", got, tt.want)
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
