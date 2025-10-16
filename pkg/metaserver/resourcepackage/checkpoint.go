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

package resourcepackage

import (
	"encoding/json"
	"sync"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager"
	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager/checksum"

	nodev1alpha1 "github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
)

type NodeProfileCheckpoint interface {
	checkpointmanager.Checkpoint
	GetProfile() (*nodev1alpha1.NodeProfileDescriptor, metav1.Time)
	SetProfile(npd *nodev1alpha1.NodeProfileDescriptor, t metav1.Time)
}

type NodeProfileData struct {
	// Value only store spec of dynamic config crd
	Value     *nodev1alpha1.NodeProfileDescriptor
	Timestamp int64
}

// Data holds checkpoint data and its checksum
type Data struct {
	sync.Mutex
	Item *DataItem
}

// DataItem stores the checkpoint data and checksum.
type DataItem struct {
	Data     NodeProfileData   `json:"data"`
	Checksum checksum.Checksum `json:"checksum"`
}

// NewCheckpoint returns an instance of Checkpoint
func NewCheckpoint(npdData NodeProfileData) NodeProfileCheckpoint {
	return &Data{
		Item: &DataItem{
			Data: npdData,
		},
	}
}

func (d *Data) MarshalCheckpoint() ([]byte, error) {
	d.Lock()
	defer d.Unlock()

	d.Item.Checksum = checksum.New(d.Item.Data)
	return json.Marshal(*(d.Item))
}

func (d *Data) UnmarshalCheckpoint(blob []byte) error {
	d.Lock()
	defer d.Unlock()

	return json.Unmarshal(blob, d.Item)
}

func (d *Data) VerifyChecksum() error {
	d.Lock()
	defer d.Unlock()

	return d.Item.Checksum.Verify(d.Item.Data)
}

// GetProfile retrieves a NodeProfileDescriptor.
func (d *Data) GetProfile() (*nodev1alpha1.NodeProfileDescriptor, metav1.Time) {
	d.Lock()
	defer d.Unlock()
	return d.Item.Data.Value, metav1.Unix(d.Item.Data.Timestamp, 0)
}

// SetProfile sets a NodeProfileDescriptor for a node.
func (d *Data) SetProfile(npd *nodev1alpha1.NodeProfileDescriptor, t metav1.Time) {
	d.Lock()
	defer d.Unlock()

	d.Item.Data = NodeProfileData{
		Value:     npd,
		Timestamp: t.Unix(),
	}
}
