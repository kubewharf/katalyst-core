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
	"fmt"
	"strings"

	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-api/pkg/apis/workload/v1alpha1"
	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager"
	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager/checksum"
)

const (
	// Delimiter used on checkpoints written to disk
	delimiter = "_"
	spdPrefix = "SPD"
)

// ServiceProfileCheckpoint defines the operations to retrieve spd
type ServiceProfileCheckpoint interface {
	checkpointmanager.Checkpoint
	GetSPD() *v1alpha1.ServiceProfileDescriptor
}

// Data to be stored as checkpoint
type Data struct {
	SPD      *v1alpha1.ServiceProfileDescriptor
	Checksum checksum.Checksum
}

// NewServiceProfileCheckpoint returns new spd checkpoint
func NewServiceProfileCheckpoint(spd *v1alpha1.ServiceProfileDescriptor) ServiceProfileCheckpoint {
	return &Data{SPD: spd}
}

// MarshalCheckpoint returns marshalled data
func (cp *Data) MarshalCheckpoint() ([]byte, error) {
	cp.Checksum = checksum.New(*cp.SPD)
	return json.Marshal(*cp)
}

// UnmarshalCheckpoint returns unmarshalled data
func (cp *Data) UnmarshalCheckpoint(blob []byte) error {
	return json.Unmarshal(blob, cp)
}

// VerifyChecksum verifies that passed checksum is same as calculated checksum
func (cp *Data) VerifyChecksum() error {
	return cp.Checksum.Verify(*cp.SPD)
}

// GetSPD retrieves the spd from the checkpoint
func (cp *Data) GetSPD() *v1alpha1.ServiceProfileDescriptor {
	return cp.SPD
}

//getSPDKey returns the full qualified path for the spd checkpoint
func getSPDKey(spd *v1alpha1.ServiceProfileDescriptor) string {
	return fmt.Sprintf("%s%s%s%s%s.yaml", spdPrefix, delimiter, spd.Namespace, delimiter, spd.Name)
}

// LoadSPDs Loads All Checkpoints from disk
func LoadSPDs(cpm checkpointmanager.CheckpointManager) ([]*v1alpha1.ServiceProfileDescriptor, error) {
	spd := make([]*v1alpha1.ServiceProfileDescriptor, 0)

	checkpointKeys, err := cpm.ListCheckpoints()
	if err != nil {
		klog.Errorf("Failed to list checkpoints: %v", err)
	}

	for _, key := range checkpointKeys {
		if !strings.HasPrefix(key, spdPrefix) {
			continue
		}

		checkpoint := NewServiceProfileCheckpoint(nil)
		err := cpm.GetCheckpoint(key, checkpoint)
		if err != nil {
			klog.Errorf("Failed to retrieve checkpoint for spd %q: %v", key, err)
			continue
		}
		spd = append(spd, checkpoint.GetSPD())
	}
	return spd, nil
}

// WriteSPD a checkpoint to a file on disk if annotation is present
func WriteSPD(cpm checkpointmanager.CheckpointManager, spd *v1alpha1.ServiceProfileDescriptor) error {
	var err error
	data := NewServiceProfileCheckpoint(spd)
	err = cpm.CreateCheckpoint(getSPDKey(spd), data)
	return err
}

// DeleteSPD deletes a checkpoint from disk if present
func DeleteSPD(cpm checkpointmanager.CheckpointManager, spd *v1alpha1.ServiceProfileDescriptor) error {
	return cpm.RemoveCheckpoint(getSPDKey(spd))
}
