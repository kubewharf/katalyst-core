//go:build linux
// +build linux

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

package cgroupid

import (
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
)

var (
	podFetcher        = makePodFetcher(makePodList())
	podUIDList        = []string{"uid-1", "uid-2", "uid-3", "uid-4"}
	containerIDList   = []string{"container-id-1", "container-id-2", "container-id-3", "container-id-4", "container-id-5", "container-id-6"}
	containerNameList = []string{"container-name-1", "container-name-2", "container-name-3", "container-name-4", "container-name-5", "container-name-6"}
	cgIDList          = []uint64{1, 2, 3, 4, 5, 6}
)

func TestGetCgroupIDForContainer(t *testing.T) {
	t.Parallel()

	cgroupIDManager := NewCgroupIDManager(podFetcher).(*cgroupIDManagerImpl)
	assert.NotNil(t, cgroupIDManager)

	cgroupIDManager.setCgroupID(podUIDList[0], containerIDList[0], cgIDList[0])
	cgroupIDManager.setCgroupID(podUIDList[1], containerIDList[1], cgIDList[1])

	tests := []struct {
		name        string
		podUID      string
		containerID string
		want        uint64
	}{
		{
			name:        "get cgID for container 1",
			podUID:      podUIDList[0],
			containerID: containerIDList[0],
			want:        cgIDList[0],
		},
		{
			name:        "get cgID for container 2",
			podUID:      podUIDList[1],
			containerID: containerIDList[1],
			want:        cgIDList[1],
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cgID, err := cgroupIDManager.GetCgroupIDForContainer(tt.podUID, tt.containerID)
			assert.NoError(t, err)
			assert.Equal(t, tt.want, cgID)
		})
	}
}

func TestListCgroupIDsForPod(t *testing.T) {
	t.Parallel()

	cgroupIDManager := NewCgroupIDManager(podFetcher).(*cgroupIDManagerImpl)
	assert.NotNil(t, cgroupIDManager)

	cgroupIDManager.setCgroupID(podUIDList[0], containerIDList[0], cgIDList[0])
	cgroupIDManager.setCgroupID(podUIDList[1], containerIDList[1], cgIDList[1])
	cgroupIDManager.setCgroupID(podUIDList[1], containerIDList[2], cgIDList[2])

	tests := []struct {
		name   string
		podUID string
		want   []uint64
	}{
		{
			name:   "get cgID for pod 1",
			podUID: podUIDList[0],
			want:   []uint64{cgIDList[0]},
		},
		{
			name:   "get cgID for pod 2",
			podUID: podUIDList[1],
			want:   []uint64{cgIDList[1], cgIDList[2]},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cgIDs, err := cgroupIDManager.ListCgroupIDsForPod(tt.podUID)
			assert.NoError(t, err)
			sort.Slice(tt.want, func(i, j int) bool { return tt.want[i] < tt.want[j] })
			sort.Slice(cgIDs, func(i, j int) bool { return cgIDs[i] < cgIDs[j] })
			assert.True(t, reflect.DeepEqual(tt.want, cgIDs))
		})
	}
}

func TestGetAbsentContainers(t *testing.T) {
	t.Parallel()

	cgroupIDManager := NewCgroupIDManager(podFetcher).(*cgroupIDManagerImpl)
	assert.NotNil(t, cgroupIDManager)

	cgroupIDManager.setCgroupID(podUIDList[0], containerIDList[0], cgIDList[0])
	cgroupIDManager.setCgroupID(podUIDList[1], containerIDList[1], cgIDList[1])
	cgroupIDManager.setCgroupID(podUIDList[1], containerIDList[2], cgIDList[2])

	tests := []struct {
		name    string
		podList []*v1.Pod
		want    map[string]sets.String
	}{
		{
			name:    "get absent containers",
			podList: makePodList(),
			want: map[string]sets.String{
				podUIDList[1]: sets.NewString(containerIDList[3]),
				podUIDList[2]: sets.NewString(containerIDList[4]),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotAbsentContainers := cgroupIDManager.getAbsentContainers(tt.podList)
			assert.Equal(t, len(tt.want), len(gotAbsentContainers))
			for wantPodUID, wantContainerSet := range tt.want {
				gotContainerSet, ok := gotAbsentContainers[wantPodUID]
				assert.True(t, ok)
				assert.True(t, wantContainerSet.Equal(gotContainerSet))
			}
		})
	}
}

func TestClearResidualPodsInCache(t *testing.T) {
	t.Parallel()

	cgroupIDManager := NewCgroupIDManager(podFetcher).(*cgroupIDManagerImpl)
	assert.NotNil(t, cgroupIDManager)

	cgroupIDManager.setCgroupID(podUIDList[0], containerIDList[0], cgIDList[0])
	cgroupIDManager.setCgroupID(podUIDList[1], containerIDList[1], cgIDList[1])
	cgroupIDManager.setCgroupID(podUIDList[1], containerIDList[2], cgIDList[2])
	cgroupIDManager.setCgroupID(podUIDList[3], containerIDList[5], cgIDList[5])

	tests := []struct {
		name    string
		podList []*v1.Pod
		want    map[string]map[string]uint64
	}{
		{
			name:    "clear residual pods in cache",
			podList: makePodList(),
			want: map[string]map[string]uint64{
				podUIDList[0]: {
					containerIDList[0]: cgIDList[0],
				},
				podUIDList[1]: {
					containerIDList[1]: cgIDList[1],
					containerIDList[2]: cgIDList[2],
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for i := 0; time.Duration(i)*cgroupIDManager.reconcilePeriod < maxResidualTime; i++ {
				cgroupIDManager.clearResidualPodsInCache(tt.podList)
			}

			assert.Equal(t, len(tt.want), len(cgroupIDManager.podCgroupIDCache))
			for wantPodUID, wantContainerMap := range tt.want {
				cgroupIDManager.Lock()
				gotContainerMap, ok := cgroupIDManager.podCgroupIDCache[wantPodUID]

				assert.True(t, ok)
				assert.Equal(t, len(wantContainerMap), len(gotContainerMap))
				for wantContainerID, wantCgID := range wantContainerMap {
					gotCgID, ok := wantContainerMap[wantContainerID]
					assert.True(t, ok)
					assert.Equal(t, wantCgID, gotCgID)
				}
				cgroupIDManager.Unlock()
			}
		})
	}
}

func makePodFetcher(podList []*v1.Pod) pod.PodFetcher {
	podFetcher := pod.PodFetcherStub{}
	if len(podList) > 0 {
		podFetcher.PodList = podList
	}
	return &podFetcher
}

func makePodList() []*v1.Pod {
	return []*v1.Pod{
		{
			ObjectMeta: metav1.ObjectMeta{
				UID: types.UID(podUIDList[0]),
			},
			Spec: v1.PodSpec{
				Containers: []v1.Container{
					{
						Name: containerNameList[0],
					},
				},
			},
			Status: v1.PodStatus{
				ContainerStatuses: []v1.ContainerStatus{
					{
						Name:        containerNameList[0],
						ContainerID: containerIDList[0],
					},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				UID: types.UID(podUIDList[1]),
			},
			Spec: v1.PodSpec{
				Containers: []v1.Container{
					{
						Name: containerNameList[1],
					},
					{
						Name: containerNameList[2],
					},
					{
						Name: containerNameList[3],
					},
				},
			},
			Status: v1.PodStatus{
				ContainerStatuses: []v1.ContainerStatus{
					{
						Name:        containerNameList[1],
						ContainerID: containerIDList[1],
					},
					{
						Name:        containerNameList[2],
						ContainerID: containerIDList[2],
					},
					{
						Name:        containerNameList[3],
						ContainerID: containerIDList[3],
					},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				UID: types.UID(podUIDList[2]),
			},
			Spec: v1.PodSpec{
				Containers: []v1.Container{
					{
						Name: containerNameList[4],
					},
				},
			},
			Status: v1.PodStatus{
				ContainerStatuses: []v1.ContainerStatus{
					{
						Name:        containerNameList[4],
						ContainerID: containerIDList[4],
					},
				},
			},
		},
	}
}
