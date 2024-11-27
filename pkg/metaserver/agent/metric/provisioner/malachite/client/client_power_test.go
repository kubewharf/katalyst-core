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

package client

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/provisioner/malachite/types"
)

func TestMalachiteClient_GetPower(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		respBody   string
		statusCode int
		want       *types.PowerData
		wantErr    assert.ErrorAssertionFunc
	}{
		{
			name: "happy path",
			respBody: `{
				"status":0,
				"data":{
					"sensors":{
						"total_power":384.0,
						"cpu_power":196.0,
						"mem_power":132.0,
						"fan_power":16.0,
						"hdd_power":null,
						"psu0_pout":null,
						"psu0_pin":null,
						"psu1_pout":null,
						"psu1_pin":null,
						"cpu0_temp":44.0,
						"cpu1_temp":43.0,
						"psu0_temp":56.0,
						"psu1_temp":54.0,
						"update_time":1731430097
					}
				}
			}`,
			statusCode: 200,
			want: &types.PowerData{
				Sensors: types.SensorData{
					TotalPowerWatt: 384.0,
					UpdateTime:     1731430097,
				},
			},
			wantErr: assert.NoError,
		},
		{
			name:       "invalid payload format is error",
			respBody:   "invalid payload",
			statusCode: 200,
			want:       nil,
			wantErr:    assert.Error,
		},
		{
			name:       "http status code other than 200 is error",
			respBody:   "{}",
			statusCode: 502,
			want:       nil,
			wantErr:    assert.Error,
		},
		{
			name:       "payload status not 0 is error",
			respBody:   `{"status":4}`,
			statusCode: 200,
			want:       nil,
			wantErr:    assert.Error,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.statusCode)
				_, _ = w.Write([]byte(tt.respBody))
			}))
			defer s.Close()

			c := &MalachiteClient{
				urls: map[string]string{"realtime/power": s.URL},
			}
			got, err := c.GetPowerData()
			if !tt.wantErr(t, err, fmt.Sprintf("GetPowerData()")) {
				return
			}
			assert.Equalf(t, tt.want, got, "GetPowerData()")
		})
	}
}
