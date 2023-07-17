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
	"fmt"

	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/malachite/client"
)

func GetCgroupStats(c client.MalachiteClient, cgroupPath string) (*MalachiteCgroupInfo, error) {
	cgroupStatsRaw, err := c.GetCgroupStats(cgroupPath)
	if err != nil {
		return nil, err
	}

	rsp := &MalachiteCgroupResponse{}
	if err := json.Unmarshal(cgroupStatsRaw, rsp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal cgroup status raw data, err %s", err)
	}

	if rsp.Status != 0 {
		return nil, fmt.Errorf("cgroup %s stats status is not ok, %d", cgroupPath, rsp.Status)
	}

	cgroupInfo := &MalachiteCgroupInfo{
		MountPoint: rsp.Data.MountPoint,
		UserPath:   rsp.Data.UserPath,
		CgroupType: rsp.Data.CgroupType,
	}

	if cgroupInfo.CgroupType == "V1" {
		subsysV1 := &SubSystemGroupsV1{}
		if err := json.Unmarshal(rsp.Data.SubSystemGroups, subsysV1); err != nil {
			return nil, fmt.Errorf("failed to Unmarshal cgroup v1 info, err %s", err)
		}
		cgV1 := &MalachiteCgroupV1Info{
			Memory:    &subsysV1.Memory.V1.MemoryV1Data,
			Blkio:     &subsysV1.Blkio.V1.BlkIOData,
			Cpu:       &subsysV1.Cpuacct.V1.CPUData,
			CpuSet:    &subsysV1.Cpuset.V1.CPUSetData,
			PerfEvent: &subsysV1.PerfEvent.PerfEventData,
			NetCls:    &subsysV1.NetCls.NetData,
		}
		cgroupInfo.V1 = cgV1
	} else if cgroupInfo.CgroupType == "V2" {
		subsysV2 := &SubSystemGroupsV2{}
		if err := json.Unmarshal(rsp.Data.SubSystemGroups, subsysV2); err != nil {
			return nil, fmt.Errorf("failed to Unmarshal cgroup v2 info, err %s", err)
		}
		cgV2 := &MalachiteCgroupV2Info{
			Memory:    &subsysV2.Memory.V2.MemoryData,
			Blkio:     &subsysV2.Blkio.V2.BlkIOData,
			Cpu:       &subsysV2.Cpuacct.V2.CPUData,
			CpuSet:    &subsysV2.Cpuset.V2.CPUSetData,
			PerfEvent: &subsysV2.PerfEvent.PerfEventData,
			NetCls:    &subsysV2.NetCls.NetData,
		}
		cgroupInfo.V2 = cgV2
	} else {
		return nil, fmt.Errorf("unknow cgroup type %s in cgroup info", cgroupInfo.CgroupType)
	}

	return cgroupInfo, nil
}

func GetAllPodsContainersStats(c client.MalachiteClient) (map[string]map[string]*MalachiteCgroupInfo, error) {
	podsContainersCgroupsPath, err := GetAllPodsContainersCgroupsPath()
	if err != nil {
		return nil, fmt.Errorf("failed to GetAllPodsContainersCgroupsPath, err %v", err)
	}

	podsContainersStats := make(map[string]map[string]*MalachiteCgroupInfo)
	for podUid, containersCgroupsPath := range podsContainersCgroupsPath {
		containersStats := make(map[string]*MalachiteCgroupInfo)
		for containerName, cgroupsPath := range containersCgroupsPath {
			cgStats, err := GetCgroupStats(c, cgroupsPath)
			if err != nil {
				klog.V(4).ErrorS(fmt.Errorf("failed to GetCgroupStats for cgroup %s in pod %s, err %s", cgroupsPath, podUid, err), "")
				continue
			}
			containersStats[containerName] = cgStats
		}
		podsContainersStats[podUid] = containersStats
	}
	return podsContainersStats, nil
}
