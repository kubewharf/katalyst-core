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
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager/checksum"
	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager/errors"

	"github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/crd"
)

func TestNewCheckpoint(t *testing.T) {
	t.Parallel()

	now := metav1.Now()
	kind := crd.ResourceKindAdminQoSConfiguration
	dynamicCRD := &crd.DynamicConfigCRD{
		AdminQoSConfiguration: &v1alpha1.AdminQoSConfiguration{
			Spec: v1alpha1.AdminQoSConfigurationSpec{
				Config: v1alpha1.AdminQoSConfig{
					EvictionConfig: &v1alpha1.EvictionConfig{
						DryRun: []string{},
						ReclaimedResourcesEvictionConfig: &v1alpha1.ReclaimedResourcesEvictionConfig{
							EvictionThreshold: map[corev1.ResourceName]float64{
								corev1.ResourceCPU: 5.0,
							},
						},
					},
				},
			},
			Status: v1alpha1.GenericConfigStatus{
				Conditions: []v1alpha1.GenericConfigCondition{
					{},
				},
			},
		},
	}

	configField := reflect.ValueOf(dynamicCRD).Elem().FieldByName(kind)

	cp := NewCheckpoint(make(map[string]TargetConfigData))
	cp.SetData(kind, configField, now)

	checkpoint, err := cp.MarshalCheckpoint()
	assert.NoError(t, err)

	err = cp.UnmarshalCheckpoint(checkpoint)
	assert.NoError(t, err)

	err = cp.VerifyChecksum()
	assert.NoError(t, err)

	dynamicConfigCRD := &crd.DynamicConfigCRD{}
	configData, timestamp := cp.GetData(kind)
	configField = reflect.ValueOf(dynamicConfigCRD).Elem().FieldByName(kind)
	configField.Set(configData)
	assert.Equal(t, metav1.Unix(now.Unix(), 0), timestamp)
	assert.Equal(t, dynamicCRD, dynamicConfigCRD)
}

// buildTestCheckpointData creates a simple checkpoint data map for test use.
func buildTestCheckpointData(t *testing.T) map[string]TargetConfigData {
	t.Helper()

	now := metav1.Now()
	kind := crd.ResourceKindAdminQoSConfiguration
	dynamicCRD := &crd.DynamicConfigCRD{
		AdminQoSConfiguration: &v1alpha1.AdminQoSConfiguration{
			Spec: v1alpha1.AdminQoSConfigurationSpec{
				Config: v1alpha1.AdminQoSConfig{
					EvictionConfig: &v1alpha1.EvictionConfig{
						DryRun: []string{},
						ReclaimedResourcesEvictionConfig: &v1alpha1.ReclaimedResourcesEvictionConfig{
							EvictionThreshold: map[corev1.ResourceName]float64{
								corev1.ResourceCPU: 5.0,
							},
						},
					},
				},
			},
			Status: v1alpha1.GenericConfigStatus{
				Conditions: []v1alpha1.GenericConfigCondition{{}},
			},
		},
	}

	cp := NewCheckpoint(make(map[string]TargetConfigData))
	configField := reflect.ValueOf(dynamicCRD).Elem().FieldByName(kind)
	cp.SetData(kind, configField, now)
	return cp.(*Data).Item.Data
}

func TestVerifyChecksumWithLegacyChecksum(t *testing.T) {
	t.Parallel()

	data := buildTestCheckpointData(t)

	// Simulate a checkpoint written by an older version: checksum is computed
	// using the legacy deep-hash method, not the new JSON-based one.
	d := &Data{
		Item: &DataItem{
			Data:     data,
			Checksum: computeLegacyChecksum(data),
		},
	}

	// JSON-based checksum should NOT match (it's a legacy checkpoint).
	assert.NotEqual(t, computeJSONChecksum(data), d.Item.Checksum,
		"legacy checksum should differ from JSON checksum")

	// VerifyChecksum should fall back to legacy checksum and succeed.
	err := d.VerifyChecksum()
	assert.NoError(t, err, "legacy checkpoint should still pass verification")
}

func TestVerifyChecksumCorrupted(t *testing.T) {
	t.Parallel()

	data := buildTestCheckpointData(t)

	// Intentionally set a wrong checksum that matches neither JSON nor legacy.
	d := &Data{
		Item: &DataItem{
			Data:     data,
			Checksum: checksum.Checksum(0xDEADBEEF),
		},
	}

	err := d.VerifyChecksum()
	assert.ErrorIs(t, err, errors.ErrCorruptCheckpoint,
		"mismatched checksum should return ErrCorruptCheckpoint")
}

func TestVerifyChecksumAllowsMigratableLegacyCheckpoint(t *testing.T) {
	t.Parallel()

	data := buildTestCheckpointData(t)
	item := &DataItem{
		Data:     data,
		Checksum: checksum.Checksum(0xDEADBEEF),
	}
	assert.NotEqual(t, computeJSONChecksum(data), item.Checksum,
		"test setup should simulate a checkpoint whose JSON checksum does not match")
	assert.NotEqual(t, computeLegacyChecksum(data), item.Checksum,
		"test setup should simulate a checkpoint whose current legacy checksum does not match")

	blob, err := json.Marshal(item)
	assert.NoError(t, err)

	restored := NewCheckpoint(make(map[string]TargetConfigData))
	err = restored.UnmarshalCheckpoint(blob)
	assert.NoError(t, err)

	err = restored.VerifyChecksum()
	assert.NoError(t, err,
		"decoded checkpoint with valid KCC data should be accepted for one-time upgrade migration")
}

func TestVerifyChecksumRejectsMigratableCheckpointWithUnknownKind(t *testing.T) {
	t.Parallel()

	data := buildTestCheckpointData(t)
	data["UnknownConfiguration"] = data[crd.ResourceKindAdminQoSConfiguration]
	item := &DataItem{
		Data:     data,
		Checksum: checksum.Checksum(0xDEADBEEF),
	}
	blob, err := json.Marshal(item)
	assert.NoError(t, err)

	restored := NewCheckpoint(make(map[string]TargetConfigData))
	err = restored.UnmarshalCheckpoint(blob)
	assert.NoError(t, err)

	err = restored.VerifyChecksum()
	assert.ErrorIs(t, err, errors.ErrCorruptCheckpoint,
		"unknown config kind should not be accepted by migration fallback")
}

func TestVerifyChecksumRejectsMigratableCheckpointWithNilValue(t *testing.T) {
	t.Parallel()

	data := buildTestCheckpointData(t)
	entry := data[crd.ResourceKindAdminQoSConfiguration]
	entry.Value = nil
	data[crd.ResourceKindAdminQoSConfiguration] = entry
	item := &DataItem{
		Data:     data,
		Checksum: checksum.Checksum(0xDEADBEEF),
	}
	blob, err := json.Marshal(item)
	assert.NoError(t, err)

	restored := NewCheckpoint(make(map[string]TargetConfigData))
	err = restored.UnmarshalCheckpoint(blob)
	assert.NoError(t, err)

	err = restored.VerifyChecksum()
	assert.ErrorIs(t, err, errors.ErrCorruptCheckpoint,
		"nil config value should not be accepted by migration fallback")
}

func TestComputeJSONChecksumDeterministic(t *testing.T) {
	t.Parallel()

	data := buildTestCheckpointData(t)

	// JSON checksum should be deterministic across repeated calls.
	sum1 := computeJSONChecksum(data)
	sum2 := computeJSONChecksum(data)
	assert.Equal(t, sum1, sum2, "JSON checksum should be deterministic")
}

func TestComputeJSONChecksumNilMap(t *testing.T) {
	t.Parallel()

	// nil map should marshal to "null" without error and produce a valid checksum.
	sum := computeJSONChecksum(nil)
	assert.NotZero(t, sum, "nil map should produce a non-zero checksum")
}

func TestMarshalCheckpointRoundTrip(t *testing.T) {
	t.Parallel()

	data := buildTestCheckpointData(t)
	d := &Data{Item: &DataItem{Data: data}}

	blob, err := d.MarshalCheckpoint()
	assert.NoError(t, err)
	assert.NotEmpty(t, blob)

	// Unmarshal into a fresh Data and verify checksum still matches.
	restored := &Data{Item: &DataItem{}}
	err = restored.UnmarshalCheckpoint(blob)
	assert.NoError(t, err)

	err = restored.VerifyChecksum()
	assert.NoError(t, err)

	// The stored checksum should be the JSON-based one (not legacy).
	assert.Equal(t, computeJSONChecksum(restored.Item.Data), restored.Item.Checksum)
}

func TestUnmarshalCheckpointInvalidJSON(t *testing.T) {
	t.Parallel()

	d := &Data{Item: &DataItem{}}
	err := d.UnmarshalCheckpoint([]byte("not valid json"))
	assert.Error(t, err)
}

func TestMarshalCheckpointProducesValidJSON(t *testing.T) {
	t.Parallel()

	data := buildTestCheckpointData(t)
	d := &Data{Item: &DataItem{Data: data}}

	blob, err := d.MarshalCheckpoint()
	assert.NoError(t, err)

	// The marshaled blob should be valid JSON with the expected top-level fields.
	var item DataItem
	err = json.Unmarshal(blob, &item)
	assert.NoError(t, err)
	assert.NotNil(t, item.Data)
	assert.NotZero(t, item.Checksum)
}
