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

package compatibility

import (
	"context"
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

type countingKubernetesDiscovery struct {
	discovery.DiscoveryInterface
	calls []string
	errs  map[string]error
}

func (c *countingKubernetesDiscovery) ServerResourcesForGroupVersion(
	groupVersion string,
) (*metav1.APIResourceList, error) {
	c.calls = append(c.calls, groupVersion)
	if err := c.errs[groupVersion]; err != nil {
		return nil, err
	}
	return c.DiscoveryInterface.ServerResourcesForGroupVersion(groupVersion)
}

func TestKubernetesVersionResolverUsesCachedVersionWithoutDiscovery(t *testing.T) {
	t.Parallel()

	store := NewVersionStore(t.TempDir())
	config := testKubernetesVersionConfig()
	resolver := NewKubernetesVersionResolver(kubernetesDiscovery("v1beta1"), store, config)
	version, err := resolver.Get(context.Background())
	require.NoError(t, err)
	require.Equal(t, "policy/v1beta1", version)

	discoveryClient := &countingKubernetesDiscovery{
		DiscoveryInterface: kubernetesDiscovery("v1"),
	}
	resolver = NewKubernetesVersionResolver(discoveryClient, store, config)
	version, err = resolver.Get(context.Background())
	require.NoError(t, err)
	require.Equal(t, "policy/v1beta1", version)
	require.Empty(t, discoveryClient.calls)
}

func TestKubernetesVersionResolverRefreshBypassesCachedVersion(t *testing.T) {
	t.Parallel()

	store := NewVersionStore(t.TempDir())
	config := testKubernetesVersionConfig()
	resolver := NewKubernetesVersionResolver(kubernetesDiscovery("v1beta1"), store, config)
	_, err := resolver.Get(context.Background())
	require.NoError(t, err)

	resolver = NewKubernetesVersionResolver(kubernetesDiscovery("v1"), store, config)
	version, err := resolver.Refresh(context.Background())
	require.NoError(t, err)
	require.Equal(t, "policy/v1", version)
}

func TestKubernetesVersionResolverPrefersFirstCandidate(t *testing.T) {
	t.Parallel()

	resolver := NewKubernetesVersionResolver(
		endpointDiscovery(true, true), nil, endpointVersionConfig())
	version, err := resolver.Get(context.Background())
	require.NoError(t, err)
	require.Equal(t, "discovery.k8s.io/v1", version)
}

func TestKubernetesVersionResolverFallsBackToNextCandidate(t *testing.T) {
	t.Parallel()

	resolver := NewKubernetesVersionResolver(
		endpointDiscovery(false, true), nil, endpointVersionConfig())
	version, err := resolver.Get(context.Background())
	require.NoError(t, err)
	require.Equal(t, "v1", version)
}

func TestKubernetesVersionResolverDoesNotFallbackAfterDiscoveryError(t *testing.T) {
	t.Parallel()

	forbidden := apierrors.NewForbidden(
		schema.GroupResource{Group: "discovery.k8s.io", Resource: "endpointslices"},
		"",
		errors.New("forbidden"),
	)
	discoveryClient := &countingKubernetesDiscovery{
		DiscoveryInterface: endpointDiscovery(false, true),
		errs: map[string]error{
			"discovery.k8s.io/v1": forbidden,
		},
	}
	resolver := NewKubernetesVersionResolver(discoveryClient, nil, endpointVersionConfig())
	_, err := resolver.Get(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, forbidden)
	require.Equal(t, []string{"discovery.k8s.io/v1"}, discoveryClient.calls)
}

func TestKubernetesVersionResolverWithNilStoreAlwaysDiscovers(t *testing.T) {
	t.Parallel()

	discoveryClient := &countingKubernetesDiscovery{
		DiscoveryInterface: endpointDiscovery(true, true),
	}
	for i := 0; i < 2; i++ {
		resolver := NewKubernetesVersionResolver(discoveryClient, nil, endpointVersionConfig())
		version, err := resolver.Get(context.Background())
		require.NoError(t, err)
		require.Equal(t, "discovery.k8s.io/v1", version)
	}
	require.Equal(t,
		[]string{"discovery.k8s.io/v1", "discovery.k8s.io/v1"},
		discoveryClient.calls,
	)
}

func TestKubernetesVersionResolverRefreshReevaluatesCandidatePriority(t *testing.T) {
	t.Parallel()

	store := NewVersionStore(t.TempDir())
	config := endpointVersionConfig()
	resolver := NewKubernetesVersionResolver(endpointDiscovery(false, true), store, config)
	version, err := resolver.Get(context.Background())
	require.NoError(t, err)
	require.Equal(t, "v1", version)

	resolver = NewKubernetesVersionResolver(endpointDiscovery(true, true), store, config)
	version, err = resolver.Refresh(context.Background())
	require.NoError(t, err)
	require.Equal(t, "discovery.k8s.io/v1", version)

	discoveryClient := &countingKubernetesDiscovery{
		DiscoveryInterface: endpointDiscovery(false, true),
	}
	resolver = NewKubernetesVersionResolver(discoveryClient, store, config)
	version, err = resolver.Get(context.Background())
	require.NoError(t, err)
	require.Equal(t, "discovery.k8s.io/v1", version)
	require.Empty(t, discoveryClient.calls)
}

func TestKubernetesVersionResolverRejectsEmptyCandidateList(t *testing.T) {
	t.Parallel()

	discoveryClient := &countingKubernetesDiscovery{
		DiscoveryInterface: endpointDiscovery(true, true),
	}
	resolver := NewKubernetesVersionResolver(discoveryClient, nil, KubernetesVersionConfig{
		APIName: "Empty API",
	})
	_, err := resolver.Get(context.Background())
	require.EqualError(t, err, "Empty API has no Kubernetes API candidates")
	require.Empty(t, discoveryClient.calls)
}

func testKubernetesVersionConfig() KubernetesVersionConfig {
	return KubernetesVersionConfig{
		APIName:            "Eviction API",
		CheckpointFileName: "katalyst_eviction_api_version",
		Candidates: []KubernetesVersionCandidate{{
			Resource:               "pods/eviction",
			DiscoveryGroupVersions: []string{"v1"},
			SupportedVersions:      []string{"policy/v1", "policy/v1beta1"},
		}},
	}
}

func endpointVersionConfig() KubernetesVersionConfig {
	return KubernetesVersionConfig{
		APIName:            "ByteSD endpoint API",
		CheckpointFileName: "katalyst_bytesd_endpoint_api_version",
		Candidates: []KubernetesVersionCandidate{
			{
				Resource:               "endpointslices",
				DiscoveryGroupVersions: []string{"discovery.k8s.io/v1"},
				SupportedVersions:      []string{"discovery.k8s.io/v1"},
			},
			{
				Resource:               "endpoints",
				DiscoveryGroupVersions: []string{"v1"},
				SupportedVersions:      []string{"v1"},
			},
		},
	}
}

func kubernetesDiscovery(version string) *fakediscovery.FakeDiscovery {
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

func endpointDiscovery(endpointSlice, endpoints bool) *fakediscovery.FakeDiscovery {
	resources := make([]*metav1.APIResourceList, 0, 2)
	if endpointSlice {
		resources = append(resources, &metav1.APIResourceList{
			GroupVersion: "discovery.k8s.io/v1",
			APIResources: []metav1.APIResource{{
				Name:  "endpointslices",
				Verbs: metav1.Verbs{"list", "create", "update", "delete"},
			}},
		})
	}
	if endpoints {
		resources = append(resources, &metav1.APIResourceList{
			GroupVersion: "v1",
			APIResources: []metav1.APIResource{{
				Name:  "endpoints",
				Verbs: metav1.Verbs{"get", "create", "update"},
			}},
		})
	}
	return &fakediscovery.FakeDiscovery{Fake: &k8stesting.Fake{Resources: resources}}
}
