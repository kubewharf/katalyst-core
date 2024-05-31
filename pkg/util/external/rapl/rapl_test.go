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

package rapl

import (
	"strings"
	"testing"

	"github.com/spf13/afero"
)

func Test_getWattOfPackage(t *testing.T) {
	t.Parallel()

	testFs := afero.NewMemMapFs()

	afero.WriteFile(testFs, "/sys/class/powercap/intel-rapl:2/constraint_1_power_limit_uw", []byte("230000000\n"), 0o755)
	// negative case; should not exist in any good-shaped machine
	afero.WriteFile(testFs, "/sys/class/powercap/intel-rapl:111/constraint_1_power_limit_uw", []byte("NG2300xx"), 0o755)
	afero.WriteFile(testFs, "/sys/class/powercap/intel-rapl:112/constraint_1_power_limit_uw", []byte{}, 0o755)

	getSetter := raplGetSetter{
		fs:       testFs,
		packages: 2,
	}

	type args struct {
		id int
	}
	tests := []struct {
		name    string
		args    args
		want    int
		wantErr bool
	}{
		{
			name: "happy path of newline gets reading",
			args: args{
				id: 2,
			},
			want:    230,
			wantErr: false,
		},
		{
			name: "rapl file non digit content returns error",
			args: args{
				id: 111,
			},
			want:    0,
			wantErr: true,
		},
		{
			name: "non existent rapl file is an error",
			args: args{
				id: 999,
			},
			want:    0,
			wantErr: true,
		},
		{
			name: "empty rapl file returns error EOF",
			args: args{
				id: 112,
			},
			want:    0,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := getSetter.getShortTermWattOfPackage(tt.args.id)
			if (err != nil) != tt.wantErr {
				t.Errorf("getShortTermWattOfPackage() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("getShortTermWattOfPackage() got = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_setWattPackage(t *testing.T) {
	t.Parallel()

	testFs := afero.NewMemMapFs()
	afero.WriteFile(testFs, "/sys/class/powercap/intel-rapl:3/constraint_1_power_limit_uw", []byte("230000000"), 0o755)

	getSetter := raplGetSetter{
		fs:       testFs,
		packages: 4,
	}

	type args struct {
		packageID int
		limit     int
	}
	tests := []struct {
		name    string
		args    args
		wantErr bool
	}{
		{
			name: "happy path writes rapl file",
			args: args{
				packageID: 3,
				limit:     123,
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if err := getSetter.setShortTermWattPackage(tt.args.packageID, tt.args.limit); (err != nil) != tt.wantErr {
				t.Errorf("setShortTermWattPackage() error = %v, wantErr %v", err, tt.wantErr)
			}

			got, _ := afero.ReadFile(testFs, "/sys/class/powercap/intel-rapl:3/constraint_1_power_limit_uw")
			if strings.TrimSpace(string(got)) != "123000000" {
				t.Errorf("expected 123000000, got %s", string(got))
			}
		})
	}
}

func Test_raplGetSetter_Get_Set(t *testing.T) {
	t.Parallel()

	testFs := afero.NewMemMapFs()

	afero.WriteFile(testFs, "/sys/class/powercap/intel-rapl:0/constraint_1_power_limit_uw", []byte("230000000\n"), 0o755)
	afero.WriteFile(testFs, "/sys/class/powercap/intel-rapl:1/constraint_1_power_limit_uw", []byte("230000000\n"), 0o755)

	raplGetSetter := raplGetSetter{
		fs:       testFs,
		packages: 2,
	}

	// get
	got, err := raplGetSetter.Get()
	if err != nil {
		t.Errorf("Get unexpected error: %#v", err)
	}
	if 460 != got {
		t.Errorf("expected 460, got %d", got)
	}

	// set and verify the written
	err = raplGetSetter.Set(400)
	if err != nil {
		t.Errorf("Set unexpected error: %#v", err)
	}
	saved0, _ := afero.ReadFile(testFs, "/sys/class/powercap/intel-rapl:0/constraint_1_power_limit_uw")
	if strings.TrimSpace(string(saved0)) != "200000000" {
		t.Errorf("expected 200000000, got %s", string(saved0))
	}
	saved1, _ := afero.ReadFile(testFs, "/sys/class/powercap/intel-rapl:1/constraint_1_power_limit_uw")
	if strings.TrimSpace(string(saved1)) != "200000000" {
		t.Errorf("expected 200000000, got %s", string(saved1))
	}

	// cleanup
	_ = raplGetSetter.Set(460)
}

type mockGetSetter struct {
	original, future int
}

func (m *mockGetSetter) Get() (int, error) {
	return m.original, nil
}

func (m *mockGetSetter) Set(value int) error {
	m.future = value
	return nil
}

func TestRAPL_SetLimitOnBasis(t *testing.T) {
	t.Parallel()

	mockGetSetter := &mockGetSetter{
		original: 280,
		future:   0,
	}

	rapl := RAPL{
		getSetter: mockGetSetter,
	}

	if err := rapl.SetLimitOnBasis(355, 380); err != nil {
		t.Errorf("unexpected error: %#v", err)
	}

	if 255 != mockGetSetter.future {
		t.Errorf("expected 255, got %d", mockGetSetter.future)
	}
}

func Test_raplGetSetter_Reset(t *testing.T) {
	t.Parallel()

	testFs := afero.NewMemMapFs()

	// short term setting pair
	afero.WriteFile(testFs, "/sys/class/powercap/intel-rapl:0/constraint_1_power_limit_uw", []byte("111000000\n"), 0o755)
	afero.WriteFile(testFs, "/sys/class/powercap/intel-rapl:1/constraint_1_power_limit_uw", []byte("111000000\n"), 0o755)
	// long term setting pair
	afero.WriteFile(testFs, "/sys/class/powercap/intel-rapl:0/constraint_0_power_limit_uw", []byte("100000000\n"), 0o755)
	afero.WriteFile(testFs, "/sys/class/powercap/intel-rapl:1/constraint_0_power_limit_uw", []byte("100000000\n"), 0o755)

	resetter := raplGetSetter{
		fs:       testFs,
		packages: 2,
	}

	resetter.Reset()

	shortTerm0, _ := afero.ReadFile(testFs, "/sys/class/powercap/intel-rapl:0/constraint_1_power_limit_uw")
	if strings.TrimSpace(string(shortTerm0)) != "120000000" {
		t.Errorf("expected 120000000, got %s", string(shortTerm0))
	}
	shortTerm1, _ := afero.ReadFile(testFs, "/sys/class/powercap/intel-rapl:1/constraint_1_power_limit_uw")
	if strings.TrimSpace(string(shortTerm1)) != "120000000" {
		t.Errorf("expected 120000000, got %s", string(shortTerm1))
	}
}

func Test_raplGetSetter_Init(t *testing.T) {
	t.Parallel()

	testFs := afero.NewMemMapFs()

	// short term setting pair
	afero.WriteFile(testFs, "/sys/class/powercap/intel-rapl:0/constraint_1_power_limit_uw", []byte("111000000\n"), 0o755)
	afero.WriteFile(testFs, "/sys/class/powercap/intel-rapl:1/constraint_1_power_limit_uw", []byte("111000000\n"), 0o755)
	afero.WriteFile(testFs, "/sys/class/powercap/intel-rapl:2/constraint_1_power_limit_uw", []byte("111000000\n"), 0o755)

	initer := raplGetSetter{
		fs: testFs,
	}

	if err := initer.Init(); err != nil {
		t.Errorf("unexpected error %#v", err)
	}

	if initer.packages != 3 {
		t.Errorf("expected 3 packages; got %d", initer.packages)
	}
}
