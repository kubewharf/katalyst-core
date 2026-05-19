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

package resourcepool

import (
	"context"

	"github.com/pkg/errors"

	nodev1alpha1 "github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/npd"
	resourcepool "github.com/kubewharf/katalyst-core/pkg/util/resource-pool"
)

// ResourcePoolManager provides access to a node's resource pool division.
type ResourcePoolManager interface {
	// NodeResourcePools returns the resource pool division for the current node.
	// The returned map's keys are NUMA IDs (as int) where resourcepool.NumaIDAll(-1)
	// indicates node-level pools. The inner map is keyed by pool name for O(1)
	// lookup: map[NUMA ID] -> map[pool name] -> v1alpha1.ResourcePool.
	NodeResourcePools(ctx context.Context) (map[int]map[string]nodev1alpha1.ResourcePool, error)

	// ConvertNPDResourcePools converts a given NodeProfileDescriptor to resource
	// pools. The returned map shares the same shape as NodeResourcePools.
	ConvertNPDResourcePools(npd *nodev1alpha1.NodeProfileDescriptor) (map[int]map[string]nodev1alpha1.ResourcePool, error)
}

// resourcePoolManager is the default implementation of ResourcePoolManager.
type resourcePoolManager struct {
	fetcher npd.NPDFetcher
}

// NodeResourcePools returns the resource pool division for the current node.
func (m *resourcePoolManager) NodeResourcePools(ctx context.Context) (map[int]map[string]nodev1alpha1.ResourcePool, error) {
	desc, err := m.fetcher.GetNPD(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get NPD from fetcher")
	}
	return m.ConvertNPDResourcePools(desc)
}

// ConvertNPDResourcePools converts a given NodeProfileDescriptor to resource pools.
func (m *resourcePoolManager) ConvertNPDResourcePools(desc *nodev1alpha1.NodeProfileDescriptor) (map[int]map[string]nodev1alpha1.ResourcePool, error) {
	if desc == nil {
		return nil, nil
	}

	return resourcepool.ConvertNPDMetricsToResourcePoolMap(desc.Status.NodeMetrics), nil
}

// NewResourcePoolManager creates a new ResourcePoolManager that uses the
// provided NPD fetcher.
func NewResourcePoolManager(fetcher npd.NPDFetcher) ResourcePoolManager {
	return &resourcePoolManager{
		fetcher: fetcher,
	}
}
