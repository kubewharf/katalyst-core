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

package config

import (
	"encoding/json"
	"reflect"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager"
	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager/checksum"

	"github.com/kubewharf/katalyst-core/pkg/config/dynamic"
)

type ConfigManagerCheckpoint interface {
	checkpointmanager.Checkpoint
	GetData(kind string) (reflect.Value, metav1.Time)
	SetData(kind string, v reflect.Value, t metav1.Time)
}

type TargetConfigData struct {
	// Value only store spec of dynamic config crd
	Value     *dynamic.DynamicConfigCRD
	Timestamp metav1.Timestamp
}

// Data holds checkpoint data and its checksum
type Data struct {
	// Data maps from kind to target config data
	Data     map[string]TargetConfigData
	Checksum checksum.Checksum
}

// NewCheckpoint returns an instance of Checkpoint
func NewCheckpoint(configResponses map[string]TargetConfigData) ConfigManagerCheckpoint {
	return &Data{
		Data: configResponses,
	}
}

func (d *Data) MarshalCheckpoint() ([]byte, error) {
	d.Checksum = checksum.New(d.Data)
	return json.Marshal(*d)
}

func (d *Data) UnmarshalCheckpoint(blob []byte) error {
	return json.Unmarshal(blob, d)
}

func (d *Data) VerifyChecksum() error {
	return d.Checksum.Verify(d.Data)
}

func (d *Data) GetData(kind string) (reflect.Value, metav1.Time) {
	if data, ok := d.Data[kind]; ok {
		configField := reflect.ValueOf(data.Value).Elem().FieldByName(kind)
		return configField, metav1.Unix(data.Timestamp.Seconds, int64(data.Timestamp.Nanos))
	}

	return reflect.Value{}, metav1.Time{}
}

func (d *Data) SetData(kind string, val reflect.Value, t metav1.Time) {
	if d.Data == nil {
		d.Data = make(map[string]TargetConfigData)
	}

	// get target dynamic configField by kind
	dynamicConfiguration := &dynamic.DynamicConfigCRD{}
	configField := reflect.ValueOf(dynamicConfiguration).Elem().FieldByName(kind)
	configField.Set(reflect.New(configField.Type().Elem()))

	// only set target dynamic configField's spec field
	specValue := val.Elem().FieldByName("Spec")
	specField := configField.Elem().FieldByName("Spec")
	specField.Set(specValue)

	d.Data[kind] = TargetConfigData{
		Value:     dynamicConfiguration,
		Timestamp: *t.ProtoTime(),
	}
}
