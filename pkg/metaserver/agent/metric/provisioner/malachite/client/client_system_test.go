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

var (
	fakeSystemCompute = &types.MalachiteSystemComputeResponse{
		Status: 0,
		Data: types.SystemComputeData{
			Load: types.Load{},
			CPU: []types.CPU{
				{
					Name: "CPU1111",
				},
			},
		},
	}

	fakeSystemMemory = &types.MalachiteSystemMemoryResponse{
		Status: 0,
		Data: types.SystemMemoryData{
			System: types.System{},
			Numa: []types.Numa{
				{},
			},
		},
	}

	fakeSystemIO = &types.MalachiteSystemDiskIoResponse{
		Status: 0,
		Data: types.SystemDiskIoData{
			DiskIo: []types.DiskIo{
				{},
			},
		},
	}

	fakeSystemNet = &types.MalachiteSystemNetworkResponse{
		Status: 0,
		Data: types.SystemNetworkData{
			NetworkCard: []types.NetworkCard{
				{},
			},
			TCP: types.TCP{},
		},
	}
)

func getSystemTestServer(data []byte) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Response == nil {
			r.Response = &http.Response{}
		}
		r.Response.StatusCode = http.StatusOK
		_, _ = w.Write(data)

	}))

}

func TestGetSystemComputeStats(t *testing.T) {
	t.Parallel()

	data, _ := json.Marshal(fakeSystemCompute)
	server := getSystemTestServer(data)
	defer server.Close()

	malachiteClient := NewMalachiteClient(&pod.PodFetcherStub{})
	malachiteClient.SetURL(map[string]string{
		SystemComputeResource: server.URL,
	})
	_, err := malachiteClient.GetSystemComputeStats()
	assert.NoError(t, err)

	malachiteClient.SetURL(map[string]string{
		SystemComputeResource: "none",
	})
	_, err = malachiteClient.GetSystemComputeStats()
	assert.NotNil(t, err)
}

func TestGetSystemMemoryStats(t *testing.T) {
	t.Parallel()

	data, _ := json.Marshal(fakeSystemMemory)
	server := getSystemTestServer(data)
	defer server.Close()

	malachiteClient := NewMalachiteClient(&pod.PodFetcherStub{})
	malachiteClient.SetURL(map[string]string{
		SystemMemoryResource: server.URL,
	})
	_, err := malachiteClient.GetSystemMemoryStats()
	assert.NoError(t, err)

	malachiteClient.SetURL(map[string]string{
		SystemMemoryResource: "none",
	})
	_, err = malachiteClient.GetSystemComputeStats()
	assert.NotNil(t, err)
}

func TestGetSystemIOStats(t *testing.T) {
	t.Parallel()

	data, _ := json.Marshal(fakeSystemIO)
	server := getSystemTestServer(data)
	defer server.Close()

	malachiteClient := NewMalachiteClient(&pod.PodFetcherStub{})
	malachiteClient.SetURL(map[string]string{
		SystemIOResource: server.URL,
	})
	_, err := malachiteClient.GetSystemIOStats()
	assert.NoError(t, err)

	malachiteClient.SetURL(map[string]string{
		SystemIOResource: "none",
	})
	_, err = malachiteClient.GetSystemComputeStats()
	assert.NotNil(t, err)
}

func TestGetSystemNetStats(t *testing.T) {
	t.Parallel()

	data, _ := json.Marshal(fakeSystemNet)
	server := getSystemTestServer(data)
	defer server.Close()

	malachiteClient := NewMalachiteClient(&pod.PodFetcherStub{})
	malachiteClient.SetURL(map[string]string{
		SystemNetResource: server.URL,
	})
	_, err := malachiteClient.GetSystemNetStats()
	assert.NoError(t, err)

	malachiteClient.SetURL(map[string]string{
		SystemNetResource: "none",
	})
	_, err = malachiteClient.GetSystemComputeStats()
	assert.NotNil(t, err)
}

func TestGetSystemNonExistStats(t *testing.T) {
	t.Parallel()

	server := getSystemTestServer([]byte{})
	defer server.Close()

	malachiteClient := NewMalachiteClient(&pod.PodFetcherStub{})
	_, err := malachiteClient.getSystemStats(100)
	assert.ErrorContains(t, err, "unknown")
}
