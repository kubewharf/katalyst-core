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

package dynamicpolicy

import (
	"testing"

	"github.com/stretchr/testify/assert"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"
)

func TestPopulateHintsByAlreadyExistedNUMABindingResult(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		req           *pluginapi.ResourceRequest
		hints         *pluginapi.ListOfTopologyHints
		wantHints     *pluginapi.ListOfTopologyHints
		expectedError bool
	}{
		{
			name: "empty result",
			req: &pluginapi.ResourceRequest{
				PodNamespace:  "test-namespace",
				PodName:       "test-pod",
				ContainerName: "test-container",
			},
			hints: &pluginapi.ListOfTopologyHints{
				Hints: []*pluginapi.TopologyHint{
					{Nodes: []uint64{0}, Preferred: false},
					{Nodes: []uint64{1}, Preferred: false},
				},
			},
			wantHints: &pluginapi.ListOfTopologyHints{
				Hints: []*pluginapi.TopologyHint{
					{Nodes: []uint64{0}, Preferred: false},
					{Nodes: []uint64{1}, Preferred: false},
				},
			},
			expectedError: false,
		},
		{
			name: "matching result",
			req: &pluginapi.ResourceRequest{
				PodNamespace:  "test-namespace",
				PodName:       "test-pod",
				ContainerName: "test-container",
				Annotations: map[string]string{
					"numa_binding": "0",
				},
			},
			hints: &pluginapi.ListOfTopologyHints{
				Hints: []*pluginapi.TopologyHint{
					{Nodes: []uint64{0}, Preferred: false},
					{Nodes: []uint64{1}, Preferred: false},
				},
			},
			wantHints: &pluginapi.ListOfTopologyHints{
				Hints: []*pluginapi.TopologyHint{
					{Nodes: []uint64{0}, Preferred: true},
					{Nodes: []uint64{1}, Preferred: false},
				},
			},
			expectedError: false,
		},
		{
			name: "non-matching result",
			req: &pluginapi.ResourceRequest{
				PodNamespace:  "test-namespace",
				PodName:       "test-pod",
				ContainerName: "test-container",
				Annotations: map[string]string{
					"numa_binding": "2",
				},
			},
			hints: &pluginapi.ListOfTopologyHints{
				Hints: []*pluginapi.TopologyHint{
					{Nodes: []uint64{0}, Preferred: false},
					{Nodes: []uint64{1}, Preferred: false},
				},
			},
			wantHints: &pluginapi.ListOfTopologyHints{
				Hints: []*pluginapi.TopologyHint{
					{Nodes: []uint64{0}, Preferred: false},
					{Nodes: []uint64{1}, Preferred: false},
				},
			},
			expectedError: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := &DynamicPolicy{
				sharedCoresNUMABindingResultAnnotationKey: "numa_binding",
			}

			err := p.populateHintsByAlreadyExistedNUMABindingResult(tt.req, tt.hints)
			if tt.expectedError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}

			assert.Equal(t, tt.wantHints, tt.hints)
		})
	}
}
