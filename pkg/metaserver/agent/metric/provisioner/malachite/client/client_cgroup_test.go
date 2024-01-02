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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/provisioner/malachite/types"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
)

var subSystemGroupsV1Data []byte
var subSystemGroupsV2Data []byte

var (
	subSystemGroupsV1 = &types.SubSystemGroupsV1{
		Memory: types.MemoryCgV1{
			V1: struct {
				MemoryV1Data types.MemoryCgDataV1 `json:"V1"`
			}(struct{ MemoryV1Data types.MemoryCgDataV1 }{MemoryV1Data: types.MemoryCgDataV1{}}),
		},
		Blkio: types.BlkioCgV1{
			V1: struct {
				BlkIOData types.BlkIOCgDataV1 `json:"V1"`
			}(struct{ BlkIOData types.BlkIOCgDataV1 }{BlkIOData: types.BlkIOCgDataV1{}}),
		},
		NetCls: types.NetClsCg{
			NetData: types.NetClsCgData{},
		},
		Cpuset: types.CpusetCgV1{
			V1: struct {
				CPUSetData types.CPUSetCgDataV1 `json:"V1"`
			}(struct{ CPUSetData types.CPUSetCgDataV1 }{CPUSetData: types.CPUSetCgDataV1{}}),
		},
		Cpuacct: types.CpuacctCgV1{
			V1: struct {
				CPUData types.CPUCgDataV1 `json:"V1"`
			}(struct{ CPUData types.CPUCgDataV1 }{CPUData: types.CPUCgDataV1{}}),
		},
	}

	subSystemGroupsV2 = &types.SubSystemGroupsV2{
		Memory: types.MemoryCgV2{
			V2: struct {
				MemoryData types.MemoryCgDataV2 `json:"V2"`
			}(struct{ MemoryData types.MemoryCgDataV2 }{MemoryData: types.MemoryCgDataV2{}}),
		},
		Blkio: types.BlkioCgV2{
			V2: struct {
				BlkIOData types.BlkIOCgDataV2 `json:"V2"`
			}(struct{ BlkIOData types.BlkIOCgDataV2 }{BlkIOData: types.BlkIOCgDataV2{}}),
		},
		NetCls: types.NetClsCg{
			NetData: types.NetClsCgData{},
		},
		Cpuset: types.CpusetCgV2{
			V2: struct {
				CPUSetData types.CPUSetCgDataV2 `json:"V2"`
			}(struct{ CPUSetData types.CPUSetCgDataV2 }{CPUSetData: types.CPUSetCgDataV2{}}),
		},
		Cpuacct: types.CpuacctCgV2{
			V2: struct {
				CPUData types.CPUCgDataV2 `json:"V2"`
			}(struct{ CPUData types.CPUCgDataV2 }{CPUData: types.CPUCgDataV2{}}),
		},
	}
)

func init() {
	subSystemGroupsV1Data, _ = json.Marshal(subSystemGroupsV1)
	subSystemGroupsV2Data, _ = json.Marshal(subSystemGroupsV2)
}

func TestGetCgroupStats(t *testing.T) {
	t.Parallel()

	cgroupData := map[string]*types.MalachiteCgroupResponse{
		"v1-path": {
			Status: 0,
			Data: types.CgroupDataInner{
				CgroupType:      "V1",
				SubSystemGroups: subSystemGroupsV1Data,
			},
		},
		"v2-path": {
			Status: 0,
			Data: types.CgroupDataInner{
				CgroupType:      "V2",
				SubSystemGroups: subSystemGroupsV2Data,
			},
		},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Response == nil {
			r.Response = &http.Response{}
		}
		r.Response.StatusCode = http.StatusOK

		q := r.URL.Query()
		rPath := q.Get(CgroupPathParamKey)
		for path, info := range cgroupData {
			if path == rPath {
				data, _ := json.Marshal(info)
				_, _ = w.Write(data)
				return
			}
		}

		r.Response.StatusCode = http.StatusBadRequest
	}))
	defer server.Close()

	malachiteClient := NewMalachiteClient(&pod.PodFetcherStub{})
	malachiteClient.SetURL(map[string]string{
		CgroupResource: server.URL,
	})

	info, err := malachiteClient.GetCgroupStats("v1-path")
	assert.NoError(t, err)
	assert.NotNil(t, info.V1)
	assert.Nil(t, info.V2)

	info, err = malachiteClient.GetCgroupStats("v2-path")
	assert.NoError(t, err)
	assert.NotNil(t, info.V2)
	assert.Nil(t, info.V1)

	_, err = malachiteClient.GetCgroupStats("no-exist-path")
	assert.NotNil(t, err)
	assert.NotNil(t, info.V2)
	assert.Nil(t, info.V1)
}
