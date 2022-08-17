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

package malachite

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

var (
	fakeSystemCompute = &MalachiteSystemComputeResponse{
		Status: 0,
		Data: SystemComputeData{
			Load: Load{},
			CPU: []CPU{
				{
					Name: "CPU1111",
				},
			},
		},
	}

	fakeSystemMemory = &MalachiteSystemMemoryResponse{
		Status: 0,
		Data: SystemMemoryData{
			System: System{},
			Numa: []Numa{
				{},
			},
		},
	}

	fakeSystemIO = &MalachiteSystemDiskIoResponse{
		Status: 0,
		Data: SystemDiskIoData{
			DiskIo: []DiskIo{
				{},
			},
		},
	}

	fakeSystemNet = &MalachiteSystemNetworkResponse{
		Status: 0,
		Data: SystemNetworkData{
			NetworkCard: []NetworkCard{
				{},
			},
			TCP: TCP{},
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
	data, _ := json.Marshal(fakeSystemCompute)
	server := getSystemTestServer(data)
	defer server.Close()

	DefaultClient.(*client).SetURL(map[string]string{
		computeResource: server.URL,
	})
	_, err := GetSystemComputeStats()
	assert.NoError(t, err)

	DefaultClient.(*client).SetURL(map[string]string{
		computeResource: "none",
	})
	_, err = GetSystemComputeStats()
	assert.NotNil(t, err)
}

func TestGetSystemMemoryStats(t *testing.T) {
	data, _ := json.Marshal(fakeSystemMemory)
	server := getSystemTestServer(data)
	defer server.Close()

	DefaultClient.(*client).SetURL(map[string]string{
		memoryResource: server.URL,
	})
	_, err := GetSystemMemoryStats()
	assert.NoError(t, err)

	DefaultClient.(*client).SetURL(map[string]string{
		memoryResource: "none",
	})
	_, err = GetSystemComputeStats()
	assert.NotNil(t, err)
}

func TestGetSystemIOStats(t *testing.T) {
	data, _ := json.Marshal(fakeSystemIO)
	server := getSystemTestServer(data)
	defer server.Close()

	DefaultClient.(*client).SetURL(map[string]string{
		ioResource: server.URL,
	})
	_, err := GetSystemIOStats()
	assert.NoError(t, err)

	DefaultClient.(*client).SetURL(map[string]string{
		ioResource: "none",
	})
	_, err = GetSystemComputeStats()
	assert.NotNil(t, err)
}

func TestGetSystemNetStats(t *testing.T) {
	data, _ := json.Marshal(fakeSystemNet)
	server := getSystemTestServer(data)
	defer server.Close()

	DefaultClient.(*client).SetURL(map[string]string{
		netResource: server.URL,
	})
	_, err := GetSystemNetStats()
	assert.NoError(t, err)

	DefaultClient.(*client).SetURL(map[string]string{
		netResource: "none",
	})
	_, err = GetSystemComputeStats()
	assert.NotNil(t, err)
}

func TestGetSystemNonExistStats(t *testing.T) {
	server := getSystemTestServer([]byte{})
	defer server.Close()

	_, err := DefaultClient.GetSystemStats(100)
	assert.ErrorContains(t, err, "unknown")
}
