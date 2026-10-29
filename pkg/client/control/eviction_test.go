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

package control

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	policyv1 "k8s.io/api/policy/v1"
	policyv1beta1 "k8s.io/api/policy/v1beta1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	fakediscovery "k8s.io/client-go/discovery/fake"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

type errorDiscovery struct {
	discovery.DiscoveryInterface
	err error
}

func (e *errorDiscovery) ServerResourcesForGroupVersion(string) (*metav1.APIResourceList, error) {
	return nil, e.err
}

func TestRealPodEjectorUsesAdvertisedEvictionVersion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		version string
		assert  func(*testing.T, runtime.Object)
	}{
		{
			name:    "policy v1",
			version: "v1",
			assert: func(t *testing.T, object runtime.Object) {
				eviction, ok := object.(*policyv1.Eviction)
				require.True(t, ok)
				assertEvictionFields(t, eviction.TypeMeta, eviction.ObjectMeta, eviction.DeleteOptions)
			},
		},
		{
			name:    "policy v1beta1",
			version: "v1beta1",
			assert: func(t *testing.T, object runtime.Object) {
				eviction, ok := object.(*policyv1beta1.Eviction)
				require.True(t, ok)
				assertEvictionFields(t, eviction.TypeMeta, eviction.ObjectMeta, eviction.DeleteOptions)
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			client := fake.NewSimpleClientset()
			var captured runtime.Object
			client.PrependReactor("create", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
				assert.Equal(t, "eviction", action.GetSubresource())
				captured = action.(k8stesting.CreateAction).GetObject()
				return true, nil, nil
			})

			ejector, err := NewRealPodEjector(client, evictionDiscovery(tt.version))
			require.NoError(t, err)
			gracePeriod := int64(30)
			err = ejector.EvictPod(
				context.Background(),
				"default",
				"test-pod",
				metav1.DeleteOptions{
					GracePeriodSeconds: &gracePeriod,
					DryRun:             []string{metav1.DryRunAll},
				},
				map[string]string{"evicted-by": "katalyst"},
			)
			require.NoError(t, err)
			require.NotNil(t, captured)
			tt.assert(t, captured)
		})
	}
}

func TestNewRealPodEjectorRejectsUnsupportedAPI(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		discovery discovery.DiscoveryInterface
		wantError string
	}{
		{
			name:      "eviction resource missing",
			discovery: &fakediscovery.FakeDiscovery{Fake: &k8stesting.Fake{}},
			wantError: "discover Eviction API",
		},
		{
			name:      "advertised version unsupported",
			discovery: evictionDiscovery("v2"),
			wantError: `pods/eviction advertises unsupported request group/version "policy/v2"`,
		},
		{
			name: "discovery forbidden",
			discovery: &errorDiscovery{err: apierrors.NewForbidden(
				schema.GroupResource{Resource: "resources"}, "", errors.New("forbidden"))},
			wantError: "discover Eviction API",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ejector, err := NewRealPodEjector(fake.NewSimpleClientset(), tt.discovery)
			assert.Nil(t, ejector)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantError)
		})
	}
}

func evictionDiscovery(version string) *fakediscovery.FakeDiscovery {
	return &fakediscovery.FakeDiscovery{Fake: &k8stesting.Fake{
		Resources: []*metav1.APIResourceList{{
			GroupVersion: "v1",
			APIResources: []metav1.APIResource{{
				Name:       "pods/eviction",
				Namespaced: true,
				Kind:       "Eviction",
				Verbs:      metav1.Verbs{"create"},
				Group:      "policy",
				Version:    version,
			}},
		}},
	}}
}

func assertEvictionFields(
	t *testing.T,
	typeMeta metav1.TypeMeta,
	objectMeta metav1.ObjectMeta,
	deleteOptions *metav1.DeleteOptions,
) {
	t.Helper()

	assert.Equal(t, "Eviction", typeMeta.Kind)
	assert.Contains(t, []string{"policy/v1", "policy/v1beta1"}, typeMeta.APIVersion)
	assert.Equal(t, "default", objectMeta.Namespace)
	assert.Equal(t, "test-pod", objectMeta.Name)
	assert.Equal(t, map[string]string{"evicted-by": "katalyst"}, objectMeta.Annotations)
	require.NotNil(t, deleteOptions)
	require.NotNil(t, deleteOptions.GracePeriodSeconds)
	assert.Equal(t, int64(30), *deleteOptions.GracePeriodSeconds)
	assert.Equal(t, []string{metav1.DryRunAll}, deleteOptions.DryRun)
}
