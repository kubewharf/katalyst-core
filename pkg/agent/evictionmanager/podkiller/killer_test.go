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

package podkiller

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"
	critesting "k8s.io/cri-api/pkg/apis/testing"

	katalyst_base "github.com/kubewharf/katalyst-core/cmd/base"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

func TestEvictionQueue(t *testing.T) {
	t.Parallel()

	pods := []*v1.Pod{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "pod-1"},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "pod-2"},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "pod-3"},
		},
	}

	ctx, err := katalyst_base.GenerateFakeGenericContext([]runtime.Object{pods[0], pods[1]})
	require.NoError(t, err)

	deleteKiller, BuildErr := NewDeletionAPIKiller(nil, ctx.Client.KubeClient, &events.FakeRecorder{}, metrics.DummyMetrics{})
	require.NoError(t, BuildErr)

	err = deleteKiller.Evict(context.Background(), pods[0], 0, "test-api", "test")
	require.NoError(t, err)

	err = deleteKiller.Evict(context.Background(), pods[0], 0, "test-api", "test")
	require.NoError(t, err)

	err = deleteKiller.Evict(context.Background(), pods[1], 0, "test-api", "test")
	require.NoError(t, err)

	err = evict(ctx.Client.KubeClient, &events.FakeRecorder{}, metrics.DummyMetrics{}, pods[2],
		0, "test-api", "test", func(_ *v1.Pod, gracePeriod int64) error {
			return fmt.Errorf("test")
		})
	require.ErrorContains(t, err, "test")
}

func TestContainerKiller_Evict(t *testing.T) {
	t.Parallel()

	fakeRuntimeService := critesting.NewFakeRuntimeService()
	containerKiller := &ContainerKiller{
		containerManager: fakeRuntimeService,
		recorder:         events.NewFakeRecorder(5),
		emitter:          metrics.DummyMetrics{},
	}
	containerKiller.containerManager = fakeRuntimeService
	assert.Equal(t, consts.KillerNameContainerKiller, containerKiller.Name())

	fakeRuntimeService.Containers["container-01"] = &critesting.FakeContainer{}
	fakeRuntimeService.Containers["container-02"] = &critesting.FakeContainer{}

	tests := []struct {
		name                   string
		pod                    *v1.Pod
		wantKillContainerCount int
		wantStopFailed         bool
	}{
		{
			name: "1 container",
			pod: &v1.Pod{
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Name: "container-01",
						},
					},
				},
				Status: v1.PodStatus{
					ContainerStatuses: []v1.ContainerStatus{
						{
							Name:        "container-01",
							ContainerID: "containerd://container-01",
						},
					},
				},
			},
			wantKillContainerCount: 1,
			wantStopFailed:         false,
		},
		{
			name: "2 containers",
			pod: &v1.Pod{
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Name: "container-01",
						},
						{
							Name: "container-02",
						},
					},
				},
				Status: v1.PodStatus{
					ContainerStatuses: []v1.ContainerStatus{
						{
							Name:        "container-01",
							ContainerID: "containerd://container-01",
						},
						{
							Name:        "container-02",
							ContainerID: "containerd://container-02",
						},
					},
				},
			},
			wantKillContainerCount: 2,
			wantStopFailed:         false,
		},
		{
			name: "1 container but has error",
			pod: &v1.Pod{
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Name: "container-03",
						},
					},
				},
				Status: v1.PodStatus{
					ContainerStatuses: []v1.ContainerStatus{
						{
							Name:        "container-03",
							ContainerID: "containerd://container-03",
						},
					},
				},
			},
			wantStopFailed: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fakeRuntimeService.Called = make([]string, 0)
			evictErr := containerKiller.Evict(context.TODO(), tt.pod, 0, "test", "test")
			if tt.wantStopFailed {
				require.Error(t, evictErr)
			} else {
				require.NoError(t, evictErr)
				require.Equal(t, tt.wantKillContainerCount, len(fakeRuntimeService.GetCalls()))
			}
		})
	}
}
