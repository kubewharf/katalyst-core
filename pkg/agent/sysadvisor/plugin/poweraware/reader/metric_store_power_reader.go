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

package reader

import (
	"context"
	"time"

	"github.com/pkg/errors"

	"github.com/kubewharf/katalyst-core/pkg/consts"
	utilmetric "github.com/kubewharf/katalyst-core/pkg/util/metric"
)

// malachite realtime power metric server imposes delay of up to 2 seconds coupled with sampling interval of 1 sec
const powerTolerationTime = 3 * time.Second

type nodeMetricGetter interface {
	GetNodeMetric(metricName string) (utilmetric.MetricData, error)
}

type metricStorePowerReader struct {
	nodeMetricGetter
}

func (m *metricStorePowerReader) Init() error {
	return nil
}

func isDataFresh(data utilmetric.MetricData, now time.Time) bool {
	if data.Time == nil {
		return false
	}
	return now.Before(data.Time.Add(powerTolerationTime))
}

func (m *metricStorePowerReader) Get(ctx context.Context) (int, error) {
	return m.get(ctx, time.Now())
}

func (m *metricStorePowerReader) get(ctx context.Context, now time.Time) (int, error) {
	data, err := m.GetNodeMetric(consts.MetricTotalPowerUsedWatts)
	if err != nil {
		return 0, errors.Wrap(err, "failed to get metric from metric store")
	}

	if !isDataFresh(data, now) {
		return 0, errors.New("power data in metric store is stale")
	}

	return int(data.Value), nil
}

func (m *metricStorePowerReader) Cleanup() {}

func NewMetricStorePowerReader(nodeMetricGetter nodeMetricGetter) PowerReader {
	return &metricStorePowerReader{
		nodeMetricGetter: nodeMetricGetter,
	}
}
