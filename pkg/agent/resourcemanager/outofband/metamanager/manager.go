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

package metamanager

import (
	"context"
	"sync"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

type Manager struct {
	ctx context.Context

	emitter metrics.MetricEmitter

	*metaserver.MetaServer
	mutex sync.RWMutex

	cachedPods CachedPodListFunc

	podFirstRemoveTime map[string]time.Time

	podAddedFuncs   []PodAddedFunc
	podDeletedFuncs []PodDeletedFunc
}

func NewManager(
	emitter metrics.MetricEmitter,
	cachedPods CachedPodListFunc,
	metaServer *metaserver.MetaServer) *Manager {
	m := &Manager{
		emitter:            emitter,
		MetaServer:         metaServer,
		cachedPods:         cachedPods,
		podAddedFuncs:      make([]PodAddedFunc, 0),
		podDeletedFuncs:    make([]PodDeletedFunc, 0),
		podFirstRemoveTime: make(map[string]time.Time),
	}
	return m
}

func (m *Manager) Run(ctx context.Context, reconcilePeriod time.Duration) {
	m.ctx = ctx
	go wait.Until(m.reconcile, reconcilePeriod, m.ctx.Done())
}

func (m *Manager) reconcile() {

	activePods, err := m.MetaServer.GetPodList(m.ctx, native.PodIsActive)
	if err != nil {
		klog.Errorf("metamanager reconcile GetPodList fail: %v", err)
		_ = m.emitter.StoreInt64(MetricReconcileFail, 1, metrics.MetricTypeNameRaw)
		return
	}

	// reconcile new pods
	podsToBeAdded := m.reconcileNewPods(activePods)
	if len(podsToBeAdded) > 0 {
		m.notifyAddPods(podsToBeAdded)
	}

	// reconcile pod terminated and had been deleted
	podsTobeRemoved := m.reconcileRemovePods(activePods)
	if len(podsTobeRemoved) > 0 {
		m.notifyDeletePods(podsTobeRemoved)
	}
}

func (m *Manager) RegistPodAddedFunc(podAddedFunc PodAddedFunc) {
	m.podAddedFuncs = append(m.podAddedFuncs, podAddedFunc)
}

func (m *Manager) RegistPodDeletedFunc(podDeletedFunc PodDeletedFunc) {
	m.podDeletedFuncs = append(m.podDeletedFuncs, podDeletedFunc)
}

// reconcileNewPods checks new pods between activePods from metaServer and pods in manager cache
func (m *Manager) reconcileNewPods(activePods []*v1.Pod) []string {
	podsToBeAdded := make([]string, 0)
	podList := m.cachedPods()

	for _, pod := range activePods {
		if !podList.Has(string(pod.UID)) {
			podsToBeAdded = append(podsToBeAdded, string(pod.UID))
		}
	}

	return podsToBeAdded
}

// reconcileRemovePods checks deleted pods between activePods from metaServer and pods in manager cache
func (m *Manager) reconcileRemovePods(activePods []*v1.Pod) map[string]struct{} {
	podsToBeRemoved := make(map[string]struct{})
	podList := m.cachedPods()

	for _, pod := range activePods {
		if podList.Has(string(pod.UID)) {
			podList = podList.Delete(string(pod.UID))
		}
	}

	// gc pod remove timestamp
	m.mutex.Lock()
	for _, pod := range activePods {
		delete(m.podFirstRemoveTime, string(pod.UID))
	}
	m.mutex.Unlock()

	// check pod can be removed
	for _, podUID := range podList.UnsortedList() {
		if m.canPodDelete(podUID) {
			podsToBeRemoved[podUID] = struct{}{}
		}
	}

	return podsToBeRemoved
}

func (m *Manager) notifyAddPods(podUIDs []string) {
	if len(m.podAddedFuncs) > 0 {
		klog.V(5).Infof("metaManager notifyAddPods: %v", podUIDs)

		for _, podUID := range podUIDs {
			for _, addFunc := range m.podAddedFuncs {
				addFunc(podUID)
			}
		}
	}
}

func (m *Manager) notifyDeletePods(podUIDSet map[string]struct{}) {
	if len(m.podDeletedFuncs) > 0 {
		klog.V(5).Infof("metaManager notifyDeletePods: %v", podUIDSet)

		for podUID := range podUIDSet {
			for _, deleteFuncs := range m.podDeletedFuncs {
				deleteFuncs(podUID)
			}
		}
	}
}

func (m *Manager) canPodDelete(podUID string) bool {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	// generate pod cgroup path, use cpu as subsystem
	_, err := common.GetPodAbsCgroupPath(common.CgroupSubsysCPU, podUID)
	if err != nil {
		// GetPodAbsCgroupPath return error only if pod cgroup path not exist
		klog.Warning(err.Error())
		delete(m.podFirstRemoveTime, podUID)
		return true
	}

	// pod is not exist in metaServer, deletionTimestamp can not be got by pod
	// first deletion check time should be record
	firstRemoveTime, ok := m.podFirstRemoveTime[podUID]
	if !ok {
		m.podFirstRemoveTime[podUID] = time.Now()
	} else {
		if time.Now().After(firstRemoveTime.Add(forceRemoveDuration)) {
			delete(m.podFirstRemoveTime, podUID)
			return true
		}

		return false
	}

	return false
}
