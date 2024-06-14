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

func TestMSRDev_Read(t *testing.T) {
	t.Parallel()
	type args struct {
		msr int64
	}
	tests := []struct {
		name    string
		args    args
		want    uint64
		wantErr bool
	}{
		{
			name: "happy path",
			args: args{
				msr: 22,
			},
			want:    0x3B00000000000000,
			wantErr: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			d := msr.MSRDev{}
			got, err := d.Read(tt.args.msr)
			if (err != nil) != tt.wantErr {
				t.Errorf("Read() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("Read() got = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestReadMSR(t *testing.T) {
	t.Parallel()
	type args struct {
		cpu uint32
		msr int64
	}
	tests := []struct {
		name    string
		args    args
		want    uint64
		wantErr bool
	}{
		{
			name: "happy path",
			args: args{
				cpu: 1,
				msr: 5,
			},
			want:    0x3B00000000000000,
			wantErr: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := msr.ReadMSR(tt.args.cpu, tt.args.msr)
			if (err != nil) != tt.wantErr {
				t.Errorf("ReadMSR() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("ReadMSR() got = %v, want %v", got, tt.want)
			}
		})
	}
}
