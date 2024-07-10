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

package external

import (
	"context"
	"sync"

	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/external/cgroupid"
	"github.com/kubewharf/katalyst-core/pkg/util/external/network"
	"github.com/kubewharf/katalyst-core/pkg/util/external/power"
	"github.com/kubewharf/katalyst-core/pkg/util/external/rdt"
)

var (
	initUnsupportedManagerOnce sync.Once
	unsupportedManager         *unsupportedExternalManagerImpl
)

type unsupportedExternalManagerImpl struct {
	start bool
	cgroupid.CgroupIDManager

	network.NetworkManager
	rdt.RDTManager
	power.PowerLimiter
}

// Run starts an unsupportedExternalManagerImpl
func (m *unsupportedExternalManagerImpl) Run(_ context.Context) {
}

// InitExternalManager initializes an externalManagerImpl
func InitExternalManager(podFetcher pod.PodFetcher) ExternalManager {
	initUnsupportedManagerOnce.Do(func() {
		unsupportedManager = &unsupportedExternalManagerImpl{
			start:           false,
			CgroupIDManager: cgroupid.NewCgroupIDManager(podFetcher),
			NetworkManager:  network.NewNetworkManager(),
			RDTManager:      rdt.NewDefaultManager(),
			// todo: use unsupported power limiter
			PowerLimiter: power.NewLimiter(),
		}
	})

	return unsupportedManager
}
