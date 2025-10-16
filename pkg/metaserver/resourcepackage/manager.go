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
	"time"

	"github.com/pkg/errors"

	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"

	nodev1alpha1 "github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/npd"
	resourcepackage "github.com/kubewharf/katalyst-core/pkg/util/resource-package"
)

const (
	updateConfigInterval     = 5 * time.Second
	updateConfigJitterFactor = 0.5
)

// ResourcePackageManager provides access to a node's resource package division
type ResourcePackageManager interface {
	// NodeResourcePackages returns the resource package division for the
	// specified node. The returned map's keys are NUMA IDs (as int)
	// and the values are slices of ResourcePackage belonging to that
	// NUMA node: map[NUMA ID] -> []nodev1alpha1.ResourcePackage.
	NodeResourcePackages(ctx context.Context) (map[int][]nodev1alpha1.ResourcePackage, error)

	// ConvertNPDResourcePackages converts a given NodeProfileDescriptor to
	// resource packages. The returned map's keys are NUMA IDs (as int)
	// and the values are slices of ResourcePackage belonging to that
	// NUMA node: map[NUMA ID] -> []nodev1alpha1.ResourcePackage.
	ConvertNPDResourcePackages(npd *nodev1alpha1.NodeProfileDescriptor) (map[int][]nodev1alpha1.ResourcePackage, error)

	// Run starts a loop that periodically fetches the latest NPD and updates
	Run(ctx context.Context)
}

// resourcePackageManager is the default implementation of ResourcePackageManager
type resourcePackageManager struct {
	// fetcher provides access to node-level package information (from
	// the NPD component).
	fetcher npd.NPDFetcher

	npd *nodev1alpha1.NodeProfileDescriptor
}

// NodeResourcePackages returns the resource package division for the
// specified node. The returned map's keys are NUMA IDs (as int)
// and the values are slices of ResourcePackage belonging to that
// NUMA node: map[NUMA ID] -> []nodev1alpha1.ResourcePackage.
func (m *resourcePackageManager) NodeResourcePackages(ctx context.Context) (map[int][]nodev1alpha1.ResourcePackage, error) {
	if m.npd == nil {
		return nil, errors.Errorf("failed to get npd")
	}
	return m.ConvertNPDResourcePackages(m.npd)
}

// ConvertNPDResourcePackages converts a given NodeProfileDescriptor to
// resource packages. The returned map's keys are NUMA IDs (as int)
// and the values are slices of ResourcePackage belonging to that
// NUMA node: map[NUMA ID] -> []nodev1alpha1.ResourcePackage
func (m *resourcePackageManager) ConvertNPDResourcePackages(npd *nodev1alpha1.NodeProfileDescriptor) (map[int][]nodev1alpha1.ResourcePackage, error) {
	resourcePackageMetrics := resourcepackage.ConvertNPDMetricsToResourcePackages(npd.Status.NodeMetrics)
	resourcePackageMap := make(map[int][]nodev1alpha1.ResourcePackage)

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

// Run starts a loop that periodically fetches the latest NPD and updates
func (m *resourcePackageManager) Run(ctx context.Context) {
	go wait.JitterUntilWithContext(ctx, func(context.Context) {
		latestNpd, err := m.fetcher.GetNPD(ctx)
		if err != nil {
			klog.Errorf("try get new npd error: %v", err)
		}
		if !apiequality.Semantic.DeepEqual(m.npd, latestNpd) {
			m.npd = latestNpd
		}
	}, updateConfigInterval, updateConfigJitterFactor, true)
	<-ctx.Done()
}

// NewResourcePackageManager creates a new ResourcePackageManager that uses the provided NPD fetcher.
func NewResourcePackageManager(fetcher npd.NPDFetcher) ResourcePackageManager {
	return &resourcePackageManager{
		fetcher: fetcher,
	}
}
