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

package external

import (
	"context"
	"sync"

	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/poweraware/component/capper/amd"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/poweraware/component/capper/intel"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/external/cgroupid"
	"github.com/kubewharf/katalyst-core/pkg/util/external/network"
	"github.com/kubewharf/katalyst-core/pkg/util/external/power"
	"github.com/kubewharf/katalyst-core/pkg/util/external/rdt"
	utils "github.com/kubewharf/katalyst-core/pkg/util/lowlevel"
)

var (
	initManagerOnce sync.Once
	manager         *externalManagerImpl
)

type externalManagerImpl struct {
	mutex sync.Mutex
	start bool
	cgroupid.CgroupIDManager

	network.NetworkManager
	rdt.RDTManager
	power.PowerLimiter
}

// InitExternalManager initializes an externalManagerImpl
func InitExternalManager(podFetcher pod.PodFetcher) ExternalManager {
	var powerLimiter power.PowerLimiter
	if utils.IsAMD() {
		powerLimiter = amd.NewPowerLimiter()
	} else {
		powerLimiter = intel.NewRAPLLimiter()
	}
	initManagerOnce.Do(func() {
		manager = &externalManagerImpl{
			start:           false,
			CgroupIDManager: cgroupid.NewCgroupIDManager(podFetcher),
			NetworkManager:  network.NewNetworkManager(),
			RDTManager:      rdt.NewDefaultManager(),
			PowerLimiter:    powerLimiter,
		}
	})

	return manager
}

// Run starts an externalManagerImpl
func (m *externalManagerImpl) Run(ctx context.Context) {
	m.mutex.Lock()
	if m.start {
		m.mutex.Unlock()
		return
	}
	m.start = true

	go m.CgroupIDManager.Run(ctx)

	m.mutex.Unlock()
	<-ctx.Done()
}

// SetNetworkManager replaces defaultNetworkManager with a custom implementation
func (m *externalManagerImpl) SetNetworkManager(n network.NetworkManager) {
	m.setComponentImplementation(func() {
		m.NetworkManager = n
	})
}

func (m *externalManagerImpl) setComponentImplementation(setter func()) {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	if m.start {
		klog.Warningf("external manager has already started, not allowed to set implementations")
		return
	}

	setter()
}
