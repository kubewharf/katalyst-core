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

package data

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/kubewharf/katalyst-api/pkg/metric"
	"github.com/kubewharf/katalyst-core/pkg/custom-metric/store/data/types"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

func TestCache(t *testing.T) {
	t.Parallel()

	c := NewCachedMetric(metrics.DummyMetrics{}, ObjectMetricStoreTypeBucket)

	var (
		exist      bool
		names      []string
		metaList   []types.MetricMeta
		metricList []types.Metric
	)

	t.Log("#### 1: Add with none-namespaced metric")

	s1 := &types.SeriesMetric{
		MetricMetaImp: types.MetricMetaImp{
			Name: "m-1",
		},
		ObjectMetaImp: types.ObjectMetaImp{},
		BasicMetric: types.BasicMetric{
			Labels: map[string]string{
				"Name": "m-1",
			},
		},
	}
	s1.AddMetric(&types.SeriesItem{
		Value:     1,
		Timestamp: 1,
	})
	c.AddSeriesMetric(s1)

	names = c.ListAllMetricNames()
	assert.ElementsMatch(t, []string{"m-1"}, names)

	var err error

	metricList, exist, err = c.GetMetric("", "m-1", "", nil, false, nil, nil, false)
	assert.NoError(t, err)
	assert.Equal(t, true, exist)
	assert.Equal(t, s1, metricList[0])

	_, exist, err = c.GetMetric("", "m-2", "", nil, false, nil, nil, false)
	assert.NoError(t, err)
	assert.Equal(t, false, exist)

	t.Log("#### 2: Add with namespaced metric")

	s2 := &types.SeriesMetric{
		MetricMetaImp: types.MetricMetaImp{
			Name:       "m-2",
			Namespaced: true,
		},
		ObjectMetaImp: types.ObjectMetaImp{
			ObjectNamespace: "n-2",
		},
		BasicMetric: types.BasicMetric{
			Labels: map[string]string{
				"Name": "m-2",
			},
		},
	}
	s2.AddMetric(&types.SeriesItem{
		Value:     2,
		Timestamp: 3,
	})
	c.AddSeriesMetric(s2)

	names = c.ListAllMetricNames()
	assert.ElementsMatch(t, []string{"m-1", "m-2"}, names)

	metricList, exist, err = c.GetMetric("", "m-1", "", nil, false, nil, nil, false)
	assert.NoError(t, err)
	assert.Equal(t, true, exist)
	assert.Equal(t, s1, metricList[0])

	metricList, exist, err = c.GetMetric("n-2", "m-2", "", nil, false, nil, nil, false)
	assert.NoError(t, err)
	assert.Equal(t, true, exist)
	assert.Equal(t, s2, metricList[0])

	t.Log("#### 3: Add pod with objected metric")

	s3 := &types.SeriesMetric{
		MetricMetaImp: types.MetricMetaImp{
			Name:       "m-3",
			Namespaced: true,
			ObjectKind: "pod",
		},
		ObjectMetaImp: types.ObjectMetaImp{
			ObjectNamespace: "n-3",
			ObjectName:      "pod-3",
		},
		BasicMetric: types.BasicMetric{
			Labels: map[string]string{
				"Name": "m-3",
			},
		},
	}
	s3.AddMetric(&types.SeriesItem{
		Value:     4,
		Timestamp: 5,
	})
	c.AddSeriesMetric(s3)

	names = c.ListAllMetricNames()
	assert.ElementsMatch(t, []string{"m-1", "m-2", "m-3"}, names)

	metaList = c.ListAllMetricMeta(false)
	assert.ElementsMatch(t, []types.MetricMetaImp{
		{
			Name: "m-1",
		},
		{
			Name:       "m-2",
			Namespaced: true,
		},
	}, metaList)

	metaList = c.ListAllMetricMeta(true)
	assert.ElementsMatch(t, []types.MetricMetaImp{
		{
			Name:       "m-3",
			Namespaced: true,
			ObjectKind: "pod",
		},
	}, metaList)

	_, exist, err = c.GetMetric("n-4", "m-3", "", nil, false, &schema.GroupResource{Resource: "pod"}, nil, false)
	assert.NoError(t, err)
	assert.Equal(t, true, exist)

	metricList, exist, err = c.GetMetric("n-3", "m-3", "", nil, false, &schema.GroupResource{Resource: "pod"}, nil, false)
	assert.NoError(t, err)
	assert.Equal(t, true, exist)
	assert.Equal(t, s3, metricList[0])

	t.Log("#### 4: Add pod with the same metric Name")

	s3_1 := &types.SeriesMetric{
		MetricMetaImp: types.MetricMetaImp{
			Name:       "m-3",
			Namespaced: true,
			ObjectKind: "pod",
		},
		ObjectMetaImp: types.ObjectMetaImp{
			ObjectNamespace: "n-3",
			ObjectName:      "pod-3",
		},
		BasicMetric: types.BasicMetric{
			Labels: map[string]string{
				"Name": "m-3",
			},
		},
	}
	s3_1.AddMetric(&types.SeriesItem{
		Value:     7,
		Timestamp: 8,
	})
	c.AddSeriesMetric(s3_1)

	names = c.ListAllMetricNames()
	assert.ElementsMatch(t, []string{"m-1", "m-2", "m-3"}, names)

	s3 = &types.SeriesMetric{
		MetricMetaImp: types.MetricMetaImp{
			Name:       "m-3",
			Namespaced: true,
			ObjectKind: "pod",
		},
		ObjectMetaImp: types.ObjectMetaImp{
			ObjectNamespace: "n-3",
			ObjectName:      "pod-3",
		},
		BasicMetric: types.BasicMetric{
			Labels: map[string]string{
				"Name": "m-3",
			},
		},
	}
	s3.AddMetric(&types.SeriesItem{
		Value:     4,
		Timestamp: 5,
	})
	s3.AddMetric(&types.SeriesItem{
		Value:     7,
		Timestamp: 8,
	})
	metricList, exist, err = c.GetMetric("n-3", "m-3", "", nil, false, &schema.GroupResource{Resource: "pod"}, nil, false)
	assert.NoError(t, err)
	assert.Equal(t, true, exist)
	assert.Equal(t, s3, metricList[0])

	t.Log("#### 5: Add pod another meta")

	s4 := &types.SeriesMetric{
		MetricMetaImp: types.MetricMetaImp{
			Name:       "m-3",
			Namespaced: true,
			ObjectKind: "pod",
		},
		ObjectMetaImp: types.ObjectMetaImp{
			ObjectNamespace: "n-3",
			ObjectName:      "pod-4",
		},
		BasicMetric: types.BasicMetric{
			Labels: map[string]string{
				"Name": "m-3",
			},
		},
	}
	s4.AddMetric(&types.SeriesItem{
		Value:     10,
		Timestamp: 12,
	})
	c.AddSeriesMetric(s4)

	names = c.ListAllMetricNames()
	assert.ElementsMatch(t, []string{"m-1", "m-2", "m-3"}, names)

	metricList, exist, err = c.GetMetric("n-3", "m-3", "", nil, false, &schema.GroupResource{Resource: "pod"}, nil, false)
	assert.NoError(t, err)
	assert.Equal(t, true, exist)
	assert.ElementsMatch(t, []*types.SeriesMetric{s3, s4}, metricList)

	metricList, exist, err = c.GetMetric("n-3", "m-3", "pod-3", nil, false, &schema.GroupResource{Resource: "pod"}, nil, false)
	assert.NoError(t, err)
	assert.Equal(t, true, exist)
	assert.ElementsMatch(t, []*types.SeriesMetric{s3}, metricList)

	metricList, exist, err = c.GetMetric("n-3", "m-3", "pod-4", nil, false, &schema.GroupResource{Resource: "pod"}, nil, false)
	assert.NoError(t, err)
	assert.Equal(t, true, exist)
	assert.ElementsMatch(t, []*types.SeriesMetric{s4}, metricList)

	t.Log("#### 6: Add pod with the duplicated Timestamp")

	s3_2 := &types.SeriesMetric{
		MetricMetaImp: types.MetricMetaImp{
			Name:       "m-3",
			Namespaced: true,
			ObjectKind: "pod",
		},
		ObjectMetaImp: types.ObjectMetaImp{
			ObjectNamespace: "n-3",
			ObjectName:      "pod-3",
		},
		BasicMetric: types.BasicMetric{
			Labels: map[string]string{
				"Name":  "m-3",
				"extra": "m-3",
			},
		},
	}
	s3_2.AddMetric(&types.SeriesItem{
		Value:     10,
		Timestamp: 9,
	})
	s3_2.AddMetric(&types.SeriesItem{
		Value:     9,
		Timestamp: 8,
	})
	c.AddSeriesMetric(s3_2)

	names = c.ListAllMetricNames()
	assert.ElementsMatch(t, []string{"m-1", "m-2", "m-3"}, names)

	s3 = &types.SeriesMetric{
		MetricMetaImp: types.MetricMetaImp{
			Name:       "m-3",
			Namespaced: true,
			ObjectKind: "pod",
		},
		ObjectMetaImp: types.ObjectMetaImp{
			ObjectNamespace: "n-3",
			ObjectName:      "pod-3",
		},
		BasicMetric: types.BasicMetric{
			Labels: map[string]string{
				"Name": "m-3",
			},
		},
	}
	s3.AddMetric(&types.SeriesItem{
		Value:     4,
		Timestamp: 5,
	})
	s3.AddMetric(&types.SeriesItem{
		Value:     7,
		Timestamp: 8,
	})

	s3_1 = &types.SeriesMetric{
		MetricMetaImp: types.MetricMetaImp{
			Name:       "m-3",
			Namespaced: true,
			ObjectKind: "pod",
		},
		ObjectMetaImp: types.ObjectMetaImp{
			ObjectNamespace: "n-3",
			ObjectName:      "pod-3",
		},
		BasicMetric: types.BasicMetric{
			Labels: map[string]string{
				"Name":  "m-3",
				"extra": "m-3",
			},
		},
	}
	s3_1.AddMetric(&types.SeriesItem{
		Value:     9,
		Timestamp: 8,
	})
	s3_1.AddMetric(&types.SeriesItem{
		Value:     10,
		Timestamp: 9,
	})
	metricList, exist, err = c.GetMetric("n-3", "m-3", "", nil, false, &schema.GroupResource{Resource: "pod"}, nil, false)
	assert.NoError(t, err)
	assert.Equal(t, true, exist)
	assert.ElementsMatch(t, []*types.SeriesMetric{s3, s3_1, s4}, metricList)

	selector, _ := labels.Parse("extra=m-3")
	metricList, exist, err = c.GetMetric("n-3", "m-3", "", nil, false, &schema.GroupResource{Resource: "pod"}, selector, false)
	assert.NoError(t, err)
	assert.Equal(t, true, exist)
	assert.ElementsMatch(t, []*types.SeriesMetric{s3_1}, metricList)

	s3_latest := &types.SeriesMetric{
		MetricMetaImp: types.MetricMetaImp{
			Name:       "m-3",
			Namespaced: true,
			ObjectKind: "pod",
		},
		ObjectMetaImp: types.ObjectMetaImp{
			ObjectNamespace: "n-3",
			ObjectName:      "pod-3",
		},
		BasicMetric: types.BasicMetric{
			Labels: map[string]string{
				"Name":  "m-3",
				"extra": "m-3",
			},
		},
	}
	s3_latest.AddMetric(&types.SeriesItem{
		Value:     10,
		Timestamp: 9,
	})
	metricList, exist, err = c.GetMetric("n-3", "m-3", "", nil, false, &schema.GroupResource{Resource: "pod"}, nil, true)
	assert.NoError(t, err)
	assert.Equal(t, true, exist)
	assert.ElementsMatch(t, []*types.SeriesMetric{s3_latest, s4}, metricList)

	t.Log("#### 7: list all metric")

	metricList = c.GetAllMetricsInNamespace("")
	assert.ElementsMatch(t, []*types.SeriesMetric{s1}, metricList)

	metricList = c.GetAllMetricsInNamespace("n-2")
	assert.ElementsMatch(t, []*types.SeriesMetric{s2}, metricList)

	metricList = c.GetAllMetricsInNamespace("n-3")
	assert.ElementsMatch(t, []*types.SeriesMetric{s3, s3_1, s4}, metricList)

	t.Log("#### 8: gcMetric")
	c.gcWithTimestamp(3)
	c.Purge()
	names = c.ListAllMetricNames()
	assert.ElementsMatch(t, []string{"m-3"}, names)

	metricList = c.GetAllMetricsInNamespace("")
	assert.ElementsMatch(t, []*types.SeriesMetric{}, metricList)

	metricList = c.GetAllMetricsInNamespace("n-2")
	assert.ElementsMatch(t, []*types.SeriesMetric{}, metricList)

	metricList = c.GetAllMetricsInNamespace("n-3")
	assert.ElementsMatch(t, []*types.SeriesMetric{s3, s3_1, s4}, metricList)

	c.gcWithTimestamp(8)
	names = c.ListAllMetricNames()
	assert.ElementsMatch(t, []string{"m-3"}, names)

	s3 = &types.SeriesMetric{
		MetricMetaImp: types.MetricMetaImp{
			Name:       "m-3",
			Namespaced: true,
			ObjectKind: "pod",
		},
		ObjectMetaImp: types.ObjectMetaImp{
			ObjectNamespace: "n-3",
			ObjectName:      "pod-3",
		},
		BasicMetric: types.BasicMetric{
			Labels: map[string]string{
				"Name":  "m-3",
				"extra": "m-3",
			},
		},
	}
	s3.AddMetric(&types.SeriesItem{
		Value:     10,
		Timestamp: 9,
	})

	s4 = &types.SeriesMetric{
		MetricMetaImp: types.MetricMetaImp{
			Name:       "m-3",
			Namespaced: true,
			ObjectKind: "pod",
		},
		ObjectMetaImp: types.ObjectMetaImp{
			ObjectNamespace: "n-3",
			ObjectName:      "pod-4",
		},
		BasicMetric: types.BasicMetric{
			Labels: map[string]string{
				"Name": "m-3",
			},
		},
	}
	s4.AddMetric(&types.SeriesItem{
		Value:     10,
		Timestamp: 12,
	})
	metricList = c.GetAllMetricsInNamespace("n-3")
	assert.ElementsMatch(t, []*types.SeriesMetric{s3, s4}, metricList)

	bytes, err := json.Marshal(metricList)
	assert.NoError(t, err)
	t.Logf("%v", string(bytes))

	res, err := types.UnmarshalMetricList(bytes)
	assert.NoError(t, err)
	assert.ElementsMatch(t, metricList, res)

	t.Log("#### 9: get agg metric")

	s5 := &types.SeriesMetric{
		MetricMetaImp: types.MetricMetaImp{
			Name:       "m-3",
			Namespaced: true,
			ObjectKind: "pod",
		},
		ObjectMetaImp: types.ObjectMetaImp{
			ObjectNamespace: "n-3",
			ObjectName:      "pod-5",
		},
		BasicMetric: types.BasicMetric{
			Labels: map[string]string{
				"Name": "m-3",
			},
		},
	}
	s5.AddMetric(&types.SeriesItem{
		Value:     4,
		Timestamp: 12 * time.Second.Milliseconds(),
	})
	s5.AddMetric(&types.SeriesItem{
		Value:     7,
		Timestamp: 14 * time.Second.Milliseconds(),
	})

	s5_1 := &types.SeriesMetric{
		MetricMetaImp: types.MetricMetaImp{
			Name:       "m-3",
			Namespaced: true,
			ObjectKind: "pod",
		},
		ObjectMetaImp: types.ObjectMetaImp{
			ObjectNamespace: "n-3",
			ObjectName:      "pod-5",
		},
		BasicMetric: types.BasicMetric{
			Labels: map[string]string{
				"Name":  "m-3",
				"extra": "m-3",
			},
		},
	}

	s5_1.AddMetric(&types.SeriesItem{
		Value:     12,
		Timestamp: 18 * time.Second.Milliseconds(),
	})
	s5_1.AddMetric(&types.SeriesItem{
		Value:     10,
		Timestamp: 20 * time.Second.Milliseconds(),
	})
	c.AddSeriesMetric(s5)
	c.AddSeriesMetric(s5_1)

	metricList, exist, err = c.GetMetric("n-3", "m-3", "pod-5", nil, false, &schema.GroupResource{Resource: "pod"}, nil, false)
	assert.NoError(t, err)
	assert.Equal(t, true, exist)
	assert.ElementsMatch(t, []*types.SeriesMetric{s5, s5_1}, metricList)

	agg := &types.AggregatedMetric{
		MetricMetaImp: types.MetricMetaImp{
			Name:       "m-3",
			Namespaced: true,
			ObjectKind: "pod",
		},
		ObjectMetaImp: types.ObjectMetaImp{
			ObjectNamespace: "n-3",
			ObjectName:      "pod-5",
		},
		AggregatedIdentity: types.AggregatedIdentity{
			Count:         4,
			Timestamp:     20 * time.Second.Milliseconds(),
			WindowSeconds: 8,
		},
	}

	matchedAgg := &types.AggregatedMetric{
		MetricMetaImp: types.MetricMetaImp{
			Name:       "m-3",
			Namespaced: true,
			ObjectKind: "pod",
		},
		ObjectMetaImp: types.ObjectMetaImp{
			ObjectNamespace: "n-3",
			ObjectName:      "pod-5",
		},
		AggregatedIdentity: types.AggregatedIdentity{
			Count:         2,
			Timestamp:     20 * time.Second.Milliseconds(),
			WindowSeconds: 2,
		},
	}

	agg.Name = "m-3" + metric.AggregateFunctionAvg
	agg.Value = 8.25
	metricList, exist, err = c.GetMetric("n-3", "m-3"+metric.AggregateFunctionAvg, "pod-5", nil, false, &schema.GroupResource{Resource: "pod"}, nil, false)
	assert.NoError(t, err)
	assert.Equal(t, true, exist)
	assert.ElementsMatch(t, []*types.AggregatedMetric{agg}, metricList)

	matchedAgg.Name = "m-3" + metric.AggregateFunctionAvg
	matchedAgg.Value = 11
	metricList, exist, err = c.GetMetric("n-3", "m-3"+metric.AggregateFunctionAvg, "pod-5", nil, false, &schema.GroupResource{Resource: "pod"}, selector, false)
	assert.NoError(t, err)
	assert.Equal(t, true, exist)
	assert.ElementsMatch(t, []*types.AggregatedMetric{matchedAgg}, metricList)

	agg.Name = "m-3" + metric.AggregateFunctionMax
	agg.Value = 12
	metricList, exist, err = c.GetMetric("n-3", "m-3"+metric.AggregateFunctionMax, "pod-5", nil, false, &schema.GroupResource{Resource: "pod"}, nil, false)
	assert.NoError(t, err)
	assert.Equal(t, true, exist)
	assert.ElementsMatch(t, []*types.AggregatedMetric{agg}, metricList)

	matchedAgg.Name = "m-3" + metric.AggregateFunctionMax
	matchedAgg.Value = 12
	metricList, exist, err = c.GetMetric("n-3", "m-3"+metric.AggregateFunctionMax, "pod-5", nil, false, &schema.GroupResource{Resource: "pod"}, selector, false)
	assert.NoError(t, err)
	assert.Equal(t, true, exist)
	assert.ElementsMatch(t, []*types.AggregatedMetric{matchedAgg}, metricList)

	agg.Name = "m-3" + metric.AggregateFunctionMin
	agg.Value = 4
	metricList, exist, err = c.GetMetric("n-3", "m-3"+metric.AggregateFunctionMin, "pod-5", nil, false, &schema.GroupResource{Resource: "pod"}, nil, false)
	assert.NoError(t, err)
	assert.Equal(t, true, exist)
	assert.ElementsMatch(t, []*types.AggregatedMetric{agg}, metricList)

	matchedAgg.Name = "m-3" + metric.AggregateFunctionMin
	matchedAgg.Value = 10
	metricList, exist, err = c.GetMetric("n-3", "m-3"+metric.AggregateFunctionMin, "pod-5", nil, false, &schema.GroupResource{Resource: "pod"}, selector, false)
	assert.NoError(t, err)
	assert.Equal(t, true, exist)
	assert.ElementsMatch(t, []*types.AggregatedMetric{matchedAgg}, metricList)

	agg.Name = "m-3" + metric.AggregateFunctionP99
	agg.Value = 11
	metricList, exist, err = c.GetMetric("n-3", "m-3"+metric.AggregateFunctionP99, "pod-5", nil, false, &schema.GroupResource{Resource: "pod"}, nil, false)
	assert.NoError(t, err)
	assert.Equal(t, true, exist)
	assert.ElementsMatch(t, []*types.AggregatedMetric{agg}, metricList)

	matchedAgg.Name = "m-3" + metric.AggregateFunctionP99
	matchedAgg.Value = 11
	metricList, exist, err = c.GetMetric("n-3", "m-3"+metric.AggregateFunctionP99, "pod-5", nil, false, &schema.GroupResource{Resource: "pod"}, selector, false)
	assert.NoError(t, err)
	assert.Equal(t, true, exist)
	assert.ElementsMatch(t, []*types.AggregatedMetric{matchedAgg}, metricList)

	agg.Name = "m-3" + metric.AggregateFunctionP90
	agg.Value = 11
	metricList, exist, err = c.GetMetric("n-3", "m-3"+metric.AggregateFunctionP90, "pod-5", nil, false, &schema.GroupResource{Resource: "pod"}, nil, false)
	assert.NoError(t, err)
	assert.Equal(t, true, exist)
	assert.ElementsMatch(t, []*types.AggregatedMetric{agg}, metricList)

	matchedAgg.Name = "m-3" + metric.AggregateFunctionP90
	matchedAgg.Value = 11
	metricList, exist, err = c.GetMetric("n-3", "m-3"+metric.AggregateFunctionP90, "pod-5", nil, false, &schema.GroupResource{Resource: "pod"}, selector, false)
	assert.NoError(t, err)
	assert.Equal(t, true, exist)
	assert.ElementsMatch(t, []*types.AggregatedMetric{matchedAgg}, metricList)

	agg.Name = "m-3" + metric.AggregateFunctionLatest
	agg.Value = 10
	metricList, exist, err = c.GetMetric("n-3", "m-3"+metric.AggregateFunctionLatest, "pod-5", nil, false, &schema.GroupResource{Resource: "pod"}, nil, false)
	assert.NoError(t, err)
	assert.Equal(t, true, exist)
	assert.ElementsMatch(t, []*types.AggregatedMetric{agg}, metricList)

	matchedAgg.Name = "m-3" + metric.AggregateFunctionLatest
	matchedAgg.Value = 10
	metricList, exist, err = c.GetMetric("n-3", "m-3"+metric.AggregateFunctionLatest, "pod-5", nil, false, &schema.GroupResource{Resource: "pod"}, selector, false)
	assert.NoError(t, err)
	assert.Equal(t, true, exist)
	assert.ElementsMatch(t, []*types.AggregatedMetric{matchedAgg}, metricList)
}

func TestMergeInternalMetricList(t *testing.T) {
	t.Parallel()

	type args struct {
		metricName  string
		metricLists [][]types.Metric
	}
	tests := []struct {
		name string
		args args
		want []types.Metric
	}{
		{
			args: args{
				metricName: "pod_cpu_usage_agg_max",
				metricLists: [][]types.Metric{
					{
						&types.AggregatedMetric{
							MetricMetaImp: types.MetricMetaImp{
								Name:       "pod_cpu_usage_agg_max",
								Namespaced: true,
								ObjectKind: "pod",
							},
							ObjectMetaImp: types.ObjectMetaImp{
								ObjectNamespace: "n-1",
								ObjectName:      "pod-1",
							},
							BasicMetric: types.BasicMetric{
								Labels: map[string]string{
									"Name": "m-1",
								},
							},
							AggregatedIdentity: types.AggregatedIdentity{
								Count:         4,
								Timestamp:     12 * time.Second.Milliseconds(),
								WindowSeconds: 8,
							},
							Value: 10,
						},
					},
					{
						&types.AggregatedMetric{
							MetricMetaImp: types.MetricMetaImp{
								Name:       "pod_cpu_usage_agg_max",
								Namespaced: true,
								ObjectKind: "pod",
							},
							ObjectMetaImp: types.ObjectMetaImp{
								ObjectNamespace: "n-1",
								ObjectName:      "pod-1",
							},
							BasicMetric: types.BasicMetric{
								Labels: map[string]string{
									"Name": "m-1",
								},
							},
							AggregatedIdentity: types.AggregatedIdentity{
								Count:         4,
								Timestamp:     12 * time.Second.Milliseconds(),
								WindowSeconds: 8,
							},
							Value: 10,
						},
					},
				},
			},
			want: []types.Metric{
				&types.AggregatedMetric{
					MetricMetaImp: types.MetricMetaImp{
						Name:       "pod_cpu_usage_agg_max",
						Namespaced: true,
						ObjectKind: "pod",
					},
					ObjectMetaImp: types.ObjectMetaImp{
						ObjectNamespace: "n-1",
						ObjectName:      "pod-1",
					},
					BasicMetric: types.BasicMetric{
						Labels: map[string]string{
							"Name": "m-1",
						},
					},
					AggregatedIdentity: types.AggregatedIdentity{
						Count:         4,
						Timestamp:     12 * time.Second.Milliseconds(),
						WindowSeconds: 8,
					},
					Value: 10,
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MergeInternalMetricList(tt.args.metricName, tt.args.metricLists...)
			assert.Equalf(t, tt.want, got, "MergeInternalMetricList(%v, %v)", tt.args.metricName, tt.args.metricLists)
		})
	}
}
