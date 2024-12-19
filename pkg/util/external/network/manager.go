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

package network

import (
	"sync"

	"github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	qrmgeneral "github.com/kubewharf/katalyst-core/pkg/util/qrm"
)

// NetworkManager provides methods that control network resources.
type NetworkManager interface {
	// ApplyNetClass applies the net class config for a container.
	ApplyNetClass(podUID, containerId string, data *common.NetClsData) error
	// ListNetClass lists the net class config for all containers managed by kubernetes.
	ListNetClass() ([]*common.NetClsData, error)
	// ClearNetClass clears the net class config for a container.
	ClearNetClass(cgroupID uint64) error
	// ApplyNetworkGroups apply parameters for network groups.
	ApplyNetworkGroups(map[string]*qrmgeneral.NetworkGroup) error
}

type NetworkManagerStub struct {
	sync.RWMutex
	NetClassMap map[string]map[string]*common.NetClsData
}

func (n *NetworkManagerStub) ApplyNetClass(podUID, containerId string, data *common.NetClsData) error {
	n.Lock()
	defer n.Unlock()
	if _, ok := n.NetClassMap[podUID]; !ok {
		n.NetClassMap[podUID] = make(map[string]*common.NetClsData)
	}
	n.NetClassMap[podUID][containerId] = data
	return nil
}

func (n *NetworkManagerStub) ListNetClass() ([]*common.NetClsData, error) {
	n.RLock()
	defer n.RUnlock()
	var netClassDataList []*common.NetClsData
	for _, containerNetClassMap := range n.NetClassMap {
		for _, netClassData := range containerNetClassMap {
			netClassDataList = append(netClassDataList, netClassData)
		}
	}
	return netClassDataList, nil
}

func (n *NetworkManagerStub) ClearNetClass(cgroupID uint64) error {
	n.Lock()
	defer n.Unlock()
	for podUID, containerNetClassMap := range n.NetClassMap {
		for containerID, netClassData := range containerNetClassMap {
			if netClassData.CgroupID == cgroupID {
				delete(containerNetClassMap, containerID)
				if len(containerNetClassMap) == 0 {
					delete(n.NetClassMap, podUID)
				}
			}
		}
	}
	return nil
}

func (n *NetworkManagerStub) ApplyNetworkGroups(map[string]*qrmgeneral.NetworkGroup) error {
	return nil
}
