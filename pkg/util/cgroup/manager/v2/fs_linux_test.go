//go:build linux
// +build linux

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

package v2

import (
	"reflect"
	"testing"

	"github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
)

func TestNewManager(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		want *manager
	}{
		{
			name: "test new manager",
			want: &manager{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NewManager(); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("NewManager() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_manager_ApplyMemory(t *testing.T) {
	t.Parallel()

	type args struct {
		absCgroupPath string
		data          *common.MemoryData
	}
	tests := []struct {
		name    string
		m       *manager
		args    args
		wantErr bool
	}{
		{
			name: "test apply memory with LimitInBytes",
			m:    NewManager(),
			args: args{
				absCgroupPath: "test-fake-path",
				data: &common.MemoryData{
					LimitInBytes: 1234,
				},
			},
			wantErr: true,
		},
		{
			name: "test apply memory with SoftLimitInBytes",
			m:    NewManager(),
			args: args{
				absCgroupPath: "test-fake-path",
				data: &common.MemoryData{
					SoftLimitInBytes: 2234,
				},
			},
			wantErr: true,
		},
		{
			name: "test apply memory with MinInBytes",
			m:    NewManager(),
			args: args{
				absCgroupPath: "test-fake-path",
				data: &common.MemoryData{
					MinInBytes: 3234,
				},
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &manager{}
			if err := m.ApplyMemory(tt.args.absCgroupPath, tt.args.data); (err != nil) != tt.wantErr {
				t.Errorf("manager.ApplyMemory() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func Test_manager_ApplyCPU(t *testing.T) {
	t.Parallel()

	type args struct {
		absCgroupPath string
		data          *common.CPUData
	}
	tests := []struct {
		name    string
		m       *manager
		args    args
		wantErr bool
	}{
		{
			name: "test apply cpu",
			m:    NewManager(),
			args: args{
				absCgroupPath: "test-fake-path",
				data: &common.CPUData{
					Shares: 1024,
				},
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &manager{}
			if err := m.ApplyCPU(tt.args.absCgroupPath, tt.args.data); (err != nil) != tt.wantErr {
				t.Errorf("manager.ApplyCPU() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func Test_manager_ApplyCPUSet(t *testing.T) {
	t.Parallel()

	type args struct {
		absCgroupPath string
		data          *common.CPUSetData
	}
	tests := []struct {
		name    string
		m       *manager
		args    args
		wantErr bool
	}{
		{
			name: "test apply cpuset",
			m:    NewManager(),
			args: args{
				absCgroupPath: "test-fake-path",
				data: &common.CPUSetData{
					CPUs: "0-1",
				},
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &manager{}
			if err := m.ApplyCPUSet(tt.args.absCgroupPath, tt.args.data); (err != nil) != tt.wantErr {
				t.Errorf("manager.ApplyCPUSet() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func Test_manager_ApplyNetCls(t *testing.T) {
	t.Parallel()

	type args struct {
		in0 string
		in1 *common.NetClsData
	}
	tests := []struct {
		name    string
		m       *manager
		args    args
		wantErr bool
	}{
		{
			name: "test apply netcls",
			m:    NewManager(),
			args: args{
				in1: &common.NetClsData{
					ClassID: 5,
				},
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &manager{}
			if err := m.ApplyNetCls(tt.args.in0, tt.args.in1); (err != nil) != tt.wantErr {
				t.Errorf("manager.ApplyNetCls() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func Test_manager_ApplyIOCostQoS(t *testing.T) {
	t.Parallel()

	type args struct {
		absCgroupPath string
		devID         string
		data          *common.IOCostQoSData
	}
	tests := []struct {
		name    string
		m       *manager
		args    args
		wantErr bool
	}{
		{
			name: "test apply io cost qos",
			m:    NewManager(),
			args: args{
				absCgroupPath: "test-fake-path",
				devID:         "test",
				data: &common.IOCostQoSData{
					Enable: 0,
				},
			},
			wantErr: true,
		},
		{
			name: "test apply io cost qos with nil data",
			m:    NewManager(),
			args: args{
				absCgroupPath: "test-fake-path",
				devID:         "test",
				data:          nil,
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &manager{}
			if err := m.ApplyIOCostQoS(tt.args.absCgroupPath, tt.args.devID, tt.args.data); (err != nil) != tt.wantErr {
				t.Errorf("manager.ApplyIOCostQoS() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func Test_manager_ApplyIOCostModel(t *testing.T) {
	t.Parallel()

	type args struct {
		absCgroupPath string
		devID         string
		data          *common.IOCostModelData
	}
	tests := []struct {
		name    string
		m       *manager
		args    args
		wantErr bool
	}{
		{
			name: "test apply io cost model",
			m:    NewManager(),
			args: args{
				absCgroupPath: "test-fake-path",
				devID:         "test",
				data: &common.IOCostModelData{
					CtrlMode: common.IOCostCtrlModeAuto,
				},
			},
			wantErr: true,
		},
		{
			name: "test apply io cost model with nil data",
			m:    NewManager(),
			args: args{
				absCgroupPath: "test-fake-path",
				devID:         "test",
				data:          nil,
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &manager{}
			if err := m.ApplyIOCostModel(tt.args.absCgroupPath, tt.args.devID, tt.args.data); (err != nil) != tt.wantErr {
				t.Errorf("manager.ApplyIOCostModel() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func Test_manager_ApplyIOWeight(t *testing.T) {
	t.Parallel()

	type args struct {
		absCgroupPath string
		devID         string
		weight        uint64
	}
	tests := []struct {
		name    string
		m       *manager
		args    args
		wantErr bool
	}{
		{
			name: "test apply io weight",
			m:    NewManager(),
			args: args{
				absCgroupPath: "test-fake-path",
				devID:         "test",
				weight:        100,
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &manager{}
			if err := m.ApplyIOWeight(tt.args.absCgroupPath, tt.args.devID, tt.args.weight); (err != nil) != tt.wantErr {
				t.Errorf("manager.ApplyIOWeight() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func Test_manager_ApplyUnifiedData(t *testing.T) {
	t.Parallel()

	type args struct {
		absCgroupPath  string
		cgroupFileName string
		data           string
	}
	tests := []struct {
		name    string
		m       *manager
		args    args
		wantErr bool
	}{
		{
			name: "test apply unified data",
			m:    NewManager(),
			args: args{
				absCgroupPath:  "test-fake-path",
				cgroupFileName: "test-cg-file-name",
				data:           "test-data",
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &manager{}
			if err := m.ApplyUnifiedData(tt.args.absCgroupPath, tt.args.cgroupFileName, tt.args.data); (err != nil) != tt.wantErr {
				t.Errorf("manager.ApplyUnifiedData() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func Test_manager_GetMemory(t *testing.T) {
	t.Parallel()

	type args struct {
		absCgroupPath string
	}
	tests := []struct {
		name    string
		m       *manager
		args    args
		want    *common.MemoryStats
		wantErr bool
	}{
		{
			name: "test get memory",
			m:    NewManager(),
			args: args{
				absCgroupPath: "test-fake-path",
			},
			want:    nil,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &manager{}
			got, err := m.GetMemory(tt.args.absCgroupPath)
			if (err != nil) != tt.wantErr {
				t.Errorf("manager.GetMemory() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("manager.GetMemory() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_manager_GetCPUSet(t *testing.T) {
	t.Parallel()

	type args struct {
		absCgroupPath string
	}
	tests := []struct {
		name    string
		m       *manager
		args    args
		want    *common.CPUSetStats
		wantErr bool
	}{
		{
			name: "test get cpuset",
			m:    NewManager(),
			args: args{
				absCgroupPath: "test-fake-path",
			},
			want:    nil,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &manager{}
			got, err := m.GetCPUSet(tt.args.absCgroupPath)
			if (err != nil) != tt.wantErr {
				t.Errorf("manager.GetCPUSet() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("manager.GetCPUSet() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_manager_GetCPU(t *testing.T) {
	t.Parallel()

	type args struct {
		absCgroupPath string
	}
	tests := []struct {
		name    string
		m       *manager
		args    args
		want    *common.CPUStats
		wantErr bool
	}{
		{
			name: "test get cpu",
			m:    NewManager(),
			args: args{
				absCgroupPath: "test-fake-path",
			},
			want:    nil,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &manager{}
			got, err := m.GetCPU(tt.args.absCgroupPath)
			if (err != nil) != tt.wantErr {
				t.Errorf("manager.GetCPU() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("manager.GetCPU() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_manager_GetIOCostQoS(t *testing.T) {
	t.Parallel()

	type args struct {
		absCgroupPath string
	}
	tests := []struct {
		name    string
		m       *manager
		args    args
		want    map[string]*common.IOCostQoSData
		wantErr bool
	}{
		{
			name: "test get io cost qos",
			m:    NewManager(),
			args: args{
				absCgroupPath: "test-fake-path",
			},
			want:    nil,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &manager{}
			got, err := m.GetIOCostQoS(tt.args.absCgroupPath)
			if (err != nil) != tt.wantErr {
				t.Errorf("manager.GetIOCostQoS() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("manager.GetIOCostQoS() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_manager_GetIOCostModel(t *testing.T) {
	t.Parallel()

	type args struct {
		absCgroupPath string
	}
	tests := []struct {
		name    string
		m       *manager
		args    args
		want    map[string]*common.IOCostModelData
		wantErr bool
	}{
		{
			name: "test get io cost model",
			m:    NewManager(),
			args: args{
				absCgroupPath: "test-fake-path",
			},
			want:    nil,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &manager{}
			got, err := m.GetIOCostModel(tt.args.absCgroupPath)
			if (err != nil) != tt.wantErr {
				t.Errorf("manager.GetIOCostModel() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("manager.GetIOCostModel() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_manager_GetDeviceIOWeight(t *testing.T) {
	t.Parallel()

	type args struct {
		absCgroupPath string
		devID         string
	}
	tests := []struct {
		name    string
		m       *manager
		args    args
		want    uint64
		want1   bool
		wantErr bool
	}{
		{
			name: "test get io weight",
			m:    NewManager(),
			args: args{
				absCgroupPath: "test-fake-path",
				devID:         "test",
			},
			want:    0,
			want1:   false,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &manager{}
			got, got1, err := m.GetDeviceIOWeight(tt.args.absCgroupPath, tt.args.devID)
			if (err != nil) != tt.wantErr {
				t.Errorf("manager.GetDeviceIOWeight() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("manager.GetDeviceIOWeight() got = %v, want %v", got, tt.want)
			}
			if got1 != tt.want1 {
				t.Errorf("manager.GetDeviceIOWeight() got1 = %v, want %v", got1, tt.want1)
			}
		})
	}
}

func Test_manager_GetIOStat(t *testing.T) {
	t.Parallel()

	type args struct {
		absCgroupPath string
	}
	tests := []struct {
		name    string
		m       *manager
		args    args
		want    map[string]map[string]string
		wantErr bool
	}{
		{
			name: "test get io stat",
			m:    NewManager(),
			args: args{
				absCgroupPath: "test-fake-path",
			},
			want:    nil,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &manager{}
			got, err := m.GetIOStat(tt.args.absCgroupPath)
			if (err != nil) != tt.wantErr {
				t.Errorf("manager.GetIOStat() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("manager.GetIOStat() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_manager_GetMetrics(t *testing.T) {
	t.Parallel()

	type args struct {
		relCgroupPath string
		in1           map[string]struct{}
	}
	tests := []struct {
		name    string
		m       *manager
		args    args
		want    *common.CgroupMetrics
		wantErr bool
	}{
		{
			name: "test get metrics",
			m:    NewManager(),
			args: args{
				relCgroupPath: "test-fake-path",
			},
			want:    nil,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &manager{}
			got, err := m.GetMetrics(tt.args.relCgroupPath, tt.args.in1)
			if (err != nil) != tt.wantErr {
				t.Errorf("manager.GetMetrics() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("manager.GetMetrics() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_manager_GetPids(t *testing.T) {
	t.Parallel()

	type args struct {
		absCgroupPath string
	}
	tests := []struct {
		name    string
		m       *manager
		args    args
		want    []string
		wantErr bool
	}{
		{
			name: "test get pids",
			m:    NewManager(),
			args: args{
				absCgroupPath: "test-fake-path",
			},
			want:    nil,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &manager{}
			got, err := m.GetPids(tt.args.absCgroupPath)
			if (err != nil) != tt.wantErr {
				t.Errorf("manager.GetPids() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("manager.GetPids() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_manager_GetTasks(t *testing.T) {
	t.Parallel()

	type args struct {
		absCgroupPath string
	}
	tests := []struct {
		name    string
		m       *manager
		args    args
		want    []string
		wantErr bool
	}{
		{
			name: "test get tasks",
			m:    NewManager(),
			args: args{
				absCgroupPath: "test-fake-path",
			},
			want:    nil,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &manager{}
			got, err := m.GetTasks(tt.args.absCgroupPath)
			if (err != nil) != tt.wantErr {
				t.Errorf("manager.GetTasks() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("manager.GetTasks() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_numToStr(t *testing.T) {
	t.Parallel()

	type args struct {
		value int64
	}
	tests := []struct {
		name    string
		args    args
		wantRet string
	}{
		{
			name: "test num to str",
			args: args{
				value: 1000,
			},
			wantRet: "1000",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if gotRet := numToStr(tt.args.value); gotRet != tt.wantRet {
				t.Errorf("numToStr() = %v, want %v", gotRet, tt.wantRet)
			}
		})
	}
}

func Test_parseDeviceIOCostQoS(t *testing.T) {
	t.Parallel()

	type args struct {
		str string
	}
	tests := []struct {
		name    string
		args    args
		want    string
		want1   *common.IOCostQoSData
		wantErr bool
	}{
		{
			name: "test parse device io cost qos",
			args: args{
				str: "8:160 enable=0 ctrl=auto rpct=0.00 rlat=250000 wpct=0.00 wlat=250000 min=1.00 max=10000.00",
			},
			want: "8:160",
			want1: &common.IOCostQoSData{
				Enable:              0,
				CtrlMode:            common.IOCostCtrlModeAuto,
				ReadLatencyPercent:  0,
				ReadLatencyUS:       250000,
				WriteLatencyPercent: 0,
				WriteLatencyUS:      250000,
				VrateMin:            1,
				VrateMax:            10000,
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, got1, err := parseDeviceIOCostQoS(tt.args.str)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseDeviceIOCostQoS() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("parseDeviceIOCostQoS() got = %v, want %v", got, tt.want)
			}
			if !reflect.DeepEqual(got1, tt.want1) {
				t.Errorf("parseDeviceIOCostQoS() got1 = %v, want %v", got1, tt.want1)
			}
		})
	}
}

func Test_parseDeviceIOCostModel(t *testing.T) {
	t.Parallel()

	type args struct {
		str string
	}
	tests := []struct {
		name    string
		args    args
		want    string
		want1   *common.IOCostModelData
		wantErr bool
	}{
		{
			name: "test parse device io cost model",
			args: args{
				str: "8:160 ctrl=auto model=linear rbps=174019176 rseqiops=41708 rrandiops=370 wbps=178075866 wseqiops=42705 wrandiops=378",
			},
			want: "8:160",
			want1: &common.IOCostModelData{
				CtrlMode:      common.IOCostCtrlModeAuto,
				Model:         common.IOCostModelLinear,
				ReadBPS:       174019176,
				ReadSeqIOPS:   41708,
				ReadRandIOPS:  370,
				WriteBPS:      178075866,
				WriteSeqIOPS:  42705,
				WriteRandIOPS: 378,
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, got1, err := parseDeviceIOCostModel(tt.args.str)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseDeviceIOCostModel() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("parseDeviceIOCostModel() got = %v, want %v", got, tt.want)
			}
			if !reflect.DeepEqual(got1, tt.want1) {
				t.Errorf("parseDeviceIOCostModel() got1 = %v, want %v", got1, tt.want1)
			}
		})
	}
}
