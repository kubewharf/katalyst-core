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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	metrics "k8s.io/metrics/pkg/apis/metrics/v1beta1"
	"k8s.io/utils/pointer"

	apis "github.com/kubewharf/katalyst-api/pkg/apis/autoscaling/v1alpha1"
	workload "github.com/kubewharf/katalyst-api/pkg/apis/workload/v1alpha1"
	apimetricpod "github.com/kubewharf/katalyst-api/pkg/metric/pod"
)

func TestGetRecommendedPodResources(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		spd  *workload.ServiceProfileDescriptor
		want struct {
			recommendedResources []apis.RecommendedContainerResources
			err                  error
		}
	}{
		{
			name: "test1",
			spd: &workload.ServiceProfileDescriptor{
				Status: workload.ServiceProfileDescriptorStatus{
					AggMetrics: []workload.AggPodMetrics{
						{
							Aggregator: "avg",
							Items: []metrics.PodMetrics{
								{
									Timestamp: metav1.NewTime(time.Date(2022, 1, 1, 1, 0, 0, 0, time.UTC)),
									Window:    metav1.Duration{Duration: time.Hour},
									Containers: []metrics.ContainerMetrics{
										{
											Name: "c1",
											Usage: map[v1.ResourceName]resource.Quantity{
												apimetricpod.CustomMetricPodCPULoad1Min: *resource.NewQuantity(20, resource.DecimalSI),
											},
										},
									},
								},
							},
						},
						{
							Aggregator: "avg",
							Items: []metrics.PodMetrics{
								{
									Timestamp: metav1.NewTime(time.Date(2022, 1, 1, 2, 0, 0, 0, time.UTC)),
									Window:    metav1.Duration{Duration: time.Hour},
									Containers: []metrics.ContainerMetrics{
										{
											Name: "c1",
											Usage: map[v1.ResourceName]resource.Quantity{
												apimetricpod.CustomMetricPodCPULoad1Min: *resource.NewQuantity(20, resource.DecimalSI),
											},
										},
									},
								},
							},
						},
					},
				},
			},
			want: struct {
				recommendedResources []apis.RecommendedContainerResources
				err                  error
			}{
				recommendedResources: []apis.RecommendedContainerResources{
					{
						ContainerName: pointer.String("c1"),
						Requests: &apis.RecommendedRequestResources{
							Resources: map[v1.ResourceName]resource.Quantity{
								v1.ResourceCPU: *resource.NewMilliQuantity(20000, resource.DecimalSI),
							},
						},
					},
				},
				err: nil,
			},
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := NewCPURecommender()
			_, recommendedResources, err := r.GetRecommendedPodResources(tc.spd, []*corev1.Pod{})
			assert.Equal(t, tc.want.recommendedResources, recommendedResources)
			assert.IsType(t, tc.want.err, err)
		})
	}
}
