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

	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

func TestGetCpuCodeName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		setFakeMetric func(store *metric.FakeMetricsFetcher)
		want          string
	}{
		{
			name: "test",
			setFakeMetric: func(store *metric.FakeMetricsFetcher) {
				store.SetByStringIndex(consts.MetricCPUCodeName, "abc")
			},
			want: "abc",
		},
		{
			name: "test",
			setFakeMetric: func(store *metric.FakeMetricsFetcher) {
				store.SetByStringIndex(consts.MetricCPUCodeName, "eee")
			},
			want: "eee",
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			metricsFetcher := metric.NewFakeMetricsFetcher(metrics.DummyMetrics{})
			store := metricsFetcher.(*metric.FakeMetricsFetcher)
			tt.setFakeMetric(store)
			assert.Equalf(t, tt.want, GetCpuCodeName(metricsFetcher), "GetCpuCodeName")
		})
	}
}

func TestGetIsVm(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		setFakeMetric func(store *metric.FakeMetricsFetcher)
		wantBool      bool
		wantStr       string
	}{
		{
			name: "test",
			setFakeMetric: func(store *metric.FakeMetricsFetcher) {
				store.SetByStringIndex(consts.MetricInfoIsVM, "true")
			},
			wantBool: true,
			wantStr:  "true",
		},
		{
			name: "test",
			setFakeMetric: func(store *metric.FakeMetricsFetcher) {
				store.SetByStringIndex(consts.MetricInfoIsVM, "false")
			},
			wantBool: false,
			wantStr:  "false",
		},
		{
			name: "test",
			setFakeMetric: func(store *metric.FakeMetricsFetcher) {
				store.SetByStringIndex(consts.MetricInfoIsVM, "true1")
			},
			wantBool: false,
			wantStr:  "",
		},
		{
			name: "test",
			setFakeMetric: func(store *metric.FakeMetricsFetcher) {
				store.SetByStringIndex(consts.MetricInfoIsVM, "123")
			},
			wantBool: false,
			wantStr:  "",
		},
		{
			name: "test",
			setFakeMetric: func(store *metric.FakeMetricsFetcher) {
				store.SetByStringIndex(consts.MetricInfoIsVM, nil)
			},
			wantBool: false,
			wantStr:  "",
		},
		{
			name: "test",
			setFakeMetric: func(store *metric.FakeMetricsFetcher) {
				store.SetByStringIndex(consts.MetricInfoIsVM, "")
			},
			wantBool: false,
			wantStr:  "",
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			metricsFetcher := metric.NewFakeMetricsFetcher(metrics.DummyMetrics{})
			store := metricsFetcher.(*metric.FakeMetricsFetcher)
			tt.setFakeMetric(store)

			wantBool, wantStr := GetIsVm(metricsFetcher)
			assert.Equalf(t, tt.wantBool, wantBool, "GetCpuCodeName")
			assert.Equalf(t, tt.wantStr, wantStr, "GetCpuCodeName")
		})
	}
}
