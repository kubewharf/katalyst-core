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
	"fmt"
	"io/ioutil"
	"net/http"
	"time"

	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/provisioner/malachite/types"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

func (c *MalachiteClient) GetSystemInfoStats() (*types.SystemInfoData, error) {
	statsData, err := c.getSystemStats(Info)
	if err != nil {
		return nil, err
	}

	rsp := &types.MalachiteSystemInfoResponse{}
	if err := json.Unmarshal(statsData, rsp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal system info stats raw data, err %s", err)
	}

	if rsp.Status != 0 {
		return nil, fmt.Errorf("system info stats status is not ok, %d", rsp.Status)
	}

	c.checkSystemStatsOutOfDate("info", UpdateTimeout, rsp.Data.UpdateTime)
	return &rsp.Data, nil
}

func (c *MalachiteClient) GetSystemComputeStats() (*types.SystemComputeData, error) {
	statsData, err := c.getSystemStats(Compute)
	if err != nil {
		return nil, err
	}

	rsp := &types.MalachiteSystemComputeResponse{}
	if err := json.Unmarshal(statsData, rsp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal system compute stats raw data, err %s", err)
	}

	if rsp.Status != 0 {
		return nil, fmt.Errorf("system compute stats status is not ok, %d", rsp.Status)
	}

	c.checkSystemStatsOutOfDate("compute", UpdateTimeout, rsp.Data.UpdateTime)
	return &rsp.Data, nil
}

func (c *MalachiteClient) GetSystemMemoryStats() (*types.SystemMemoryData, error) {
	statsData, err := c.getSystemStats(Memory)
	if err != nil {
		return nil, err
	}

	rsp := &types.MalachiteSystemMemoryResponse{}
	if err := json.Unmarshal(statsData, rsp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal system memory stats raw data, err %s", err)
	}

	if rsp.Status != 0 {
		return nil, fmt.Errorf("system memory stats status is not ok, %d", rsp.Status)
	}

	c.checkSystemStatsOutOfDate("memory", UpdateTimeout, rsp.Data.UpdateTime)
	return &rsp.Data, nil
}

func (c *MalachiteClient) GetSystemIOStats() (*types.SystemSystemIoData, error) {
	statsData, err := c.getSystemStats(IO)
	if err != nil {
		return nil, err
	}

	rsp := &types.MalachiteSystemIoResponse{}
	if err := json.Unmarshal(statsData, rsp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal system io stats raw data, err %s", err)
	}

	if rsp.Status != 0 {
		return nil, fmt.Errorf("system io stats status is not ok, %d", rsp.Status)
	}

	c.checkSystemStatsOutOfDate("io", UpdateTimeout, rsp.Data.UpdateTime)
	return &rsp.Data, nil
}

func (c *MalachiteClient) GetSystemNetStats() (*types.SystemNetworkData, error) {
	statsData, err := c.getSystemStats(Net)
	if err != nil {
		return nil, err
	}

	rsp := &types.MalachiteSystemNetworkResponse{}
	if err := json.Unmarshal(statsData, rsp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal system network stats raw data, err %s", err)
	}

	if rsp.Status != 0 {
		return nil, fmt.Errorf("system network stats status is not ok, %d", rsp.Status)
	}

	c.checkSystemStatsOutOfDate("network", UpdateTimeout, rsp.Data.UpdateTime)
	return &rsp.Data, nil
}

func (c *MalachiteClient) getSystemStats(kind SystemResourceKind) ([]byte, error) {
	c.RLock()
	defer c.RUnlock()

	resource := ""
	switch kind {
	case Info:
		resource = SystemInfoResource
	case Compute:
		resource = SystemComputeResource
	case Memory:
		resource = SystemMemoryResource
	case IO:
		resource = SystemIOResource
	case Net:
		resource = SystemNetResource
	default:
		return nil, fmt.Errorf("unknown system resource kind, %v", kind)
	}

	url, ok := c.urls[resource]
	if !ok {
		return nil, fmt.Errorf("no url for %v", resource)
	}

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to http.NewRequest, url: %s, err %s", url, err)
	}

	rsp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to http.DefaultClient.Do, url: %s, err %s", req.URL, err)
	}

	defer func() { _ = rsp.Body.Close() }()

	if rsp.StatusCode != 200 {
		return nil, fmt.Errorf("invalid http response status code %d, url: %s", rsp.StatusCode, req.URL)
	}

	return ioutil.ReadAll(rsp.Body)
}

func (c *MalachiteClient) checkSystemStatsOutOfDate(statsType string, timeout time.Duration, updateTimestamp int64) {
	updateTime := time.Unix(updateTimestamp, 0)
	if time.Since(updateTime) <= timeout {
		return
	}

	general.Warningf(
		"malachite system %s stats outdated, last update time %s",
		statsType, updateTime)
	_ = c.emitter.StoreInt64(metricMalachiteSystemStatsOutOfDate, 1, metrics.MetricTypeNameCount, metrics.MetricTag{
		Key: "type",
		Val: statsType,
	})
}
