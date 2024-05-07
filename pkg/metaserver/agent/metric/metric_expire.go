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
	"errors"
	"time"

	utilmetric "github.com/kubewharf/katalyst-core/pkg/util/metric"
)

const (
	minimumMetricInsurancePeriod = 60 * time.Second
)

var errMetricDataExpired = errors.New("metric data expired")

func IsMetricDataExpired(err error) bool {
	return errors.Is(err, errMetricDataExpired)
}

type CheckMetricDataExpireFunc func(utilmetric.MetricData, error) (utilmetric.MetricData, error)

func checkMetricDataExpireFunc(metricsInsurancePeriod time.Duration) CheckMetricDataExpireFunc {
	if metricsInsurancePeriod == 0 {
		return func(metricData utilmetric.MetricData, err error) (utilmetric.MetricData, error) {
			return metricData, err
		}
	}

	if metricsInsurancePeriod < minimumMetricInsurancePeriod {
		metricsInsurancePeriod = minimumMetricInsurancePeriod
	}

	return func(metricData utilmetric.MetricData, err error) (utilmetric.MetricData, error) {
		if err != nil {
			return metricData, err
		}

		if metricData.Time == nil || metricData.Time.IsZero() || metricData.Time.Unix() == 0 {
			return metricData, nil
		}

		expireAt := time.Now().Add(-metricsInsurancePeriod)
		if metricData.Time.Before(expireAt) {
			return metricData, errMetricDataExpired
		}

		return metricData, nil
	}
}
