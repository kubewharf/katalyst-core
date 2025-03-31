//go:build !linux
// +build !linux

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
	"context"

	"github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	qrmgeneral "github.com/kubewharf/katalyst-core/pkg/util/qrm"
)

type unsupportedNetworkManager struct{}

// NewNetworkManager returns a defaultNetworkManager.
func NewNetworkManager() NetworkManager {
	return &unsupportedNetworkManager{}
}

// ApplyNetClass applies the net class config for a container.
func (*unsupportedNetworkManager) ApplyNetClass(podUID, containerId string, data *common.NetClsData) error {
	return nil
}

// ListNetClass lists the net class config for all containers managed by kubernetes.
func (*unsupportedNetworkManager) ListNetClass() ([]*common.NetClsData, error) {
	return nil, nil
}

// ClearNetClass clears the net class config for a container.
func (*unsupportedNetworkManager) ClearNetClass(cgroupID uint64) error {
	return nil
}

// ApplyNetworkGroups apply parameters for network groups.
func (*unsupportedNetworkManager) ApplyNetworkGroups(map[string]*qrmgeneral.NetworkGroup) error {
	return nil
}

func (*unsupportedNetworkManager) Run(ctx context.Context) {}
