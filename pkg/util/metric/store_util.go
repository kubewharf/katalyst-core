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

package metric

import (
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

type Aggregator string

const (
	AggregatorSum Aggregator = "sum"
	AggregatorAvg Aggregator = "avg"
)

// AggregatePodNumaMetric handles numa-level metric for all pods
func (c *MetricStore) AggregatePodNumaMetric(podList []*v1.Pod, numa, metricName string, agg Aggregator) float64 {
	sumMetric := 0.
	validPods := sets.NewString()
	for _, pod := range podList {
		if validPods.Has(string(pod.UID)) {
			continue
		}

		for _, container := range pod.Spec.Containers {
			metric, err := c.GetContainerNumaMetric(string(pod.UID), container.Name, numa, metricName)
			if err != nil {
				klog.Errorf("failed to get numa-metric pod %v, container %v, numa %v, metric %v, err: %v",
					pod.Name, container.Name, numa, metricName, err)
				continue
			}
			validPods.Insert(string(pod.UID))
			sumMetric += metric
		}
	}

	switch agg {
	case AggregatorSum:
		return sumMetric
	case AggregatorAvg:
		if validPods.Len() > 0 {
			return sumMetric / float64(validPods.Len())
		}
	}
	return sumMetric
}

// AggregatePodMetric handles metric for all pods
func (c *MetricStore) AggregatePodMetric(podList []*v1.Pod, metricName string, agg Aggregator) float64 {
	sumMetric := 0.
	validPods := sets.NewString()
	for _, pod := range podList {
		if validPods.Has(string(pod.UID)) {
			continue
		}

		for _, container := range pod.Spec.Containers {
			metric, err := c.GetContainerMetric(string(pod.UID), container.Name, metricName)
			if err != nil {
				klog.Errorf("failed to get metric pod %v, container %v, metric %v, err: %v",
					pod.Name, container.Name, metricName, err)
				continue
			}
			validPods.Insert(string(pod.UID))
			sumMetric += metric
		}
	}

	switch agg {
	case AggregatorSum:
		return sumMetric
	case AggregatorAvg:
		if validPods.Len() > 0 {
			return sumMetric / float64(validPods.Len())
		}
	}
	return sumMetric
}

// AggregateCoreMetric handles metric for all cores
func (c *MetricStore) AggregateCoreMetric(cpuset machine.CPUSet, metricName string, agg Aggregator) float64 {
	sumMetric, coreCount := 0., 0.
	for _, cpu := range cpuset.ToSliceInt() {
		metric, err := c.GetCPUMetric(cpu, metricName)
		if err != nil {
			klog.Errorf("failed to get metric cpu %v, metric %v, err: %", cpu, metricName, err)
			continue
		}

		coreCount++
		sumMetric += metric
	}

	switch agg {
	case AggregatorSum:
		return sumMetric
	case AggregatorAvg:
		return sumMetric / coreCount
	}
	return sumMetric
}
