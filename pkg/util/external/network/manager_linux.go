//go:build linux
// +build linux

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
	"errors"

	"github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	qrmgeneral "github.com/kubewharf/katalyst-core/pkg/util/qrm"
)

type defaultNetworkManager struct{}

// NewNetworkManager returns a defaultNetworkManager.
func NewNetworkManager() NetworkManager {
	return &defaultNetworkManager{}
}

// ApplyNetClass applies the net class config for a container.
func (*defaultNetworkManager) ApplyNetClass(podUID, containerId string, data *common.NetClsData) error {
	// TODO: implement traffic tagging by using eBPF
	return errors.New("not implemented yet")
}

// ListNetClass lists the net class config for all containers managed by kubernetes.
func (m *defaultNetworkManager) ListNetClass() ([]*common.NetClsData, error) {
	// TODO: implement traffic tagging by using eBPF
	return nil, errors.New("not implemented yet")
}

// ClearNetClass clears the net class config for a container.
func (*defaultNetworkManager) ClearNetClass(cgroupID uint64) error {
	// TODO: clear the eBPF map when a pod is removed
	return errors.New("not implemented yet")
}

// ApplyNetworkGroups apply parameters for network groups.
func (n *defaultNetworkManager) ApplyNetworkGroups(map[string]*qrmgeneral.NetworkGroup) error {
	return nil
}
