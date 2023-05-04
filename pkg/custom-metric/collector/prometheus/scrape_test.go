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

package prometheus

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/kubewharf/katalyst-core/pkg/custom-metric/store/data"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

func Test_scrape(t *testing.T) {
	ctx := context.Background()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`  # HELP none_namespace_metric 
# TYPE none_namespace_metric gauge
none_namespace_metric{test="none_namespace_metric_l"} 0 3
# HELP none_object_metric 
# TYPE none_object_metric gauge
none_object_metric{label_1="none_object_metric",namespace="n1"} 16 4
# HELP full_metric 
# TYPE full_metric gauge
full_metric{label_test="full",namespace="n1",object="pod",object_name="pod_1",timestamp="12"} 176 55
# HELP with_labeled_timestamp 
# TYPE with_labeled_timestamp gauge
with_labeled_timestamp{label_test="labeled",namespace="n1",object="pod",object_name="pod_2",timestamp="123"} 179
# HELP without_timestamp 
# TYPE without_timestamp gauge
without_timestamp{label_test="without_timestamp",namespace="n1",object="pod",object_name="pod_3"} 173
		`))
	}))
	defer server.Close()

	client, _ := newPrometheusClient()
	s, _ := NewScrapeManager(ctx, time.Hour, client, "fake-node", server.URL, metrics.DummyMetrics{})
	// to make sure the metric will only be collected once
	s.scrape()
	time.Sleep(time.Second * 5)

	handler := func(d []*data.MetricSeries, tags ...metrics.MetricTag) error {
		assert.NotNil(t, d)
		return fmt.Errorf("test error")
	}
	s.HandleMetric(handler)

	var dataList []*data.MetricSeries
	handler = func(d []*data.MetricSeries, tags ...metrics.MetricTag) error {
		assert.NotNil(t, d)
		dataList = append(dataList, d...)
		return nil
	}
	s.HandleMetric(handler)

	assert.Equal(t, 4, len(dataList))
	assert.ElementsMatch(t, []*data.MetricSeries{
		{
			Name: "none_namespace_metric",
			Labels: map[string]string{
				"test": "none_namespace_metric_l",
			},
			Series: []*data.MetricData{
				{
					Data:      0,
					Timestamp: 3,
				},
			},
		},
		{
			Name: "none_object_metric",
			Labels: map[string]string{
				"label_1":   "none_object_metric",
				"namespace": "n1",
			},
			Series: []*data.MetricData{
				{
					Data:      16,
					Timestamp: 4,
				},
			},
		},
		{
			Name: "full_metric",
			Labels: map[string]string{
				"label_test":  "full",
				"namespace":   "n1",
				"object":      "pod",
				"object_name": "pod_1",
			},
			Series: []*data.MetricData{
				{
					Data:      176,
					Timestamp: 55,
				},
			},
		},
		{
			Name: "with_labeled_timestamp",
			Labels: map[string]string{
				"label_test":  "labeled",
				"namespace":   "n1",
				"object":      "pod",
				"object_name": "pod_2",
			},
			Series: []*data.MetricData{
				{
					Data:      179,
					Timestamp: 123,
				},
			},
		},
	}, dataList)
	assert.Equal(t, 0, len(s.storedSeriesMap))

	s.Stop()
}
