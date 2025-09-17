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

package allocator

import (
	"context"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/plan"
)

func Test_isValidPath(t *testing.T) {
	t.Parallel()
	type args struct {
		name string
	}
	tests := []struct {
		name    string
		args    args
		wantErr bool
	}{
		{
			name: "happy path",
			args: args{
				name: "shared-50",
			},
			wantErr: false,
		},
		{
			name: "2-layered path is not allowed",
			args: args{
				name: "root/shared-50",
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if err := validatePath(tt.args.name); (err != nil) != tt.wantErr {
				t.Errorf("validatePath() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func Test_resctrlAllocator_AllocateGroupPlan(t *testing.T) {
	t.Parallel()

	dummyFS := afero.NewMemMapFs()
	_ = afero.WriteFile(dummyFS, "/sys/fs/resctrl/shared-50/schemata", []byte("ddd"), 0o644)

	type args struct {
		ctrlGroup string
		plan      plan.GroupCCDPlan
	}
	tests := []struct {
		name    string
		args    args
		wantErr bool
	}{
		{
			name: "non existent group leads to failure",
			args: args{
				ctrlGroup: "nonexistent",
				plan:      plan.GroupCCDPlan{0: 5_000, 1: 4_500},
			},
			wantErr: true,
		},
		{
			name: "happy path",
			args: args{
				ctrlGroup: "shared-50",
				plan:      plan.GroupCCDPlan{0: 5_000, 1: 4_500},
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := &resctrlAllocator{fs: dummyFS}
			if err := r.allocateGroupPlan(tt.args.ctrlGroup, tt.args.plan); (err != nil) != tt.wantErr {
				t.Errorf("Allocate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func Test_resctrlAllocator_Allocate(t *testing.T) {
	t.Parallel()
	testFS := afero.NewMemMapFs()
	_ = afero.WriteFile(testFS, "/sys/fs/resctrl/shared-50/schemata", []byte("ddd"), 0o644)
	_ = afero.WriteFile(testFS, "/sys/fs/resctrl/shared-30/schemata", []byte("ddd"), 0o644)

	type fields struct {
		fs afero.Fs
	}
	type args struct {
		ctx  context.Context
		plan *plan.MBPlan
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		wantErr bool
		want    map[string]string
	}{
		{
			name: "happy path",
			fields: fields{
				fs: testFS,
			},
			args: args{
				ctx: context.TODO(),
				plan: &plan.MBPlan{
					MBGroups: map[string]plan.GroupCCDPlan{
						"shared-50": {0: 4_000, 2: 4_500},
						"shared-30": {1: 6_000, 2: 3_000},
					},
				},
			},
			wantErr: false,
			want: map[string]string{
				"shared-50": "MB:0=32;2=36;\n",
				"shared-30": "MB:1=48;2=24;\n",
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := &resctrlAllocator{
				fs: tt.fields.fs,
			}
			if err := r.Allocate(tt.args.ctx, tt.args.plan); (err != nil) != tt.wantErr {
				t.Errorf("Allocate() error = %v, wantErr %v", err, tt.wantErr)
			}

			for path, content := range tt.want {
				buff, err := afero.ReadFile(r.fs, "/sys/fs/resctrl/"+path+"/schemata")
				assert.NoError(t, err)
				t.Logf("content got: %s", string(buff))
				assert.Equal(t, content, string(buff))
			}
		})
	}
}

func Test_resctrlAllocator_getResetPlan(t *testing.T) {
	t.Parallel()

	dummyFS := afero.NewMemMapFs()
	_ = dummyFS.MkdirAll("/sys/fs/resctrl/", 0o755)
	_ = afero.WriteFile(dummyFS, "/sys/fs/resctrl/schemata", []byte(`dummy`), 0o644)
	_ = afero.WriteFile(dummyFS, "/sys/fs/resctrl/shared-50/schemata", []byte(`dummy`), 0o644)
	_ = afero.WriteFile(dummyFS, "/sys/fs/resctrl/info/last_cmd_status", []byte(`dummy`), 0o644)

	type fields struct {
		fs afero.Fs
	}
	type args struct {
		ccds sets.Int
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		want    *plan.MBPlan
		wantErr bool
	}{
		{
			name: "happy path",
			fields: fields{
				fs: dummyFS,
			},
			args: args{
				ccds: sets.NewInt(0, 8),
			},
			want: &plan.MBPlan{
				MBGroups: map[string]plan.GroupCCDPlan{
					"/":         map[int]int{0: 256000, 8: 256000},
					"shared-50": map[int]int{0: 256000, 8: 256000},
				},
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := &resctrlAllocator{
				fs: tt.fields.fs,
			}
			got, gotErr := r.getResetPlan(tt.args.ccds)
			assert.Equal(t, tt.wantErr, gotErr != nil, "getResetPlan() error")
			assert.Equalf(t, tt.want, got, "getResetPlan() plan")
		})
	}
}
