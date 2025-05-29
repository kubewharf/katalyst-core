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

// malachite compute metric server imposes delay of up to 10 seconds coupled with sampling interval of 5 sec
const cpuFreqTolerationTime = 15 * time.Second

type CPUFreqReader struct {
	NodeMetricGetter
}

func (c *CPUFreqReader) Init() error {
	return nil
}

func (c *CPUFreqReader) Get(ctx context.Context) (int, error) {
	return c.get(ctx, time.Now())
}

func (m *CPUFreqReader) get(ctx context.Context, now time.Time) (int, error) {
	data, err := m.GetNodeMetric(consts.MetricScalingCPUFreqMHZ)
	if err != nil {
		return 0, errors.Wrap(err, "failed to get cpu freq from metric store")
	}

	// 0 actually is error, typically caused by null response from malachite realtime power service
	if data.Value == 0 {
		return 0, errors.New("got invalid 0 cpu freq from metric store")
	}

	if !isCPUFreqDataFresh(data, now) {
		return 0, errors.New("cpu freq in metric store is stale")
	}

	return int(data.Value), nil
}

func isCPUFreqDataFresh(data utilmetric.MetricData, now time.Time) bool {
	if data.Time == nil {
		return false
	}
	return now.Before(data.Time.Add(cpuFreqTolerationTime))
}

func (c *CPUFreqReader) Cleanup() {}

func NewCPUFreqReader(nodeMetricGetter NodeMetricGetter) MetricReader {
	return &CPUFreqReader{
		NodeMetricGetter: nodeMetricGetter,
	}
}
