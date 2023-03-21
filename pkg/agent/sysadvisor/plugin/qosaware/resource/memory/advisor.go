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

package memory

import (
	"fmt"
	"sync"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/memory/headroompolicy"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/util"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

const (
	memoryResourceAdvisorName string = "memory-resource-advisor"

	startUpPeriod time.Duration = 30 * time.Second
)

// memoryResourceAdvisor updates memory headroom for reclaimed resource
type memoryResourceAdvisor struct {
	name                       string
	memoryLimitSystem          uint64
	reservedForAllocateDefault int64
	startTime                  time.Time
	isReady                    bool

	policy headroompolicy.Policy

	metaCache *metacache.MetaCache
	emitter   metrics.MetricEmitter

	mutex sync.RWMutex
}

// NewMemoryResourceAdvisor returns a memoryResourceAdvisor instance
func NewMemoryResourceAdvisor(conf *config.Configuration, metaCache *metacache.MetaCache,
	metaServer *metaserver.MetaServer, emitter metrics.MetricEmitter) (*memoryResourceAdvisor, error) {
	mra := &memoryResourceAdvisor{
		name:              memoryResourceAdvisorName,
		memoryLimitSystem: metaServer.MemoryCapacity,
		startTime:         time.Now(),
		isReady:           false,
		metaCache:         metaCache,
		emitter:           emitter,
	}

	reservedDefault := conf.ReclaimedResourceConfiguration.ReservedResourceForAllocate[v1.ResourceMemory]
	mra.reservedForAllocateDefault = reservedDefault.Value()

	policyName := conf.MemoryAdvisorConfiguration.MemoryAdvisorPolicy
	memPolicy, err := headroompolicy.NewPolicy(types.MemoryAdvisorPolicyName(policyName), metaCache, metaServer)
	if err != nil {
		return nil, fmt.Errorf("new policy %v for memory resource advisor failed: %v", policyName, err)
	}
	mra.policy = memPolicy

	return mra, nil
}

func (mra *memoryResourceAdvisor) Name() string {
	return mra.name
}

func (mra *memoryResourceAdvisor) Update() error {
	mra.mutex.Lock()
	defer mra.mutex.Unlock()
	// Skip update during startup
	if time.Now().Before(mra.startTime.Add(startUpPeriod)) {
		klog.Infof("[qosaware-memory] starting up")
		return fmt.Errorf("[qosaware-memory] starting up")
	}

	// Check if essential pool info exists. Skip update if not in which case sysadvisor
	// is ignorant of pools and containers
	if _, err := mra.getPoolSize(state.PoolNameReserve); err != nil {
		klog.Warningf("[qosaware-memory] skip update. %v", err)
		return fmt.Errorf("[qosaware-memory] skip update. %v", err)
	}

	mra.policy.SetMemory(int64(mra.memoryLimitSystem), mra.getReservedResource())

	if err := mra.policy.Update(); err != nil {
		klog.Errorf("[qosaware-memory] update policy error %v", err)
		mra.isReady = false
		return err
	}
	mra.isReady = true
	return nil
}

func (mra *memoryResourceAdvisor) GetChannel() interface{} {
	klog.Warningf("[qosaware-memory] get channel is not supported")
	return nil
}

func (mra *memoryResourceAdvisor) GetHeadroom() (resource.Quantity, error) {
	mra.mutex.RLock()
	defer mra.mutex.RUnlock()
	if !mra.isReady {
		return resource.Quantity{}, fmt.Errorf("not ready")
	}

	return mra.policy.GetHeadroom()
}

func (mra *memoryResourceAdvisor) getReservedResource() int64 {
	// todo: get kcc config stored in metacache
	return mra.reservedForAllocateDefault
}

func (mra *memoryResourceAdvisor) getPoolSize(poolName string) (int, error) {
	pi, ok := mra.metaCache.GetPoolInfo(poolName)
	if !ok {
		return 0, fmt.Errorf("%v pool not exist", poolName)
	}

	poolSize := util.CountCPUAssignmentCPUs(pi.TopologyAwareAssignments)
	klog.Infof("[qosaware-memory] %v pool size %v", poolName, poolSize)

	return poolSize, nil
}
