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

package resourcepackage

import (
	"context"

	nodev1alpha1 "github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/npd"
)

// ResourcePackageManager provides access to a node's resource package division
type ResourcePackageManager interface {
	// NodeResourcePackages returns the resource package division for the
	// specified node. The returned map's keys are NUMA IDs (as strings)
	// and the values are slices of ResourcePackage belonging to that
	// NUMA node: map[NUMA ID] -> []nodev1alpha1.ResourcePackage.
	NodeResourcePackages(ctx context.Context, nodeName string) (map[string][]nodev1alpha1.ResourcePackage, error)

	// Run starts any background processing required by the manager.
	Run(ctx context.Context)
}

// resourcePackageManager is the default implementation of ResourcePackageManager
type resourcePackageManager struct {
	// fetcher provides access to node-level package information (from
	// the NPD component).
	fetcher npd.NPDFetcher
}

func (m *resourcePackageManager) NodeResourcePackages(ctx context.Context, nodeName string) (map[string][]nodev1alpha1.ResourcePackage, error) {
	// TODO: return the resource package division for the given node.
	// The returned value should map NUMA IDs (string) to the list of
	// ResourcePackage objects associated with that NUMA node.
	return nil, nil
}

// Run starts background routines used by the resource package manager.
func (m *resourcePackageManager) Run(ctx context.Context) {
}

// NewResourcePackageManager creates a new ResourcePackageManager that uses the provided NPD fetcher.
func NewResourcePackageManager(fetcher npd.NPDFetcher) ResourcePackageManager {
	return &resourcePackageManager{
		fetcher: fetcher,
	}
}
