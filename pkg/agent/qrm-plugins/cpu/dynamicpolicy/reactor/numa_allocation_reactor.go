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
	"strings"

	"github.com/samber/lo"
	v1 "k8s.io/api/core/v1"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util/reactor"
)

type numaPodAllocationWrapper struct {
	*state.AllocationInfo
}

// UpdateAllocation updates numa allocation result for pod
func (p numaPodAllocationWrapper) UpdateAllocation(pod *v1.Pod) error {
	annotations := pod.GetAnnotations()
	if annotations == nil {
		annotations = make(map[string]string)
	}

	numaAllocationResult, err := p.getNUMAAllocationResult()
	if err != nil {
		return fmt.Errorf("failed to get numa allocation result: %v", err)
	}

	annotations[apiconsts.PodAnnotationNUMABindResultKey] = numaAllocationResult
	pod.SetAnnotations(annotations)

	return nil
}

func (p numaPodAllocationWrapper) getNUMAAllocationResult() (string, error) {
	numaList := p.AllocationInfo.GetAllocationResultNUMASet().ToSliceInt()
	if len(numaList) == 0 {
		return "", fmt.Errorf("numa id is empty")
	}
	intSlice := make([]string, len(numaList))
	for i, numaID := range numaList {
		intSlice[i] = strconv.Itoa(numaID)
	}
	return strings.Join(intSlice, ","), nil
}

// NeedUpdateAllocation checks if numa allocation result needs to be updated
func (p numaPodAllocationWrapper) NeedUpdateAllocation(pod *v1.Pod) bool {
	if p.CheckSideCar() {
		return false
	}

	if _, ok := pod.Annotations[apiconsts.PodAnnotationNUMABindResultKey]; !ok {
		return true
	}

	return false
}

type numaPodAllocationReactor struct {
	reactor.AllocationReactor
}

// NewNUMAPodAllocationReactor creates a new numa pod allocation reactor
func NewNUMAPodAllocationReactor(r reactor.AllocationReactor) reactor.AllocationReactor {
	return &numaPodAllocationReactor{
		AllocationReactor: r,
	}
}

// UpdateAllocation updates numa allocation result for pod
func (r *numaPodAllocationReactor) UpdateAllocation(ctx context.Context, allocation commonstate.Allocation) error {
	if lo.IsNil(allocation) {
		return fmt.Errorf("allocation is nil")
	}

	allocationInfo, ok := allocation.(*state.AllocationInfo)
	if !ok {
		return fmt.Errorf("allocation info is not of type memory.AllocationInfo")
	}

	return r.AllocationReactor.UpdateAllocation(ctx, numaPodAllocationWrapper{
		AllocationInfo: allocationInfo,
	})
}
