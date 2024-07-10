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

package ihpa

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	v2 "k8s.io/api/autoscaling/v2"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/metrics/pkg/apis/metrics/v1beta1"
	"k8s.io/utils/pointer"

	"github.com/kubewharf/katalyst-api/pkg/apis/autoscaling/v1alpha2"
	apiworkload "github.com/kubewharf/katalyst-api/pkg/apis/workload/v1alpha1"
	katalystmetric "github.com/kubewharf/katalyst-api/pkg/metric"
	resourceportrait "github.com/kubewharf/katalyst-core/pkg/controller/spd/indicator-plugin/plugins/resource-portrait"
)

func Test_generateOwnerReference(t *testing.T) {
	t.Parallel()
	t.Run("normal", func(t *testing.T) {
		t.Parallel()
		ihpa := v1alpha2.IntelligentHorizontalPodAutoscaler{
			ObjectMeta: metav1.ObjectMeta{Name: "test"},
		}
		expected := metav1.OwnerReference{
			APIVersion: "autoscaling.katalyst.kubewharf.io/v1alpha2",
			Kind:       "IntelligentHorizontalPodAutoscaler",
			Name:       "test",
		}
		owner := generateOwnerReference(&ihpa)
		assert.Equal(t, expected, owner)
	})
}

func Test_calculateCronReplicas(t *testing.T) {
	t.Parallel()
	t.Run("normal", func(t *testing.T) {
		t.Parallel()

		var min int32 = 1
		var max int32 = 10
		bounds := []v1alpha2.TimeBound{
			{
				Start: metav1.Time{Time: time.Now()},
				End:   metav1.Time{Time: time.Now().Add(time.Minute)},
				Bounds: []v1alpha2.Bound{
					{
						CronTab:     "* * * * *",
						MaxReplicas: &max,
						MinReplicas: &min,
					},
				},
			},
			{
				End: metav1.Time{Time: time.Now().Add(time.Minute)},
				Bounds: []v1alpha2.Bound{
					{
						CronTab:     "* * * * *",
						MaxReplicas: &max,
						MinReplicas: &min,
					},
				},
			},
			{
				Start: metav1.Time{Time: time.Now()},
				Bounds: []v1alpha2.Bound{
					{
						CronTab:     "* * * * *",
						MaxReplicas: &max,
						MinReplicas: &min,
					},
				},
			},
			{
				Bounds: []v1alpha2.Bound{
					{
						CronTab:     "* * * * *",
						MaxReplicas: &min,
						MinReplicas: &min,
					},
				},
			},
		}
		minPtr, max := calculateCronReplicas(&min, max, bounds)
		assert.Equal(t, int32(1), *minPtr)
		assert.Equal(t, int32(1), max)
	})
}

func Test_generateMetricSpecs(t *testing.T) {
	t.Parallel()
	t.Run("normal", func(t *testing.T) {
		t.Parallel()
		ihpa := v1alpha2.IntelligentHorizontalPodAutoscaler{
			Spec: v1alpha2.IntelligentHorizontalPodAutoscalerSpec{
				Autoscaler: v1alpha2.AutoscalerSpec{
					Metrics: []v1alpha2.MetricSpec{
						{
							Metric: &v2.MetricSpec{},
						},
						{
							Metric: &v2.MetricSpec{
								Type: v2.ResourceMetricSourceType,
								Resource: &v2.ResourceMetricSource{
									Name: v1.ResourceCPU,
									Target: v2.MetricTarget{
										Type:               v2.UtilizationMetricType,
										AverageUtilization: pointer.Int32(50),
									},
								},
							},
						},
						{
							Metric: &v2.MetricSpec{
								Type: v2.ResourceMetricSourceType,
								Resource: &v2.ResourceMetricSource{
									Name: v1.ResourceMemory,
									Target: v2.MetricTarget{
										Type:               v2.UtilizationMetricType,
										AverageUtilization: pointer.Int32(50),
									},
								},
							},
						},
						{
							CustomMetric: &v1alpha2.CustomMetricSpec{
								Identify: "test",
								Query:    "test",
								Value:    resource.NewMilliQuantity(int64(20), resource.BinarySI),
							},
						},
						{
							CustomMetric: &v1alpha2.CustomMetricSpec{
								Identify: "cpu",
								Query:    "test",
								Value:    resource.NewMilliQuantity(0, resource.BinarySI),
							},
						},
					},
				},
			},
		}

		podTemplate := v1.PodTemplateSpec{Spec: v1.PodSpec{
			Containers: []v1.Container{
				{
					Resources: v1.ResourceRequirements{
						Requests: v1.ResourceList{
							v1.ResourceCPU:    resource.MustParse("20m"),
							v1.ResourceMemory: resource.MustParse("20m"),
						},
					},
				},
			},
		}}

		expected := []v2.MetricSpec{
			{}, {
				Type: v2.ResourceMetricSourceType,
				Resource: &v2.ResourceMetricSource{
					Name: v1.ResourceCPU,
					Target: v2.MetricTarget{
						Type:               v2.UtilizationMetricType,
						AverageUtilization: pointer.Int32(50),
					},
				},
			}, {
				Type: v2.ResourceMetricSourceType,
				Resource: &v2.ResourceMetricSource{
					Name: v1.ResourceMemory,
					Target: v2.MetricTarget{
						Type:               v2.UtilizationMetricType,
						AverageUtilization: pointer.Int32(50),
					},
				},
			}, {
				Type: v2.ExternalMetricSourceType,
				External: &v2.ExternalMetricSource{
					Metric: v2.MetricIdentifier{
						Name: katalystmetric.MetricNameSPDAggMetrics,
						Selector: &metav1.LabelSelector{
							MatchLabels: map[string]string{
								katalystmetric.MetricSelectorKeySPDName:          ihpa.Name,
								katalystmetric.MetricSelectorKeySPDResourceName:  "test",
								katalystmetric.MetricSelectorKeySPDContainerName: ResourcePortraitContainerName,
								katalystmetric.MetricSelectorKeySPDScopeName:     resourceportrait.ResourcePortraitPluginName,
							},
						},
					},
					Target: v2.MetricTarget{
						Type:         v2.AverageValueMetricType,
						AverageValue: resource.NewMilliQuantity(20, resource.DecimalSI),
					},
				},
			}, {
				Type: v2.ExternalMetricSourceType,
				External: &v2.ExternalMetricSource{
					Metric: v2.MetricIdentifier{
						Name: katalystmetric.MetricNameSPDAggMetrics,
						Selector: &metav1.LabelSelector{
							MatchLabels: map[string]string{
								katalystmetric.MetricSelectorKeySPDName:          ihpa.Name,
								katalystmetric.MetricSelectorKeySPDResourceName:  "cpu",
								katalystmetric.MetricSelectorKeySPDContainerName: ResourcePortraitContainerName,
								katalystmetric.MetricSelectorKeySPDScopeName:     resourceportrait.ResourcePortraitPluginName,
							},
						},
					},
					Target: v2.MetricTarget{
						Type:         v2.AverageValueMetricType,
						AverageValue: resource.NewMilliQuantity(10, resource.DecimalSI),
					},
				},
			},
		}
		metricSpecs := generateMetricSpecs(&ihpa, &podTemplate)

		assert.Equal(t, expected, metricSpecs)
	})
}

func Test_generateHPA(t *testing.T) {
	t.Parallel()
	t.Run("normal", func(t *testing.T) {
		t.Parallel()
		var min int32 = 1
		ihpa := v1alpha2.IntelligentHorizontalPodAutoscaler{
			ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "test"},
			Spec: v1alpha2.IntelligentHorizontalPodAutoscalerSpec{
				Autoscaler:    v1alpha2.AutoscalerSpec{},
				ScaleStrategy: "Preview",
				TimeBounds: []v1alpha2.TimeBound{
					{
						Start: metav1.Time{Time: time.Now().Add(-time.Minute)},
						End:   metav1.Time{Time: time.Now().Add(time.Minute)},
						Bounds: []v1alpha2.Bound{
							{
								CronTab:     "* * * * *",
								MaxReplicas: &min,
								MinReplicas: &min,
							},
						},
					},
				},
			},
		}

		expected := &v2.HorizontalPodAutoscaler{
			ObjectMeta: metav1.ObjectMeta{Name: "ihpa-test", Namespace: "test", OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "autoscaling.katalyst.kubewharf.io/v1alpha2",
				Kind:       "IntelligentHorizontalPodAutoscaler",
				Name:       "test",
			}}},
			Spec: v2.HorizontalPodAutoscalerSpec{
				Metrics: []v2.MetricSpec{},
				ScaleTargetRef: v2.CrossVersionObjectReference{
					Kind:       "VirtualWorkload",
					Name:       ihpa.Name,
					APIVersion: "autoscaling.katalyst.kubewharf.io/v1alpha2",
				},
				MaxReplicas: 1,
				MinReplicas: &min,
			},
		}
		got := generateHPA(&ihpa, &v1.PodTemplateSpec{})
		assert.Equal(t, expected, got)
	})
}

func Test_updateStatus(t *testing.T) {
	t.Parallel()
	t.Run("normal", func(t *testing.T) {
		t.Parallel()

		now := time.Now()
		ihpa := &v1alpha2.IntelligentHorizontalPodAutoscaler{}
		expected := ihpa.DeepCopy()
		updateStatus(ihpa, nil, nil)
		assert.Equal(t, expected.Status, ihpa.Status)

		hpa := &v2.HorizontalPodAutoscaler{
			Spec: v2.HorizontalPodAutoscalerSpec{
				Metrics: []v2.MetricSpec{
					{
						Type: v2.ExternalMetricSourceType,
						External: &v2.ExternalMetricSource{
							Metric: v2.MetricIdentifier{
								Name: katalystmetric.MetricNameSPDAggMetrics,
								Selector: &metav1.LabelSelector{
									MatchLabels: map[string]string{
										katalystmetric.MetricSelectorKeySPDName:          "",
										katalystmetric.MetricSelectorKeySPDContainerName: ResourcePortraitContainerName,
										katalystmetric.MetricSelectorKeySPDResourceName:  "test",
										katalystmetric.MetricSelectorKeySPDScopeName:     resourceportrait.ResourcePortraitPluginName,
									},
								},
							},
							Target: v2.MetricTarget{
								Type:         v2.AverageValueMetricType,
								AverageValue: resource.NewMilliQuantity(10, resource.DecimalSI),
							},
						},
					},
				},
			},
			Status: v2.HorizontalPodAutoscalerStatus{
				LastScaleTime:   &metav1.Time{Time: now},
				CurrentReplicas: 5,
			},
		}

		ihpa.Spec.Autoscaler.Metrics = append(ihpa.Spec.Autoscaler.Metrics, v1alpha2.MetricSpec{})
		ihpa.Spec.Autoscaler.Metrics = append(ihpa.Spec.Autoscaler.Metrics, v1alpha2.MetricSpec{
			CustomMetric: &v1alpha2.CustomMetricSpec{Identify: "test-x"},
		})
		ihpa.Spec.Autoscaler.Metrics = append(ihpa.Spec.Autoscaler.Metrics, v1alpha2.MetricSpec{
			CustomMetric: &v1alpha2.CustomMetricSpec{
				Identify: "test",
				Query:    "test",
				Value:    resource.NewMilliQuantity(int64(10), resource.BinarySI),
			},
		})

		spd := &apiworkload.ServiceProfileDescriptor{
			Status: apiworkload.ServiceProfileDescriptorStatus{
				AggMetrics: []apiworkload.AggPodMetrics{
					{
						Scope: "test",
					},
					{
						Scope: resourceportrait.ResourcePortraitPluginName,
						Items: []v1beta1.PodMetrics{
							{
								Timestamp: metav1.Time{Time: time.Now().Add(-time.Minute)},
								Containers: []v1beta1.ContainerMetrics{
									{
										Name: ResourcePortraitContainerName,
										Usage: v1.ResourceList{
											"test": *resource.NewMilliQuantity(int64(100), resource.BinarySI),
										},
									},
								},
							},
							{
								Timestamp: metav1.Time{Time: time.Now().Add(time.Minute)},
							},
						},
					},
				},
			},
		}

		expected.Status.CurrentReplicas = 5
		expected.Status.DesiredReplicas = 10
		expected.Status.LastScaleTime = &metav1.Time{Time: now}
		updateStatus(ihpa, hpa, spd)
		assert.Equal(t, expected.Status, ihpa.Status)
	})
}
