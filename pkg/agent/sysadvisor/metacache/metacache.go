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

package metacache

import (
	"fmt"
	"sync"

	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager"
	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager/errors"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric"
)

// [notice]
// to compatible with checkpoint checksum calculation,
// we should make guarantees below in checkpoint properties assignment
// 1. resource.Quantity use resource.MustParse("0") to initialize, not to use resource.Quantity{}
// 2. CPUSet use NewCPUSet(...) to initialize, not to use CPUSet{}
// 3. not use omitempty in map property and must make new map to do initialization

const (
	stateFileName string = "sys_advisor_state"
)

// MetaCache stores metadata and info of pod, node, pool, subnuma etc. as a cache,
// and synchronizes data to sysadvisor state file. It is thread-safe to read and write.
// Deep copy logic is performed during accessing metacache entries instead of directly
// return pointer of each struct to avoid mis-overwrite.
type MetaCache struct {
	podEntries  types.PodEntries
	poolEntries types.PoolEntries
	mutex       sync.RWMutex

	checkpointManager checkpointmanager.CheckpointManager
	checkpointName    string

	metricsFetcher metric.MetricsFetcher
}

// NewMetaCache returns the single instance of MetaCache
func NewMetaCache(conf *config.Configuration, metricsFetcher metric.MetricsFetcher) (*MetaCache, error) {
	stateFileDir := conf.GenericSysAdvisorConfiguration.StateFileDirectory
	checkpointManager, err := checkpointmanager.NewCheckpointManager(stateFileDir)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize checkpoint manager: %v", err)
	}

	mc := &MetaCache{
		podEntries:        make(types.PodEntries),
		poolEntries:       make(types.PoolEntries),
		checkpointManager: checkpointManager,
		checkpointName:    stateFileName,
		metricsFetcher:    metricsFetcher,
	}

	// Restore from checkpoint before any function call to metacache api
	if err := mc.restoreState(); err != nil {
		return mc, err
	}

	return mc, nil
}

// GetContainerInfo returns a ContainerInfo copy keyed by pod uid and container name
func (mc *MetaCache) GetContainerInfo(podUID string, containerName string) (*types.ContainerInfo, bool) {
	mc.mutex.RLock()
	defer mc.mutex.RUnlock()

	podInfo, ok := mc.podEntries[podUID]
	if !ok {
		return nil, false
	}
	containerInfo, ok := podInfo[containerName]

	return containerInfo.Clone(), ok
}

// SetContainerInfo updates ContainerInfo keyed by pod uid and container name
func (mc *MetaCache) SetContainerInfo(podUID string, containerName string, containerInfo *types.ContainerInfo) error {
	mc.mutex.Lock()
	defer mc.mutex.Unlock()

	podInfo, ok := mc.podEntries[podUID]
	if !ok {
		mc.podEntries[podUID] = make(types.ContainerEntries)
		podInfo = mc.podEntries[podUID]
	}
	podInfo[containerName] = containerInfo

	return mc.storeState()
}

// RangeContainer applies a function to every podUID, containerName, containerInfo set.
// Deep copy logic is applied so that pod and container entries will not be overwritten.
func (mc *MetaCache) RangeContainer(f func(podUID string, containerName string, containerInfo *types.ContainerInfo) bool) {
	mc.mutex.RLock()
	defer mc.mutex.RUnlock()

	for podUID, podInfo := range mc.podEntries.Clone() {
		for containerName, containerInfo := range podInfo {
			if !f(podUID, containerName, containerInfo) {
				break
			}
		}
	}
}

// RangeAndUpdateContainer applies a function to every podUID, containerName, containerInfo set.
// Not recommended to use if RangeContainer satisfies the requirement.
func (mc *MetaCache) RangeAndUpdateContainer(f func(podUID string, containerName string, containerInfo *types.ContainerInfo) bool) {
	mc.mutex.Lock()
	defer mc.mutex.Unlock()

	for podUID, podInfo := range mc.podEntries {
		for containerName, containerInfo := range podInfo {
			if !f(podUID, containerName, containerInfo) {
				break
			}
		}
	}
}

// GetContainerMetric returns the metric value of a container
func (mc *MetaCache) GetContainerMetric(podUID string, containerName string, metricName string) (float64, error) {
	return mc.metricsFetcher.GetContainerMetric(podUID, containerName, metricName)
}

// AddContainer adds a container keyed by pod uid and container name. For repeatedly added
// container, only mutable meta data will be updated, i.e. request quantity changed by vpa
func (mc *MetaCache) AddContainer(podUID string, containerName string, containerInfo *types.ContainerInfo) error {
	if podInfo, ok := mc.podEntries[podUID]; ok {
		if ci, ok := podInfo[containerName]; ok {
			ci.UpdateMeta(containerInfo)
			return nil
		}
	}
	return mc.SetContainerInfo(podUID, containerName, containerInfo)
}

// RemovePod deletes a PodInfo keyed by pod uid. Repeatedly remove will be ignored.
func (mc *MetaCache) RemovePod(podUID string) error {
	mc.mutex.Lock()
	defer mc.mutex.Unlock()

	_, ok := mc.podEntries[podUID]
	if !ok {
		return nil
	}
	delete(mc.podEntries, podUID)

	return mc.storeState()
}

// DeleteContainer deletes a ContainerInfo keyed by pod uid and container name
func (mc *MetaCache) DeleteContainer(podUID string, containerName string) error {
	mc.mutex.Lock()
	defer mc.mutex.Unlock()

	podInfo, ok := mc.podEntries[podUID]
	if !ok {
		return nil
	}
	_, ok = podInfo[containerName]
	if !ok {
		return nil
	}
	delete(podInfo, containerName)

	return mc.storeState()
}

// SetPoolInfo stores a PoolInfo by pool name
func (mc *MetaCache) SetPoolInfo(poolName string, poolInfo *types.PoolInfo) error {
	mc.mutex.Lock()
	defer mc.mutex.Unlock()

	mc.poolEntries[poolName] = poolInfo

	return mc.storeState()
}

// GetPoolInfo returns a PoolInfo copy by pool name
func (mc *MetaCache) GetPoolInfo(poolName string) (*types.PoolInfo, bool) {
	mc.mutex.RLock()
	defer mc.mutex.RUnlock()

	poolInfo, ok := mc.poolEntries[poolName]
	return poolInfo.Clone(), ok
}

// DeletePool deletes a PoolInfo keyed by pool name
func (mc *MetaCache) DeletePool(poolName string) error {
	mc.mutex.Lock()
	defer mc.mutex.Unlock()

	delete(mc.poolEntries, poolName)

	return mc.storeState()
}

// GCPoolEntries deletes GCPoolEntries not existing on node
func (mc *MetaCache) GCPoolEntries(livingPoolNameSet map[string]struct{}) error {
	mc.mutex.Lock()
	defer mc.mutex.Unlock()

	for poolName := range mc.poolEntries {
		if _, ok := livingPoolNameSet[poolName]; !ok {
			delete(mc.poolEntries, poolName)
		}
	}

	return mc.storeState()
}

func (mc *MetaCache) storeState() error {
	checkpoint := NewMetaCacheCheckpoint()
	checkpoint.PodEntries = mc.podEntries
	checkpoint.PoolEntries = mc.poolEntries

	if err := mc.checkpointManager.CreateCheckpoint(mc.checkpointName, checkpoint); err != nil {
		klog.Errorf("[metacache] store state failed: %v", err)
		return err
	}
	klog.Infof("[metacache] store state succeeded")

	return nil
}

func (mc *MetaCache) restoreState() error {
	checkpoint := NewMetaCacheCheckpoint()

	if err := mc.checkpointManager.GetCheckpoint(mc.checkpointName, checkpoint); err != nil {
		if err == errors.ErrCheckpointNotFound {
			klog.Infof("[metacache] checkpoint %v not found, create", mc.checkpointName)
			return mc.storeState()
		}
		klog.Errorf("[metacache] restore state failed: %v", err)
		return err
	}

	mc.podEntries = checkpoint.PodEntries
	mc.poolEntries = checkpoint.PoolEntries

	klog.Infof("[metacache] restore state succeeded")

	return nil
}
