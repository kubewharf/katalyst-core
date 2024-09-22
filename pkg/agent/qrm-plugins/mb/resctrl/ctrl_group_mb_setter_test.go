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

package resctrl

import (
	"testing"

	"github.com/stretchr/testify/mock"
)

type mockCCDMBSetter struct {
	mock.Mock
}

func (m *mockCCDMBSetter) UpdateSchemata(ctrlGroup string, update string) error {
	args := m.Called(ctrlGroup, update)
	return args.Error(0)
}

func Test_ctrlGroupMBSetter_Set(t *testing.T) {
	t.Parallel()

	ccdMBSetter := new(mockCCDMBSetter)
	ccdMBSetter.On("UpdateSchemata", "foo", "MB:2=200;").Return(nil)

	type fields struct {
		ccdMBSetter SchemataUpdater
	}
	type args struct {
		ctrlGroup string
		ccdMB     map[int]int
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		wantErr bool
	}{
		{
			name: "happy path",
			fields: fields{
				ccdMBSetter: ccdMBSetter,
			},
			args: args{
				ctrlGroup: "foo",
				ccdMB:     map[int]int{2: 25_000},
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := ctrlGroupMBSetter{
				schemataUpdater: tt.fields.ccdMBSetter,
			}
			if err := c.SetMB(tt.args.ctrlGroup, tt.args.ccdMB); (err != nil) != tt.wantErr {
				t.Errorf("Set() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
