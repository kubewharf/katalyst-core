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
	"reflect"
	"testing"

	"github.com/stretchr/testify/mock"
)

type mockCCDReader struct {
	mock.Mock
}

func (m *mockCCDReader) ReadMB(MonGroup string, ccd int) (int, error) {
	args := m.Called(MonGroup, ccd)
	return args.Int(0), args.Error(1)
}

func Test_monGroupReader_ReadMB(t *testing.T) {
	t.Parallel()

	mockDieReader := new(mockCCDReader)
	mockDieReader.On("ReadMB", "/sys/fs/resctrl/dedicated/mon_groups/pod001", 3).Return(111, nil)

	type fields struct {
		ccdReader CCDMBReader
	}
	type args struct {
		monGroup string
		dies     []int
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		want    map[int]int
		wantErr bool
	}{
		{
			name: "happy path",
			fields: fields{
				ccdReader: mockDieReader,
			},
			args: args{
				monGroup: "/sys/fs/resctrl/dedicated/mon_groups/pod001",
				dies:     []int{3},
			},
			want:    map[int]int{3: 111},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := monGroupReader{
				ccdReader: tt.fields.ccdReader,
			}
			got, err := m.ReadMB(tt.args.monGroup, tt.args.dies)
			if (err != nil) != tt.wantErr {
				t.Errorf("GetMB() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("GetMB() got = %v, want %v", got, tt.want)
			}
		})
	}
}
