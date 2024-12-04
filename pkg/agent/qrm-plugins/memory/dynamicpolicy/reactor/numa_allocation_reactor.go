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

package reactor

import (
	"context"
	"fmt"
	"strconv"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/memory/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/client/control"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
)

type numaAllocationReactor struct {
	podFetcher pod.PodFetcher
	podUpdater control.PodUpdater
	client     kubernetes.Interface
}

func NewNUMAAllocationReactor(podFetcher pod.PodFetcher, client kubernetes.Interface) AllocationReactor {
	return &numaAllocationReactor{podFetcher: podFetcher, podUpdater: control.NewRealPodUpdater(client), client: client}
}

func (r *numaAllocationReactor) UpdateAllocation(ctx context.Context, allocation *state.AllocationInfo) error {
	if allocation == nil {
		return fmt.Errorf("allocation info is nil")
	}

	var getPod *v1.Pod
	getPod, err := r.podFetcher.GetPod(ctx, allocation.PodUid)
	if err != nil {
		getPod, err = r.client.CoreV1().Pods(allocation.PodNamespace).Get(ctx, allocation.PodName, metav1.GetOptions{ResourceVersion: "0"})
		if err != nil {
			return err
		}
	}

	if !r.needUpdateNUMAAllocation(getPod) {
		return nil
	}

	podCopy := getPod.DeepCopy()
	err = r.setNUMAAllocation(podCopy, allocation)
	if err != nil {
		return err
	}

	err = r.podUpdater.PatchPod(ctx, getPod, podCopy)
	if err != nil {
		return fmt.Errorf("failed to patch pod numa allocation annotation: %v", err)
	}

	return nil
}

func (r *numaAllocationReactor) setNUMAAllocation(pod *v1.Pod, allocation *state.AllocationInfo) error {
	numaID, err := allocation.GetSpecifiedNUMABindingNUMAID()
	if err != nil {
		return err
	}

	annotations := pod.GetAnnotations()
	if annotations == nil {
		annotations = make(map[string]string)
	}
	annotations[apiconsts.PodAnnotationNUMABindResultKey] = strconv.Itoa(numaID)
	pod.SetAnnotations(annotations)

	return nil
}

func (r *numaAllocationReactor) needUpdateNUMAAllocation(pod *v1.Pod) bool {
	if _, ok := pod.Annotations[apiconsts.PodAnnotationNUMABindResultKey]; !ok {
		return true
	}

	return false
}
