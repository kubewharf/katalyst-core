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

package spd

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	workloadapis "github.com/kubewharf/katalyst-api/pkg/apis/workload/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/client/clientset/versioned/scheme"
	"github.com/kubewharf/katalyst-core/pkg/util"
)

// PerformanceLevel is an enumeration type, the smaller the
// positive value, the better the performance
type PerformanceLevel int

const (
	PerformanceLevelUnknown PerformanceLevel = -1
	PerformanceLevelPerfect PerformanceLevel = 0
	PerformanceLevelGood    PerformanceLevel = 1
	PerformanceLevelPoor    PerformanceLevel = 2

	MaxPerformanceScore float64 = 100
	MinPerformanceScore float64 = 0
)

type IndicatorTarget map[string]util.IndicatorTarget

type ServiceProfilingManager interface {
	// ServiceBusinessPerformanceLevel returns the service business performance level for the given pod
	ServiceBusinessPerformanceLevel(ctx context.Context, podMeta metav1.ObjectMeta) (PerformanceLevel, error)

	// ServiceBusinessPerformanceScore returns the service business performance score for the given pod
	// The score is in range [MinPerformanceScore, MaxPerformanceScore]
	ServiceBusinessPerformanceScore(ctx context.Context, podMeta metav1.ObjectMeta) (float64, error)

	// ServiceSystemPerformanceTarget returns the system performance target for the given pod
	ServiceSystemPerformanceTarget(ctx context.Context, podMeta metav1.ObjectMeta) (IndicatorTarget, error)

	// ServiceBaseline returns whether this pod is baseline
	ServiceBaseline(ctx context.Context, podMeta metav1.ObjectMeta) (bool, error)

	// ServiceExtendedIndicator load the extended indicators and return whether the pod is baseline for the extended indicators
	ServiceExtendedIndicator(ctx context.Context, podMeta metav1.ObjectMeta, indicators interface{}) (bool, error)

	// ServiceAggregateMetrics retrieves aggregated metrics from profiling data using different aggregators,
	// each designed for a specific level of aggregation:
	// - podAggregator: Aggregates metrics across different pods.
	// - containerAggregator: Aggregates metrics across different containers.
	// - metricsAggregator: Aggregates metrics across different time windows.
	ServiceAggregateMetrics(ctx context.Context, podMeta metav1.ObjectMeta, name v1.ResourceName, milliValue bool,
		podAggregator, containerAggregator, metricsAggregator workloadapis.Aggregator) (*resource.Quantity, error)

	// Run starts the service profiling manager
	Run(ctx context.Context)
}

type DummyPodServiceProfile struct {
	PerformanceLevel PerformanceLevel
	Score            float64
	AggregatedMetric *resource.Quantity
}

type DummyServiceProfilingManager struct {
	podProfiles map[types.UID]DummyPodServiceProfile
}

func (d *DummyServiceProfilingManager) ServiceAggregateMetrics(_ context.Context, podMeta metav1.ObjectMeta, _ v1.ResourceName,
	_ bool, _, _, _ workloadapis.Aggregator,
) (*resource.Quantity, error) {
	profile, ok := d.podProfiles[podMeta.UID]
	if !ok {
		return &resource.Quantity{}, nil
	}
	return profile.AggregatedMetric, nil
}

func (d *DummyServiceProfilingManager) ServiceExtendedIndicator(_ context.Context, _ metav1.ObjectMeta, _ interface{}) (bool, error) {
	return false, nil
}

func (d *DummyServiceProfilingManager) ServiceBaseline(_ context.Context, _ metav1.ObjectMeta) (bool, error) {
	return false, nil
}

func NewDummyServiceProfilingManager(podProfiles map[types.UID]DummyPodServiceProfile) *DummyServiceProfilingManager {
	return &DummyServiceProfilingManager{podProfiles: podProfiles}
}

func (d *DummyServiceProfilingManager) ServiceBusinessPerformanceLevel(_ context.Context, podMeta metav1.ObjectMeta) (PerformanceLevel, error) {
	profile, ok := d.podProfiles[podMeta.UID]
	if !ok {
		return PerformanceLevelPerfect, nil
	}
	return profile.PerformanceLevel, nil
}

func (d *DummyServiceProfilingManager) ServiceBusinessPerformanceScore(_ context.Context, podMeta metav1.ObjectMeta) (float64, error) {
	profile, ok := d.podProfiles[podMeta.UID]
	if !ok {
		return 100, nil
	}
	return profile.Score, nil
}

func (d *DummyServiceProfilingManager) ServiceSystemPerformanceTarget(_ context.Context, _ metav1.ObjectMeta) (IndicatorTarget, error) {
	return IndicatorTarget{}, nil
}

func (d *DummyServiceProfilingManager) Run(_ context.Context) {}

var _ ServiceProfilingManager = &DummyServiceProfilingManager{}

type serviceProfilingManager struct {
	fetcher SPDFetcher
}

// ServiceAggregateMetrics get service aggregate metrics by given resource name and aggregators
func (m *serviceProfilingManager) ServiceAggregateMetrics(ctx context.Context, podMeta metav1.ObjectMeta, name v1.ResourceName, milliValue bool,
	podAggregator, containerAggregator, metricsAggregator workloadapis.Aggregator,
) (*resource.Quantity, error) {
	spd, err := m.fetcher.GetSPD(ctx, podMeta)
	if err != nil {
		return nil, err
	}

	for _, metrics := range spd.Status.AggMetrics {
		if metrics.Aggregator != podAggregator {
			continue
		}

		aggregatedContainerMetrics := make([]resource.Quantity, 0, len(metrics.Items))
		for _, podMetrics := range metrics.Items {
			containerMetrics := make([]resource.Quantity, 0, len(podMetrics.Containers))
			for _, containerRawMetrics := range podMetrics.Containers {
				if metric, ok := containerRawMetrics.Usage[name]; ok {
					if milliValue {
						metric = *resource.NewQuantity(metric.MilliValue(), metric.Format)
					}
					containerMetrics = append(containerMetrics, metric)
				}
			}

			metric, err := util.AggregateMetrics(containerMetrics, containerAggregator)
			if err != nil {
				return nil, err
			}

			if metric != nil {
				aggregatedContainerMetrics = append(aggregatedContainerMetrics, *metric)
			}
		}

		metric, err := util.AggregateMetrics(aggregatedContainerMetrics, metricsAggregator)
		if err != nil {
			return nil, err
		}

		if metric != nil {
			if milliValue {
				metric = resource.NewMilliQuantity(metric.Value(), metric.Format)
			}
			return metric, nil
		}
	}

	return nil, errors.NewNotFound(workloadapis.Resource(workloadapis.ResourceNameServiceProfileDescriptors),
		fmt.Sprintf("%v for pod(%v/%v)", name, podMeta.Namespace, podMeta.Name))
}

func (m *serviceProfilingManager) ServiceExtendedIndicator(ctx context.Context, podMeta metav1.ObjectMeta, indicators interface{}) (bool, error) {
	spd, err := m.fetcher.GetSPD(ctx, podMeta)
	if err != nil {
		return false, err
	}

	extendedBaselineSentinel, err := util.GetSPDExtendedBaselineSentinel(spd)
	if err != nil {
		return false, err
	}

	name, o, err := util.GetExtendedIndicator(indicators)
	if err != nil {
		return false, err
	}

	for _, indicator := range spd.Spec.ExtendedIndicator {
		if indicator.Name != name {
			continue
		}

		object := indicator.Indicators.Object
		raw := indicator.Indicators.Raw
		if object == nil && raw == nil {
			return false, fmt.Errorf("%s inidators object is nil", name)
		}

		if object != nil {
			t := reflect.TypeOf(indicators)
			if t.Kind() != reflect.Ptr {
				return false, fmt.Errorf("indicators must be pointers to structs")
			}

			v := reflect.ValueOf(object)
			if !v.CanConvert(t) {
				return false, fmt.Errorf("%s indicators object cannot convert to %v", name, t.Name())
			}

			reflect.ValueOf(indicators).Elem().Set(v.Convert(t).Elem())
		} else {
			object, ok := indicators.(runtime.Object)
			if !ok {
				return false, fmt.Errorf("%s indicators object cannot convert to runtime.Object", name)
			}

			deserializer := scheme.Codecs.UniversalDeserializer()
			_, _, err := deserializer.Decode(raw, nil, object)
			if err != nil {
				return false, err
			}
		}

		return util.IsExtendedBaselinePod(podMeta, indicator.BaselinePercent, extendedBaselineSentinel, name)
	}

	return false, errors.NewNotFound(schema.GroupResource{
		Group:    workloadapis.GroupName,
		Resource: strings.ToLower(o.GetObjectKind().GroupVersionKind().Kind),
	}, name)
}

func (m *serviceProfilingManager) ServiceBaseline(ctx context.Context, podMeta metav1.ObjectMeta) (bool, error) {
	spd, err := m.fetcher.GetSPD(ctx, podMeta)
	if err != nil && !errors.IsNotFound(err) {
		return false, err
	} else if err != nil {
		return false, nil
	}

	baselineSentinel, err := util.GetSPDBaselineSentinel(spd)
	if err != nil {
		return false, err
	}

	isBaseline, err := util.IsBaselinePod(podMeta, spd.Spec.BaselinePercent, baselineSentinel)
	if err != nil {
		return false, err
	}

	return isBaseline, nil
}

func NewServiceProfilingManager(fetcher SPDFetcher) ServiceProfilingManager {
	return &serviceProfilingManager{
		fetcher: fetcher,
	}
}

func (m *serviceProfilingManager) ServiceBusinessPerformanceScore(_ context.Context, _ metav1.ObjectMeta) (float64, error) {
	// todo: implement service business performance score using SPD to calculate
	return MaxPerformanceScore, nil
}

// ServiceBusinessPerformanceLevel gets the service business performance level by SPD, and use the poorest business indicator
// performance level as the service business performance level.
func (m *serviceProfilingManager) ServiceBusinessPerformanceLevel(ctx context.Context, podMeta metav1.ObjectMeta) (PerformanceLevel, error) {
	spd, err := m.fetcher.GetSPD(ctx, podMeta)
	if err != nil {
		return PerformanceLevelUnknown, err
	}

	indicatorTarget, err := util.GetServiceBusinessIndicatorTarget(spd)
	if err != nil {
		return PerformanceLevelUnknown, err
	}

	indicatorValue, err := util.GetServiceBusinessIndicatorValue(spd)
	if err != nil {
		return PerformanceLevelUnknown, err
	}

	indicatorLevelMap := make(map[string]PerformanceLevel)
	for indicatorName, target := range indicatorTarget {
		if _, ok := indicatorValue[indicatorName]; !ok {
			indicatorLevelMap[indicatorName] = PerformanceLevelUnknown
			continue
		}

		if target.UpperBound != nil && indicatorValue[indicatorName] > *target.UpperBound {
			indicatorLevelMap[indicatorName] = PerformanceLevelPoor
		} else if target.LowerBound != nil && indicatorValue[indicatorName] < *target.LowerBound {
			indicatorLevelMap[indicatorName] = PerformanceLevelPerfect
		} else {
			indicatorLevelMap[indicatorName] = PerformanceLevelGood
		}
	}

	// calculate the poorest performance level of indicator as the final performance level
	result := PerformanceLevelUnknown
	for indicator, level := range indicatorLevelMap {
		// if indicator level unknown just return error because indicator current value not found
		if level == PerformanceLevelUnknown {
			return PerformanceLevelUnknown, fmt.Errorf("indicator %s current value not found", indicator)
		}

		// choose the higher value of performance level, which is has poorer performance
		if result < level {
			result = level
		}
	}

	return result, nil
}

// ServiceSystemPerformanceTarget gets the service system performance target by spd and return the indicator name
// and its upper and lower bounds
func (m *serviceProfilingManager) ServiceSystemPerformanceTarget(ctx context.Context, podMeta metav1.ObjectMeta) (IndicatorTarget, error) {
	spd, err := m.fetcher.GetSPD(ctx, podMeta)
	if err != nil {
		return nil, err
	}

	return util.GetServiceSystemIndicatorTarget(spd)
}

func (m *serviceProfilingManager) Run(ctx context.Context) {
	m.fetcher.Run(ctx)
}
