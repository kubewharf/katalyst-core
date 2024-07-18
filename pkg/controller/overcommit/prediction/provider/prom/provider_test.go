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

package prom

import (
	"testing"
	"time"

	v12 "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/prometheus/common/model"
	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"

	"github.com/kubewharf/katalyst-core/pkg/controller/overcommit/prediction/common"
)

func TestBuildQuery(t *testing.T) {
	t.Parallel()

	p := &Provider{}

	for _, tc := range []struct {
		name       string
		metricName string
		matches    []common.Metadata
		expectRes  string
	}{
		{
			name:       "cpu",
			metricName: v1.ResourceCPU.String(),
			matches: []common.Metadata{
				{Key: "namespace", Value: "default"}, {Key: "pod", Value: "katalyst-controller-.*-.*"},
			},
			expectRes: `max(sum(rate(container_cpu_usage_seconds_total{namespace="default",pod=~"katalyst-controller-.*-.*"}[2m])) by (pod))`,
		},
		{
			name:       "memory",
			metricName: v1.ResourceMemory.String(),
			matches: []common.Metadata{
				{Key: "namespace", Value: "default"}, {Key: "pod", Value: "katalyst-controller-.*-.*"},
			},
			expectRes: `max(sum(container_memory_working_set_bytes{namespace="default",pod=~"katalyst-controller-.*-.*"}) by (pod))`,
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			res, err := p.BuildQuery(tc.metricName, tc.matches)
			assert.NoError(t, err)
			assert.Equal(t, tc.expectRes, res)
		})
	}
}

func TestPromResultsToTimeSeries(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name      string
		value     model.Value
		expectRes []*common.TimeSeries
	}{
		{
			name: "matrix type",
			value: model.Matrix{
				&model.SampleStream{
					Metric: map[model.LabelName]model.LabelValue{"test": "test"},
					Values: []model.SamplePair{
						{
							Timestamp: 10,
							Value:     0.5,
						},
						{
							Timestamp: 11,
							Value:     0.6,
						},
						{
							Timestamp: 12,
							Value:     0.7,
						},
					},
				},
			},
			expectRes: []*common.TimeSeries{
				{
					Metadata: []common.Metadata{
						{
							Key:   "test",
							Value: "test",
						},
					},
					Samples: []common.Sample{
						{
							Value:     0.5,
							Timestamp: 10,
						},
						{
							Value:     0.6,
							Timestamp: 11,
						},
						{
							Value:     0.7,
							Timestamp: 12,
						},
					},
				},
			},
		},
		{
			name:      "unsupported type",
			value:     model.Vector{},
			expectRes: nil,
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			res, err := promResultsToTimeSeries(tc.value)
			if res == nil {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, len(res), len(tc.expectRes))
				for i := range res {
					assert.Equal(t, *res[i], *tc.expectRes[i])
				}
			}
		})
	}
}

func TestComputeWindowShard(t *testing.T) {
	t.Parallel()

	p := &Provider{}

	for _, tc := range []struct {
		name           string
		query          string
		window         *v12.Range
		maxPointsLimit int
		expectShards   int
	}{
		{
			name:  "test1",
			query: "testQuery",
			window: &v12.Range{
				Step:  time.Second * 60,
				Start: time.Now().Add(-9 * time.Minute),
				End:   time.Now(),
			},
			maxPointsLimit: 5,
			expectShards:   2,
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p.maxPointsLimit = tc.maxPointsLimit
			shareds := p.computeWindowShard(tc.query, tc.window)
			assert.Equal(t, tc.expectShards, len(shareds.windows))
		})
	}
}
