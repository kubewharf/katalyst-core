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
	"fmt"

	"k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/utils/pointer"

	workloadapis "github.com/kubewharf/katalyst-api/pkg/apis/workload/v1alpha1"
)

type IndicatorTarget struct {
	UpperBound *float64
	LowerBound *float64
}

// GetServiceBusinessIndicatorTarget get service business indicator target from spd
// if there are duplicate upper bound or lower bound, return error
func GetServiceBusinessIndicatorTarget(spd *workloadapis.ServiceProfileDescriptor) (map[string]IndicatorTarget, error) {
	var errList []error
	indicators := make(map[string]IndicatorTarget)
	for _, indicator := range spd.Spec.BusinessIndicator {
		_, ok := indicators[string(indicator.Name)]
		if ok {
			errList = append(errList, fmt.Errorf("duplicate indicator %s", indicator.Name))
			continue
		}

		targetIndicator, err := getIndicatorTarget(indicator.Indicators)
		if err != nil {
			errList = append(errList, fmt.Errorf("get indicator %s failed: %v", indicator.Name, err))
		}

		indicators[string(indicator.Name)] = *targetIndicator
	}

	if len(errList) > 0 {
		return nil, fmt.Errorf("failed to get service business indicators: %v", errors.NewAggregate(errList))
	}

	return indicators, nil
}

// GetServiceSystemIndicatorTarget get service system indicator target from spd
// if there are duplicate upper bound or lower bound, return error
func GetServiceSystemIndicatorTarget(spd *workloadapis.ServiceProfileDescriptor) (map[string]IndicatorTarget, error) {
	var errList []error
	indicators := make(map[string]IndicatorTarget)
	for _, indicator := range spd.Spec.SystemIndicator {
		_, ok := indicators[string(indicator.Name)]
		if ok {
			errList = append(errList, fmt.Errorf("duplicate indicator %s", indicator.Name))
			continue
		}

		targetIndicator, err := getIndicatorTarget(indicator.Indicators)
		if err != nil || targetIndicator == nil {
			errList = append(errList, fmt.Errorf("get indicator %s failed: %v", indicator.Name, err))
			continue
		}

		indicators[string(indicator.Name)] = *targetIndicator
	}

	if len(errList) > 0 {
		return nil, fmt.Errorf("failed to get service system indicators: %v", errors.NewAggregate(errList))
	}

	return indicators, nil
}

// GetServiceBusinessIndicatorValue returns the current value of business indicators
// The returned map is a map from indicator name to its current value, and if duplicate
// indicators are found, the first one is used
func GetServiceBusinessIndicatorValue(spd *workloadapis.ServiceProfileDescriptor) (map[string]float64, error) {
	indicatorValues := make(map[string]float64)
	for _, indicator := range spd.Status.BusinessStatus {
		if indicator.Current == nil {
			continue
		}

		_, ok := indicatorValues[string(indicator.Name)]
		if ok {
			continue
		}

		indicatorValues[string(indicator.Name)] = float64(*indicator.Current)
	}

	return indicatorValues, nil
}

func getIndicatorTarget(indicators []workloadapis.Indicator) (*IndicatorTarget, error) {
	targetIndicator := &IndicatorTarget{}
	for _, target := range indicators {
		switch target.IndicatorLevel {
		case workloadapis.IndicatorLevelUpperBound:
			if targetIndicator.UpperBound != nil {
				return nil, fmt.Errorf("duplicate upper bound for indicator")
			}
			targetIndicator.UpperBound = pointer.Float64(float64(target.Value))
		case workloadapis.IndicatorLevelLowerBound:
			if targetIndicator.LowerBound != nil {
				return nil, fmt.Errorf("duplicate lower bound for indicator")
			}
			targetIndicator.LowerBound = pointer.Float64(float64(target.Value))
		default:
			return nil, fmt.Errorf("invalid indicator level %s", target.IndicatorLevel)
		}
	}

	// check whether target indicator is validated
	if targetIndicator.LowerBound != nil && targetIndicator.UpperBound != nil &&
		*targetIndicator.LowerBound > *targetIndicator.UpperBound {
		return nil, fmt.Errorf("lower bound %v is higher than uppoer bound %v for indicator",
			*targetIndicator.LowerBound, *targetIndicator.UpperBound)
	}

	return targetIndicator, nil
}
