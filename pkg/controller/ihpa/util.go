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
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
	v2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
	"k8s.io/metrics/pkg/apis/metrics/v1beta1"

	"github.com/kubewharf/katalyst-api/pkg/apis/autoscaling/v1alpha2"
	apiworkload "github.com/kubewharf/katalyst-api/pkg/apis/workload/v1alpha1"
	apimetric "github.com/kubewharf/katalyst-api/pkg/metric"
	resourceportrait "github.com/kubewharf/katalyst-core/pkg/controller/spd/indicator-plugin/plugins/resource-portrait"
)

func generateOwnerReference(ihpa *v1alpha2.IntelligentHorizontalPodAutoscaler) metav1.OwnerReference {
	return metav1.OwnerReference{
		APIVersion: "autoscaling.katalyst.kubewharf.io/v1alpha2",
		Kind:       "IntelligentHorizontalPodAutoscaler",
		Name:       ihpa.GetName(),
		UID:        ihpa.GetUID(),
	}
}

func generateMetricSpecs(ihpa *v1alpha2.IntelligentHorizontalPodAutoscaler, podTemplate *corev1.PodTemplateSpec) []v2.MetricSpec {
	metricSpecs := make([]v2.MetricSpec, 0)
	resourceMetrics := make([]v2.MetricSpec, 0)
	// only for cpu/memory AverageUtilization without setting prediction metric target average value
	cpuSum, memorySum := getAllCPUAndMemoryRequests(podTemplate)
	for _, metric := range ihpa.Spec.Autoscaler.Metrics {
		if metric.Metric != nil {
			if metric.Metric.Resource != nil && metric.Metric.Resource.Target.AverageUtilization != nil {
				resourceMetrics = append(resourceMetrics, *metric.Metric)
			}
			metricSpecs = append(metricSpecs, *metric.Metric)
		}
	}
	for _, metric := range ihpa.Spec.Autoscaler.Metrics {
		if metric.Metric == nil && metric.CustomMetric != nil {
			metricSpec := v2.MetricSpec{
				Type: v2.ExternalMetricSourceType,
				External: &v2.ExternalMetricSource{
					Metric: v2.MetricIdentifier{
						Name: apimetric.MetricNameSPDAggMetrics,
						Selector: &metav1.LabelSelector{
							MatchLabels: map[string]string{
								apimetric.MetricSelectorKeySPDName:          ihpa.Spec.Autoscaler.ScaleTargetRef.Name,
								apimetric.MetricSelectorKeySPDResourceName:  string(metric.CustomMetric.Identify),
								apimetric.MetricSelectorKeySPDContainerName: ResourcePortraitContainerName,
								apimetric.MetricSelectorKeySPDScopeName:     resourceportrait.ResourcePortraitPluginName,
							},
						},
					},
					Target: v2.MetricTarget{
						Type:         v2.AverageValueMetricType,
						AverageValue: resource.NewMilliQuantity(metric.CustomMetric.Value.MilliValue(), resource.DecimalSI),
					},
				},
			}

			if metric.CustomMetric.Value.MilliValue() == 0 {
				if strings.HasPrefix(string(metric.CustomMetric.Identify), string(corev1.ResourceCPU)) {
					for _, hpaMetric := range resourceMetrics {
						if hpaMetric.Resource.Name != corev1.ResourceCPU {
							continue
						}
						metricSpec.External.Target.AverageValue = resource.NewMilliQuantity(int64(float64(cpuSum)*float64(*hpaMetric.Resource.Target.AverageUtilization)/100.), resource.DecimalSI)
					}
				} else if strings.HasPrefix(string(metric.CustomMetric.Identify), string(corev1.ResourceMemory)) {
					for _, hpaMetric := range resourceMetrics {
						if hpaMetric.Resource.Name != corev1.ResourceMemory {
							continue
						}
						metricSpec.External.Target.AverageValue = resource.NewMilliQuantity(int64(float64(memorySum)*float64(*hpaMetric.Resource.Target.AverageUtilization)/100.), resource.DecimalSI)
					}
				}
			}

			metricSpecs = append(metricSpecs, metricSpec)
		}
	}

	return metricSpecs
}

func generateHPA(ihpa *v1alpha2.IntelligentHorizontalPodAutoscaler, podTemplate *corev1.PodTemplateSpec) *v2.HorizontalPodAutoscaler {
	hpa := v2.HorizontalPodAutoscaler{ObjectMeta: metav1.ObjectMeta{Namespace: ihpa.Namespace, Name: generateHPAName(ihpa.Name)}}
	hpa.OwnerReferences = append(hpa.OwnerReferences, generateOwnerReference(ihpa))
	hpa.Spec.ScaleTargetRef = ihpa.Spec.Autoscaler.ScaleTargetRef
	hpa.Spec.MaxReplicas = ihpa.Spec.Autoscaler.MaxReplicas
	hpa.Spec.MinReplicas = ihpa.Spec.Autoscaler.MinReplicas
	hpa.Spec.Behavior = ihpa.Spec.Autoscaler.Behavior
	hpa.Spec.Metrics = generateMetricSpecs(ihpa, podTemplate)

	if ihpa.Spec.ScaleStrategy == v1alpha2.Preview {
		hpa.Spec.ScaleTargetRef = v2.CrossVersionObjectReference{
			Kind:       "VirtualWorkload",
			Name:       ihpa.Name,
			APIVersion: "autoscaling.katalyst.kubewharf.io/v1alpha2",
		}
	}

	if len(ihpa.Spec.TimeBounds) > 0 {
		hpa.Spec.MinReplicas, hpa.Spec.MaxReplicas = calculateCronReplicas(hpa.Spec.MinReplicas, hpa.Spec.MaxReplicas, ihpa.Spec.TimeBounds)
	}

	return &hpa
}

func calculateCronReplicas(min *int32, max int32, timeBounds []v1alpha2.TimeBound) (*int32, int32) {
	if len(timeBounds) == 0 {
		return min, max
	}

	now := time.Now()
	for _, timeBound := range timeBounds {
		if !isTimeBoundValid(now, timeBound.Start.Time, timeBound.End.Time) {
			continue
		}
		for _, bound := range timeBound.Bounds {
			schedule, err := cron.ParseStandard(bound.CronTab)
			if err != nil {
				klog.Errorf("ihpa parse crontab err: %v", err)
				continue
			}

			// The timing granularity of CronTab is minute level, and the time is aligned to the minute level.
			nextTime := schedule.Next(now.Add(-time.Minute))
			klog.V(5).InfoS("[ihpa] crontab next time", "now", now, "next", nextTime, "now/next", fmt.Sprintf("%d/%d", now.Unix()/60, nextTime.Unix()/60))
			if now.Unix()/60 != nextTime.Unix()/60 {
				continue
			}

			if bound.MinReplicas != nil {
				min = bound.MinReplicas
			}
			if bound.MaxReplicas != nil {
				max = *bound.MaxReplicas
			}
		}
	}
	return min, max
}

func isTimeBoundValid(now, start, end time.Time) bool {
	_, offset := now.Zone()
	now = now.Add(time.Duration(offset) * time.Second)

	klog.V(5).InfoS("[ihpa] time bound", "now", now, "start", start, "end", end)
	if start.IsZero() && end.IsZero() {
		return true
	}

	if !start.IsZero() && !end.IsZero() {
		return now.After(start) && now.Before(end)
	} else if !start.IsZero() {
		return now.After(start)
	} else {
		return now.Before(end)
	}
}

func updateStatus(ihpa *v1alpha2.IntelligentHorizontalPodAutoscaler, hpa *v2.HorizontalPodAutoscaler, spd *apiworkload.ServiceProfileDescriptor) {
	if ihpa == nil || hpa == nil || spd == nil {
		return
	}

	ihpa.Status.LastScaleTime = hpa.Status.LastScaleTime
	ihpa.Status.CurrentReplicas = hpa.Status.CurrentReplicas
	ihpa.Status.CurrentMetrics = hpa.Status.CurrentMetrics

	for _, aggMetrics := range spd.Status.AggMetrics {
		if aggMetrics.Scope != resourceportrait.ResourcePortraitPluginName {
			continue
		}

		var currentMetric *v1beta1.PodMetrics
		now := time.Now()
		for _, item := range aggMetrics.Items {
			if now.After(item.Timestamp.Time) {
				currentMetric = item.DeepCopy()
			} else {
				break
			}
		}
		if currentMetric == nil {
			break
		}

		var containerMetrics *v1beta1.ContainerMetrics
		for _, item := range currentMetric.Containers {
			if item.Name == ResourcePortraitContainerName {
				containerMetrics = &item
			}
		}
		if containerMetrics == nil {
			break
		}

		var desiredReplicasMax int32
		for resourceName, resourceQuantity := range containerMetrics.Usage {
			for _, metricSpec := range hpa.Spec.Metrics {
				if metricSpec.Type != v2.ExternalMetricSourceType {
					continue
				}
				if metricSpec.External == nil ||
					metricSpec.External.Metric.Selector == nil ||
					metricSpec.External.Target.AverageValue == nil {
					continue
				}
				if metricSpec.External.Metric.Name != apimetric.MetricNameSPDAggMetrics ||
					metricSpec.External.Metric.Selector.MatchLabels[apimetric.MetricSelectorKeySPDResourceName] != string(resourceName) ||
					metricSpec.External.Metric.Selector.MatchLabels[apimetric.MetricSelectorKeySPDName] != ihpa.Spec.Autoscaler.ScaleTargetRef.Name ||
					metricSpec.External.Metric.Selector.MatchLabels[apimetric.MetricSelectorKeySPDContainerName] != ResourcePortraitContainerName ||
					metricSpec.External.Metric.Selector.MatchLabels[apimetric.MetricSelectorKeySPDScopeName] != resourceportrait.ResourcePortraitPluginName {
					continue
				}
				if metricSpec.External.Target.AverageValue.MilliValue() == 0 {
					continue
				}

				desiredReplicas := int32(math.Ceil(float64(resourceQuantity.MilliValue()) / float64(metricSpec.External.Target.AverageValue.MilliValue())))
				if desiredReplicasMax < desiredReplicas {
					desiredReplicasMax = desiredReplicas
				}
			}
		}
		ihpa.Status.DesiredReplicas = desiredReplicasMax
	}
}

func getAllCPUAndMemoryRequests(podTemplate *corev1.PodTemplateSpec) (int64, int64) {
	var cpuSum, memorySum int64
	for _, c := range podTemplate.Spec.Containers {
		if val, ok := c.Resources.Requests[corev1.ResourceCPU]; ok {
			cpuSum += val.MilliValue()
		}
		if val, ok := c.Resources.Requests[corev1.ResourceMemory]; ok {
			memorySum += val.MilliValue()
		}
	}
	return cpuSum, memorySum
}

func generateHPAName(ihpaName string) string {
	return fmt.Sprintf("%s-%s", IHPAControllerName, ihpaName)
}
