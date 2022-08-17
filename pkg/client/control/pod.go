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
	"encoding/json"
	"fmt"

	core "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/strategicpatch"
	"k8s.io/client-go/kubernetes"
	statusutil "k8s.io/kubernetes/pkg/util/pod"

	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

// PodUpdater is used to update Pod
type PodUpdater interface {
	UpdatePod(ctx context.Context, pod *core.Pod, opts metav1.UpdateOptions) (*core.Pod, error)
	UpdatePodStatus(ctx context.Context, pod *core.Pod, opts metav1.UpdateOptions) (*core.Pod, error)
	PatchPod(ctx context.Context, oldPod, newPod *core.Pod) error
	PatchPodStatus(ctx context.Context, oldPod, newPod *core.Pod) error
}

type DummyPodUpdater struct{}

func (d *DummyPodUpdater) UpdatePod(_ context.Context, _ *core.Pod, _ metav1.UpdateOptions) (*core.Pod, error) {
	return nil, nil
}

func (d *DummyPodUpdater) PatchPod(_ context.Context, _, _ *core.Pod) error {
	return nil
}

func (d *DummyPodUpdater) UpdatePodStatus(_ context.Context, _ *core.Pod, _ metav1.UpdateOptions) (*core.Pod, error) {
	return nil, nil
}

func (d *DummyPodUpdater) PatchPodStatus(_ context.Context, _, _ *core.Pod) error {
	return nil
}

type RealPodUpdater struct {
	client kubernetes.Interface
}

func NewRealPodUpdater(client kubernetes.Interface) *RealPodUpdater {
	return &RealPodUpdater{
		client: client,
	}
}

func (r *RealPodUpdater) UpdatePod(ctx context.Context, pod *core.Pod, opts metav1.UpdateOptions) (*core.Pod, error) {
	if pod == nil {
		return nil, fmt.Errorf("can't update a nil Pod")
	}

	return r.client.CoreV1().Pods(pod.Namespace).Update(ctx, pod, opts)
}

func (r *RealPodUpdater) PatchPod(ctx context.Context, oldPod, newPod *core.Pod) error {
	if oldPod == nil || newPod == nil {
		return fmt.Errorf("can't update a nil Pod")
	}

	oldData, err := json.Marshal(oldPod)
	if err != nil {
		return err
	}

	newData, err := json.Marshal(newPod)
	if err != nil {
		return err
	}

	patchBytes, err := strategicpatch.CreateTwoWayMergePatch(oldData, newData, &core.Pod{})
	if err != nil {
		return fmt.Errorf("failed to create merge patch for pod %q/%q: %v", oldPod.Namespace, oldPod.Name, err)
	} else if general.JsonPathEmpty(patchBytes) {
		return nil
	}

	_, err = r.client.CoreV1().Pods(oldPod.Namespace).Patch(ctx, oldPod.Name, types.StrategicMergePatchType, patchBytes, metav1.PatchOptions{})
	return err
}

func (r *RealPodUpdater) UpdatePodStatus(ctx context.Context, pod *core.Pod, opts metav1.UpdateOptions) (*core.Pod, error) {
	if pod == nil {
		return nil, fmt.Errorf("can't update a nil Pod's status")
	}

	return r.client.CoreV1().Pods(pod.Namespace).UpdateStatus(ctx, pod, opts)
}

func (r *RealPodUpdater) PatchPodStatus(_ context.Context, oldPod, newPod *core.Pod) error {
	_, _, _, err := statusutil.PatchPodStatus(r.client, oldPod.Namespace, oldPod.Name, oldPod.UID, oldPod.Status, newPod.Status)
	return err
}

// RealPodUpdaterWithMetric todo: implement with emitting metrics on updating
type RealPodUpdaterWithMetric struct {
	client kubernetes.Interface
	RealPodUpdater
}
