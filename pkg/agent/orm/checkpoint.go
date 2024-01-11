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

package orm

import (
	"fmt"
	"path/filepath"

	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager/errors"

	"github.com/kubewharf/katalyst-core/pkg/agent/orm/checkpoint"
	"github.com/kubewharf/katalyst-core/pkg/agent/orm/endpoint"
	"github.com/kubewharf/katalyst-core/pkg/consts"
)

func (m *ManagerImpl) checkpointFile() string {
	return filepath.Join(m.socketdir, consts.KubeletQoSResourceManagerCheckpoint)
}

func (m *ManagerImpl) writeCheckpoint() error {
	data := checkpoint.New(m.podResources.toCheckpointData())
	err := m.checkpointManager.CreateCheckpoint(consts.KubeletQoSResourceManagerCheckpoint, data)
	if err != nil {
		err = fmt.Errorf("[ORM] failed to write checkpoint file %q: %v", consts.KubeletQoSResourceManagerCheckpoint, err)
		klog.Warning(err)
		return err
	}
	return nil
}

func (m *ManagerImpl) readCheckpoint() error {
	resEntries := make([]checkpoint.PodResourcesEntry, 0)
	cp := checkpoint.New(resEntries)
	err := m.checkpointManager.GetCheckpoint(consts.KubeletQoSResourceManagerCheckpoint, cp)
	if err != nil {
		if err == errors.ErrCheckpointNotFound {
			klog.Warningf("[ORM] Failed to retrieve checkpoint for %q: %v", consts.KubeletQoSResourceManagerCheckpoint, err)
			return nil
		}
		return err
	}

	podResources := cp.GetData()
	klog.V(5).Infof("[ORM] read checkpoint %v", podResources)
	m.podResources.fromCheckpointData(podResources)

	m.mutex.Lock()

	allocatedResourceNames := m.podResources.allAllocatedResourceNames()

	for _, allocatedResourceName := range allocatedResourceNames.UnsortedList() {
		m.endpoints[allocatedResourceName] = endpoint.EndpointInfo{E: endpoint.NewStoppedEndpointImpl(allocatedResourceName), Opts: nil}
	}

	m.mutex.Unlock()

	return nil
}
