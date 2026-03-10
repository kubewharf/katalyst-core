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
	"strconv"

	"github.com/pkg/errors"

	apierrors "k8s.io/apimachinery/pkg/util/errors"

	nodev1alpha1 "github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/npd"
	resourcepackage "github.com/kubewharf/katalyst-core/pkg/util/resource-package"
)

// ResourcePackageManager provides access to a node's resource package division
type ResourcePackageManager interface {
	// NodeResourcePackages returns the resource package division for the
	// specified node. The returned map's keys are NUMA IDs (as int)
	// and the values are slices of ResourcePackageItem (containing ResourcePackage and Config)
	// belonging to that NUMA node: map[NUMA ID] -> []resourcepackage.ResourcePackageItem.
	NodeResourcePackages(ctx context.Context) (map[int][]resourcepackage.ResourcePackageItem, error)

	// ConvertNPDResourcePackages converts a given NodeProfileDescriptor to
	// resource packages. The returned map's keys are NUMA IDs (as int)
	// and the values are slices of ResourcePackageItem (containing ResourcePackage and Config)
	// belonging to that NUMA node: map[NUMA ID] -> []resourcepackage.ResourcePackageItem.
	ConvertNPDResourcePackages(npd *nodev1alpha1.NodeProfileDescriptor) (map[int][]resourcepackage.ResourcePackageItem, error)
}

// resourcePackageManager is the default implementation of ResourcePackageManager
type resourcePackageManager struct {
	fetcher npd.NPDFetcher
}

// NodeResourcePackages returns the resource package division for the
// specified node. The returned map's keys are NUMA IDs (as int)
// and the values are slices of ResourcePackageItem (containing ResourcePackage and Config)
// belonging to that NUMA node: map[NUMA ID] -> []resourcepackage.ResourcePackageItem.
func (m *resourcePackageManager) NodeResourcePackages(ctx context.Context) (map[int][]resourcepackage.ResourcePackageItem, error) {
	npd, err := m.fetcher.GetNPD(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get NPD from fetcher")
	}
	return m.ConvertNPDResourcePackages(npd)
}

// ConvertNPDResourcePackages converts a given NodeProfileDescriptor to
// resource packages. The returned map's keys are NUMA IDs (as int)
// and the values are slices of ResourcePackageItem (containing ResourcePackage and Config)
// belonging to that NUMA node: map[NUMA ID] -> []resourcepackage.ResourcePackageItem
func (m *resourcePackageManager) ConvertNPDResourcePackages(npd *nodev1alpha1.NodeProfileDescriptor) (map[int][]resourcepackage.ResourcePackageItem, error) {
	resourcePackageMetrics := resourcepackage.ConvertNPDMetricsToResourcePackages(npd.Status.NodeMetrics)
	resourcePackageMap := make(map[int][]resourcepackage.ResourcePackageItem)

	var errList []error
	for _, metric := range resourcePackageMetrics {
		numaID, err := strconv.Atoi(metric.NumaID)
		if err != nil {
			errList = append(errList, errors.Wrap(err, "numa ID invalid"))
			continue
		}
		resourcePackageMap[numaID] = metric.ResourcePackages
	}
	return resourcePackageMap, apierrors.NewAggregate(errList)
}

// NewResourcePackageManager creates a new ResourcePackageManager that uses the provided NPD fetcher.
func NewResourcePackageManager(fetcher npd.NPDFetcher) ResourcePackageManager {
	return &resourcePackageManager{
		fetcher: fetcher,
	}
}
