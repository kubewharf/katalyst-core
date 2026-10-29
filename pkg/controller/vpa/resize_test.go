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

package vpa

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	core "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	fakediscovery "k8s.io/client-go/discovery/fake"
	k8stesting "k8s.io/client-go/testing"

	apis "github.com/kubewharf/katalyst-api/pkg/apis/autoscaling/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/client/control"
)

type failingDiscovery struct {
	discovery.DiscoveryInterface
	err error
}

func (f *failingDiscovery) ServerResourcesForGroupVersion(string) (*metav1.APIResourceList, error) {
	return nil, f.err
}

type recordingPodUpdater struct {
	control.DummyPodUpdater
	resizedPod *core.Pod
	err        error
}

func (r *recordingPodUpdater) UpdatePodResize(_ context.Context, pod *core.Pod, _ metav1.UpdateOptions) (*core.Pod, error) {
	r.resizedPod = pod.DeepCopy()
	return r.resizedPod, r.err
}

func TestDiscoverPodResizeMode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		discovery discovery.DiscoveryInterface
		want      podResizeMode
		wantErr   bool
	}{
		{
			name: "native update is supported",
			discovery: &fakediscovery.FakeDiscovery{Fake: &k8stesting.Fake{
				Resources: []*metav1.APIResourceList{{
					GroupVersion: "v1",
					APIResources: []metav1.APIResource{{
						Name:  "pods/resize",
						Verbs: metav1.Verbs{"get", "update", "patch"},
					}},
				}},
			}},
			want: podResizeModeNative,
		},
		{
			name: "resize without update verb uses legacy",
			discovery: &fakediscovery.FakeDiscovery{Fake: &k8stesting.Fake{
				Resources: []*metav1.APIResourceList{{
					GroupVersion: "v1",
					APIResources: []metav1.APIResource{{
						Name:  "pods/resize",
						Verbs: metav1.Verbs{"get"},
					}},
				}},
			}},
			want: podResizeModeLegacy,
		},
		{
			name: "core v1 without resize uses legacy",
			discovery: &fakediscovery.FakeDiscovery{Fake: &k8stesting.Fake{
				Resources: []*metav1.APIResourceList{{
					GroupVersion: "v1",
					APIResources: []metav1.APIResource{{Name: "pods"}},
				}},
			}},
			want: podResizeModeLegacy,
		},
		{
			name:      "missing core v1 uses legacy",
			discovery: &fakediscovery.FakeDiscovery{Fake: &k8stesting.Fake{}},
			want:      podResizeModeLegacy,
		},
		{
			name: "other discovery error is returned",
			discovery: &failingDiscovery{err: apierrors.NewForbidden(
				schema.GroupResource{Resource: "resources"}, "", errors.New("forbidden"),
			)},
			want:    podResizeModeLegacy,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := discoverPodResizeMode(tt.discovery)
			assert.Equal(t, tt.wantErr, err != nil)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestResizePodNative(t *testing.T) {
	t.Parallel()

	pod := &core.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "pod", Namespace: "default"},
		Spec: core.PodSpec{
			Containers: []core.Container{
				{
					Name: "target",
					Resources: core.ResourceRequirements{
						Requests: core.ResourceList{
							core.ResourceCPU:             resource.MustParse("2"),
							core.ResourceMemory:          resource.MustParse("2Gi"),
							core.ResourceName("example"): resource.MustParse("1"),
						},
					},
				},
				{
					Name: "untouched",
					Resources: core.ResourceRequirements{
						Requests: core.ResourceList{core.ResourceCPU: resource.MustParse("500m")},
					},
					ResizePolicy: []core.ContainerResizePolicy{{
						ResourceName:  core.ResourceCPU,
						RestartPolicy: core.RestartContainer,
					}},
				},
			},
		},
	}

	updater := &recordingPodUpdater{}
	controller := &VPAController{ctx: context.Background(), podUpdater: updater}
	err := controller.resizePodNative(
		pod,
		map[string]core.ResourceRequirements{
			"target": {
				Requests: core.ResourceList{
					core.ResourceCPU:    resource.MustParse("1"),
					core.ResourceMemory: resource.MustParse("1Gi"),
				},
			},
		},
		map[string]apis.ContainerResourcePolicy{
			"target": {ResourceResizePolicy: apis.ResourceResizePolicyRestart},
		},
	)

	assert.NoError(t, err)
	if assert.NotNil(t, updater.resizedPod) {
		target := updater.resizedPod.Spec.Containers[0]
		assert.Equal(t, resource.MustParse("1"), target.Resources.Requests[core.ResourceCPU])
		assert.Equal(t, resource.MustParse("1Gi"), target.Resources.Requests[core.ResourceMemory])
		assert.Equal(t, resource.MustParse("1"), target.Resources.Requests[core.ResourceName("example")])
		assert.Equal(t, []core.ContainerResizePolicy{
			{ResourceName: core.ResourceCPU, RestartPolicy: core.RestartContainer},
			{ResourceName: core.ResourceMemory, RestartPolicy: core.RestartContainer},
		}, target.ResizePolicy)
		assert.Equal(t, pod.Spec.Containers[1], updater.resizedPod.Spec.Containers[1])
	}
	assert.Empty(t, pod.Spec.Containers[0].ResizePolicy)
}

func TestResizePodNativePolicyOnlyChange(t *testing.T) {
	t.Parallel()

	pod := &core.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "pod", Namespace: "default"},
		Spec: core.PodSpec{Containers: []core.Container{{
			Name: "target",
			Resources: core.ResourceRequirements{
				Requests: core.ResourceList{core.ResourceCPU: resource.MustParse("1")},
			},
		}}},
	}
	updater := &recordingPodUpdater{}
	controller := &VPAController{ctx: context.Background(), podUpdater: updater}

	err := controller.resizePodNative(
		pod,
		map[string]core.ResourceRequirements{
			"target": {Requests: core.ResourceList{core.ResourceCPU: resource.MustParse("1")}},
		},
		map[string]apis.ContainerResourcePolicy{
			"target": {ResourceResizePolicy: apis.ResourceResizePolicyNone},
		},
	)

	assert.NoError(t, err)
	if assert.NotNil(t, updater.resizedPod) {
		assert.Equal(t, []core.ContainerResizePolicy{
			{ResourceName: core.ResourceCPU, RestartPolicy: core.NotRequired},
			{ResourceName: core.ResourceMemory, RestartPolicy: core.NotRequired},
		}, updater.resizedPod.Spec.Containers[0].ResizePolicy)
	}
}

func TestResizePodNativeNoChange(t *testing.T) {
	t.Parallel()

	resources := core.ResourceRequirements{
		Requests: core.ResourceList{core.ResourceCPU: resource.MustParse("1")},
	}
	policies := []core.ContainerResizePolicy{
		{ResourceName: core.ResourceCPU, RestartPolicy: core.NotRequired},
		{ResourceName: core.ResourceMemory, RestartPolicy: core.NotRequired},
	}
	pod := &core.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "pod", Namespace: "default"},
		Spec: core.PodSpec{Containers: []core.Container{{
			Name:         "target",
			Resources:    resources,
			ResizePolicy: policies,
		}}},
	}
	updater := &recordingPodUpdater{}
	controller := &VPAController{ctx: context.Background(), podUpdater: updater}

	err := controller.resizePodNative(
		pod,
		map[string]core.ResourceRequirements{"target": resources},
		map[string]apis.ContainerResourcePolicy{
			"target": {ResourceResizePolicy: apis.ResourceResizePolicyNone},
		},
	)

	assert.NoError(t, err)
	assert.Nil(t, updater.resizedPod)
}

func TestResizePodNativeReturnsUpdaterError(t *testing.T) {
	t.Parallel()

	expectedErr := errors.New("resize failed")
	updater := &recordingPodUpdater{err: expectedErr}
	controller := &VPAController{ctx: context.Background(), podUpdater: updater}
	pod := &core.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "pod", Namespace: "default"},
		Spec: core.PodSpec{Containers: []core.Container{{
			Name: "target",
			Resources: core.ResourceRequirements{
				Requests: core.ResourceList{core.ResourceCPU: resource.MustParse("2")},
			},
		}}},
	}

	err := controller.resizePodNative(
		pod,
		map[string]core.ResourceRequirements{
			"target": {Requests: core.ResourceList{core.ResourceCPU: resource.MustParse("1")}},
		},
		nil,
	)

	assert.ErrorIs(t, err, expectedErr)
	assert.Empty(t, pod.Annotations)
}
