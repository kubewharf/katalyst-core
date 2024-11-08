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

package allocation

import (
	"context"
	"fmt"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/memory/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

type PodAllocations map[string]*Allocation

func (p *PodAllocations) Clone() PodAllocations {
	a := make(PodAllocations)
	for uid, allocation := range *p {
		a[uid] = allocation.Clone()
	}
	return a
}

type Request struct {
	CPUMilli int64
	Memory   int64
}

type Allocation struct {
	types.NamespacedName
	Request
	BindingNUMA int
}

func (n *Allocation) Clone() *Allocation {
	if n == nil {
		return nil
	}
	return &Allocation{
		BindingNUMA:    n.BindingNUMA,
		NamespacedName: n.NamespacedName,
		Request: Request{
			CPUMilli: n.CPUMilli,
			Memory:   n.Memory,
		},
	}
}

func (n *Allocation) String() string {
	if n == nil {
		return ""
	}
	return fmt.Sprintf("%s/%d/%d/%d", n.NamespacedName, n.BindingNUMA, n.CPUMilli, n.Memory)
}

type Updater interface {
	UpdateAllocation(PodAllocations) error
}

func GetPodAllocations(ctx context.Context, metaServer *metaserver.MetaServer, memoryState state.ReadonlyState) (PodAllocations, error) {
	allocations := make(PodAllocations)
	podEntries := memoryState.GetPodResourceEntries()[v1.ResourceMemory]
	for uid, podEntry := range podEntries {
		for _, container := range podEntry {
			if !container.CheckMainContainer() || !container.CheckShared() {
				continue
			}

			bindingNUMA := -1
			if container.CheckNUMABinding() {
				bindingNUMA = container.NumaAllocationResult.ToSliceNoSortInt()[0]
			}

			pod, err := metaServer.GetPod(ctx, uid)
			if err != nil {
				general.Errorf("get pod %s failed: %v", uid, err)
				return nil, err
			}

			if !native.PodIsActive(pod) {
				continue
			}

			allocations[uid] = &Allocation{
				NamespacedName: types.NamespacedName{
					Namespace: pod.Namespace,
					Name:      pod.Name,
				},
				BindingNUMA: bindingNUMA,
				Request:     getPodRequest(pod),
			}
		}
	}

	return allocations, nil
}

func getPodRequest(pod *v1.Pod) Request {
	req := native.SumUpPodRequestResources(pod)
	cpuRequest := native.CPUQuantityGetter()(req)
	memoryRequest := native.MemoryQuantityGetter()(req)
	return Request{
		CPUMilli: cpuRequest.MilliValue(),
		Memory:   memoryRequest.Value(),
	}
}
