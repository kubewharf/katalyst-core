//go:build linux
// +build linux

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

package helper

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

func TestGetDeviceMetric(t *testing.T) {
	t.Parallel()
	_, err := GetDeviceMetricWithTime(metric.NewFakeMetricsFetcher(metrics.DummyMetrics{}), metrics.DummyMetrics{}, "not-exist", "sdb")
	assert.NotNil(t, err)
	_, err = GetDeviceMetric(metric.NewFakeMetricsFetcher(metrics.DummyMetrics{}), metrics.DummyMetrics{}, "not-exist", "sdb")
	assert.NotNil(t, err)
}
