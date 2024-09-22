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

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
)

func Test_ccdMBSetter_UpdateSchemata(t *testing.T) {
	t.Parallel()
	fs := afero.NewMemMapFs()
	_ = afero.WriteFile(fs, "/foo/schemata", []byte("MB:2=32;3=16;"), 0644)

	type fields struct {
		fs afero.Fs
	}
	type args struct {
		ctrlGroup   string
		instruction string
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
				fs: fs,
			},
			args: args{
				ctrlGroup:   "foo",
				instruction: "MB:2=32;3:16;",
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := ccdMBSetter{
				fs: tt.fields.fs,
			}
			if err := c.UpdateSchemata(tt.args.ctrlGroup, tt.args.instruction); (err != nil) != tt.wantErr {
				t.Errorf("UpdateSchemata() error = %v, wantErr %v", err, tt.wantErr)
			}

			buff, err := afero.ReadFile(fs, "/foo/schemata")
			assert.NoError(t, err)
			t.Logf("content got: %s", string(buff))
			assert.True(t, "MB:2=32;3=16;" == string(buff) || "MB:3=16;2=32;" == string(buff))
		})
	}
}
