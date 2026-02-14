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

package types

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func TestClonePodEntries(t *testing.T) {
	t.Parallel()

	ci := &ContainerInfo{
		PodUID:         "uid1",
		PodNamespace:   "ns1",
		PodName:        "pod1",
		ContainerName:  "c1",
		ContainerType:  1,
		ContainerIndex: 0,
		Labels:         map[string]string{"k1": "v1", "k2": "v2"},
		Annotations:    map[string]string{"k1": "v1", "k2": "v2"},
		QoSLevel:       consts.PodAnnotationQoSLevelSharedCores,
		CPURequest:     1,
		MemoryRequest:  2,
		RampUp:         false,
		OwnerPoolName:  "batch",
		TopologyAwareAssignments: map[int]machine.CPUSet{
			1: machine.NewCPUSet(1, 2),
		},
		OriginalTopologyAwareAssignments: map[int]machine.CPUSet{
			1: machine.NewCPUSet(1, 2),
		},
		RegionNames: sets.NewString("r1", "r2"),
	}

	podEntries := PodEntries{
		ci.PodUID: ContainerEntries{
			ci.ContainerName: ci,
		},
	}

	copyPodEntries := podEntries.Clone()

	assert.True(t, reflect.DeepEqual(copyPodEntries, podEntries))
}

func TestContainerInfo_GetResourcePackageName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		ci       *ContainerInfo
		expected string
	}{
		{
			name: "normal case",
			ci: &ContainerInfo{
				Annotations: map[string]string{
					consts.PodAnnotationResourcePackageKey: "pkg1",
				},
			},
			expected: "pkg1",
		},
		{
			name:     "nil container info",
			ci:       nil,
			expected: "",
		},
		{
			name: "nil annotations",
			ci: &ContainerInfo{
				Annotations: nil,
			},
			expected: "",
		},
		{
			name: "no resource package annotation",
			ci: &ContainerInfo{
				Annotations: map[string]string{
					"other": "val",
				},
			},
			expected: "",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.expected, tt.ci.GetResourcePackageName())
		})
	}
}
