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

package cgnode

import (
	"reflect"
	"testing"
)

func Test_parse(t *testing.T) {
	t.Parallel()
	type args struct {
		content string
	}
	tests := []struct {
		name    string
		args    args
		want    []int
		wantErr bool
	}{
		{
			name: "happy path of single value",
			args: args{
				content: "12",
			},
			want:    []int{12},
			wantErr: false,
		},
		{
			name: "happy path of single value and new line",
			args: args{
				content: "12\n",
			},
			want:    []int{12},
			wantErr: false,
		},
		{
			name: "x,y both",
			args: args{
				content: "2,3",
			},
			want:    []int{2, 3},
			wantErr: false,
		},
		{
			name: "x-y range",
			args: args{
				content: "4-7",
			},
			want:    []int{4, 5, 6, 7},
			wantErr: false,
		},
		{
			name: "multi lines",
			args: args{
				content: "1\n 4-7,22-23 \n89,90\n",
			},
			want:    []int{1, 4, 5, 6, 7, 22, 23, 89, 90},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := parse(tt.args.content)
			if (err != nil) != tt.wantErr {
				t.Errorf("parse() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parse() got = %v, want %v", got, tt.want)
			}
		})
	}
}

func Benchmark_parseSingleRange(b *testing.B) {
	// run the Fib function b.N times
	for n := 0; n < b.N; n++ {
		_, _ = parseSingleRange("4-7")
	}
}
