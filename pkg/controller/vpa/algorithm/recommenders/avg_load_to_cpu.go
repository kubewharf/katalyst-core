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

package recommenders

import (
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	apis "github.com/kubewharf/katalyst-api/pkg/apis/autoscaling/v1alpha1"
	workload "github.com/kubewharf/katalyst-api/pkg/apis/workload/v1alpha1"
	apimetricpod "github.com/kubewharf/katalyst-api/pkg/metric/pod"
	"github.com/kubewharf/katalyst-core/pkg/controller/vpa/algorithm"
)

var simpleCPURecommenderName = "AvgLoadToCpuRequest"

// SimpleCPURecommender recommend cpu according avg load
type SimpleCPURecommender struct{}

// NewCPURecommender construct CPURecommender
func NewCPURecommender() algorithm.ResourceRecommender {
	r := &SimpleCPURecommender{}
	return r
}

func (r *SimpleCPURecommender) Name() string {
	return simpleCPURecommenderName
}

func (r *SimpleCPURecommender) GetRecommendedPodResources(
	spd *workload.ServiceProfileDescriptor, _ []*corev1.Pod,
) ([]apis.RecommendedPodResources, []apis.RecommendedContainerResources, error) {
	if spd == nil {
		return nil, nil, fmt.Errorf("invalid spd")
	}

	for _, aggPodMetrics := range spd.Status.AggMetrics {
		if aggPodMetrics.Aggregator == workload.Avg {
			containerLoads := r.computeAVGPodMetrics(aggPodMetrics.Items, apimetricpod.CustomMetricPodCPULoad1Min)
			if len(containerLoads) == 0 {
				return nil, nil, fmt.Errorf("failed to compute avg loads")
			}

			containerRecommendResources := make([]apis.RecommendedContainerResources, 0, len(containerLoads))
			for container, load := range containerLoads {
				recommendResources := apis.RecommendedContainerResources{
					ContainerName: &container,
					Requests: &apis.RecommendedRequestResources{
						Resources: map[corev1.ResourceName]resource.Quantity{
							corev1.ResourceCPU: *load,
						},
					},
				}
				containerRecommendResources = append(containerRecommendResources, recommendResources)
			}
			return nil, containerRecommendResources, nil
		}
	}

	return nil, nil, fmt.Errorf("cannot find avg metrics")
}

type ContainerStatistic struct {
	UsageIntegral resource.Quantity
	TimeSum       metav1.Duration
}

func (r *SimpleCPURecommender) computeAVGPodMetrics(podMetrics []workload.PodMetrics, resourceName corev1.ResourceName) map[string]*resource.Quantity {
	containerResources := make(map[string]*resource.Quantity)
	statistics := make(map[string]*ContainerStatistic)

	for _, podMetric := range podMetrics {
		for _, container := range podMetric.Containers {
			load, ok := container.Usage[resourceName]
			if !ok {
				continue
			}

			if _, ok := statistics[container.Name]; !ok {
				statistics[container.Name] = &ContainerStatistic{}
			}

			s := statistics[container.Name]
			originResource := s.UsageIntegral
			integral := resource.NewMilliQuantity(
				int64(load.AsApproximateFloat64()*podMetric.Window.Seconds()*1000), resource.DecimalSI)
			originResource.Add(*integral)
			s.UsageIntegral = originResource

			s.TimeSum = metav1.Duration{Duration: time.Duration(s.TimeSum.Nanoseconds() + podMetric.Window.Nanoseconds())}
		}
	}

	for name, s := range statistics {
		l := s.UsageIntegral
		resourceAvg := l.AsApproximateFloat64() / s.TimeSum.Seconds()
		containerResources[name] = resource.NewMilliQuantity(int64(resourceAvg*1000), resource.DecimalSI)
	}

	return containerResources
}
