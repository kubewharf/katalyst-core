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

	"github.com/kubewharf/katalyst-core/pkg/client"
	pkgconfig "github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/cnc"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
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

type ServiceProfilingManager interface {
	// ServiceBusinessPerformanceLevel returns the service business performance level for the given pod
	ServiceBusinessPerformanceLevel(ctx context.Context, pod *v1.Pod) (PerformanceLevel, error)

	// ServiceBusinessPerformanceScore returns the service business performance score for the given pod
	// The score is in range [0, 100]
	ServiceBusinessPerformanceScore(ctx context.Context, pod *v1.Pod) (float64, error)

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

func (d *DummyServiceProfilingManager) Run(_ context.Context) {}

var _ ServiceProfilingManager = &DummyServiceProfilingManager{}

type serviceProfilingManager struct {
	fetcher SPDFetcher
}

func NewServiceProfilingManager(clientSet *client.GenericClientSet, emitter metrics.MetricEmitter,
	cncFetcher cnc.CNCFetcher, conf *pkgconfig.Configuration) (ServiceProfilingManager, error) {
	fetcher, err := NewSPDFetcher(clientSet, emitter, cncFetcher, conf)
	if err != nil {
		return nil, fmt.Errorf("initializes service profiling manager failed: %s", err)
	}

	return &serviceProfilingManager{
		fetcher: fetcher,
	}, nil
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

func (m *serviceProfilingManager) Run(ctx context.Context) {
	m.fetcher.Run(ctx)
}
