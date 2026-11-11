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
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
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

func TestValidatePodResizeSupport(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		discovery discovery.DiscoveryInterface
		wantErr   string
	}{
		{
			name: "native update is not implemented",
			discovery: &fakediscovery.FakeDiscovery{Fake: &k8stesting.Fake{
				Resources: []*metav1.APIResourceList{{
					GroupVersion: "v1",
					APIResources: []metav1.APIResource{{
						Name:  "pods/resize",
						Verbs: metav1.Verbs{"get", "update", "patch"},
					}},
				}},
			}},
			wantErr: "VPA is not implemented for native pod resize",
		},
		{
			name: "resize without update verb is supported",
			discovery: &fakediscovery.FakeDiscovery{Fake: &k8stesting.Fake{
				Resources: []*metav1.APIResourceList{{
					GroupVersion: "v1",
					APIResources: []metav1.APIResource{{
						Name:  "pods/resize",
						Verbs: metav1.Verbs{"get"},
					}},
				}},
			}},
		},
		{
			name: "core v1 without resize is supported",
			discovery: &fakediscovery.FakeDiscovery{Fake: &k8stesting.Fake{
				Resources: []*metav1.APIResourceList{{
					GroupVersion: "v1",
					APIResources: []metav1.APIResource{{Name: "pods"}},
				}},
			}},
		},
		{
			name:      "missing core v1 is supported",
			discovery: &fakediscovery.FakeDiscovery{Fake: &k8stesting.Fake{}},
		},
		{
			name: "other discovery error is returned",
			discovery: &failingDiscovery{err: apierrors.NewForbidden(
				schema.GroupResource{Resource: "resources"}, "", errors.New("forbidden"),
			)},
			wantErr: "discover core v1 resources: resources is forbidden: forbidden",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := validatePodResizeSupport(tt.discovery)
			if tt.wantErr != "" {
				assert.EqualError(t, err, tt.wantErr)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
