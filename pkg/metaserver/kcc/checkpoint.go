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
	"reflect"
	"sync"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager"
	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager/checksum"

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

	// only set target dynamic configField's spec field
	specValue := val.Elem().FieldByName("Spec")
	specField := configField.Elem().FieldByName("Spec")
	specField.Set(specValue)

	d.Item.Data[kind] = TargetConfigData{
		Value:     dynamicConfiguration,
		Timestamp: t.Unix(),
	}
}
