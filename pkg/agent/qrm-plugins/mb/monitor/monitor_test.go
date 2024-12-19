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
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/readmb/rmbtype"
	"github.com/stretchr/testify/assert"
	"reflect"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/mock"
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/qosgroup"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/readmb"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/writemb"
)

type mockReadMBReader struct {
	mock.Mock
}

func (m *mockReadMBReader) GetMB(qosGroup string) (map[int]rmbtype.MBStat, error) {
	args := m.Called(qosGroup)
	return args.Get(0).(map[int]rmbtype.MBStat), args.Error(1)
}

type mockWriteMBReader struct {
	mock.Mock
}

func (m *mockWriteMBReader) GetMB(ccd int) (int, error) {
	args := m.Called(ccd)
	return args.Int(0), args.Error(1)
}

func Test_mbMonitor_GetMBQoSGroups(t1 *testing.T) {
	t1.Parallel()

	rmbReader := new(mockReadMBReader)
	rmbReader.On("GetMB", "shared-50").Return(map[int]rmbtype.MBStat{2: {Total: 200}, 3: {Total: 300}}, nil)

	wmbReader := new(mockWriteMBReader)
	wmbReader.On("GetMB", 2).Return(20, nil)
	wmbReader.On("GetMB", 3).Return(30, nil)

	stubFs := afero.NewMemMapFs()
	_ = stubFs.MkdirAll("/sys/fs/resctrl/shared-50", 0o755)

	type fields struct {
		rmbReader readmb.ReadMBReader
		wmbReader writemb.WriteMBReader
		fs        afero.Fs
	}
	tests := []struct {
		name    string
		fields  fields
		want    map[qosgroup.QoSGroup]*MBQoSGroup
		wantErr bool
	}{
		{
			name: "happy path",
			fields: fields{
				rmbReader: rmbReader,
				wmbReader: wmbReader,
				fs:        stubFs,
			},
			want: map[qosgroup.QoSGroup]*MBQoSGroup{
				"shared-50": {
					CCDs: sets.Int{2: sets.Empty{}, 3: sets.Empty{}},
					CCDMB: map[int]*MBData{
						2: {ReadsMB: 200, WritesMB: 20},
						3: {ReadsMB: 300, WritesMB: 30},
					},
				},
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t1.Run(tt.name, func(t1 *testing.T) {
			t1.Parallel()
			t := mbMonitor{
				rmbReader: tt.fields.rmbReader,
				wmbReader: tt.fields.wmbReader,
				fs:        tt.fields.fs,
			}
			got, err := t.GetMBQoSGroups()
			if (err != nil) != tt.wantErr {
				t1.Errorf("GetMBQoSGroups() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t1.Errorf("GetMBQoSGroups() got = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_getGroupCCDMBs(t *testing.T) {
	t.Parallel()
	type args struct {
		rGroupCCDMB map[qosgroup.QoSGroup]map[int]rmbtype.MBStat
		wGroupCCDMB map[qosgroup.QoSGroup]map[int]int
	}
	tests := []struct {
		name string
		args args
		want map[qosgroup.QoSGroup]map[int]*MBData
	}{
		{
			name: "happy path 1 gos group",
			args: args{
				rGroupCCDMB: map[qosgroup.QoSGroup]map[int]rmbtype.MBStat{
					qosgroup.QoSGroupDedicated: {2: {Total: 200}, 3: {Total: 300}},
				},
				wGroupCCDMB: map[qosgroup.QoSGroup]map[int]int{
					qosgroup.QoSGroupDedicated: {2: 20, 3: 30},
				},
			},
			want: map[qosgroup.QoSGroup]map[int]*MBData{
				qosgroup.QoSGroupDedicated: {
					2: {TotalMB: 0, ReadsMB: 200, WritesMB: 20},
					3: {TotalMB: 0, ReadsMB: 300, WritesMB: 30},
				},
			},
		},
		{
			name: "drop out ccd wmb if r/w ratio unreasonable",
			args: args{
				rGroupCCDMB: map[qosgroup.QoSGroup]map[int]rmbtype.MBStat{
					qosgroup.QoSGroupDedicated: {2: {Total: 0}, 3: {Total: 300}},
					qosgroup.QoSGroupSystem:    {2: {Total: 200}, 3: {Total: 1}},
				},
				wGroupCCDMB: map[qosgroup.QoSGroup]map[int]int{
					qosgroup.QoSGroupDedicated: {2: 20, 3: 30},
					qosgroup.QoSGroupSystem:    {2: 20, 3: 30},
				},
			},
			want: map[qosgroup.QoSGroup]map[int]*MBData{
				qosgroup.QoSGroupDedicated: {
					2: {TotalMB: 0, ReadsMB: 0, WritesMB: 0},
					3: {TotalMB: 0, ReadsMB: 300, WritesMB: 30},
				},
				qosgroup.QoSGroupSystem: {
					2: {TotalMB: 0, ReadsMB: 200, WritesMB: 20},
					3: {TotalMB: 0, ReadsMB: 1, WritesMB: 0},
				},
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := getGroupCCDMBs(tt.args.rGroupCCDMB, tt.args.wGroupCCDMB)
			assert.Equal(t, tt.want, got)
		})
	}
}

func Test_distributeLocalRemote(t *testing.T) {
	t.Parallel()
	type args struct {
		r         int
		w         int
		readLocal int
	}
	tests := []struct {
		name        string
		args        args
		wantRLocal  int
		wantRRemote int
		wantWLocal  int
		wantWRemote int
	}{
		{
			name: "happy path",
			args: args{
				r:         100,
				w:         50,
				readLocal: 70,
			},
			wantRLocal:  70,
			wantRRemote: 30,
			wantWLocal:  35,
			wantWRemote: 15,
		},
		{
			name: "random test",
			args: args{
				r:         12750,
				w:         4976,
				readLocal: 12749,
			},
			wantRLocal:  12749,
			wantRRemote: 1,
			wantWLocal:  4975,
			wantWRemote: 1,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gotRLocal, gotRRemote, gotWLocal, gotWRemote := distributeLocalRemote(tt.args.r, tt.args.w, tt.args.readLocal)
			assert.Equalf(t, tt.wantRLocal, gotRLocal, "distributeLocalRemote(%v, %v, %v)", tt.args.r, tt.args.w, tt.args.readLocal)
			assert.Equalf(t, tt.wantRRemote, gotRRemote, "distributeLocalRemote(%v, %v, %v)", tt.args.r, tt.args.w, tt.args.readLocal)
			assert.Equalf(t, tt.wantWLocal, gotWLocal, "distributeLocalRemote(%v, %v, %v)", tt.args.r, tt.args.w, tt.args.readLocal)
			assert.Equalf(t, tt.wantWRemote, gotWRemote, "distributeLocalRemote(%v, %v, %v)", tt.args.r, tt.args.w, tt.args.readLocal)
		})
	}
}
