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
	"fmt"

	policyv1 "k8s.io/api/policy/v1"
	policyv1beta1 "k8s.io/api/policy/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/kubernetes"
	policyv1client "k8s.io/client-go/kubernetes/typed/policy/v1"
	policyv1beta1client "k8s.io/client-go/kubernetes/typed/policy/v1beta1"

	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

const (
	evictionV1      = "policy/v1"
	evictionV1beta1 = "policy/v1beta1"
)

// PodEjector is used to evict Pods
type PodEjector interface {
	DeletePod(ctx context.Context, namespace, name string, opts metav1.DeleteOptions) error
	EvictPod(ctx context.Context, namespace, name string, opts metav1.DeleteOptions, annotations map[string]string) error
}

type DummyPodEjector struct{}

func (d DummyPodEjector) DeletePod(_ context.Context, _, _ string, _ metav1.DeleteOptions) error {
	return nil
}

func (d DummyPodEjector) EvictPod(
	_ context.Context,
	_, _ string,
	_ metav1.DeleteOptions,
	_ map[string]string,
) error {
	return nil
}

type evictionAdapter interface {
	Evict(ctx context.Context, namespace, name string, opts metav1.DeleteOptions, annotations map[string]string) error
}

type RealPodEjector struct {
	client          kubernetes.Interface
	evictionAdapter evictionAdapter
}

func NewRealPodEjector(
	client kubernetes.Interface,
	discoveryClient discovery.DiscoveryInterface,
) (PodEjector, error) {
	_, resource, err := native.FindSupportedAPIResource(
		discoveryClient, "pods/eviction", []string{"create"}, "v1")
	if err != nil {
		return nil, fmt.Errorf("discover Eviction API: %w", err)
	}

	groupVersion := schema.GroupVersion{
		Group:   resource.Group,
		Version: resource.Version,
	}.String()

	ejector := &RealPodEjector{client: client}
	switch groupVersion {
	case evictionV1:
		ejector.evictionAdapter = &evictionV1Adapter{client: client.PolicyV1()}
	case evictionV1beta1:
		ejector.evictionAdapter = &evictionV1beta1Adapter{client: client.PolicyV1beta1()}
	default:
		return nil, fmt.Errorf(
			"pods/eviction advertises unsupported request group/version %q", groupVersion)
	}
	return ejector, nil
}

func (d *RealPodEjector) DeletePod(ctx context.Context, namespace, name string, opts metav1.DeleteOptions) error {
	return d.client.CoreV1().Pods(namespace).Delete(ctx, name, opts)
}

func (d *RealPodEjector) EvictPod(
	ctx context.Context,
	namespace, name string,
	opts metav1.DeleteOptions,
	annotations map[string]string,
) error {
	return d.evictionAdapter.Evict(ctx, namespace, name, opts, annotations)
}

type evictionV1Adapter struct {
	client policyv1client.PolicyV1Interface
}

func (e *evictionV1Adapter) Evict(
	ctx context.Context,
	namespace, name string,
	opts metav1.DeleteOptions,
	annotations map[string]string,
) error {
	eviction := &policyv1.Eviction{
		TypeMeta: metav1.TypeMeta{
			APIVersion: evictionV1,
			Kind:       "Eviction",
		},
		ObjectMeta: metav1.ObjectMeta{
			Namespace:   namespace,
			Name:        name,
			Annotations: annotations,
		},
		DeleteOptions: &opts,
	}
	return e.client.Evictions(namespace).Evict(ctx, eviction)
}

type evictionV1beta1Adapter struct {
	client policyv1beta1client.PolicyV1beta1Interface
}

func (e *evictionV1beta1Adapter) Evict(
	ctx context.Context,
	namespace, name string,
	opts metav1.DeleteOptions,
	annotations map[string]string,
) error {
	eviction := &policyv1beta1.Eviction{
		TypeMeta: metav1.TypeMeta{
			APIVersion: evictionV1beta1,
			Kind:       "Eviction",
		},
		ObjectMeta: metav1.ObjectMeta{
			Namespace:   namespace,
			Name:        name,
			Annotations: annotations,
		},
		DeleteOptions: &opts,
	}
	return e.client.Evictions(namespace).Evict(ctx, eviction)
}

// RealPodEjectorWithMetric todo: implement with emitting metrics on updating
type RealPodEjectorWithMetric struct {
	client kubernetes.Interface
	RealPodUpdater
}
