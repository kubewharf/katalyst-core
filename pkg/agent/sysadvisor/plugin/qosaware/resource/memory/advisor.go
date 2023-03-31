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
	"sync"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/memory/headroompolicy"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

func init() {
	headroompolicy.RegisterInitializer(types.MemoryHeadroomPolicyCanonical, headroompolicy.NewPolicyCanonical)
}

const (
	memoryResourceAdvisorName string = "memory-resource-advisor"

	startUpPeriod time.Duration = 30 * time.Second
)

// memoryResourceAdvisor updates memory headroom for reclaimed resource
type memoryResourceAdvisor struct {
	name                string
	startTime           time.Time
	reservedForAllocate int64

	headroomPolicy headroompolicy.HeadroomPolicy
	mutex          sync.RWMutex

	conf       *config.Configuration
	metaCache  *metacache.MetaCache
	metaServer *metaserver.MetaServer
	emitter    metrics.MetricEmitter
}

// NewMemoryResourceAdvisor returns a memoryResourceAdvisor instance
func NewMemoryResourceAdvisor(conf *config.Configuration, metaCache *metacache.MetaCache,
	metaServer *metaserver.MetaServer, emitter metrics.MetricEmitter) *memoryResourceAdvisor {
	ra := &memoryResourceAdvisor{
		name:      memoryResourceAdvisorName,
		startTime: time.Now(),

		conf:       conf,
		metaCache:  metaCache,
		metaServer: metaServer,
		emitter:    emitter,
	}

	// todo: support dynamic reserved resource from kcc
	reservedDefault := conf.ReclaimedResourceConfiguration.ReservedResourceForAllocate[v1.ResourceMemory]
	ra.reservedForAllocate = reservedDefault.Value()

	// Keep canonical policy by default as baseline
	ra.headroomPolicy = headroompolicy.NewPolicyCanonical(metaCache, metaServer)

	return ra
}

func (ra *memoryResourceAdvisor) Name() string {
	return ra.name
}

func (ra *memoryResourceAdvisor) Update() {
	ra.mutex.Lock()
	defer ra.mutex.Unlock()

	// Skip update during startup
	if time.Now().Before(ra.startTime.Add(startUpPeriod)) {
		klog.Infof("[qosaware-memory] skip update: starting up")
		return
	}

	// Check if essential pool info exists. Skip update if not in which case sysadvisor
	// is ignorant of pools and containers
	reservePoolInfo, ok := ra.metaCache.GetPoolInfo(state.PoolNameReserve)
	if !ok || reservePoolInfo == nil {
		klog.Warningf("[qosaware-memory] skip update: reserve pool not exist")
		return
	}

	// capacity and reserved can both be adjusted dynamically during running process
	memoryLimitSystem := ra.metaServer.MemoryCapacity
	ra.headroomPolicy.SetEssentials(float64(memoryLimitSystem), float64(ra.reservedForAllocate))

	if err := ra.headroomPolicy.Update(); err != nil {
		klog.Errorf("[qosaware-memory] update headroom policy failed: %v", err)
	}
}

func (ra *memoryResourceAdvisor) GetChannel() interface{} {
	klog.Warningf("[qosaware-memory] get channel is not supported")
	return nil
}

func (ra *memoryResourceAdvisor) GetHeadroom() (resource.Quantity, error) {
	ra.mutex.RLock()
	defer ra.mutex.RUnlock()

	return ra.headroomPolicy.GetHeadroom()
}
