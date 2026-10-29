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

package native

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	fakediscovery "k8s.io/client-go/discovery/fake"
	k8stesting "k8s.io/client-go/testing"
)

type failingDiscovery struct {
	discovery.DiscoveryInterface
	err error
}

func (f *failingDiscovery) ServerResourcesForGroupVersion(string) (*metav1.APIResourceList, error) {
	return nil, f.err
}

func TestFindSupportedGroupVersion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		resources []*metav1.APIResourceList
		want      string
		wantErr   string
	}{
		{
			name: "prefers first supported group version",
			resources: []*metav1.APIResourceList{
				hpaResourceList("autoscaling/v2", "get", "create", "patch"),
				hpaResourceList("autoscaling/v2beta2", "get", "create", "patch"),
			},
			want: "autoscaling/v2",
		},
		{
			name: "falls back when preferred group version is missing",
			resources: []*metav1.APIResourceList{
				hpaResourceList("autoscaling/v2beta2", "get", "create", "patch"),
			},
			want: "autoscaling/v2beta2",
		},
		{
			name: "falls back when preferred resource lacks a required verb",
			resources: []*metav1.APIResourceList{
				hpaResourceList("autoscaling/v2", "get"),
				hpaResourceList("autoscaling/v2beta2", "get", "create", "patch"),
			},
			want: "autoscaling/v2beta2",
		},
		{
			name:    "returns deterministic error when no candidate is supported",
			wantErr: `API resource "horizontalpodautoscalers" with verbs [get create patch] is not served in group versions [autoscaling/v2 autoscaling/v2beta2]`,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			client := &fakediscovery.FakeDiscovery{Fake: &k8stesting.Fake{
				Resources: tt.resources,
			}}
			got, err := FindSupportedGroupVersion(
				client,
				"horizontalpodautoscalers",
				[]string{"get", "create", "patch"},
				"autoscaling/v2",
				"autoscaling/v2beta2",
			)
			if tt.wantErr != "" {
				require.EqualError(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestFindSupportedAPIResourceReturnsSubresourceMetadata(t *testing.T) {
	t.Parallel()

	client := &fakediscovery.FakeDiscovery{Fake: &k8stesting.Fake{
		Resources: []*metav1.APIResourceList{{
			GroupVersion: "v1",
			APIResources: []metav1.APIResource{{
				Name:       "pods/eviction",
				Namespaced: true,
				Kind:       "Eviction",
				Verbs:      metav1.Verbs{"create"},
				Group:      "policy",
				Version:    "v1",
			}},
		}},
	}}

	groupVersion, resource, err := FindSupportedAPIResource(
		client, "pods/eviction", []string{"create"}, "v1")
	require.NoError(t, err)
	assert.Equal(t, "v1", groupVersion)
	require.NotNil(t, resource)
	assert.Equal(t, "policy", resource.Group)
	assert.Equal(t, "v1", resource.Version)
}

func TestFindSupportedAPIResourcePropagatesDiscoveryError(t *testing.T) {
	t.Parallel()

	forbidden := apierrors.NewForbidden(
		schema.GroupResource{Resource: "resources"}, "", errors.New("forbidden"))
	_, _, err := FindSupportedAPIResource(
		&failingDiscovery{err: forbidden}, "pods", []string{"get"}, "v1")
	require.Error(t, err)
	assert.ErrorIs(t, err, forbidden)
	assert.Contains(t, err.Error(), "discover API resources for v1")
}

func hpaResourceList(groupVersion string, verbs ...string) *metav1.APIResourceList {
	return &metav1.APIResourceList{
		GroupVersion: groupVersion,
		APIResources: []metav1.APIResource{{
			Name:  "horizontalpodautoscalers",
			Verbs: metav1.Verbs(verbs),
		}},
	}
}
