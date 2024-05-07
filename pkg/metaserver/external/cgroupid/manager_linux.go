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

package cgroupid

import (
	"context"
	"fmt"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	"github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

const (
	maxResidualTime = 5 * time.Minute
)

var (
	initManagerOnce sync.Once
	cgIDManager     *cgroupIDManagerImpl
)

type (
	ContainerCache map[string]uint64         // Keyed by container id
	PodCache       map[string]ContainerCache // Keyed by pod UID
)

type cgroupIDManagerImpl struct {
	sync.RWMutex
	pod.PodFetcher

	reconcilePeriod  time.Duration
	podCgroupIDCache PodCache
	residualHitMap   map[string]int64
}

// NewCgroupIDManager returns a CgroupIDManager
func NewCgroupIDManager(podFetcher pod.PodFetcher) CgroupIDManager {
	initManagerOnce.Do(func() {
		cgIDManager = &cgroupIDManagerImpl{
			PodFetcher:       podFetcher,
			podCgroupIDCache: make(PodCache),
			reconcilePeriod:  5 * time.Second,
			residualHitMap:   make(map[string]int64),
		}
	})

	return cgIDManager
}

// Run starts a cgroupIDManagerImpl
func (m *cgroupIDManagerImpl) Run(ctx context.Context) {
	wait.UntilWithContext(ctx, m.reconcileCgroupIDMap, m.reconcilePeriod)
}

// GetCgroupIDForContainer returns the cgroup id of a given container.
func (m *cgroupIDManagerImpl) GetCgroupIDForContainer(podUID, containerID string) (uint64, error) {
	if cgroupID, found := m.getCgroupIDFromCache(podUID, containerID); found {
		return cgroupID, nil
	}

	cgroupID, err := m.getCgroupIDFromSystem(podUID, containerID)
	if err != nil {
		return 0, fmt.Errorf("getCgroupIDFromSystem failed, err: %v", err)
	}

	m.setCgroupID(podUID, containerID, cgroupID)

	return cgroupID, nil
}

// ListCgroupIDsForPod returns the cgroup ids of a given pod.
func (m *cgroupIDManagerImpl) ListCgroupIDsForPod(podUID string) ([]uint64, error) {
	m.RLock()
	defer m.RUnlock()

	containerCgroupIDMap, ok := m.podCgroupIDCache[podUID]
	if !ok {
		return nil, general.ErrNotFound
	}

	var cgIDList []uint64
	for _, cgID := range containerCgroupIDMap {
		cgIDList = append(cgIDList, cgID)
	}

	return cgIDList, nil
}

func (m *cgroupIDManagerImpl) reconcileCgroupIDMap(ctx context.Context) {
	podList, err := m.GetPodList(ctx, nil)
	if err != nil {
		klog.Errorf("[cgroupIDManagerImpl.reconcileCgroupIDMap] get pod list failed, err: %v", err)
		return
	}

	m.clearResidualPodsInCache(podList)
	m.addAbsentCgroupIDsToCache(m.getAbsentContainers(podList))
}

// addAbsentCgroupIDsToCache adds absent cgroup ids to cache.
func (m *cgroupIDManagerImpl) addAbsentCgroupIDsToCache(absentContainers map[string]sets.String) {
	klog.V(4).Infof("[cgroupIDManagerImpl] exec addAbsentCgroupIDsToCache")

	for podUID, absentContainerSet := range absentContainers {
		for {
			containerID, found := absentContainerSet.PopAny()
			if !found {
				break
			}

			cgID, err := m.getCgroupIDFromSystem(podUID, containerID)
			if err != nil {
				klog.Errorf("[cgroupIDManagerImpl.addAbsentCgroupIDsToCache] get cgroup id failed, pod: %s, container: %s, err: %v",
					podUID, containerID, err)
				continue
			}

			klog.Infof("[cgroupIDManagerImpl.addAbsentCgroupIDsToCache] add absent cgroup id to cache, "+
				"pod: %s, container: %s, cgroup id: %d", podUID, containerID, cgID)
			m.setCgroupID(podUID, containerID, cgID)
		}
	}
}

func (m *cgroupIDManagerImpl) getAbsentContainers(podList []*v1.Pod) map[string]sets.String {
	absentContainersMap := make(map[string]sets.String)

	m.RLock()
	defer m.RUnlock()

	for _, pod := range podList {
		podUID := string(pod.UID)
		containerCache, ok := m.podCgroupIDCache[podUID]
		if !ok {
			containerCache = make(ContainerCache)
		}
		for _, container := range pod.Spec.Containers {
			containerId, err := m.GetContainerID(podUID, container.Name)
			if err != nil {
				klog.Errorf("[cgroupIDManagerImpl.addNewCgroupIDsToCache] get container id failed, pod: %s, container: %s, err: %v",
					podUID, container.Name, err)
				continue
			}
			if _, ok := containerCache[containerId]; !ok {
				if _, ok := absentContainersMap[podUID]; !ok {
					absentContainersMap[podUID] = sets.NewString()
				}
				absentContainersMap[podUID].Insert(containerId)
			}
		}
	}

	return absentContainersMap
}

// clearResidualPodsInCache cleans residual pods in podCgroupIDCache.
func (m *cgroupIDManagerImpl) clearResidualPodsInCache(podList []*v1.Pod) {
	klog.V(4).Infof("[cgroupIDManagerImpl] exec clearResidualPodsInCache")
	residualSet := make(map[string]bool)

	podSet := sets.NewString()
	for _, pod := range podList {
		podSet.Insert(fmt.Sprintf("%v", pod.UID))
	}

	m.Lock()
	defer m.Unlock()

	for podUID := range m.podCgroupIDCache {
		if !podSet.Has(podUID) && !residualSet[podUID] {
			residualSet[podUID] = true
			m.residualHitMap[podUID] += 1
			klog.V(4).Infof("[cgroupIDManagerImpl.clearResidualPodsInCache] found pod: %s with cache but doesn't show up in pod watcher, hit count: %d", podUID, m.residualHitMap[podUID])
		}
	}

	podsToDelete := sets.NewString()
	for podUID, hitCount := range m.residualHitMap {
		if !residualSet[podUID] {
			klog.V(4).Infof("[cgroupIDManagerImpl.clearResidualPodsInCache] already found pod: %s in pod watcher or its cache is cleared, delete it from residualHitMap", podUID)
			delete(m.residualHitMap, podUID)
			continue
		}

		if time.Duration(hitCount)*m.reconcilePeriod >= maxResidualTime {
			podsToDelete.Insert(podUID)
		}
	}

	if podsToDelete.Len() > 0 {
		for {
			podUID, found := podsToDelete.PopAny()
			if !found {
				break
			}

			klog.Infof("[cgroupIDManagerImpl.clearResidualPodsInCache] clear residual pod: %s in cache", podUID)
			delete(m.podCgroupIDCache, podUID)
		}
	}
}

func (m *cgroupIDManagerImpl) getCgroupIDFromCache(podUID, containerID string) (uint64, bool) {
	m.RLock()
	defer m.RUnlock()

	containerCache, ok := m.podCgroupIDCache[podUID]
	if !ok {
		return 0, false
	}
	cgroupID, ok := containerCache[containerID]
	if !ok {
		return 0, false
	}

	return cgroupID, true
}

func (m *cgroupIDManagerImpl) getCgroupIDFromSystem(podUID, containerID string) (uint64, error) {
	containerAbsCGPath, err := common.GetContainerAbsCgroupPath("", podUID, containerID)
	if err != nil {
		return 0, fmt.Errorf("GetContainerAbsCgroupPath failed, err: %v", err)
	}

	cgID, err := cgroupPathToID(containerAbsCGPath)
	if err != nil {
		return 0, fmt.Errorf("cgroupPathToID failed, err: %v", err)
	}

	return cgID, nil
}

func (m *cgroupIDManagerImpl) setCgroupID(podUID, containerID string, cgroupID uint64) {
	m.Lock()
	defer m.Unlock()

	_, ok := m.podCgroupIDCache[podUID]
	if !ok {
		m.podCgroupIDCache[podUID] = make(ContainerCache)
	}

	m.podCgroupIDCache[podUID][containerID] = cgroupID
}

func cgroupPathToID(cgPath string) (uint64, error) {
	var fstat syscall.Statfs_t
	err := syscall.Statfs(cgPath, &fstat)
	if err != nil {
		return 0, fmt.Errorf("get file fstat failed, cgPath: %s, err: %v", cgPath, err)
	}
	if fstat.Type != unix.CGROUP2_SUPER_MAGIC && fstat.Type != unix.CGROUP_SUPER_MAGIC {
		return 0, fmt.Errorf("get file fstat failed, cgPath: %s, invalid file type: %v", cgPath, fstat.Type)
	}

	handle, _, err := unix.NameToHandleAt(unix.AT_FDCWD, cgPath, 0)
	if err != nil {
		return 0, fmt.Errorf("call name_to_handle_at failed, cgPath: %s, err: %v", cgPath, err)
	}
	if handle.Size() != 8 {
		return 0, fmt.Errorf("call name_to_handle_at failed, cgPath: %s, invalid size: %v", cgPath, handle.Size())
	}

	return general.NativeEndian.Uint64(handle.Bytes()), nil
}
