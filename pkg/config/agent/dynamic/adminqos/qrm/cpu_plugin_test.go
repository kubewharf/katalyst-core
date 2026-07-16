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

package qrm

import (
	"testing"

	configv1alpha1 "github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/crd"
)

func TestCPUPluginConfigurationApplyDynamicBulkheadEnable(t *testing.T) {
	t.Parallel()

	boolPtr := func(v bool) *bool { return &v }

	tests := []struct {
		name           string
		initialEnable  bool
		bulkheadConfig *configv1alpha1.BulkheadConfig
		wantEnable     bool
		wantTopology   bool
		wantMems       bool
		wantWorkqueue  bool
	}{
		{
			name:          "nil bulkhead config keeps old value",
			initialEnable: true,
			wantEnable:    true,
			wantMems:      true,
		},
		{
			name:           "nil enable keeps old value",
			initialEnable:  true,
			bulkheadConfig: &configv1alpha1.BulkheadConfig{},
			wantEnable:     true,
			wantMems:       true,
		},
		{
			name:           "enable true overrides",
			bulkheadConfig: &configv1alpha1.BulkheadConfig{Enable: boolPtr(true)},
			wantEnable:     true,
			wantMems:       true,
		},
		{
			name:           "enable false overrides",
			initialEnable:  true,
			bulkheadConfig: &configv1alpha1.BulkheadConfig{Enable: boolPtr(false)},
			wantMems:       true,
		},
		{
			name: "cpuset mems false overrides default",
			bulkheadConfig: &configv1alpha1.BulkheadConfig{
				EnableBulkheadCpusetMems: boolPtr(false),
			},
		},
		{
			name: "all bulkhead fields override",
			bulkheadConfig: &configv1alpha1.BulkheadConfig{
				Enable:                       boolPtr(true),
				EnableBulkheadCpusetTopology: boolPtr(true),
				EnableBulkheadCpusetMems:     boolPtr(true),
				EnableBulkheadWorkqueue:      boolPtr(true),
			},
			wantEnable:    true,
			wantTopology:  true,
			wantMems:      true,
			wantWorkqueue: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c := NewCPUPluginConfiguration()
			c.BulkheadConfig.Enable = tt.initialEnable
			c.ApplyConfiguration(&crd.DynamicConfigCRD{
				AdminQoSConfiguration: &configv1alpha1.AdminQoSConfiguration{
					Spec: configv1alpha1.AdminQoSConfigurationSpec{
						Config: configv1alpha1.AdminQoSConfig{
							QRMPluginConfig: &configv1alpha1.QRMPluginConfig{
								CPUPluginConfig: &configv1alpha1.CPUPluginConfig{
									BulkheadConfig: tt.bulkheadConfig,
								},
							},
						},
					},
				},
			})

			if c.BulkheadConfig.Enable != tt.wantEnable {
				t.Fatalf("Enable = %t, want %t", c.BulkheadConfig.Enable, tt.wantEnable)
			}
			if c.BulkheadConfig.EnableBulkheadCpusetTopology != tt.wantTopology {
				t.Fatalf("EnableBulkheadCpusetTopology = %t, want %t", c.BulkheadConfig.EnableBulkheadCpusetTopology, tt.wantTopology)
			}
			if c.BulkheadConfig.EnableBulkheadCpusetMems != tt.wantMems {
				t.Fatalf("EnableBulkheadCpusetMems = %t, want %t", c.BulkheadConfig.EnableBulkheadCpusetMems, tt.wantMems)
			}
			if c.BulkheadConfig.EnableBulkheadWorkqueue != tt.wantWorkqueue {
				t.Fatalf("EnableBulkheadWorkqueue = %t, want %t", c.BulkheadConfig.EnableBulkheadWorkqueue, tt.wantWorkqueue)
			}
		})
	}
}

func TestCPUPluginConfigurationApplyDynamicBulkheadNonReclaimPoolMinSize(t *testing.T) {
	t.Parallel()

	int64Ptr := func(v int64) *int64 { return &v }

	tests := []struct {
		name           string
		initialMinSize int64
		bulkheadConfig *configv1alpha1.BulkheadConfig
		wantMinSize    int64
	}{
		{
			name:           "nil bulkhead config keeps old value",
			initialMinSize: 2,
			wantMinSize:    2,
		},
		{
			name:           "nil non reclaim min keeps old value",
			initialMinSize: 2,
			bulkheadConfig: &configv1alpha1.BulkheadConfig{},
			wantMinSize:    2,
		},
		{
			name:           "zero overrides old value",
			initialMinSize: 2,
			bulkheadConfig: &configv1alpha1.BulkheadConfig{NonReclaimPoolMinSize: int64Ptr(0)},
			wantMinSize:    0,
		},
		{
			name:           "positive value overrides",
			bulkheadConfig: &configv1alpha1.BulkheadConfig{NonReclaimPoolMinSize: int64Ptr(4)},
			wantMinSize:    4,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c := NewCPUPluginConfiguration()
			c.BulkheadConfig.NonReclaimPoolMinSize = tt.initialMinSize
			c.ApplyConfiguration(&crd.DynamicConfigCRD{
				AdminQoSConfiguration: &configv1alpha1.AdminQoSConfiguration{
					Spec: configv1alpha1.AdminQoSConfigurationSpec{
						Config: configv1alpha1.AdminQoSConfig{
							QRMPluginConfig: &configv1alpha1.QRMPluginConfig{
								CPUPluginConfig: &configv1alpha1.CPUPluginConfig{
									BulkheadConfig: tt.bulkheadConfig,
								},
							},
						},
					},
				},
			})

			if c.BulkheadConfig.NonReclaimPoolMinSize != tt.wantMinSize {
				t.Fatalf("NonReclaimPoolMinSize = %d, want %d", c.BulkheadConfig.NonReclaimPoolMinSize, tt.wantMinSize)
			}
		})
	}
}
