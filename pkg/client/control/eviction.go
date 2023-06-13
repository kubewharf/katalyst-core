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

	policy "k8s.io/api/policy/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// PodEjector is used to evict Pods
type PodEjector interface {
	DeletePod(ctx context.Context, namespace, name string, opts metav1.DeleteOptions) error
	EvictPod(ctx context.Context, eviction *policy.Eviction) error
}

type DummyPodEjector struct{}

func (d DummyPodEjector) DeletePod(_ context.Context, _, _ string, _ metav1.DeleteOptions) error {
	return nil
}

func (d DummyPodEjector) EvictPod(_ context.Context, _ *policy.Eviction) error {
	return nil
}

type RealPodEjector struct {
	client kubernetes.Interface
}

func NewRealPodEjector(client kubernetes.Interface) *RealPodEjector {
	return &RealPodEjector{
		client: client,
	}
}

func (d *RealPodEjector) DeletePod(ctx context.Context, namespace, name string, opts metav1.DeleteOptions) error {
	return d.client.CoreV1().Pods(namespace).Delete(ctx, name, opts)
}

func (d *RealPodEjector) EvictPod(ctx context.Context, eviction *policy.Eviction) error {
	return d.client.PolicyV1beta1().Evictions(eviction.Namespace).Evict(ctx, eviction)
}

// RealPodEjectorWithMetric todo: implement with emitting metrics on updating
type RealPodEjectorWithMetric struct {
	client kubernetes.Interface
	RealPodUpdater
}
