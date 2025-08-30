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

func TestMalachiteClient_GetMBData(t *testing.T) {
	t.Parallel()

	notGoodSample := `{"status":404}`
	validSample := `{
    "status": 0,
    "data": {
        "resctrl": {
			"group_mbm": {
				"root": [
	                {
						"id": 21,
						"mbm_local_bytes": 127109639580672,
						"mbm_total_bytes": 335021042372672,
						"mbm_victim_bytes_psec": 0
					},
                    {
                        "id": 22,
                        "mbm_local_bytes": 121527184609280,
                        "mbm_total_bytes": 353262632387840,
                        "mbm_victim_bytes_psec": 0
                    }
				],
				"shared-50": [
	                {
	                    "id": 21,
						"mbm_local_bytes": 115340555748096,
	                    "mbm_total_bytes": 335021042372672,
	                    "mbm_victim_bytes_psec": 0
	                },
	                {
						"id": 22,
	                    "mbm_local_bytes": 17538131215616,
	                    "mbm_total_bytes": 171281952255936,
	                    "mbm_victim_bytes_psec": 0
					}
				]
			},
            "path": "/sys/fs/resctrl/",
            "update_time": 1755884558
        }
    }
}
`
	_ = validSample

	tests := []struct {
		name       string
		respBody   string
		statusCode int
		want       *types.MBData
		wantErr    assert.ErrorAssertionFunc
	}{
		{
			name:       "negative for not good sample",
			respBody:   notGoodSample,
			statusCode: 200,
			want:       nil,
			wantErr:    assert.Error,
		},
		{
			name:       "happy path",
			respBody:   validSample,
			statusCode: 200,
			want: &types.MBData{
				MBBody: types.MBGroupData{
					"root": {
						{
							CCDID:          21,
							MBLocalCounter: 127109639580672,
							MBTotalCounter: 335021042372672,
						},
						{
							CCDID:          22,
							MBLocalCounter: 121527184609280,
							MBTotalCounter: 353262632387840,
						},
					},
					"shared-50": {
						{
							CCDID:          21,
							MBLocalCounter: 115340555748096,
							MBTotalCounter: 335021042372672,
						},
						{
							CCDID:          22,
							MBLocalCounter: 17538131215616,
							MBTotalCounter: 171281952255936,
						},
					},
				},
				UpdateTime: 1755884558,
			},
			wantErr: assert.NoError,
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
				urls:    map[string]string{"realtime/resctrl": s.URL},
				emitter: &metrics.DummyMetrics{},
			}
			got, err := c.GetMBData()
			if !tt.wantErr(t, err, fmt.Sprintf("GetMBData()")) {
				return
			}
			assert.Equalf(t, tt.want, got, "GetMBData()")
		})
	}
}
