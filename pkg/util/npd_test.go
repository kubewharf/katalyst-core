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

package util

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
)

func TestInsertNPDScopedNodeMetrics(t *testing.T) {
	t.Parallel()

	status := &v1alpha1.NodeProfileDescriptorStatus{
		NodeMetrics: []v1alpha1.ScopedNodeMetrics{
			{
				Scope: "test",
				Metrics: []v1alpha1.MetricValue{
					{
						MetricName: "cpu",
						Value:      resource.MustParse("1"),
					},
				},
			},
		},
	}
	metrics := &v1alpha1.ScopedNodeMetrics{
		Scope: "test",
		Metrics: []v1alpha1.MetricValue{
			{
				MetricName: "cpu",
				Value:      resource.MustParse("2"),
			},
		},
	}
	expectedStatus := v1alpha1.NodeProfileDescriptorStatus{
		NodeMetrics: []v1alpha1.ScopedNodeMetrics{
			{
				Scope: "test",
				Metrics: []v1alpha1.MetricValue{
					{
						MetricName: "cpu",
						Value:      resource.MustParse("2"),
					},
				},
			},
		},
	}

	InsertNPDScopedNodeMetrics(status, metrics)
	assert.Equal(t, expectedStatus, *status)

	InsertNPDScopedNodeMetrics(status, nil)
	assert.Equal(t, expectedStatus, *status)
}

func TestInsertNPDScopedPodMetrics(t *testing.T) {
	t.Parallel()

	status := &v1alpha1.NodeProfileDescriptorStatus{
		PodMetrics: []v1alpha1.ScopedPodMetrics{
			{
				Scope: "test",
				PodMetrics: []v1alpha1.PodMetric{
					{
						Name:      "testPod",
						Namespace: "default",
						Metrics: []v1alpha1.MetricValue{
							{
								MetricName: "cpu",
								Value:      resource.MustParse("1"),
							},
						},
					},
				},
			},
		},
	}
	metrics := &v1alpha1.ScopedPodMetrics{
		Scope: "test",
		PodMetrics: []v1alpha1.PodMetric{
			{
				Name:      "testPod",
				Namespace: "default",
				Metrics: []v1alpha1.MetricValue{
					{
						MetricName: "cpu",
						Value:      resource.MustParse("2"),
					},
					{
						MetricName: "memory",
						Value:      resource.MustParse("4Gi"),
					},
				},
			},
		},
	}
	expectedStatus := v1alpha1.NodeProfileDescriptorStatus{
		PodMetrics: []v1alpha1.ScopedPodMetrics{
			{
				Scope: "test",
				PodMetrics: []v1alpha1.PodMetric{
					{
						Name:      "testPod",
						Namespace: "default",
						Metrics: []v1alpha1.MetricValue{
							{
								MetricName: "cpu",
								Value:      resource.MustParse("2"),
							},
							{
								MetricName: "memory",
								Value:      resource.MustParse("4Gi"),
							},
						},
					},
				},
			},
		},
	}

	InsertNPDScopedPodMetrics(status, metrics)
	assert.Equal(t, expectedStatus, *status)

	InsertNPDScopedPodMetrics(status, nil)
	assert.Equal(t, expectedStatus, *status)
}
