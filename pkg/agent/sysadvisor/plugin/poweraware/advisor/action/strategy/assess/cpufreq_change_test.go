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

package assess

import "testing"

func Test_cpuFreqChangeAssessor_AccumulateEffect(t *testing.T) {
	t.Parallel()
	type fields struct {
		initFreqKHZ int
	}
	type args struct {
		current int
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		want    int
		wantErr bool
	}{
		{
			name: "happy path",
			fields: fields{
				initFreqKHZ: 2500_000,
			},
			args: args{
				current: 2000_000,
			},
			want:    20,
			wantErr: false,
		},
		{
			name: "negative low current freq",
			fields: fields{
				initFreqKHZ: 2500_000,
			},
			args: args{
				current: 777_000,
			},
			want:    0,
			wantErr: true,
		},
		{
			name: "fine for higher freq no change as temporary spike",
			fields: fields{
				initFreqKHZ: 2500_000,
			},
			args: args{
				current: 2600_000,
			},
			want:    0,
			wantErr: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := &cpuFreqChangeAssessor{
				highFreqKHZ: tt.fields.initFreqKHZ,
			}
			got, err := c.assessEffectByFreq(tt.args.current)
			if (err != nil) != tt.wantErr {
				t.Errorf("AssessEffect() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("AssessEffect() got = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_cpuFreqChangeAssessor_AssessTarget(t *testing.T) {
	t.Parallel()
	type fields struct {
		initFreqMhz int
	}
	type args struct {
		actualWatt         int
		desiredWatt        int
		maxDecreasePercent int
	}
	tests := []struct {
		name   string
		fields fields
		args   args
		want   int
	}{
		{
			name: "happy path",
			fields: fields{
				initFreqMhz: 2500,
			},
			args: args{
				actualWatt:         100,
				desiredWatt:        80,
				maxDecreasePercent: 10,
			},
			want: 95,
		},
		{
			name: "happy path",
			fields: fields{
				initFreqMhz: 2500,
			},
			args: args{
				actualWatt:         100,
				desiredWatt:        80,
				maxDecreasePercent: 0,
			},
			want: 100,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := &cpuFreqChangeAssessor{
				highFreqKHZ: tt.fields.initFreqMhz,
			}
			if got := c.AssessTarget(tt.args.actualWatt, tt.args.desiredWatt, tt.args.maxDecreasePercent); got != tt.want {
				t.Errorf("AssessTarget() = %v, want %v", got, tt.want)
			}
		})
	}
}
