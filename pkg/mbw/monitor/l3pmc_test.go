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
	"context"
	"sync"
	"testing"
)

func TestMBMonitor_StartL3PMCEvent(t *testing.T) {
	t.Parallel()
	type fields struct {
		SysInfo     *SysInfo
		Controller  MBController
		Interval    uint64
		Started     bool
		MonitorOnly bool
		mctx        context.Context
		done        context.CancelFunc
	}
	type args struct {
		cpu   int
		event int
	}
	tests := []struct {
		name   string
		fields fields
		args   args
	}{
		{
			name: "happy path of LAT1",
			fields: fields{
				SysInfo: &SysInfo{
					Family: 0x19,
					Model:  0x10,
				},
			},
			args: args{
				cpu:   0,
				event: L3PMC_EVE_LAT1,
			},
		},
		{
			name: "2nd path of LAT1",
			fields: fields{
				SysInfo: &SysInfo{
					Family: 0x19,
					Model:  0x09,
				},
			},
			args: args{
				cpu:   1,
				event: L3PMC_EVE_LAT1,
			},
		},
		{
			name: "3rd path of LAT1",
			fields: fields{
				SysInfo: &SysInfo{
					Family: 0x18,
					Model:  0x10,
				},
			},
			args: args{
				cpu:   2,
				event: L3PMC_EVE_LAT1,
			},
		},
		{
			name: "happy path of LAT2",
			fields: fields{
				SysInfo: &SysInfo{
					Family: 0x19,
					Model:  0x10,
				},
			},
			args: args{
				cpu:   0,
				event: L3PMC_EVE_LAT2,
			},
		},
		{
			name: "2nd path of LAT2",
			fields: fields{
				SysInfo: &SysInfo{
					Family: 0x19,
					Model:  0x09,
				},
			},
			args: args{
				cpu:   1,
				event: L3PMC_EVE_LAT2,
			},
		},
		{
			name: "3rd path of LAT2",
			fields: fields{
				SysInfo: &SysInfo{
					Family: 0x18,
					Model:  0x10,
				},
			},
			args: args{
				cpu:   2,
				event: L3PMC_EVE_LAT2,
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := MBMonitor{
				SysInfo:     tt.fields.SysInfo,
				Controller:  tt.fields.Controller,
				Interval:    tt.fields.Interval,
				Started:     tt.fields.Started,
				MonitorOnly: tt.fields.MonitorOnly,
				mctx:        tt.fields.mctx,
				done:        tt.fields.done,
			}
			m.StartL3PMCEvent(tt.args.cpu, tt.args.event)
		})
	}
}

func TestReadL3PMCEvent(t *testing.T) {
	t.Parallel()
	type args struct {
		cpu   int
		event int
	}
	tests := []struct {
		name    string
		args    args
		want    uint64
		wantErr bool
	}{
		{
			name: "happy path no error of LAT1",
			args: args{
				cpu:   0,
				event: L3PMC_EVE_LAT1,
			},
			want:    0,
			wantErr: false,
		},
		{
			name: "happy path no error of LAT2",
			args: args{
				cpu:   1,
				event: L3PMC_EVE_LAT2,
			},
			want:    0,
			wantErr: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ReadL3PMCEvent(tt.args.cpu, tt.args.event)
			if (err != nil) != tt.wantErr {
				t.Errorf("ReadL3PMCEvent() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("ReadL3PMCEvent() got = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestStopL3PMCEvent(t *testing.T) {
	t.Parallel()
	type args struct {
		cpu   int
		event int
	}
	tests := []struct {
		name string
		args args
	}{
		{
			name: "happy path of LAT1",
			args: args{
				cpu:   0,
				event: L3PMC_EVE_LAT1,
			},
		},
		{
			name: "happy path of LAT2",
			args: args{
				cpu:   1,
				event: L3PMC_EVE_LAT2,
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			StopL3PMCEvent(tt.args.cpu, tt.args.event)
		})
	}
}

func TestMBMonitor_ReadL3MissLatency(t *testing.T) {
	t.Parallel()
	type fields struct {
		SysInfo     *SysInfo
		Controller  MBController
		Interval    uint64
		Started     bool
		MonitorOnly bool
		mctx        context.Context
		done        context.CancelFunc
	}
	tests := []struct {
		name    string
		fields  fields
		wantErr bool
	}{
		{
			name: "happy path no error",
			fields: fields{
				SysInfo: &SysInfo{
					CCDMap: map[int][]int{0: {0, 1, 2}},
					MemoryLatency: MemoryLatencyInfo{
						CCDLocker: sync.RWMutex{},
						L3Latency: make([]L3PMCLatencyInfo, 3),
					},
				},
				Controller:  MBController{},
				Interval:    0,
				Started:     false,
				MonitorOnly: false,
				mctx:        nil,
				done:        nil,
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := &MBMonitor{
				SysInfo:     tt.fields.SysInfo,
				Controller:  tt.fields.Controller,
				Interval:    tt.fields.Interval,
				Started:     tt.fields.Started,
				MonitorOnly: tt.fields.MonitorOnly,
				mctx:        tt.fields.mctx,
				done:        tt.fields.done,
			}
			if err := m.ReadL3MissLatency(); (err != nil) != tt.wantErr {
				t.Errorf("ReadL3MissLatency() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
