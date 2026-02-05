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

package sankey

import (
	"reflect"
	"testing"
)

func Test_domainFlower_InvertFlow(t *testing.T) {
	t.Parallel()
	type args struct {
		outgoingLocalRatio []float64
		incoming           []int
	}
	tests := []struct {
		name         string
		args         args
		wantOutgoing []int
		wantErr      bool
	}{
		{
			name: "happy path of all local",
			args: args{
				outgoingLocalRatio: []float64{1.0, 1.0},
				incoming:           []int{1_234, 5_678},
			},
			wantOutgoing: []int{1_234, 5_678},
			wantErr:      false,
		},
		{
			name: "happy path of cross domains",
			args: args{
				outgoingLocalRatio: []float64{0.8, 0.5},
				incoming:           []int{1_550, 950},
			},
			wantOutgoing: []int{999, 1_500},
			wantErr:      false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			d := domainFlower{}
			gotOutgoing, err := d.InvertFlow(tt.args.outgoingLocalRatio, tt.args.incoming)
			if (err != nil) != tt.wantErr {
				t.Errorf("InvertFlow() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(gotOutgoing, tt.wantOutgoing) {
				t.Errorf("InvertFlow() gotOutgoing = %v, want %v", gotOutgoing, tt.wantOutgoing)
			}
		})
	}
}
