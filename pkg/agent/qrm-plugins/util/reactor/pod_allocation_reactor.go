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

	"github.com/samber/lo"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/client/control"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
)

type PodAllocation interface {
	NeedUpdateAllocation(pod *v1.Pod) bool
	UpdateAllocation(pod *v1.Pod) error
}

type podAllocationReactor struct {
	podFetcher pod.PodFetcher
	podUpdater control.PodUpdater
	client     kubernetes.Interface
}

func NewPodAllocationReactor(podFetcher pod.PodFetcher, client kubernetes.Interface) AllocationReactor {
	return &podAllocationReactor{podFetcher: podFetcher, podUpdater: control.NewRealPodUpdater(client), client: client}
}

func (r *podAllocationReactor) UpdateAllocation(ctx context.Context, allocation commonstate.Allocation) error {
	if lo.IsNil(allocation) {
		return fmt.Errorf("allocation is nil")
	}

	var getPod *v1.Pod
	getPod, err := r.podFetcher.GetPod(context.WithValue(ctx, pod.BypassCacheKey, pod.BypassCacheTrue), allocation.GetPodUid())
	if err != nil {
		getPod, err = r.client.CoreV1().Pods(allocation.GetPodNamespace()).Get(ctx, allocation.GetPodName(), metav1.GetOptions{ResourceVersion: "0"})
		if err != nil {
			return err
		}
	}

	podAllocation, ok := allocation.(PodAllocation)
	if !ok {
		return fmt.Errorf("allocation info is not PodAllocation")
	}

	if !podAllocation.NeedUpdateAllocation(getPod) {
		return nil
	}

	podCopy := getPod.DeepCopy()
	err = podAllocation.UpdateAllocation(podCopy)
	if err != nil {
		return err
	}

	err = r.podUpdater.PatchPod(ctx, getPod, podCopy)
	if err != nil {
		return fmt.Errorf("failed to patch pod numa allocation annotation: %v", err)
	}

	return nil
}
