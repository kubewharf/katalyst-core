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

package cgroup

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/malachite/client"
)

var subSystemGroupsV1Data []byte
var subSystemGroupsV2Data []byte

var (
	subSystemGroupsV1 = &SubSystemGroupsV1{
		Memory: MemoryCg{
			V1: struct {
				MemoryV1Data MemoryCgDataV1 `json:"V1"`
			}(struct{ MemoryV1Data MemoryCgDataV1 }{MemoryV1Data: MemoryCgDataV1{}}),
		},
		Blkio: BlkioCg{
			V1: struct {
				BlkIOData BlkIOCgDataV1 `json:"V1"`
			}(struct{ BlkIOData BlkIOCgDataV1 }{BlkIOData: BlkIOCgDataV1{}}),
		},
		NetCls: NetClsCg{
			NetData: NetClsCgData{},
		},
		PerfEvent: PerfEventCg{
			PerfEventData: PerfEventData{},
		},
		Cpuset: CpusetCg{
			V1: struct {
				CPUSetData CPUSetCgDataV1 `json:"V1"`
			}(struct{ CPUSetData CPUSetCgDataV1 }{CPUSetData: CPUSetCgDataV1{}}),
		},
		Cpuacct: CpuacctCg{
			V1: struct {
				CPUData CPUCgDataV1 `json:"V1"`
			}(struct{ CPUData CPUCgDataV1 }{CPUData: CPUCgDataV1{}}),
		},
	}

	subSystemGroupsV2 = &SubSystemGroupsV2{
		Memory: MemoryCgV2{
			V2: struct {
				MemoryData MemoryCgDataV2 `json:"V2"`
			}(struct{ MemoryData MemoryCgDataV2 }{MemoryData: MemoryCgDataV2{}}),
		},
		Blkio: BlkioCgV2{
			V2: struct {
				BlkIOData BlkIOCgDataV2 `json:"V2"`
			}(struct{ BlkIOData BlkIOCgDataV2 }{BlkIOData: BlkIOCgDataV2{}}),
		},
		NetCls: NetClsCg{
			NetData: NetClsCgData{},
		},
		PerfEvent: PerfEventCg{
			PerfEventData: PerfEventData{},
		},
		Cpuset: CpusetCgV2{
			V2: struct {
				CPUSetData CPUSetCgDataV2 `json:"V2"`
			}(struct{ CPUSetData CPUSetCgDataV2 }{CPUSetData: CPUSetCgDataV2{}}),
		},
		Cpuacct: CpuacctCgV2{
			V2: struct {
				CPUData CPUCgDataV2 `json:"V2"`
			}(struct{ CPUData CPUCgDataV2 }{CPUData: CPUCgDataV2{}}),
		},
	}
)

func init() {
	subSystemGroupsV1Data, _ = json.Marshal(subSystemGroupsV1)
	subSystemGroupsV2Data, _ = json.Marshal(subSystemGroupsV2)
}

func TestGetCgroupStats(t *testing.T) {
	t.Parallel()

	cgroupData := map[string]*MalachiteCgroupResponse{
		"v1-path": {
			Status: 0,
			Data: CgroupDataInner{
				CgroupType:      "V1",
				SubSystemGroups: subSystemGroupsV1Data,
			},
		},
		"v2-path": {
			Status: 0,
			Data: CgroupDataInner{
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
		rPath := q.Get(client.CgroupPathParamKey)
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

	malachiteClient := client.New()
	malachiteClient.(*client.Client).SetURL(map[string]string{
		client.CgroupResource: server.URL,
	})

	info, err := GetCgroupStats(malachiteClient, "v1-path")
	assert.NoError(t, err)
	assert.NotNil(t, info.V1)
	assert.Nil(t, info.V2)

	info, err = GetCgroupStats(malachiteClient, "v2-path")
	assert.NoError(t, err)
	assert.NotNil(t, info.V2)
	assert.Nil(t, info.V1)

	_, err = GetCgroupStats(malachiteClient, "no-exist-path")
	assert.NotNil(t, err)
	assert.NotNil(t, info.V2)
	assert.Nil(t, info.V1)
}
