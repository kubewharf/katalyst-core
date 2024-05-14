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
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/api/resource"
	kubeletconfigv1beta1 "k8s.io/kubelet/config/v1beta1"
	"k8s.io/kubernetes/pkg/features"

	"github.com/kubewharf/katalyst-api/pkg/consts"
)

func TestCheckFeatureGateEnable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		conf     *kubeletconfigv1beta1.KubeletConfiguration
		features []string
		enabled  bool
		err      error
	}{
		{
			name: "nil configration",
			err:  fmt.Errorf("nil KubeletConfiguration"),
		},
		{
			name: "partial enabled",
			conf: &kubeletconfigv1beta1.KubeletConfiguration{
				FeatureGates: map[string]bool{
					"a": true,
				},
			},
			features: []string{"a", "b"},
			enabled:  false,
			err:      nil,
		},
		{
			name: "total enabled",
			conf: &kubeletconfigv1beta1.KubeletConfiguration{
				FeatureGates: map[string]bool{
					"a": true,
					"b": true,
					"c": false,
				},
			},
			features: []string{"a", "b"},
			enabled:  true,
			err:      nil,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ok, err := CheckFeatureGateEnable(tt.conf, tt.features...)
			if tt.err == nil {
				assert.Equal(t, tt.enabled, ok)
			} else {
				assert.NotNil(t, err)
			}
		})
	}
}

func TestGetReservedQuantity(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		conf             *kubeletconfigv1beta1.KubeletConfiguration
		resourceName     string
		resourceQuantity resource.Quantity
		valid            bool
		err              error
	}{
		{
			name: "nil configration",
			err:  fmt.Errorf("nil KubeletConfiguration"),
		},
		{
			name: "resource both exists",
			conf: &kubeletconfigv1beta1.KubeletConfiguration{
				KubeReserved: map[string]string{
					"cpu": "1024m",
				},
				SystemReserved: map[string]string{
					"cpu": "102m",
				},
			},
			resourceName:     "cpu",
			resourceQuantity: resource.MustParse("1126m"),
			valid:            true,
			err:              nil,
		},
		{
			name: "resource only-one exists",
			conf: &kubeletconfigv1beta1.KubeletConfiguration{
				KubeReserved: map[string]string{
					"cpu": "1024m",
				},
			},
			resourceName:     "cpu",
			resourceQuantity: resource.MustParse("1024m"),
			valid:            true,
			err:              nil,
		},
		{
			name: "resource not exists",
			conf: &kubeletconfigv1beta1.KubeletConfiguration{
				KubeReserved: map[string]string{
					"cpu": "1024m",
				},
				SystemReserved: map[string]string{
					"cpu": "102m",
				},
			},
			resourceName: "memory",
			valid:        false,
			err:          nil,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			q, ok, err := GetReservedQuantity(tt.conf, tt.resourceName)
			if tt.err == nil {
				assert.Equal(t, tt.valid, ok)
				assert.Equal(t, tt.resourceQuantity.Value(), q.Value())
			} else {
				assert.NotNil(t, err)
			}
		})
	}
}

func TestGetInTreeProviderPolicies(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		conf *kubeletconfigv1beta1.KubeletConfiguration
		res  map[string]string
		err  error
	}{
		{
			name: "nil configration",
			err:  fmt.Errorf("nil KubeletConfiguration"),
		},
		{
			name: "cpu only",
			conf: &kubeletconfigv1beta1.KubeletConfiguration{
				FeatureGates: map[string]bool{
					string(features.CPUManager): true,
				},
				CPUManagerPolicy: "test-cpu-policy",
			},
			res: map[string]string{
				consts.KCNRAnnotationCPUManager:    "test-cpu-policy",
				consts.KCNRAnnotationMemoryManager: string(consts.MemoryManagerOff),
			},
			err: nil,
		},
		{
			name: "all policies",
			conf: &kubeletconfigv1beta1.KubeletConfiguration{
				FeatureGates: map[string]bool{
					string(features.CPUManager):    true,
					string(features.MemoryManager): true,
				},
				CPUManagerPolicy:    "test-cpu-policy",
				MemoryManagerPolicy: "test-memory-policy",
			},
			res: map[string]string{
				consts.KCNRAnnotationCPUManager:    "test-cpu-policy",
				consts.KCNRAnnotationMemoryManager: "test-memory-policy",
			},
			err: nil,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			res, err := GetInTreeProviderPolicies(tt.conf)
			if tt.err == nil {
				assert.Equal(t, tt.res, res)
			} else {
				assert.NotNil(t, err)
			}
		})
	}
}
