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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	utilmetric "github.com/kubewharf/katalyst-core/pkg/util/metric"
)

func TestMetricExpire(t *testing.T) {
	t.Parallel()

	updateTime := time.Now().Add(-65 * time.Second)
	metricData := utilmetric.MetricData{
		Time: &updateTime,
	}
	checkMetricDataExpire := checkMetricDataExpireFunc(time.Duration(0))
	_, err := checkMetricDataExpire(metricData, nil)
	assert.NoError(t, err)

	checkMetricDataExpire = checkMetricDataExpireFunc(64 * time.Second)
	_, err = checkMetricDataExpire(metricData, nil)
	assert.Equal(t, errMetricDataExpired, err)

	checkMetricDataExpire = checkMetricDataExpireFunc(66 * time.Second)
	_, err = checkMetricDataExpire(metricData, nil)
	assert.NoError(t, err)

	expectErr := errors.New("test")
	_, err = checkMetricDataExpire(metricData, expectErr)
	assert.Equal(t, expectErr, err)

	unixTime := time.Unix(0, 0)
	metricData.Time = &unixTime
	checkMetricDataExpire = checkMetricDataExpireFunc(66 * time.Second)
	_, err = checkMetricDataExpire(metricData, nil)
	assert.NoError(t, err)

	emptyTime := time.Time{}
	metricData.Time = &emptyTime
	checkMetricDataExpire = checkMetricDataExpireFunc(66 * time.Second)
	_, err = checkMetricDataExpire(metricData, nil)
	assert.NoError(t, err)

	metricData.Time = nil
	checkMetricDataExpire = checkMetricDataExpireFunc(66 * time.Second)
	_, err = checkMetricDataExpire(metricData, nil)
	assert.NoError(t, err)
}
