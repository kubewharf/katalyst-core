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

package spec

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestGetPowerAlertResponseTimeLimit(t *testing.T) {
	t.Parallel()
	type args struct {
		alert PowerAlert
	}
	tests := []struct {
		name    string
		args    args
		want    time.Duration
		wantErr bool
	}{
		{
			name: "happy path gets known setting, upper case of alert is ok",
			args: args{
				alert: "S0",
			},
			want:    time.Minute * 2,
			wantErr: false,
		},
		{
			name: "negative path returns error",
			args: args{
				alert: "other",
			},
			want:    0,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := GetPowerAlertResponseTimeLimit(tt.args.alert)
			if err != nil != tt.wantErr {
				t.Errorf("unexpected error case: expected %v, got %v", tt.wantErr, err)
			}
			assert.Equalf(t, tt.want, got, "GetPowerAlertResponseTimeLimit(%v)", tt.args.alert)
		})
	}
}

func TestGetPowerSpec(t *testing.T) {
	t.Parallel()

	loc, _ := time.LoadLocation("America/Los_Angeles")
	timeTest := time.Date(2024, time.June, 1, 12, 15, 58, 0, loc).UTC()
	timeInRFC3339 := "2024-06-01T19:15:58Z"

	type args struct {
		node       *v1.Node
		annoPrefix string
	}
	tests := []struct {
		name    string
		args    args
		want    *PowerSpec
		wantErr assert.ErrorAssertionFunc
	}{
		{
			name: "happy path get valid power spec",
			args: args{
				node: &v1.Node{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							"foo/power-alert":       "s0",
							"foo/power-budget":      "128",
							"foo/power-internal-op": "0",
							"foo/power-alert-time":  timeInRFC3339,
						},
					},
				},
				annoPrefix: "foo",
			},
			want: &PowerSpec{
				Alert:      PowerAlertS0,
				Budget:     128,
				InternalOp: InternalOpAuto,
				AlertTime:  timeTest,
			},
			wantErr: assert.NoError,
		},
		{
			name: "no power_alert annotation is OK alert",
			args: args{
				node: &v1.Node{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{},
					},
				},
			},
			want: &PowerSpec{
				Alert:      PowerAlertOK,
				Budget:     0,
				InternalOp: 0,
			},
			wantErr: assert.NoError,
		},
		{
			name: "float is OK converting to int",
			args: args{
				node: &v1.Node{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							"foo/power-alert":      "s0",
							"foo/power-budget":     "128.51",
							"foo/power-alert-time": timeInRFC3339,
						},
					},
				},
				annoPrefix: "foo",
			},
			want: &PowerSpec{
				Alert:     PowerAlertS0,
				Budget:    128,
				AlertTime: timeTest,
			},
			wantErr: assert.NoError,
		},
		{
			name: "missing budget is a bad spec",
			args: args{
				node: &v1.Node{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							"foo/power-alert":       "f2",
							"foo/power-internal-op": "0",
							"foo/power-alert-time":  timeInRFC3339,
						},
					},
				},
				annoPrefix: "foo",
			},
			want:    nil,
			wantErr: assert.Error,
		},
		{
			name: "missing power_internal_op fine default to autonomous",
			args: args{
				node: &v1.Node{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							"foo/power-alert":      "P2",
							"foo/power-budget":     "128",
							"foo/power-alert-time": timeInRFC3339,
						},
					},
				},
				annoPrefix: "foo",
			},
			want: &PowerSpec{
				Alert:      PowerAlertP2,
				Budget:     128,
				InternalOp: InternalOpAuto,
				AlertTime:  timeTest,
			},
			wantErr: assert.NoError,
		},
		{
			name: "non int power_inter_op is bad spec",
			args: args{
				node: &v1.Node{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							"foo/power-alert":       "s0",
							"foo/power-budget":      "128",
							"foo/power-internal-op": "non-int",
							"foo/power-alert-time":  timeInRFC3339,
						},
					},
				},
				annoPrefix: "foo",
			},
			want:    nil,
			wantErr: assert.Error,
		},
		{
			name: "non rfc3339 time bad time format",
			args: args{
				node: &v1.Node{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							"foo/power-alert":       "s0",
							"foo/power-budget":      "128",
							"foo/power-internal-op": "0",
							"foo/power-alert-time":  "2024-06-01 15:17:30",
						},
					},
				},
				annoPrefix: "foo",
			},
			want:    nil,
			wantErr: assert.Error,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := getPowerSpec(tt.args.annoPrefix, tt.args.node)
			if !tt.wantErr(t, err, fmt.Sprintf("getPowerSpec(%v)", tt.args.node)) {
				return
			}
			assert.Equalf(t, tt.want, got, "getPowerSpec(%v)", tt.args.node)
		})
	}
}
