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

package checkpoint

import (
	"encoding/json"

	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager"
	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager/checksum"
)

// ResourceManagerCheckpoint defines the operations to retrieve pod resources
type ResourceManagerCheckpoint interface {
	checkpointmanager.Checkpoint
	GetData() []PodResourcesEntry
}

// PodResourcesEntry connects pod information to resources
type PodResourcesEntry struct {
	PodUID         string
	ContainerName  string
	ResourceName   string
	AllocationInfo string
}

// checkpointData struct is used to store pod to resource allocation information
// in a checkpoint file.
// TODO: add version control when we need to change checkpoint format.
type checkpointData struct {
	PodResourceEntries []PodResourcesEntry
}

// Data holds checkpoint data and its checksum
type Data struct {
	Data     checkpointData
	Checksum checksum.Checksum
}

// New returns an instance of Checkpoint
func New(resEntries []PodResourcesEntry) ResourceManagerCheckpoint {
	return &Data{
		Data: checkpointData{
			PodResourceEntries: resEntries,
		},
	}
}

// MarshalCheckpoint returns marshaled data
func (cp *Data) MarshalCheckpoint() ([]byte, error) {
	cp.Checksum = checksum.New(cp.Data)
	return json.Marshal(*cp)
}

// UnmarshalCheckpoint returns unmarshalled data
func (cp *Data) UnmarshalCheckpoint(blob []byte) error {
	return json.Unmarshal(blob, cp)
}

// VerifyChecksum verifies that passed checksum is same as calculated checksum
func (cp *Data) VerifyChecksum() error {
	return cp.Checksum.Verify(cp.Data)
}

// GetData returns resource entries and registered resources
func (cp *Data) GetData() []PodResourcesEntry {
	return cp.Data.PodResourceEntries
}
