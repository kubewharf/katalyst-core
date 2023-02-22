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
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/config/dynamic"
)

func TestNewCheckpoint(t *testing.T) {
	now := metav1.Now()
	kind := "EvictionConfiguration"
	crd := &dynamic.DynamicConfigCRD{
		EvictionConfiguration: &v1alpha1.EvictionConfiguration{
			Spec: v1alpha1.EvictionConfigurationSpec{
				Config: v1alpha1.EvictionConfig{
					EvictionPluginsConfig: v1alpha1.EvictionPluginsConfig{
						ReclaimedResourcesEvictionPluginConfig: v1alpha1.ReclaimedResourcesEvictionPluginConfig{
							EvictionThreshold: map[corev1.ResourceName]float64{
								corev1.ResourceCPU: 5.0,
							},
						},
					},
				},
			},
		},
	}

	configField := reflect.ValueOf(crd).Elem().FieldByName(kind)

	cp := NewCheckpoint(make(map[string]TargetConfigData))
	cp.SetData(kind, configField, now)

	checkpoint, err := cp.MarshalCheckpoint()
	assert.NoError(t, err)

	err = cp.UnmarshalCheckpoint(checkpoint)
	assert.NoError(t, err)

	err = cp.VerifyChecksum()
	assert.NoError(t, err)

	pt := now.ProtoTime()
	dynamicConfigCRD := &dynamic.DynamicConfigCRD{}
	configData, timestamp := cp.GetData(kind)
	configField = reflect.ValueOf(dynamicConfigCRD).Elem().FieldByName(kind)
	configField.Set(configData)
	assert.Equal(t, metav1.Unix(pt.Seconds, int64(pt.Nanos)), timestamp)
	assert.Equal(t, crd, dynamicConfigCRD)
}
