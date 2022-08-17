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
	"encoding/json"

	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager"
	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager/checksum"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
)

var _ checkpointmanager.Checkpoint = &MetaCacheCheckpoint{}

type MetaCacheCheckpoint struct {
	PodEntries  types.PodEntries  `json:"pod_entries"`
	PoolEntries types.PoolEntries `json:"pool_entries"`
	Checksum    checksum.Checksum `json:"checksum"`
}

func NewMetaCacheCheckpoint() *MetaCacheCheckpoint {
	return &MetaCacheCheckpoint{
		PodEntries:  make(types.PodEntries),
		PoolEntries: make(types.PoolEntries),
	}
}

// MarshalCheckpoint returns marshalled checkpoint
func (cp *MetaCacheCheckpoint) MarshalCheckpoint() ([]byte, error) {
	// make sure checksum wasn't set before so it doesn't affect output checksum
	cp.Checksum = 0
	cp.Checksum = checksum.New(cp)
	return json.Marshal(*cp)
}

// UnmarshalCheckpoint tries to unmarshal passed bytes to checkpoint
func (cp *MetaCacheCheckpoint) UnmarshalCheckpoint(blob []byte) error {
	return json.Unmarshal(blob, cp)
}

// VerifyChecksum verifies that current checksum of checkpoint is valid
func (cp *MetaCacheCheckpoint) VerifyChecksum() error {
	ck := cp.Checksum
	cp.Checksum = 0
	err := ck.Verify(cp)
	cp.Checksum = ck
	return err
}
