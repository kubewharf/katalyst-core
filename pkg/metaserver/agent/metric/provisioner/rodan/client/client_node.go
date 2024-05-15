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

	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/provisioner/rodan/types"
)

func (c *RodanClient) GetNodeMemoryStats() ([]types.Cell, error) {
	url := c.urls[types.NodeMemoryPath]

	data, err := c.metricFunc(url, nil)
	if err != nil {
		klog.Errorf("GetNodeMemoryStats fail, err: %v", err)
		return nil, err
	}

	rsp := &types.NodeMemoryResponse{}
	if err = json.Unmarshal(data, rsp); err != nil {
		klog.Errorf("faild to unmarshal node memory response, err: %v, data: %v", err, string(data))
		return nil, err
	}

	if len(rsp.Data) == 0 {
		err = fmt.Errorf("GetNodeMemoryStats fail, empty data")
		klog.Error(err)
		return nil, err
	}

	return rsp.Data, nil
}

func (c *RodanClient) GetNodeCgroupMemoryStats() ([]types.Cell, error) {
	url := c.urls[types.NodeCgroupMemoryPath]

	data, err := c.metricFunc(url, nil)
	if err != nil {
		klog.Errorf("GetNodeNUMAMemoryStatus fail, err: %v", err)
		return nil, err
	}

	rsp := &types.NodeCgroupMemoryResponse{}
	if err = json.Unmarshal(data, rsp); err != nil {
		klog.Errorf("faild to unmarshal node cgroup memory response, err: %v, data: %v", err, string(data))
		return nil, err
	}

	return rsp.Data, err
}

func (c *RodanClient) GetNUMAMemoryStats() (map[int][]types.Cell, error) {
	url := c.urls[types.NumaMemoryPath]

	data, err := c.metricFunc(url, nil)
	if err != nil {
		klog.Errorf("GetNUMAMemoryStats fail, err: %v", err)
		return nil, err
	}

	rsp := &types.NUMAMemoryResponse{}
	if err = json.Unmarshal(data, rsp); err != nil {
		err = fmt.Errorf("failed to unmarshal numa memory response, err: %v, data: %v", err, string(data))
		return nil, err
	}

	res := make(map[int][]types.Cell)
	for _, cell := range rsp.Data {
		numaNode, metric, err := types.ParseNumastatKey(cell.Key)
		if err != nil {
			return nil, err
		}

		if _, ok := res[numaNode]; !ok {
			res[numaNode] = make([]types.Cell, 0)
		}
		res[numaNode] = append(res[numaNode], types.Cell{
			Key: metric,
			Val: cell.Val,
		})
	}

	return res, err
}

func (c *RodanClient) GetCoreCPUStats() (map[int][]types.Cell, error) {
	url := c.urls[types.NodeCPUPath]

	data, err := c.metricFunc(url, nil)
	if err != nil {
		klog.Errorf("GetCoreCPUStats fail, err: %v", err)
		return nil, err
	}

	rsp := &types.CoreCPUResponse{}
	if err = json.Unmarshal(data, rsp); err != nil {
		err = fmt.Errorf("failed to unmarshal core cpu response, err: %v, data: %v", err, string(data))
		return nil, err
	}

	res := make(map[int][]types.Cell)
	for _, cell := range rsp.Data {
		cpu, metric, err := types.ParseCorestatKey(cell.Key)
		if err != nil {
			return nil, err
		}

		if _, ok := res[cpu]; !ok {
			res[cpu] = make([]types.Cell, 0)
		}
		res[cpu] = append(res[cpu], types.Cell{
			Key: metric,
			Val: cell.Val,
		})
	}

	return res, err
}

func (c *RodanClient) GetNodeSysctl() ([]types.Cell, error) {
	url := c.urls[types.NodeSysctlPath]

	data, err := c.metricFunc(url, nil)
	if err != nil {
		klog.Errorf("GetNodeSysctl fail, err: %v", err)
		return nil, err
	}

	rsp := &types.NodeSysctlResponse{}
	if err = json.Unmarshal(data, rsp); err != nil {
		err = fmt.Errorf("failed to unmarshal node sysctl response, err: %v, data: %v", err, string(data))
		return nil, err
	}

	if len(rsp.Data) == 0 {
		err = fmt.Errorf("GetNodeSysctl fail, empty data")
		klog.Error(err)
		return nil, err
	}

	return rsp.Data, nil
}
