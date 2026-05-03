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

package scheduler

import (
	"testing"

	"github.com/stretchr/testify/assert"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/strategy/allocate"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/qrm"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

func TestSchedulerStrategy_Filter(t *testing.T) {
	t.Parallel()

	strategy := NewSchedulerStrategy()

	testCases := []struct {
		name                    string
		ctx                     *allocate.AllocationContext
		availableDevices        []string
		expectedFilteredDevices []string
	}{
		{
			name: "nil ResourceReq",
			ctx: &allocate.AllocationContext{
				ResourceReq: nil,
				GPUQRMPluginConfig: &qrm.GPUQRMPluginConfig{
					GPUSelectionResultAnnotationKey: "gpu-selection-result",
				},
				Emitter: metrics.DummyMetrics{},
			},
			availableDevices:        []string{"gpu-0", "gpu-1"},
			expectedFilteredDevices: []string{"gpu-0", "gpu-1"},
		},
		{
			name: "nil GPUQRMPluginConfig",
			ctx: &allocate.AllocationContext{
				ResourceReq: &pluginapi.ResourceRequest{
					Annotations: map[string]string{
						"gpu-selection-result": "gpu-0",
					},
				},
				GPUQRMPluginConfig: nil,
				Emitter:            metrics.DummyMetrics{},
			},
			availableDevices:        []string{"gpu-0", "gpu-1"},
			expectedFilteredDevices: []string{"gpu-0", "gpu-1"},
		},
		{
			name: "missing annotation",
			ctx: &allocate.AllocationContext{
				ResourceReq: &pluginapi.ResourceRequest{
					Annotations: map[string]string{},
				},
				GPUQRMPluginConfig: &qrm.GPUQRMPluginConfig{
					GPUSelectionResultAnnotationKey: "gpu-selection-result",
				},
				Emitter: metrics.DummyMetrics{},
			},
			availableDevices:        []string{"gpu-0", "gpu-1"},
			expectedFilteredDevices: []string{"gpu-0", "gpu-1"},
		},
		{
			name: "empty annotation value",
			ctx: &allocate.AllocationContext{
				ResourceReq: &pluginapi.ResourceRequest{
					Annotations: map[string]string{
						"gpu-selection-result": "",
					},
				},
				GPUQRMPluginConfig: &qrm.GPUQRMPluginConfig{
					GPUSelectionResultAnnotationKey: "gpu-selection-result",
				},
				Emitter: metrics.DummyMetrics{},
			},
			availableDevices:        []string{"gpu-0", "gpu-1"},
			expectedFilteredDevices: []string{"gpu-0", "gpu-1"},
		},
		{
			name: "valid intersection",
			ctx: &allocate.AllocationContext{
				ResourceReq: &pluginapi.ResourceRequest{
					Annotations: map[string]string{
						"gpu-selection-result": "gpu-0,gpu-2",
					},
				},
				GPUQRMPluginConfig: &qrm.GPUQRMPluginConfig{
					GPUSelectionResultAnnotationKey: "gpu-selection-result",
				},
				Emitter: metrics.DummyMetrics{},
			},
			availableDevices:        []string{"gpu-0", "gpu-1", "gpu-2", "gpu-3"},
			expectedFilteredDevices: []string{"gpu-0", "gpu-2"},
		},
		{
			name: "intersection is empty, should emit metric and return all available",
			ctx: &allocate.AllocationContext{
				ResourceReq: &pluginapi.ResourceRequest{
					Annotations: map[string]string{
						"gpu-selection-result": "gpu-4,gpu-5",
					},
				},
				GPUQRMPluginConfig: &qrm.GPUQRMPluginConfig{
					GPUSelectionResultAnnotationKey: "gpu-selection-result",
				},
				Emitter: metrics.DummyMetrics{},
			},
			availableDevices:        []string{"gpu-0", "gpu-1"},
			expectedFilteredDevices: []string{"gpu-0", "gpu-1"},
		},
		{
			name: "all requested devices available",
			ctx: &allocate.AllocationContext{
				ResourceReq: &pluginapi.ResourceRequest{
					Annotations: map[string]string{
						"gpu-selection-result": "gpu-1",
					},
				},
				GPUQRMPluginConfig: &qrm.GPUQRMPluginConfig{
					GPUSelectionResultAnnotationKey: "gpu-selection-result",
				},
				Emitter: metrics.DummyMetrics{},
			},
			availableDevices:        []string{"gpu-1"},
			expectedFilteredDevices: []string{"gpu-1"},
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			filteredDevices, err := strategy.Filter(tc.ctx, tc.availableDevices)
			assert.NoError(t, err)
			assert.ElementsMatch(t, tc.expectedFilteredDevices, filteredDevices)
		})
	}
}
