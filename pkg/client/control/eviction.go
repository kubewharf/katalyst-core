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
	"fmt"
	"sync"

	policyv1 "k8s.io/api/policy/v1"
	policyv1beta1 "k8s.io/api/policy/v1beta1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	policyv1client "k8s.io/client-go/kubernetes/typed/policy/v1"
	policyv1beta1client "k8s.io/client-go/kubernetes/typed/policy/v1beta1"

	"github.com/kubewharf/katalyst-core/pkg/util/compatibility"
)

const (
	evictionV1                        = "policy/v1"
	evictionV1beta1                   = "policy/v1beta1"
	evictionVersionCheckpointFileName = "katalyst_eviction_api_version"
)

func EvictionVersionConfig() compatibility.KubernetesVersionConfig {
	return compatibility.KubernetesVersionConfig{
		APIName:            "Eviction API",
		CheckpointFileName: evictionVersionCheckpointFileName,
		Candidates: []compatibility.KubernetesVersionCandidate{{
			Resource:               "pods/eviction",
			DiscoveryGroupVersions: []string{"v1"},
			SupportedVersions:      []string{evictionV1, evictionV1beta1},
		}},
	}
}

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
	versionResolver compatibility.VersionResolver
	mutex           sync.Mutex
}

func NewRealPodEjector(
	client kubernetes.Interface,
	versionResolver compatibility.VersionResolver,
) (PodEjector, error) {
	if versionResolver == nil {
		return nil, fmt.Errorf("eviction API version resolver is nil")
	}

	return &RealPodEjector{
		client:          client,
		versionResolver: versionResolver,
	}, nil
}

func (d *RealPodEjector) newAdapter(groupVersion string) evictionAdapter {
	switch groupVersion {
	case evictionV1:
		return &evictionV1Adapter{client: d.client.PolicyV1()}
	case evictionV1beta1:
		return &evictionV1beta1Adapter{client: d.client.PolicyV1beta1()}
	default:
		return nil
	}
}

func (d *RealPodEjector) getAdapter(ctx context.Context) (evictionAdapter, error) {
	d.mutex.Lock()
	defer d.mutex.Unlock()

	if d.evictionAdapter != nil {
		return d.evictionAdapter, nil
	}

	version, err := d.versionResolver.Get(ctx)
	if err != nil {
		return nil, err
	}
	adapter := d.newAdapter(version)
	if adapter == nil {
		return nil, fmt.Errorf("unsupported eviction API version %q", version)
	}
	d.evictionAdapter = adapter
	return adapter, nil
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
	adapter, err := d.getAdapter(ctx)
	if err != nil {
		return err
	}

	err = adapter.Evict(ctx, namespace, name, opts, annotations)
	if !isEvictionAPIVersionUnavailable(err) {
		return err
	}

	d.mutex.Lock()
	defer d.mutex.Unlock()

	version, err := d.versionResolver.Refresh(ctx)
	if err != nil {
		return err
	}
	adapter = d.newAdapter(version)
	if adapter == nil {
		return fmt.Errorf("unsupported eviction API version %q", version)
	}
	d.evictionAdapter = adapter
	return d.evictionAdapter.Evict(ctx, namespace, name, opts, annotations)
}

func isEvictionAPIVersionUnavailable(err error) bool {
	if !apierrors.IsNotFound(err) {
		return false
	}

	var statusErr *apierrors.StatusError
	if !errors.As(err, &statusErr) || statusErr.ErrStatus.Details == nil {
		return true
	}
	details := statusErr.ErrStatus.Details
	return details.Name == "" && details.Group == "policy"
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
