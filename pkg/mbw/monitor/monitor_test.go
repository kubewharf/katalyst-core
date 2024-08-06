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
	"time"

	v1 "github.com/google/cadvisor/info/v1"
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/global"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// not to verify much other than expecting no error
// most of this test purpose is more test coverage
func Test_newExtKatalystMachineInfo(t *testing.T) {
	t.Parallel()
	type args struct {
		machineInfoConfig *global.MachineInfoConfiguration
	}
	tests := []struct {
		name    string
		args    args
		want    *machine.KatalystMachineInfo
		wantErr bool
	}{
		{
			name: "happy path en error",
			args: args{
				machineInfoConfig: &global.MachineInfoConfiguration{},
			},
			want:    nil,
			wantErr: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := newExtKatalystMachineInfo(tt.args.machineInfoConfig)
			if (err != nil) != tt.wantErr {
				t.Errorf("newExtKatalystMachineInfo() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
		})
	}
}

func Test_newMonitor(t *testing.T) {
	t.Parallel()
	type args struct {
		sysInfo *machine.KatalystMachineInfo
	}
	tests := []struct {
		name    string
		args    args
		want    *MBMonitor
		wantErr bool
	}{
		{
			name: "happy path no error",
			args: args{
				sysInfo: &machine.KatalystMachineInfo{
					MachineInfo: &v1.MachineInfo{
						Timestamp:        time.Time{},
						CPUVendorID:      "",
						NumCores:         1,
						NumPhysicalCores: 0,
						NumSockets:       0,
						CpuFrequency:     0,
						MemoryCapacity:   0,
						MemoryByType:     nil,
						NVMInfo:          v1.NVMInfo{},
						HugePages:        nil,
						MachineID:        "",
						SystemUUID:       "",
						BootID:           "",
						Filesystems:      nil,
						DiskMap:          nil,
						NetworkDevices:   nil,
						Topology:         nil,
						CloudProvider:    "",
						InstanceType:     "",
						InstanceID:       "",
					},
					CPUTopology: &machine.CPUTopology{
						NumCPUs:      1,
						NumCores:     2,
						NumSockets:   1,
						NumNUMANodes: 1,
					},
					MemoryTopology: &machine.MemoryTopology{
						PMU:             machine.PMUInfo{},
						MemoryBandwidth: machine.MemoryBandwidthInfo{},
						MemoryLatency:   machine.MemoryLatencyInfo{},
						RMIDPerPackage:  nil,
					},
					DieTopology: &machine.DieTopology{
						NumPackages:      1,
						PackageMap:       nil,
						PackagePerSocket: 0,
						RDTScalar:        0,
						DieSize:          0,
						NumCCDs:          0,
						CCDMap:           nil,
						NumaMap:          nil,
						FakeNUMAEnabled:  false,
					},
					ExtraCPUInfo: &machine.ExtraCPUInfo{
						Vendor: "dummy-test",
					},
					ExtraNetworkInfo: nil,
					ExtraTopologyInfo: &machine.ExtraTopologyInfo{
						NumaDistanceMap: map[int][]machine.NumaDistanceInfo{
							0: {{
								NumaID:   0,
								Distance: 10,
							}},
						},
						SiblingNumaInfo: &machine.SiblingNumaInfo{
							SiblingNumaMap:                  map[int]sets.Int{0: sets.NewInt()},
							SiblingNumaAvgMBWAllocatableMap: nil,
							SiblingNumaAvgMBWCapacityMap:    nil,
						},
					},
				},
			},
			want:    nil,
			wantErr: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := newMonitor(tt.args.sysInfo)
			if (err != nil) != tt.wantErr {
				t.Errorf("newMonitor() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
		})
	}
}

func TestMBMonitor_ServeCoreMB(t *testing.T) {
	t.Parallel()
	type fields struct {
		SysInfo     *machine.KatalystMachineInfo
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
				SysInfo: &machine.KatalystMachineInfo{
					CPUTopology: &machine.CPUTopology{
						NumCPUs:      1,
						NumCores:     2,
						NumSockets:   1,
						NumNUMANodes: 1,
					},
					DieTopology: &machine.DieTopology{
						PackageMap: nil,
					},
					MemoryTopology: &machine.MemoryTopology{
						MemoryBandwidth: machine.MemoryBandwidthInfo{
							CoreLocker:    sync.RWMutex{},
							PackageLocker: sync.RWMutex{},
							Cores: []machine.CoreMB{{
								Package:    0,
								LRMB:       0,
								LRMB_Delta: 0,
								RRMB_Delta: 0,
								TRMB:       0,
								TRMB_Delta: 0,
							}, {
								Package:    0,
								LRMB:       0,
								LRMB_Delta: 0,
								RRMB_Delta: 0,
								TRMB:       0,
								TRMB_Delta: 0,
							}},
							Numas: []machine.NumaMB{{
								Package: 0,
								LRMB:    0,
								RRMB:    0,
								TRMB:    0,
								Total:   0,
							}},
							Packages: nil,
						},
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
			m := MBMonitor{
				KatalystMachineInfo: tt.fields.SysInfo,
				Controller:          tt.fields.Controller,
				Interval:            tt.fields.Interval,
				Started:             tt.fields.Started,
				MonitorOnly:         tt.fields.MonitorOnly,
				mctx:                tt.fields.mctx,
				done:                tt.fields.done,
			}
			if err := m.ServeCoreMB(); (err != nil) != tt.wantErr {
				t.Errorf("ServeCoreMB() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
