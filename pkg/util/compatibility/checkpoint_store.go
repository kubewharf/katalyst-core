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

package compatibility

import (
	"encoding/json"
	"errors"
	"fmt"

	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager"
	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager/checksum"
	checkpointerrors "k8s.io/kubernetes/pkg/kubelet/checkpointmanager/errors"
)

const versionCheckpointSchemaVersion = 1

type VersionCheckpoint struct {
	SchemaVersion int               `json:"schemaVersion"`
	APIVersion    string            `json:"version"`
	Checksum      checksum.Checksum `json:"checksum"`
}

func NewVersionCheckpoint(version string) VersionCheckpoint {
	return VersionCheckpoint{
		SchemaVersion: versionCheckpointSchemaVersion,
		APIVersion:    version,
	}
}

func (c *VersionCheckpoint) Version() string {
	return c.APIVersion
}

func (c *VersionCheckpoint) SetVersion(version string) {
	c.APIVersion = version
}

func (c *VersionCheckpoint) MarshalCheckpoint() ([]byte, error) {
	c.Checksum = 0
	c.Checksum = checksum.New(c)
	return json.Marshal(c)
}

func (c *VersionCheckpoint) UnmarshalCheckpoint(blob []byte) error {
	if err := json.Unmarshal(blob, c); err != nil {
		return err
	}
	if c.SchemaVersion != versionCheckpointSchemaVersion {
		return fmt.Errorf("unsupported compatibility checkpoint schema version %d", c.SchemaVersion)
	}
	if c.APIVersion == "" {
		return errors.New("compatibility checkpoint version is empty")
	}
	return nil
}

func (c *VersionCheckpoint) VerifyChecksum() error {
	checksumValue := c.Checksum
	c.Checksum = 0
	err := checksumValue.Verify(c)
	c.Checksum = checksumValue
	return err
}

type checkpointVersionStore struct {
	checkpointManager checkpointmanager.CheckpointManager
}

func NewVersionStore(directory string) VersionStore {
	if directory == "" {
		return nil
	}

	checkpointManager, err := checkpointmanager.NewCheckpointManager(directory)
	if err != nil {
		klog.ErrorS(err, "Disable compatibility checkpoint persistence", "directory", directory)
		return nil
	}
	return &checkpointVersionStore{checkpointManager: checkpointManager}
}

func (s *checkpointVersionStore) Get(checkpoint APIVersionCheckpoint) (bool, error) {
	err := s.checkpointManager.GetCheckpoint(checkpoint.FileName(), checkpoint)
	if errors.Is(err, checkpointerrors.ErrCheckpointNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (s *checkpointVersionStore) Put(checkpoint APIVersionCheckpoint) error {
	return s.checkpointManager.CreateCheckpoint(checkpoint.FileName(), checkpoint)
}

func (s *checkpointVersionStore) Delete(checkpoint APIVersionCheckpoint) error {
	return s.checkpointManager.RemoveCheckpoint(checkpoint.FileName())
}
