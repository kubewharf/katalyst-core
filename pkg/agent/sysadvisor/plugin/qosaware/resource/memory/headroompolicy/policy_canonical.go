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

package headroompolicy

import (
	"fmt"
	"sync"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/helper"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

type PolicyCanonical struct {
	Mutex            sync.RWMutex
	MemoryHeadroom   int64
	MemoryReserved   int64
	MemoryTotal      int64
	MetaCache        *metacache.MetaCache
	MetaServer       *metaserver.MetaServer
	LastUpdateStatus types.UpdateStatus
}

func NewPolicyCanonical(metaCache *metacache.MetaCache, metaServer *metaserver.MetaServer) *PolicyCanonical {
	cp := &PolicyCanonical{
		MetaCache:        metaCache,
		MetaServer:       metaServer,
		LastUpdateStatus: types.UpdateFailed,
	}
	return cp
}

func (cp *PolicyCanonical) Update() error {
	var errList []error
	cp.Mutex.Lock()
	defer func() {
		if len(errList) == 0 {
			cp.LastUpdateStatus = types.UpdateSucceeded
		} else {
			cp.LastUpdateStatus = types.UpdateFailed
		}
		cp.Mutex.Unlock()
	}()
	var (
		memoryEstimation float64 = 0
		containerCnt     float64 = 0
	)

	calculateContainerEstimationFunc := func(podUID string, containerName string, ci *types.ContainerInfo) bool {
		containerEstimation, err := helper.EstimateContainerResourceUsage(ci, v1.ResourceMemory, cp.MetaCache)
		if err != nil {
			errList = append(errList)
			return true
		}
		klog.Infof("[qosaware-memory-canonical] pod %v container %v estimation %.2e", ci.PodName, containerName, containerEstimation)
		memoryEstimation += containerEstimation
		containerCnt += 1
		return true
	}
	cp.MetaCache.RangeContainer(calculateContainerEstimationFunc)
	klog.Infof("[qosaware-memory-canonical] memory requirement estimation: %.2e, #container %v", memoryEstimation, containerCnt)

	cp.MemoryHeadroom = general.MaxInt64(cp.MemoryTotal-cp.MemoryReserved-int64(memoryEstimation), 0)
	return errors.NewAggregate(errList)
}

func (cp *PolicyCanonical) SetMemory(limit, reserved int64) {
	cp.Mutex.Lock()
	defer cp.Mutex.Unlock()
	cp.MemoryTotal = limit
	cp.MemoryReserved = reserved
}

func (cp *PolicyCanonical) GetHeadroom() (resource.Quantity, error) {
	cp.Mutex.RLock()
	defer cp.Mutex.RUnlock()
	if cp.LastUpdateStatus != types.UpdateSucceeded {
		return resource.Quantity{}, fmt.Errorf("[qosaware-memory-canonical] last updated failed")
	}
	return *resource.NewQuantity(cp.MemoryHeadroom, resource.BinarySI), nil
}
