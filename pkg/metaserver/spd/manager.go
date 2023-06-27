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

	v1 "k8s.io/api/core/v1"

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
)

type IndicatorTarget map[string]util.IndicatorTarget

type ServiceProfilingManager interface {
	// ServiceBusinessPerformanceLevel returns the service business performance level for the given pod
	ServiceBusinessPerformanceLevel(ctx context.Context, pod *v1.Pod) (PerformanceLevel, error)

	// ServiceBusinessPerformanceScore returns the service business performance score for the given pod
	// The score is in range [0, 100]
	ServiceBusinessPerformanceScore(ctx context.Context, pod *v1.Pod) (float64, error)

	// ServiceSystemPerformanceTarget returns the system performance target for the given pod
	ServiceSystemPerformanceTarget(ctx context.Context, pod *v1.Pod) (IndicatorTarget, error)

	// Run starts the service profiling manager
	Run(ctx context.Context)
}

type DummyServiceProfilingManager struct{}

func (d *DummyServiceProfilingManager) ServiceBusinessPerformanceLevel(_ context.Context, _ *v1.Pod) (PerformanceLevel, error) {
	return PerformanceLevelPerfect, nil
}

func (d *DummyServiceProfilingManager) ServiceBusinessPerformanceScore(_ context.Context, _ *v1.Pod) (float64, error) {
	return 100, nil
}

func (d *DummyServiceProfilingManager) ServiceSystemPerformanceTarget(_ context.Context, _ *v1.Pod) (IndicatorTarget, error) {
	return IndicatorTarget{}, nil
}

func (d *DummyServiceProfilingManager) Run(_ context.Context) {}

var _ ServiceProfilingManager = &DummyServiceProfilingManager{}

type serviceProfilingManager struct {
	fetcher SPDFetcher
}

func NewServiceProfilingManager(fetcher SPDFetcher) ServiceProfilingManager {
	return &serviceProfilingManager{
		fetcher: fetcher,
	}
}

func (m *serviceProfilingManager) ServiceBusinessPerformanceScore(_ context.Context, _ *v1.Pod) (float64, error) {
	// todo: implement service business performance score using spd to calculate
	return 1., nil
}

// ServiceBusinessPerformanceLevel gets the service business performance level by spd, and use the poorest business indicator
// performance level as the service business performance level.
func (m *serviceProfilingManager) ServiceBusinessPerformanceLevel(ctx context.Context, pod *v1.Pod) (PerformanceLevel, error) {
	spd, err := m.fetcher.GetSPD(ctx, pod)
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
func (m *serviceProfilingManager) ServiceSystemPerformanceTarget(ctx context.Context, pod *v1.Pod) (IndicatorTarget, error) {
	spd, err := m.fetcher.GetSPD(ctx, pod)
	if err != nil {
		return nil, err
	}

	return util.GetServiceSystemIndicatorTarget(spd)
}

func (m *serviceProfilingManager) Run(ctx context.Context) {
	m.fetcher.Run(ctx)
}
