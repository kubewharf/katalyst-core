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

package system

import (
	"encoding/json"
	"fmt"

	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/malachite/client"
)

func GetSystemComputeStats(c client.MalachiteClient) (*SystemComputeData, error) {
	statsData, err := c.GetSystemStats(client.Compute)
	if err != nil {
		return nil, err
	}

	rsp := &MalachiteSystemComputeResponse{}
	if err := json.Unmarshal(statsData, rsp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal system compute stats raw data, err %s", err)
	}

	if rsp.Status != 0 {
		return nil, fmt.Errorf("system compute stats status is not ok, %d", rsp.Status)
	}

	return &rsp.Data, nil
}

func GetSystemMemoryStats(c client.MalachiteClient) (*SystemMemoryData, error) {
	statsData, err := c.GetSystemStats(client.Memory)
	if err != nil {
		return nil, err
	}

	rsp := &MalachiteSystemMemoryResponse{}
	if err := json.Unmarshal(statsData, rsp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal system memory stats raw data, err %s", err)
	}

	if rsp.Status != 0 {
		return nil, fmt.Errorf("system memory stats status is not ok, %d", rsp.Status)
	}

	return &rsp.Data, nil
}

func GetSystemIOStats(c client.MalachiteClient) (*SystemDiskIoData, error) {
	statsData, err := c.GetSystemStats(client.IO)
	if err != nil {
		return nil, err
	}

	rsp := &MalachiteSystemDiskIoResponse{}
	if err := json.Unmarshal(statsData, rsp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal system io stats raw data, err %s", err)
	}

	if rsp.Status != 0 {
		return nil, fmt.Errorf("system io stats status is not ok, %d", rsp.Status)
	}

	return &rsp.Data, nil
}

func GetSystemNetStats(c client.MalachiteClient) (*SystemNetworkData, error) {
	statsData, err := c.GetSystemStats(client.Net)
	if err != nil {
		return nil, err
	}

	rsp := &MalachiteSystemNetworkResponse{}
	if err := json.Unmarshal(statsData, rsp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal system network stats raw data, err %s", err)
	}

	if rsp.Status != 0 {
		return nil, fmt.Errorf("system network stats status is not ok, %d", rsp.Status)
	}

	return &rsp.Data, nil
}
