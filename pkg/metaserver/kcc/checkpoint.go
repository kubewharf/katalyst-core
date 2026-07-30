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

package kcc

import (
	"encoding/json"
	"hash/fnv"
	"reflect"
	"sync"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager"
	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager/checksum"
	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager/errors"
	hashutil "k8s.io/kubernetes/pkg/util/hash"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/crd"
)

type ConfigManagerCheckpoint interface {
	checkpointmanager.Checkpoint
	GetData(kind string) (reflect.Value, metav1.Time)
	SetData(kind string, v reflect.Value, t metav1.Time)
}

type TargetConfigData struct {
	// Value only store spec of dynamic config crd
	Value     *crd.DynamicConfigCRD
	Timestamp int64
}

// Data holds checkpoint data and its checksum
type Data struct {
	sync.Mutex
	Item *DataItem

	loadedFromCheckpoint bool
}

type DataItem struct {
	// Data maps from kind to target config data
	Data     map[string]TargetConfigData
	Checksum checksum.Checksum
}

// NewCheckpoint returns an instance of Checkpoint
func NewCheckpoint(configResponses map[string]TargetConfigData) ConfigManagerCheckpoint {
	return &Data{
		Item: &DataItem{
			Data: configResponses,
		},
	}
}

func (d *Data) MarshalCheckpoint() ([]byte, error) {
	d.Lock()
	defer d.Unlock()

	// Compute checksum based on JSON-serialized data bytes instead of Go struct
	// deep hash. This makes checksum verification resilient to struct field
	// additions/removals during version upgrades, as long as the JSON payload
	// remains compatible.
	d.Item.Checksum = computeJSONChecksum(d.Item.Data)
	return json.Marshal(*(d.Item))
}

func (d *Data) UnmarshalCheckpoint(blob []byte) error {
	d.Lock()
	defer d.Unlock()

	if err := json.Unmarshal(blob, d.Item); err != nil {
		return err
	}
	d.loadedFromCheckpoint = true
	return nil
}

func (d *Data) VerifyChecksum() error {
	d.Lock()
	defer d.Unlock()

	// First try the JSON-based checksum (new format).
	if d.Item.Checksum == computeJSONChecksum(d.Item.Data) {
		return nil
	}

	// Fall back to the legacy deep-hash checksum for backward compatibility
	// with checkpoints written by older versions.
	legacyChecksum := computeLegacyChecksum(d.Item.Data)
	if d.Item.Checksum == legacyChecksum {
		return nil
	}

	if d.isMigratableDecodedCheckpoint() {
		return nil
	}

	return errors.ErrCorruptCheckpoint
}

func (d *Data) isMigratableDecodedCheckpoint() bool {
	if !d.loadedFromCheckpoint || d.Item == nil || len(d.Item.Data) == 0 {
		return false
	}

	for kind, data := range d.Item.Data {
		if !isValidMigratableConfig(kind, data) {
			return false
		}
	}

	return true
}

func isValidMigratableConfig(kind string, data TargetConfigData) bool {
	if data.Value == nil || data.Timestamp <= 0 {
		return false
	}

	configValue := reflect.ValueOf(data.Value)
	if configValue.Kind() != reflect.Ptr || configValue.IsNil() {
		return false
	}

	configField := configValue.Elem().FieldByName(kind)
	if !configField.IsValid() || configField.Kind() != reflect.Ptr || configField.IsNil() {
		return false
	}

	return true
}

// computeJSONChecksum computes a checksum from the JSON-serialized form of
// the data map. JSON serialization produces stable output for maps (keys are
// sorted), so the result is deterministic and resilient to Go struct changes
// as long as the JSON representation stays compatible.
func computeJSONChecksum(data map[string]TargetConfigData) checksum.Checksum {
	blob, err := json.Marshal(data)
	if err != nil {
		return 0
	}

	h := fnv.New32a()
	_, _ = h.Write(blob)
	return checksum.Checksum(uint64(h.Sum32()))
}

// computeLegacyChecksum reproduces the old checksum calculation based on
// spew.DeepHashObject. It is kept only for backward compatibility when
// reading checkpoints written by previous versions.
func computeLegacyChecksum(data map[string]TargetConfigData) checksum.Checksum {
	h := fnv.New32a()
	hashutil.DeepHashObject(h, data)
	return checksum.Checksum(uint64(h.Sum32()))
}

func (d *Data) GetData(kind string) (reflect.Value, metav1.Time) {
	d.Lock()
	defer d.Unlock()

	if data, ok := d.Item.Data[kind]; ok {
		configField := reflect.ValueOf(data.Value).Elem().FieldByName(kind)
		return configField, metav1.Unix(data.Timestamp, 0)
	}

	return reflect.Value{}, metav1.Time{}
}

func (d *Data) SetData(kind string, val reflect.Value, t metav1.Time) {
	d.Lock()
	defer d.Unlock()

	if d.Item.Data == nil {
		d.Item.Data = make(map[string]TargetConfigData)
	}

	// get target dynamic configField by kind
	dynamicConfiguration := &crd.DynamicConfigCRD{}
	configField := reflect.ValueOf(dynamicConfiguration).Elem().FieldByName(kind)
	configField.Set(reflect.New(configField.Type().Elem()))

	// set target dynamic configField's spec field
	specValue := val.Elem().FieldByName("Spec")
	specField := configField.Elem().FieldByName("Spec")
	specField.Set(specValue)

	// set target dynamic configField's status field
	statusValue := val.Elem().FieldByName("Status")
	statusField := configField.Elem().FieldByName("Status")
	statusField.Set(statusValue)

	d.Item.Data[kind] = TargetConfigData{
		Value:     dynamicConfiguration,
		Timestamp: t.Unix(),
	}
}
