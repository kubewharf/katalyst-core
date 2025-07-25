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
	"fmt"
	"strings"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

type Aggregator string

const (
	AggregatorSum Aggregator = "sum"
	AggregatorAvg Aggregator = "avg"

	MaxTagLength = 255
)

// ContainerMetricFilter is used to filter out unnecessary metrics if this function returns false
type ContainerMetricFilter func(pod *v1.Pod, container *v1.Container) bool

var DefaultContainerMetricFilter = func(_ *v1.Pod, _ *v1.Container) bool { return true }

// AggregatePodNumaMetric handles numa-level metric for all pods
func (c *MetricStore) AggregatePodNumaMetric(podList []*v1.Pod, numa int, metricName string, agg Aggregator, filter ContainerMetricFilter) MetricData {
	now := time.Now()
	data := MetricData{Value: .0, Time: &now}

	validPods := sets.NewString()
	for _, pod := range podList {
		if validPods.Has(string(pod.UID)) {
			continue
		}

		for _, container := range pod.Spec.Containers {
			if !filter(pod, &container) {
				continue
			}

			metric, err := c.GetContainerNumaMetric(string(pod.UID), container.Name, numa, metricName)
			if err != nil {
				klog.Errorf("failed to get numa-metric pod %v, container %v, numa %v, metric %v, err: %v",
					pod.Name, container.Name, numa, metricName, err)
				continue
			}
			validPods.Insert(string(pod.UID))

			data.Value += metric.Value
			data.Time = general.MaxTimePtr(data.Time, metric.Time)
		}
	}

	switch agg {
	case AggregatorAvg:
		if validPods.Len() > 0 {
			data.Value /= float64(validPods.Len())
		}
	}
	return data
}

// AggregatePodMetric handles metric for all pods
func (c *MetricStore) AggregatePodMetric(podList []*v1.Pod, metricName string, agg Aggregator, filter ContainerMetricFilter) MetricData {
	now := time.Now()
	data := MetricData{Value: .0, Time: &now}

	validPods := sets.NewString()
	for _, pod := range podList {
		if validPods.Has(string(pod.UID)) {
			continue
		}

		for _, container := range pod.Spec.Containers {
			if !filter(pod, &container) {
				continue
			}

			metric, err := c.GetContainerMetric(string(pod.UID), container.Name, metricName)
			if err != nil {
				klog.Errorf("failed to get metric pod %v, container %v, metric %v, err: %v",
					pod.Name, container.Name, metricName, err)
				continue
			}
			validPods.Insert(string(pod.UID))

			data.Value += metric.Value
			data.Time = general.MaxTimePtr(data.Time, metric.Time)
		}
	}

	switch agg {
	case AggregatorAvg:
		if validPods.Len() > 0 {
			data.Value /= float64(validPods.Len())
		}
	}
	return data
}

// AggregateCoreMetric handles metric for all cores
func (c *MetricStore) AggregateCoreMetric(cpuset machine.CPUSet, metricName string, agg Aggregator) MetricData {
	now := time.Now()
	data := MetricData{Value: .0, Time: &now}

	coreCount := 0.
	for _, cpu := range cpuset.ToSliceInt() {
		metric, err := c.GetCPUMetric(cpu, metricName)
		if err != nil {
			klog.V(4).Infof("failed to get metric cpu %v, metric %v, err: %v", cpu, metricName, err)
			continue
		}

		coreCount++
		data.Value += metric.Value
		data.Time = general.MaxTimePtr(data.Time, metric.Time)
	}

	switch agg {
	case AggregatorAvg:
		if coreCount > 0 {
			data.Value /= coreCount
		}
	}
	return data
}

// MetricTagValueFormat formats the given tag value to a string that is suitable for metric tagging
func MetricTagValueFormat(tagValue interface{}) string {
	return general.TruncateString(strings.ReplaceAll(fmt.Sprintf("%v", tagValue), " ", "_"), MaxTagLength)
}

var ccdCountMap = map[string]int{
	consts.PlatformGeona:   12,
	consts.PlatformMilan:   8,
	consts.PlatformRome:    8,
	consts.PlatformRapids:  1,
	consts.PlatformLake:    1,
	consts.PlatformUnknown: 1,
}

var socketBandwidthMap = map[string]uint64{
	consts.PlatformGeona:  322 * 1e9, // logical.max = 460, real.max = 460 * 70%
	consts.PlatformMilan:  142 * 1e9, // logical.max = 204, real.max = 204 * 70%
	consts.PlatformRome:   142 * 1e9, // logical.max = 204, real.max = 204 * 70%
	consts.PlatformRapids: 215 * 1e9, // logical.max = 307, real.max = 307 * 70%, Intel:SapphireRapids
	consts.PlatformLake:   98 * 1e9,  // logical.max = 140, real.max = 140 * 70%, intel:SkyLake/CascadeLake/IceLake
}

func GetCPUPlatform(cpuCode string) (string, error) {
	switch {
	case strings.Contains(cpuCode, consts.AMDGenoaArch):
		return consts.PlatformGeona, nil
	case strings.Contains(cpuCode, consts.AMDMilanArch):
		return consts.PlatformMilan, nil
	case strings.Contains(cpuCode, consts.AMDRomeArch):
		return consts.PlatformRome, nil
	case strings.Contains(cpuCode, consts.IntelRapidsArch):
		return consts.PlatformRapids, nil
	case strings.Contains(cpuCode, consts.IntelLakeArch):
		return consts.PlatformLake, nil
	default:
		return consts.PlatformUnknown, fmt.Errorf("model name not found")
	}
}

func GetPlatformCCDCount(platform string) int {
	count, ok := ccdCountMap[platform]
	if !ok || count == 0 {
		count = 1
	}
	return count
}

func GetPlatformSocketBandwidth(platform string) uint64 {
	bandwidth, ok := socketBandwidthMap[platform]
	if !ok {
		bandwidth = consts.MaxMBGBps // default
	}
	return bandwidth
}
