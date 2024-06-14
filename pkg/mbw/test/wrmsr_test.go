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

package test

import (
	"testing"

	"github.com/kubewharf/katalyst-core/pkg/mbw/utils/msr"
)

func TestMSRDev_Write(t *testing.T) {
	t.Parallel()
	type args struct {
		regno int64
		value uint64
	}
	tests := []struct {
		name    string
		args    args
		wantErr bool
	}{
		{
			name: "happy path",
			args: args{
				regno: 7,
				value: 123,
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			d := msr.MSRDev{}
			if err := d.Write(tt.args.regno, tt.args.value); (err != nil) != tt.wantErr {
				t.Errorf("Write() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestWriteMSR(t *testing.T) {
	t.Parallel()
	type args struct {
		cpu   uint32
		msr   int64
		value uint64
	}
	tests := []struct {
		name    string
		args    args
		wantErr bool
	}{
		{
			name: "happy path",
			args: args{
				cpu:   1,
				msr:   156,
				value: 123,
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if err := msr.WriteMSR(tt.args.cpu, tt.args.msr, tt.args.value); (err != nil) != tt.wantErr {
				t.Errorf("WriteMSR() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
