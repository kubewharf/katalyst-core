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
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	fakediscovery "k8s.io/client-go/discovery/fake"
	"k8s.io/client-go/kubernetes/fake"
	coretesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/events"
	critesting "k8s.io/cri-api/pkg/apis/testing"

	katalyst_base "github.com/kubewharf/katalyst-core/cmd/base"
	"github.com/kubewharf/katalyst-core/pkg/config"
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
		fakeRuntimeService.Called = make([]string, 0)
		evictErr := containerKiller.Evict(context.TODO(), tt.pod, 0, "test", "test")
		if tt.wantStopFailed {
			require.Error(t, evictErr)
		} else {
			require.NoError(t, evictErr)
			require.Equal(t, tt.wantKillContainerCount, len(fakeRuntimeService.GetCalls()))
		}
	}
}

func TestEvictionAPIKiller_buildEvictionExplicitTriggerAnnotations(t *testing.T) {
	t.Parallel()

	newConf := func(key, value string) *config.Configuration {
		c := config.NewConfiguration()
		c.GenericEvictionConfiguration.EvictionExplicitTriggerAnnotationKey = key
		c.GenericEvictionConfiguration.EvictionExplicitTriggerAnnotationValue = value
		return c
	}
	newConfWithNilGenericEviction := func() *config.Configuration {
		c := config.NewConfiguration()
		c.GenericEvictionConfiguration = nil
		return c
	}

	tests := []struct {
		name string
		conf *config.Configuration
		want map[string]string
	}{
		{
			name: "nil conf returns nil",
			conf: nil,
			want: nil,
		},
		{
			name: "nil generic eviction config returns nil",
			conf: newConfWithNilGenericEviction(),
			want: nil,
		},
		{
			name: "empty trigger key disables rule",
			conf: newConf("", "true"),
			want: nil,
		},
		{
			name: "empty trigger value disables rule",
			conf: newConf("evicted-by", ""),
			want: nil,
		},
		{
			name: "configured key and value are stamped",
			conf: newConf("evicted-by", "memory-pressure"),
			want: map[string]string{"evicted-by": "memory-pressure"},
		},
		{
			name: "pod annotations do not affect stamping",
			conf: newConf("evicted-by", "katalyst"),
			want: map[string]string{"evicted-by": "katalyst"},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := &EvictionAPIKiller{conf: tt.conf}
			got := e.buildEvictionExplicitTriggerAnnotations()
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestEvictionAPIKiller_EvictStampsExplicitTriggerAnnotation(t *testing.T) {
	t.Parallel()

	type tcase struct {
		name  string
		key   string
		value string
		want  map[string]string
	}
	cases := []tcase{
		{
			name:  "configured key and value are stamped",
			key:   "evicted-by",
			value: "katalyst",
			want:  map[string]string{"evicted-by": "katalyst"},
		},
		{
			name:  "empty key results in no annotation",
			key:   "",
			value: "katalyst",
			want:  nil,
		},
		{
			name:  "empty value results in no annotation",
			key:   "evicted-by",
			value: "",
			want:  nil,
		},
	}

	for _, tt := range cases {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			pod := &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "pod-1",
					Namespace:   "ns-1",
					UID:         "uid-1",
					Annotations: map[string]string{"shield": "strict"},
				},
			}

			ctx, err := katalyst_base.GenerateFakeGenericContext([]runtime.Object{pod})
			require.NoError(t, err)
			fakeKube := ctx.Client.KubeClient.(*fake.Clientset)
			fakeKube.Discovery().(*fakediscovery.FakeDiscovery).Resources = []*metav1.APIResourceList{{
				GroupVersion: "v1",
				APIResources: []metav1.APIResource{{
					Name:       "pods/eviction",
					Namespaced: true,
					Kind:       "Eviction",
					Verbs:      metav1.Verbs{"create"},
					Group:      "policy",
					Version:    "v1",
				}},
			}}

			var captured *policyv1.Eviction
			fakeKube.PrependReactor("create", "pods", func(action coretesting.Action) (bool, runtime.Object, error) {
				if action.GetSubresource() != "eviction" {
					return false, nil, nil
				}
				captured = action.(coretesting.CreateAction).GetObject().(*policyv1.Eviction)
				// Simulate the API server actually deleting the pod so that
				// waitForDeleted in evict() returns promptly.
				_ = fakeKube.Tracker().Delete(
					v1.SchemeGroupVersion.WithResource("pods"),
					captured.Namespace, captured.Name,
				)
				return true, nil, nil
			})

			conf := config.NewConfiguration()
			conf.GenericEvictionConfiguration.EvictionExplicitTriggerAnnotationKey = tt.key
			conf.GenericEvictionConfiguration.EvictionExplicitTriggerAnnotationValue = tt.value

			killer, err := NewEvictionAPIKiller(conf, ctx.Client.KubeClient, &events.FakeRecorder{}, metrics.DummyMetrics{})
			require.NoError(t, err)

			require.NoError(t, killer.Evict(context.Background(), pod, 0, "test-api", "test"))

			require.NotNil(t, captured)
			assert.Equal(t, tt.want, captured.ObjectMeta.Annotations)
			require.NotNil(t, captured.DeleteOptions)
			require.NotNil(t, captured.DeleteOptions.GracePeriodSeconds)
			assert.Equal(t, int64(0), *captured.DeleteOptions.GracePeriodSeconds)
		})
	}
}
