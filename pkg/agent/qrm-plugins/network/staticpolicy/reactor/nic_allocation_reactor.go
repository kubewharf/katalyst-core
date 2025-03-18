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

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/network/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util/reactor"
)

type nicPodAllocationWrapper struct {
	*state.AllocationInfo
}

func (p nicPodAllocationWrapper) UpdateAllocation(pod *v1.Pod) error {
	if p.AllocationInfo == nil {
		return nil
	}

	annotations := pod.GetAnnotations()
	if annotations == nil {
		annotations = make(map[string]string)
	}

	annotations[apiconsts.PodAnnotationNICSelectionResultKey] = p.AllocationInfo.Identifier
	pod.SetAnnotations(annotations)

	return nil
}

func (p nicPodAllocationWrapper) NeedUpdateAllocation(pod *v1.Pod) bool {
	if p.CheckSideCar() {
		return false
	}

	if _, ok := pod.Annotations[apiconsts.PodAnnotationNICSelectionResultKey]; !ok {
		return true
	}

	return false
}

type nicPodAllocationReactor struct {
	reactor.AllocationReactor
}

func NewNICPodAllocationReactor(r reactor.AllocationReactor) reactor.AllocationReactor {
	return &nicPodAllocationReactor{
		AllocationReactor: r,
	}
}

func (r *nicPodAllocationReactor) UpdateAllocation(ctx context.Context, allocation commonstate.Allocation) error {
	if lo.IsNil(allocation) {
		return fmt.Errorf("allocation is nil")
	}

	allocationInfo, ok := allocation.(*state.AllocationInfo)
	if !ok {
		return fmt.Errorf("allocation info is not of type network.AllocationInfo")
	}

	return r.AllocationReactor.UpdateAllocation(ctx, nicPodAllocationWrapper{
		AllocationInfo: allocationInfo,
	})
}
