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

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/util/sets"
)

func TestMax(t *testing.T) {
	t.Parallel()

	as := require.New(t)
	as.Equal(2, Max(1, 2))
}

func TestMaxUInt64(t *testing.T) {
	t.Parallel()

	as := require.New(t)
	as.Equal(uint64(2), MaxUInt64(1, 2))
}

func TestMinUInt64(t *testing.T) {
	t.Parallel()

	as := require.New(t)
	as.Equal(uint64(2), MaxUInt64(1, 2))
}

func TestMaxInt64(t *testing.T) {
	t.Parallel()

	as := require.New(t)
	as.Equal(int64(2), MaxInt64(1, 2))
}

func TestGetValueWithDefault(t *testing.T) {
	t.Parallel()

	as := require.New(t)
	as.Equal("5", GetValueWithDefault(map[string]string{"2": "2"}, "1", "5"))
	as.Equal("2", GetValueWithDefault(map[string]string{"1": "2"}, "1", "5"))
}

func TestIsNameEnabled(t *testing.T) {
	t.Parallel()

	as := require.New(t)
	as.Equal(true, IsNameEnabled("test", sets.NewString(), []string{"test"}))
	as.Equal(true, IsNameEnabled("test", sets.NewString(), []string{"*"}))
	as.Equal(false, IsNameEnabled("test", sets.NewString(), []string{}))
}

func TestParseUint64PointerToString(t *testing.T) {
	t.Parallel()

	as := require.New(t)
	var a uint64 = 5
	as.Equal(ParseUint64PointerToString(&a), "5")
}

func TestParseStringToUint64Pointer(t *testing.T) {
	t.Parallel()

	as := require.New(t)
	p, err := ParseStringToUint64Pointer("5")
	as.Nil(err)
	as.Equal(uint64(5), *p)
}

func TestGetInt64PointerFromUint64Pointer(t *testing.T) {
	t.Parallel()

	as := require.New(t)
	var a uint64 = 5
	p, err := GetInt64PointerFromUint64Pointer(&a)
	as.Nil(err)
	as.Equal(int64(5), *p)
}

func TestGetStringValueFromMap(t *testing.T) {
	t.Parallel()

	as := require.New(t)
	as.Equal("a", GetStringValueFromMap(map[string]string{"labelA": "a"}, "labelA"))
	as.Equal("", GetStringValueFromMap(map[string]string{"labelB": "a"}, "labelA"))
}

func TestGenerateHash(t *testing.T) {
	t.Parallel()

	as := require.New(t)
	as.Greater(len(GenerateHash([]byte{60, 60}, 5)), 0)
}

func TestCheckMapEqual(t *testing.T) {
	t.Parallel()

	as := require.New(t)
	as.Equal(true, CheckMapEqual(map[string]string{"labelA": "a"}, map[string]string{"labelA": "a"}))
	as.Equal(false, CheckMapEqual(map[string]string{"labelB": "a"}, map[string]string{"labelA": "a"}))
}

func TestUIntPointerToFloat64(t *testing.T) {
	t.Parallel()

	as := require.New(t)
	var a uint = 5
	as.Equal(5.0, UIntPointerToFloat64(&a))
}

func TestUInt64PointerToFloat64(t *testing.T) {
	t.Parallel()

	as := require.New(t)
	var a uint64 = 5
	as.Equal(5.0, UInt64PointerToFloat64(&a))
}

func TestJsonPathEmpty(t *testing.T) {
	t.Parallel()

	as := require.New(t)
	as.Equal(true, JsonPathEmpty([]byte("{}")))
	as.Equal(true, JsonPathEmpty([]byte("")))
}

func TestFormatMemoryQutantity(t *testing.T) {
	t.Parallel()
	as := require.New(t)
	as.Equal("1024[1Ki]", FormatMemoryQuantity(1<<10))
	as.Equal("1.048576e+06[1Mi]", FormatMemoryQuantity(1<<20))
	as.Equal("1.073741824e+09[1Gi]", FormatMemoryQuantity(1<<30))
}

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
