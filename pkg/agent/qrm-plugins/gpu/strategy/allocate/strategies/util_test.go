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

package strategies

import (
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/strategy/allocate"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func TestIsBindingContextValid(t *testing.T) {
	t.Parallel()

	registry := machine.NewDeviceTopologyRegistry(metrics.DummyMetrics{})

	tests := []struct {
		name                        string
		ctx                         *allocate.AllocationContext
		sortedDevices               []string
		skipDeviceAvailabilityCheck bool
		wantValid                   bool
		wantErr                     string
	}{
		{
			name:    "nil context",
			wantErr: "allocation context is nil",
		},
		{
			name:    "nil device request",
			ctx:     &allocate.AllocationContext{DeviceTopologyRegistry: registry},
			wantErr: "device request is nil",
		},
		{
			name: "nil resource request",
			ctx: &allocate.AllocationContext{
				DeviceReq:              &v1alpha1.DeviceRequest{},
				DeviceTopologyRegistry: registry,
			},
			wantErr: "resource request is nil",
		},
		{
			name: "nil topology registry",
			ctx: &allocate.AllocationContext{
				ResourceReq: &v1alpha1.ResourceRequest{},
				DeviceReq:   &v1alpha1.DeviceRequest{},
			},
			wantErr: "GPU topology registry is nil",
		},
		{
			name: "insufficient devices",
			ctx: &allocate.AllocationContext{
				ResourceReq:            &v1alpha1.ResourceRequest{},
				DeviceReq:              &v1alpha1.DeviceRequest{DeviceRequest: 2},
				DeviceTopologyRegistry: registry,
			},
			sortedDevices: []string{"device-0"},
			wantErr:       "not enough devices: need 2, have 1",
		},
		{
			name: "skip device availability check",
			ctx: &allocate.AllocationContext{
				ResourceReq:            &v1alpha1.ResourceRequest{},
				DeviceReq:              &v1alpha1.DeviceRequest{DeviceRequest: 2},
				DeviceTopologyRegistry: registry,
			},
			sortedDevices:               []string{"device-0"},
			skipDeviceAvailabilityCheck: true,
			wantValid:                   true,
		},
		{
			name: "valid context",
			ctx: &allocate.AllocationContext{
				ResourceReq:            &v1alpha1.ResourceRequest{},
				DeviceReq:              &v1alpha1.DeviceRequest{DeviceRequest: 1},
				DeviceTopologyRegistry: registry,
			},
			sortedDevices: []string{"device-0"},
			wantValid:     true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			valid, errMsg := IsBindingContextValid(
				tt.ctx,
				tt.sortedDevices,
				tt.skipDeviceAvailabilityCheck,
			)
			require.Equal(t, tt.wantValid, valid)
			require.Equal(t, tt.wantErr, errMsg)
		})
	}
}
