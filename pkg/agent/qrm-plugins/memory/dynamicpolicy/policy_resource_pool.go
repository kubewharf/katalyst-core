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

package dynamicpolicy

import (
	"context"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/memory/dynamicpolicy/state"
	rpvalidator "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util/resourcepoolvalidator"
	resourcepool "github.com/kubewharf/katalyst-core/pkg/util/resource-pool"
)

// memoryResourcePoolAllocatedProvider implements rpvalidator.AllocatedProvider
// for the Memory dynamic policy by walking the in-memory PodResourceEntries.
type memoryResourcePoolAllocatedProvider struct {
	state state.ReadonlyState
}

// newMemoryResourcePoolAllocatedProvider returns an AllocatedProvider that
// reports memory usage aggregated by resource pool from the given state.
func newMemoryResourcePoolAllocatedProvider(s state.ReadonlyState) rpvalidator.AllocatedProvider {
	return &memoryResourcePoolAllocatedProvider{state: s}
}

// GetAllocated returns the already-allocated quantity per resource for the
// given pool within the requested scope. The returned map covers every
// resource present in PodResourceEntries (memory + hugepages-* etc.), so the
// hint-phase NumaScope mask filter can validate hugepage capacity uniformly.
//
//   - When scope.IsNodeScope() (empty NumaIDs), the full AggregatedQuantity
//     of each matching container is summed.
//   - When scope.NumaIDs is non-empty (NumaScope), the per-NUMA shares
//     recorded in TopologyAwareAllocations are summed across all NUMA IDs in
//     the scope.
func (m *memoryResourcePoolAllocatedProvider) GetAllocated(poolName string, scope rpvalidator.Scope, excludePodUIDs ...string) (v1.ResourceList, error) {
	if m == nil || m.state == nil || poolName == "" {
		return nil, nil
	}

	excluded := make(map[string]struct{}, len(excludePodUIDs))
	for _, uid := range excludePodUIDs {
		excluded[uid] = struct{}{}
	}

	numaSet := make(map[int]struct{}, len(scope.NumaIDs))
	for _, nid := range scope.NumaIDs {
		numaSet[nid] = struct{}{}
	}

	out := v1.ResourceList{}
	for resName, podEntries := range m.state.GetPodResourceEntries() {
		var totalBytes int64
		for podUid, containerEntries := range podEntries {
			if _, skip := excluded[podUid]; skip {
				continue
			}
			for _, ai := range containerEntries {
				if ai == nil {
					continue
				}
				if resourcepool.GetResourcePoolName(ai.Annotations) != poolName {
					continue
				}

				if scope.IsNodeScope() {
					totalBytes += int64(ai.AggregatedQuantity)
					continue
				}
				for numaID, numaQty := range ai.TopologyAwareAllocations {
					if _, ok := numaSet[numaID]; ok {
						totalBytes += int64(numaQty)
					}
				}
			}
		}
		if totalBytes > 0 {
			out[resName] = *resource.NewQuantity(totalBytes, resource.BinarySI)
		}
	}

	return out, nil
}

// validateResourcePool enforces ResourcePool NodeScope MaxAllocatable for an
// incoming memory-family request (memory + hugepages-*) at GetTopologyHints
// entry. Pods without a resource pool annotation are skipped.
//
// NumaScope validation has been moved to the hint-phase mask filter
// (rpvalidator.MaskExceeds) and is no longer performed here; the Allocate
// path no longer calls this helper either to avoid duplicate work.
func (p *DynamicPolicy) validateResourcePool(ctx context.Context, req *pluginapi.ResourceRequest, requestedResources map[v1.ResourceName]int) error {
	if p == nil || p.resourcePoolValidator == nil || req == nil {
		return nil
	}
	poolName := resourcepool.GetResourcePoolName(req.Annotations)
	if poolName == "" {
		return nil
	}

	incoming := v1.ResourceList{}
	resources := make([]v1.ResourceName, 0, len(requestedResources))
	for resName, reqBytes := range requestedResources {
		incoming[resName] = *resource.NewQuantity(int64(reqBytes), resource.BinarySI)
		resources = append(resources, resName)
	}
	if len(resources) == 0 {
		return nil
	}
	return p.resourcePoolValidator.Validate(ctx, poolName, rpvalidator.NodeScope(), incoming, resources, nil, req.PodUid)
}
