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
	"sort"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"

	nodev1alpha1 "github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/util"
)

const (
	MetricScope                 = "resource-package"
	metricLabelPackageName      = "package-name"
	metricLabelNumaID           = "numa-id"
	metricLabelPinnedCPUSetPool = "pinned-cpuset-pool"
)

var defaultMetricLabels = sets.NewString(MetricScope, metricLabelPackageName, metricLabelNumaID, metricLabelPinnedCPUSetPool)

// ResourcePackageConfig holds the configuration of a resource package.
type ResourcePackageConfig struct {
	// PinnedCPUSetPool indicates the name of the cpuset pool to which the resource package is pinned.
	PinnedCPUSetPool *string `json:"pinnedCPUSetPool,omitempty"`
}

// ResourcePackageItem wraps the ResourcePackage and its configuration.
type ResourcePackageItem struct {
	nodev1alpha1.ResourcePackage `json:",inline"`
	// Config is the configuration of the resource package.
	Config *ResourcePackageConfig `json:"config,omitempty"`
}

// ResourcePackageMetric is the intermediate structure for resource package metrics.
type ResourcePackageMetric struct {
	NumaID           string                `json:"numaID"`
	ResourcePackages []ResourcePackageItem `json:"resourcePackages"`
}

// ConvertResourcePackagesToNPDMetrics example:
/* CNR
status:
	topologyZone:
	- children:
	  - name: "0"
	    type: Numa
	    resources:
	      resourcePackages:
	        - packageName: x8
	          allocatable:
	            cpu: 64
	      		memory: 512Gi
	  - name: "1"
	    type: Numa
	    resources:
	      resourcePackages:
	        - packageName: x8
	          allocatable:
	            cpu: 64
	      		memory: 512Gi
*/
/* NPD
status:
    nodeMetrics:
      - scope: "resource-package"
        metrics:
          - metricName: "cpu"
            metricLabels:
              package-name: "x8"
              numa-id: "0"
              pinned-cpuset-pool: "share"
            aggregator: "min"
            value: "64"
          - metricName: "memory"
            metricLabels:
              package-name: "x8"
              numa-id: "0"
              pinned-cpuset-pool: "share"
            aggregator: "min"
            value: "512Gi"
          - metricName: "cpu"
            metricLabels:
              package-name: "x8"
              numa-id: "1"
            aggregator: "min"
            value: "64"
          - metricName: "memory"
            metricLabels:
              package-name: "x8"
              numa-id: "1"
            aggregator: "min"
            value: "512Gi"
*/

// ConvertResourcePackagesToNPDMetrics converts resource packages to NPD metrics.
// converted npd metrics are sorted by packageName, numaID, and metricName
func ConvertResourcePackagesToNPDMetrics(resourcePackageMetrics []ResourcePackageMetric, timestamp metav1.Time) []nodev1alpha1.ScopedNodeMetrics {
	m := nodev1alpha1.ScopedNodeMetrics{
		Scope: MetricScope,
	}
	minAggregator := nodev1alpha1.AggregatorMin
	for _, pkgMetric := range resourcePackageMetrics {
		for _, pkg := range pkgMetric.ResourcePackages {
			if pkg.Allocatable == nil {
				continue
			}
			var metrics []nodev1alpha1.MetricValue
			for r, q := range *pkg.Allocatable {
				labels := map[string]string{
					metricLabelPackageName: pkg.PackageName,
					metricLabelNumaID:      pkgMetric.NumaID,
				}
				if pkg.Config != nil && pkg.Config.PinnedCPUSetPool != nil {
					labels[metricLabelPinnedCPUSetPool] = *pkg.Config.PinnedCPUSetPool
				}

				// add attributes to labels of cpu metric only
				if r.String() == string(v1.ResourceCPU) {
					for _, attr := range pkg.Attributes {
						if _, ok := labels[attr.Name]; !ok {
							labels[attr.Name] = attr.Value
						}
					}
				}

				metrics = append(metrics, nodev1alpha1.MetricValue{
					MetricName:   r.String(),
					Value:        q.DeepCopy(),
					Aggregator:   &minAggregator,
					Timestamp:    timestamp,
					MetricLabels: labels,
				})
			}
			m.Metrics = append(m.Metrics, metrics...)
		}
	}

	if len(m.Metrics) == 0 {
		return []nodev1alpha1.ScopedNodeMetrics{m}
	}
	// sort: numaID > packageName > metricName
	sort.Slice(m.Metrics, func(i, j int) bool {
		numai := m.Metrics[i].MetricLabels[metricLabelNumaID]
		numaj := m.Metrics[j].MetricLabels[metricLabelNumaID]
		if numai != numaj {
			return numai < numaj
		}
		pkgi := m.Metrics[i].MetricLabels[metricLabelPackageName]
		pkgj := m.Metrics[j].MetricLabels[metricLabelPackageName]
		if pkgi != pkgj {
			return pkgi < pkgj
		}
		return m.Metrics[i].MetricName < m.Metrics[j].MetricName
	})

	return []nodev1alpha1.ScopedNodeMetrics{m}
}

func updatePkgMapFromMetrics(metrics []nodev1alpha1.MetricValue, pkgMap map[string]map[string]ResourcePackageItem) {
	for _, v := range metrics {
		numaID := v.MetricLabels[metricLabelNumaID]
		packageName := v.MetricLabels[metricLabelPackageName]
		if numaID == "" || packageName == "" {
			continue
		}
		if v.Aggregator == nil || *v.Aggregator != nodev1alpha1.AggregatorMin {
			continue
		}
		if _, ok := pkgMap[numaID]; !ok {
			pkgMap[numaID] = make(map[string]ResourcePackageItem)
		}
		resourcePkgs := pkgMap[numaID]
		metric, ok := resourcePkgs[packageName]
		if !ok {
			metric = ResourcePackageItem{
				ResourcePackage: nodev1alpha1.ResourcePackage{
					PackageName: packageName,
					Allocatable: &v1.ResourceList{},
					Attributes:  make([]nodev1alpha1.Attribute, 0),
				},
			}
		}
		if metric.Allocatable == nil {
			metric.Allocatable = &v1.ResourceList{}
		}
		(*metric.Allocatable)[v1.ResourceName(v.MetricName)] = v.Value.DeepCopy()

		if pinnedPool, ok := v.MetricLabels[metricLabelPinnedCPUSetPool]; ok && pinnedPool != "" {
			if metric.Config == nil {
				metric.Config = &ResourcePackageConfig{}
			}
			metric.Config.PinnedCPUSetPool = &pinnedPool
		}

		// add attributes to metric which are not in defaultMetricLabels
		for k, v := range v.MetricLabels {
			if !defaultMetricLabels.Has(k) {
				metric.Attributes = append(metric.Attributes, nodev1alpha1.Attribute{
					Name:  k,
					Value: v,
				})
			}
		}
		// merge attributes to avoid duplicate
		metric.Attributes = util.MergeAttributes(nil, metric.Attributes)

		resourcePkgs[packageName] = metric
	}
}

// ConvertNPDMetricsToResourcePackages converts NPD metrics to resource packages.
// converted resource packages are sorted by packageName and numaID
func ConvertNPDMetricsToResourcePackages(metrics []nodev1alpha1.ScopedNodeMetrics) []ResourcePackageMetric {
	// numa id -> package name -> resource package metric
	pkgMap := make(map[string]map[string]ResourcePackageItem)

	for _, m := range metrics {
		if m.Scope != MetricScope {
			continue
		}
		updatePkgMapFromMetrics(m.Metrics, pkgMap)
	}
	// sort: numaID > packageName
	var packageMetrics []ResourcePackageMetric
	numaIDs := make([]string, 0, len(pkgMap))
	for numaID := range pkgMap {
		numaIDs = append(numaIDs, numaID)
	}
	sort.Strings(numaIDs)
	for _, numaID := range numaIDs {
		pkgMetric := ResourcePackageMetric{
			NumaID:           numaID,
			ResourcePackages: []ResourcePackageItem{},
		}
		resourcePkgs := pkgMap[numaID]
		// sort package names within this numa
		pkgNames := make([]string, 0, len(resourcePkgs))
		for pkgName := range resourcePkgs {
			pkgNames = append(pkgNames, pkgName)
		}
		sort.Strings(pkgNames)
		for _, pkgName := range pkgNames {
			pkgMetric.ResourcePackages = append(pkgMetric.ResourcePackages, resourcePkgs[pkgName])
		}
		packageMetrics = append(packageMetrics, pkgMetric)
	}
	return packageMetrics
}
