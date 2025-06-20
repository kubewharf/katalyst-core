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
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

func TestMalachiteClient_GetSysFreqData(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		respBody string
		want     *types.SysFreqData
		wantErr  assert.ErrorAssertionFunc
	}{
		{
			name: "happy path",
			respBody: `{"status":0,
				"data":{
					"sysfreq":{
						"cpu_freq":[{"scaling_cur_freq":2598884}],
						"update_time":1748574860
					}
				}
			}`,
			want: &types.SysFreqData{
				SysFreq: types.SysFreq{
					CPUFreq: []types.CurFreq{
						{ScalingCurFreqKHZ: 2598884},
					},
					UpdateTime: 1748574860,
				},
			},
			wantErr: assert.NoError,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(200)
				_, _ = w.Write([]byte(tt.respBody))
			}))
			defer s.Close()

			c := &MalachiteClient{
				emitter: metrics.DummyMetrics{},
				urls:    map[string]string{"realtime/freq": s.URL},
			}
			got, err := c.GetSysFreqData()
			if !tt.wantErr(t, err, fmt.Sprintf("GetSysFreqData()")) {
				return
			}
			assert.Equalf(t, tt.want, got, "GetSysFreqData()")
		})
	}
}
