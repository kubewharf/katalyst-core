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

package commonstate

import (
	"testing"

	"github.com/stretchr/testify/require"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	cpuconsts "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/consts"
)

func TestAllocationMetaGetters(t *testing.T) {
	t.Parallel()

	t.Run("normal pod name", func(t *testing.T) {
		t.Parallel()
		meta := &AllocationMeta{PodName: "test-pod"}
		require.Equal(t, "test-pod", meta.GetPodName())
	})

	t.Run("empty pod name", func(t *testing.T) {
		t.Parallel()
		meta := &AllocationMeta{}
		require.Equal(t, "", meta.GetPodName())
	})

	t.Run("cloned meta", func(t *testing.T) {
		t.Parallel()
		original := &AllocationMeta{
			PodName: "original-pod",
			Labels:  map[string]string{"key": "value"},
		}
		clone := original.Clone()
		require.Equal(t, original.GetPodName(), clone.GetPodName())
	})

	// Test other getters
	t.Run("pod uid getter", func(t *testing.T) {
		t.Parallel()
		meta := &AllocationMeta{PodUid: "test-uid"}
		require.Equal(t, "test-uid", meta.GetPodUid())
	})

	t.Run("pod namespace getter", func(t *testing.T) {
		t.Parallel()
		meta := &AllocationMeta{PodNamespace: "test-ns"}
		require.Equal(t, "test-ns", meta.GetPodNamespace())
	})

	t.Run("container name getter", func(t *testing.T) {
		t.Parallel()
		meta := &AllocationMeta{ContainerName: "test-container"}
		require.Equal(t, "test-container", meta.GetContainerName())
	})

	// Test container type checks
	t.Run("main container check", func(t *testing.T) {
		t.Parallel()
		meta := &AllocationMeta{ContainerType: pluginapi.ContainerType_MAIN.String()}
		require.True(t, meta.CheckMainContainer())
		require.False(t, meta.CheckSideCar())
	})

	t.Run("sidecar container check", func(t *testing.T) {
		t.Parallel()
		meta := &AllocationMeta{ContainerType: pluginapi.ContainerType_SIDECAR.String()}
		require.True(t, meta.CheckSideCar())
		require.False(t, meta.CheckMainContainer())
	})

	// Enhanced clone test
	t.Run("deep clone verification", func(t *testing.T) {
		t.Parallel()
		original := &AllocationMeta{
			Annotations:    map[string]string{"key": "value"},
			QoSLevel:       "test-qos",
			ContainerIndex: 1,
		}
		clone := original.Clone()
		clone.Annotations["key"] = "modified"
		clone.QoSLevel = "changed"
		clone.ContainerIndex = 2

		require.Equal(t, "value", original.Annotations["key"])
		require.Equal(t, "test-qos", original.QoSLevel)
		require.Equal(t, uint64(1), original.ContainerIndex)
	})

	// Pool name tests
	t.Run("pool name with owner", func(t *testing.T) {
		t.Parallel()
		meta := &AllocationMeta{OwnerPoolName: "test-pool"}
		require.Equal(t, "test-pool", meta.GetPoolName())
	})

	t.Run("pool name from qos", func(t *testing.T) {
		t.Parallel()
		meta := &AllocationMeta{QoSLevel: "shared_cores"}
		require.Contains(t, meta.GetPoolName(), "share")
	})

	t.Run("pod role getter", func(t *testing.T) {
		t.Parallel()
		meta := &AllocationMeta{PodRole: "test-role"}
		require.Equal(t, "test-role", meta.GetPodRole())
	})

	t.Run("pod type getter", func(t *testing.T) {
		t.Parallel()
		meta := &AllocationMeta{PodType: "test-type"}
		require.Equal(t, "test-type", meta.GetPodType())
	})

	t.Run("labels getter", func(t *testing.T) {
		t.Parallel()
		meta := &AllocationMeta{Labels: map[string]string{"key": "value"}}
		require.Equal(t, "value", meta.GetLabels()["key"])
	})

	t.Run("annotations getter", func(t *testing.T) {
		t.Parallel()
		meta := &AllocationMeta{Annotations: map[string]string{"anno": "data"}}
		require.Equal(t, "data", meta.GetAnnotations()["anno"])
	})

	t.Run("qos level getter", func(t *testing.T) {
		t.Parallel()
		meta := &AllocationMeta{QoSLevel: "dedicated"}
		require.Equal(t, "dedicated", meta.GetQoSLevel())
	})

	t.Run("check dedicated cores", func(t *testing.T) {
		t.Parallel()
		meta := &AllocationMeta{QoSLevel: consts.PodAnnotationQoSLevelDedicatedCores}
		require.True(t, meta.CheckDedicated())
	})

	t.Run("check shared cores", func(t *testing.T) {
		t.Parallel()
		meta := &AllocationMeta{QoSLevel: consts.PodAnnotationQoSLevelSharedCores}
		require.True(t, meta.CheckShared())
	})

	t.Run("check numa binding", func(t *testing.T) {
		t.Parallel()
		meta := &AllocationMeta{
			Annotations: map[string]string{
				consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
			},
		}
		require.True(t, meta.CheckNUMABinding())
	})

	t.Run("check actual numa binding", func(t *testing.T) {
		t.Parallel()
		meta := &AllocationMeta{
			Annotations: map[string]string{
				cpuconsts.CPUStateAnnotationKeyNUMAHint: "0",
			},
		}
		require.True(t, meta.CheckActualNUMABinding())
	})
}
