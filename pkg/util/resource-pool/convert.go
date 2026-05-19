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
	"sort"
	"strconv"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	nodev1alpha1 "github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
)

const (
	// MetricScope is the scope name for resource pool metrics in NPD.
	MetricScope = "resource-pool"
	// NumaIDAll indicates the resource pool is not bound to a specific NUMA node.
	NumaIDAll = -1

	metricLabelPoolName = "pool-name"
	metricLabelNumaID   = "numa-id"
)

// NumaResourcePool aggregates resource pools that belong to the same NUMA node.
// NumaID == NumaIDAll(-1) means the resource pools are node-level (not bound
// to a specific NUMA node).
type NumaResourcePool struct {
	NumaID        int                         `json:"numaID"`
	ResourcePools []nodev1alpha1.ResourcePool `json:"resourcePools"`
}

// ConvertResourcePoolsToNPDMetrics converts resource pools (organized per NUMA)
// into NPD ScopedNodeMetrics. Min/Max allocatable values are encoded with the
// AggregatorMin / AggregatorMax aggregators respectively.
//
// CNR example:
//
//	status:
//	  resources:
//	  ResourcePools:
//	    - poolName: L1
//	      minAllocatable:
//	        cpu: "1"
//	        memory: 1Gi
//	      maxAllocatable:
//	        cpu: "8"
//	        memory: 16Gi
//
// NPD example:
//
//	status:
//	  nodeMetrics:
//	    scope: resource-pool
//	    metrics:
//	      - metricName: cpu
//	        value: "8"
//	        aggregator: Max
//	        metricLabels:
//	          pool-name: L1
//	      - metricName: cpu
//	        value: "1"
//	        aggregator: Min
//	        metricLabels:
//	          pool-name: L1
func ConvertResourcePoolsToNPDMetrics(numaResourcePools []NumaResourcePool, timestamp metav1.Time) []nodev1alpha1.ScopedNodeMetrics {
	m := nodev1alpha1.ScopedNodeMetrics{
		Scope: MetricScope,
	}
	for _, numaPool := range numaResourcePools {
		numaID := ""
		if numaPool.NumaID != NumaIDAll {
			numaID = strconv.Itoa(numaPool.NumaID)
		}
		for _, pool := range numaPool.ResourcePools {
			m.Metrics = append(m.Metrics, convertResourceToMetrics(pool, numaID, nodev1alpha1.AggregatorMin, timestamp)...)
			m.Metrics = append(m.Metrics, convertResourceToMetrics(pool, numaID, nodev1alpha1.AggregatorMax, timestamp)...)
		}
	}

	if len(m.Metrics) == 0 {
		return []nodev1alpha1.ScopedNodeMetrics{m}
	}
	// sort: numaID > poolName > aggregator > metricName
	sort.Slice(m.Metrics, func(i, j int) bool {
		numaIDi := m.Metrics[i].MetricLabels[metricLabelNumaID]
		numaIDj := m.Metrics[j].MetricLabels[metricLabelNumaID]
		if numaIDi != numaIDj {
			return numaIDi < numaIDj
		}
		pooli := m.Metrics[i].MetricLabels[metricLabelPoolName]
		poolj := m.Metrics[j].MetricLabels[metricLabelPoolName]
		if pooli != poolj {
			return pooli < poolj
		}
		if *m.Metrics[i].Aggregator != *m.Metrics[j].Aggregator {
			return *m.Metrics[i].Aggregator < *m.Metrics[j].Aggregator
		}
		return m.Metrics[i].MetricName < m.Metrics[j].MetricName
	})

	return []nodev1alpha1.ScopedNodeMetrics{m}
}

// convertResourceToMetrics converts the Min or Max allocatable side of a single
// ResourcePool into a slice of MetricValue under the given aggregator.
func convertResourceToMetrics(pool nodev1alpha1.ResourcePool, numaID string, aggregator nodev1alpha1.Aggregator, timestamp metav1.Time) []nodev1alpha1.MetricValue {
	res := pool.MaxAllocatable
	if aggregator == nodev1alpha1.AggregatorMin {
		res = pool.MinAllocatable
	}
	if res == nil {
		return nil
	}

	aggregatorCopy := aggregator
	var metrics []nodev1alpha1.MetricValue
	for r, q := range *res {
		metric := nodev1alpha1.MetricValue{
			MetricName: r.String(),
			Value:      q.DeepCopy(),
			Aggregator: &aggregatorCopy,
			Timestamp:  timestamp,
			MetricLabels: map[string]string{
				metricLabelPoolName: pool.PoolName,
			},
		}
		if numaID != "" {
			metric.MetricLabels[metricLabelNumaID] = numaID
		}
		for _, attr := range pool.Attributes {
			metric.MetricLabels[attr.Name] = attr.Value
		}

		metrics = append(metrics, metric)
	}
	return metrics
}

// ConvertNPDMetricsToResourcePoolMap converts NPD ScopedNodeMetrics into a
// map keyed by NUMA ID then pool name. This is the primary representation
// used by the resource pool validator for O(1) pool lookups.
func ConvertNPDMetricsToResourcePoolMap(metrics []nodev1alpha1.ScopedNodeMetrics) map[int]map[string]nodev1alpha1.ResourcePool {
	numaPoolMap := make(map[int]map[string]nodev1alpha1.ResourcePool)

	for _, m := range metrics {
		if m.Scope != MetricScope {
			continue
		}
		for _, v := range m.Metrics {
			poolName := v.MetricLabels[metricLabelPoolName]
			if poolName == "" {
				continue
			}
			numaID := NumaIDAll
			if v.MetricLabels[metricLabelNumaID] != "" {
				var err error
				numaID, err = strconv.Atoi(v.MetricLabels[metricLabelNumaID])
				if err != nil {
					continue
				}
			}

			poolMap, ok := numaPoolMap[numaID]
			if !ok {
				poolMap = make(map[string]nodev1alpha1.ResourcePool)
				numaPoolMap[numaID] = poolMap
			}
			pool, ok := poolMap[poolName]
			if !ok {
				pool = nodev1alpha1.ResourcePool{
					PoolName: poolName,
				}
			}
			if v.Aggregator == nil {
				v.Aggregator = ptr.To(nodev1alpha1.Aggregator(""))
			}
			switch *v.Aggregator {
			case nodev1alpha1.AggregatorMax:
				pool.MaxAllocatable = addResource(pool.MaxAllocatable, v.MetricName, v.Value)
			case nodev1alpha1.AggregatorMin:
				pool.MinAllocatable = addResource(pool.MinAllocatable, v.MetricName, v.Value)
			case "":
				pool.MaxAllocatable = addResource(pool.MaxAllocatable, v.MetricName, v.Value)
				pool.MinAllocatable = addResource(pool.MinAllocatable, v.MetricName, v.Value)
			}
			pool.Attributes = addAttributes(pool.Attributes, v.MetricLabels)

			numaPoolMap[numaID][poolName] = pool
		}
	}

	return numaPoolMap
}

// ConvertNPDMetricsToResourcePools converts NPD ScopedNodeMetrics back into
// per-NUMA resource pools. The returned slice is sorted by NumaID and PoolName.
func ConvertNPDMetricsToResourcePools(metrics []nodev1alpha1.ScopedNodeMetrics) []NumaResourcePool {
	numaPoolMap := ConvertNPDMetricsToResourcePoolMap(metrics)

	var numaPools []NumaResourcePool
	for numaID := range numaPoolMap {
		var pools []nodev1alpha1.ResourcePool
		for name := range numaPoolMap[numaID] {
			pools = append(pools, numaPoolMap[numaID][name])
		}
		sort.Slice(pools, func(i, j int) bool {
			return pools[i].PoolName < pools[j].PoolName
		})
		numaPools = append(numaPools, NumaResourcePool{
			NumaID:        numaID,
			ResourcePools: pools,
		})
	}
	sort.Slice(numaPools, func(i, j int) bool {
		return numaPools[i].NumaID < numaPools[j].NumaID
	})

	return numaPools
}

// addResource sets the given resource quantity under the resource list, lazily
// allocating the underlying map if it is nil.
func addResource(rl *v1.ResourceList, rname string, quantity resource.Quantity) *v1.ResourceList {
	if rl == nil {
		rl = &v1.ResourceList{}
	}
	(*rl)[v1.ResourceName(rname)] = quantity.DeepCopy()
	return rl
}

// addAttributes merges metric labels (excluding the well-known pool-name and
// numa-id labels) into the resource pool attribute list, deduplicating by name.
func addAttributes(attrs []nodev1alpha1.Attribute, labels map[string]string) []nodev1alpha1.Attribute {
	attrMap := make(map[string]string)
	for _, attr := range attrs {
		attrMap[attr.Name] = attr.Value
	}

	for k, v := range labels {
		if k == metricLabelPoolName || k == metricLabelNumaID {
			continue
		}
		if _, ok := attrMap[k]; ok {
			continue
		}
		attrs = append(attrs, nodev1alpha1.Attribute{Name: k, Value: v})
		attrMap[k] = v
	}
	return attrs
}
