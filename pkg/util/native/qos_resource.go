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

package native

import (
	v1 "k8s.io/api/core/v1"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

// QoSResource is a collection of compute resource.
type QoSResource struct {
	ReclaimedMilliCPU int64
	ReclaimedMemory   int64
}

// For each of these resources, a pod that doesn't request the resource explicitly
// will be treated as having requested the amount indicated below, for the purpose
// of computing priority only. This ensures that when scheduling zero-request pods, such
// pods will not all be scheduled to the machine with the smallest in-use request,
// and that when scheduling regular pods, such pods will not see zero-request pods as
// consuming no resources whatsoever. We chose these values to be similar to the
// resources that we give to cluster addon pods (#10653). But they are pretty arbitrary.
// As described in #11713, we use request instead of limit to deal with resource requirements.
const (
	// DefaultReclaimedMilliCPURequest defines default milli reclaimed milli-cpu request number.
	DefaultReclaimedMilliCPURequest int64 = 100 // 0.1 core
	// DefaultReclaimedMemoryRequest defines default reclaimed memory request size.
	DefaultReclaimedMemoryRequest int64 = 200 * 1024 * 1024 // 200 MiB
)

// Add adds ResourceList into QoSResource.
func (r *QoSResource) Add(rl v1.ResourceList) {
	if r == nil {
		return
	}
	for rName, rQuant := range rl {
		switch rName {
		case consts.ReclaimedResourceMilliCPU:
			r.ReclaimedMilliCPU += rQuant.Value()
		case consts.ReclaimedResourceMemory:
			r.ReclaimedMemory += rQuant.Value()
		}
	}
}

// SetMaxResource compares with ResourceList and takes max value for each QoSResource.
func (r *QoSResource) SetMaxResource(rl v1.ResourceList) {
	if r == nil {
		return
	}
	for rName, rQuantity := range rl {
		switch rName {
		case consts.ReclaimedResourceMilliCPU:
			r.ReclaimedMilliCPU = general.MaxInt64(r.ReclaimedMilliCPU, rQuantity.Value())
		case consts.ReclaimedResourceMemory:
			r.ReclaimedMemory = general.MaxInt64(r.ReclaimedMemory, rQuantity.Value())
		}
	}
}

// CalculateQoSResource calculates the QoS Resource of a Pod
// resourceRequest = max(sum(podSpec.Containers), podSpec.InitContainers) + overHead
func CalculateQoSResource(pod *v1.Pod) (res QoSResource, non0CPU int64, non0Mem int64) {
	resPtr := &res
	for _, c := range pod.Spec.Containers {
		resPtr.Add(c.Resources.Requests)
		non0CPUReq, non0MemReq := GetNonzeroQoSRequests(&c.Resources.Requests)
		non0CPU += non0CPUReq
		non0Mem += non0MemReq
	}

	for _, ic := range pod.Spec.InitContainers {
		resPtr.SetMaxResource(ic.Resources.Requests)
		non0CPUReq, non0MemReq := GetNonzeroQoSRequests(&ic.Resources.Requests)
		non0CPU = general.MaxInt64(non0CPU, non0CPUReq)
		non0Mem = general.MaxInt64(non0Mem, non0MemReq)
	}

	// If Overhead is being utilized, add to the total requests for the pod
	if pod.Spec.Overhead != nil {
		resPtr.Add(pod.Spec.Overhead)
		if reclaimedCPUQuant, found := pod.Spec.Overhead[consts.ReclaimedResourceMilliCPU]; found {
			non0CPU += reclaimedCPUQuant.Value()
		}

		if reclaimedMemQuant, found := pod.Spec.Overhead[consts.ReclaimedResourceMemory]; found {
			non0Mem += reclaimedMemQuant.Value()
		}
	}

	return
}

// GetNonzeroQoSRequests returns the default reclaimed_millicpu and reclaimed_memory resource request if none is found or
// what is provided on the request.
func GetNonzeroQoSRequests(requests *v1.ResourceList) (int64, int64) {
	return GetRequestForQoSResource(consts.ReclaimedResourceMilliCPU, requests, true),
		GetRequestForQoSResource(consts.ReclaimedResourceMemory, requests, true)
}

// GetRequestForQoSResource returns the requested values unless nonZero is true and there is no defined request
// for CPU and memory.
// If nonZero is true and the resource has no defined request for CPU or memory, it returns a default value.
func GetRequestForQoSResource(resource v1.ResourceName, requests *v1.ResourceList, nonZero bool) int64 {
	if requests == nil {
		return 0
	}
	switch resource {
	case consts.ReclaimedResourceMilliCPU:
		// Override if un-set, but not if explicitly set to zero
		reclaimedCPUQuant, found := (*requests)[consts.ReclaimedResourceMilliCPU]
		if !found && nonZero {
			return DefaultReclaimedMilliCPURequest
		}
		return reclaimedCPUQuant.Value()
	case consts.ReclaimedResourceMemory:
		// Override if un-set, but not if explicitly set to zero
		reclaimedMemQuant, found := (*requests)[consts.ReclaimedResourceMemory]
		if !found && nonZero {
			return DefaultReclaimedMemoryRequest
		}
		return reclaimedMemQuant.Value()
	default:
		return 0
	}
}
