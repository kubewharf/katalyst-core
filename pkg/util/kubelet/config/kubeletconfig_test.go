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
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/kubernetes/pkg/features"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

func TestCheckFeatureGateEnable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		conf     *native.KubeletConfiguration
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
			conf: &native.KubeletConfiguration{
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
			conf: &native.KubeletConfiguration{
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
		conf             *native.KubeletConfiguration
		resourceName     string
		resourceQuantity resource.Quantity
		valid            bool
		err              error
	}{
		{
			name: "nil configuration",
			err:  fmt.Errorf("nil KubeletConfiguration"),
		},
		{
			name: "resource both exists",
			conf: &native.KubeletConfiguration{
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
			conf: &native.KubeletConfiguration{
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
			conf: &native.KubeletConfiguration{
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

func TestGetReservedSystemCPUList(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		conf            *native.KubeletConfiguration
		reservedCPUList string
		err             error
	}{
		{
			name: "nil configuration",
			err:  fmt.Errorf("nil KubeletConfiguration"),
		},
		{
			name: "valid reserved system cpus",
			conf: &native.KubeletConfiguration{
				ReservedSystemCPUs: "0-3",
			},
			reservedCPUList: "0-3",
			err:             nil,
		},
		{
			name:            "empty system reserved cpus",
			conf:            &native.KubeletConfiguration{},
			reservedCPUList: "",
			err:             nil,
		},
		{
			name: "invalid reserved system cpus",
			conf: &native.KubeletConfiguration{
				ReservedSystemCPUs: "0,3,",
			},
			reservedCPUList: "0,3,",
			err:             nil,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			q, err := GetReservedSystemCPUList(tt.conf)
			if tt.err == nil {
				assert.Equal(t, tt.reservedCPUList, q)
			} else {
				assert.NotNil(t, err)
			}
		})
	}
}

func TestGetReservedMemoryInfo(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		conf            *native.KubeletConfiguration
		reservedMemInfo map[int32]v1.ResourceList
		err             error
	}{
		{
			name: "nil configuration",
			err:  fmt.Errorf("nil KubeletConfiguration"),
		},
		{
			name: "valid reserved memory info",
			conf: &native.KubeletConfiguration{
				ReservedMemory: []native.MemoryReservation{
					{
						NumaNode: 0,
						Limits: map[v1.ResourceName]resource.Quantity{
							v1.ResourceMemory: resource.MustParse("1024Mi"),
						},
					},
					{
						NumaNode: 1,
						Limits: map[v1.ResourceName]resource.Quantity{
							v1.ResourceMemory: resource.MustParse("2048Mi"),
						},
					},
				},
			},
			reservedMemInfo: map[int32]v1.ResourceList{
				0: {
					v1.ResourceMemory: resource.MustParse("1024Mi"),
				},
				1: {
					v1.ResourceMemory: resource.MustParse("2048Mi"),
				},
			},
			err: nil,
		},
		{
			name:            "empty system reserved cpus",
			conf:            &native.KubeletConfiguration{},
			reservedMemInfo: map[int32]v1.ResourceList{},
			err:             nil,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			q, err := GetReservedMemoryInfo(tt.conf)
			if tt.err == nil {
				assert.Equal(t, tt.reservedMemInfo, q)
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
		conf *native.KubeletConfiguration
		res  map[string]string
		err  error
	}{
		{
			name: "nil configration",
			err:  fmt.Errorf("nil KubeletConfiguration"),
		},
		{
			name: "cpu only",
			conf: &native.KubeletConfiguration{
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
			conf: &native.KubeletConfiguration{
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
