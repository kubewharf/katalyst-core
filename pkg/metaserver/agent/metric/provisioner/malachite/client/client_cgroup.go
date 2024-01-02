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

	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/provisioner/malachite/types"
)

func (c *MalachiteClient) GetCgroupStats(cgroupPath string) (*types.MalachiteCgroupInfo, error) {
	cgroupStatsRaw, err := c.getCgroupStats(cgroupPath)
	if err != nil {
		return nil, err
	}

	rsp := &types.MalachiteCgroupResponse{}
	if err := json.Unmarshal(cgroupStatsRaw, rsp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal cgroup status raw data, err %s", err)
	}

	if rsp.Status != 0 {
		return nil, fmt.Errorf("cgroup %s stats status is not ok, %d", cgroupPath, rsp.Status)
	}

	cgroupInfo := &types.MalachiteCgroupInfo{
		MountPoint: rsp.Data.MountPoint,
		UserPath:   rsp.Data.UserPath,
		CgroupType: rsp.Data.CgroupType,
	}

	if cgroupInfo.CgroupType == "V1" {
		subsysV1 := &types.SubSystemGroupsV1{}
		if err := json.Unmarshal(rsp.Data.SubSystemGroups, subsysV1); err != nil {
			return nil, fmt.Errorf("failed to Unmarshal cgroup v1 info, err %s", err)
		}
		cgV1 := &types.MalachiteCgroupV1Info{
			Memory: &subsysV1.Memory.V1.MemoryV1Data,
			Blkio:  &subsysV1.Blkio.V1.BlkIOData,
			Cpu:    &subsysV1.Cpuacct.V1.CPUData,
			CpuSet: &subsysV1.Cpuset.V1.CPUSetData,
			NetCls: &subsysV1.NetCls.NetData,
		}
		cgroupInfo.V1 = cgV1
	} else if cgroupInfo.CgroupType == "V2" {
		subsysV2 := &types.SubSystemGroupsV2{}
		if err := json.Unmarshal(rsp.Data.SubSystemGroups, subsysV2); err != nil {
			return nil, fmt.Errorf("failed to Unmarshal cgroup v2 info, err %s", err)
		}
		cgV2 := &types.MalachiteCgroupV2Info{
			Memory: &subsysV2.Memory.V2.MemoryData,
			Blkio:  &subsysV2.Blkio.V2.BlkIOData,
			Cpu:    &subsysV2.Cpuacct.V2.CPUData,
			CpuSet: &subsysV2.Cpuset.V2.CPUSetData,
			NetCls: &subsysV2.NetCls.NetData,
		}
		cgroupInfo.V2 = cgV2
	} else {
		return nil, fmt.Errorf("unknow cgroup type %s in cgroup info", cgroupInfo.CgroupType)
	}

	return cgroupInfo, nil
}

func (c *MalachiteClient) getCgroupStats(cgroupPath string) ([]byte, error) {
	c.RLock()
	defer c.RUnlock()

	url, ok := c.urls[CgroupResource]
	if !ok {
		return nil, fmt.Errorf("no url for %v", CgroupResource)
	}

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to http.NewRequest, url: %s, err %s", url, err)
	}

	q := req.URL.Query()
	q.Add(CgroupPathParamKey, cgroupPath)
	req.URL.RawQuery = q.Encode()

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
