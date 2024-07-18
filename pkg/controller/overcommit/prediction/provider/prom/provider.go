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
	"context"
	"fmt"
	"strings"
	"time"

	prometheus "github.com/prometheus/client_golang/api"
	v1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/prometheus/common/model"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/controller/overcommit/prediction/common"
	datasource "github.com/kubewharf/katalyst-core/pkg/util/datasource/prometheus"
)

type Interface interface {
	QueryTimeSeries(ctx context.Context, query string, startTime, endTime time.Time, step time.Duration) ([]*common.TimeSeries, error)
	BuildQuery(metricName string, matchs []common.Metadata) (string, error)
}

type Provider struct {
	client         prometheus.Client
	api            v1.API
	maxPointsLimit int
}

func NewProvider(
	config *datasource.PromConfig,
) (*Provider, error) {
	client, err := datasource.NewPrometheusClient(config)
	if err != nil {
		err = fmt.Errorf("new prometheus client fail: %v", err)
		klog.Error(err)
		return nil, err
	}

	return &Provider{
		client:         client,
		api:            v1.NewAPI(client),
		maxPointsLimit: config.MaxPointsLimitPerTimeSeries,
	}, nil
}

// QueryTimeSeries query time series metrics from prometheus server
func (p *Provider) QueryTimeSeries(ctx context.Context, query string, startTime, endTime time.Time, step time.Duration) ([]*common.TimeSeries, error) {
	klog.V(6).Infof("prom provider QueryTimeSeries, query: %v, startTime: %v, endTime: %v, step: %v",
		query, startTime, endTime, step)

	r := v1.Range{
		Start: startTime,
		End:   endTime,
		Step:  step,
	}

	// max resolution per timeSeries of prom server is limited, default 11000 points.
	Shards := p.computeWindowShard(query, &r)

	res := make([]*common.TimeSeries, 0)
	for i := range Shards.windows {
		ts, err := p.queryTimeSeries(ctx, Shards.query, Shards.windows[i])
		if err != nil {
			klog.Errorf("prom provider query time series fail, query: %v, startTime: %v, endTime: %v, step: %v, err: %v",
				query, startTime, endTime, step, err)
			return nil, err
		}
		res = append(res, ts...)
	}

	return res, nil
}

func (p *Provider) BuildQuery(metricName string, matchs []common.Metadata) (string, error) {
	matchExpr := strings.Builder{}
	// generate matchs
	i := 0
	for _, data := range matchs {
		symbol, ok := matchLabels[data.Key]
		if !ok {
			symbol = "="
		}
		if i != 0 {
			matchExpr.WriteString(",")
		}
		matchExpr.WriteString(data.Key)
		matchExpr.WriteString(symbol)
		matchExpr.WriteString(fmt.Sprintf(`"%s"`, data.Value))
		i++
	}

	switch metricName {
	case corev1.ResourceCPU.String():
		return fmt.Sprintf(workloadMaxCPUUsageTemplate, matchExpr.String(), defaultCPUUsageInterval), nil
	case corev1.ResourceMemory.String():
		return fmt.Sprintf(workloadMaxMemoryUsageTemplate, matchExpr.String()), nil
	default:
		return "", fmt.Errorf("metric %v not support yet", metricName)
	}
}

func (p *Provider) queryTimeSeries(ctx context.Context, query string, queryRange *v1.Range) ([]*common.TimeSeries, error) {
	timeout, cancel := context.WithTimeout(ctx, time.Second*2)
	defer cancel()

	results, warnings, err := p.api.QueryRange(timeout, query, *queryRange)
	if len(warnings) != 0 {
		klog.V(5).Infof("prom provider queryRange warnings: %v", warnings)
	}

	if err != nil {
		return nil, err
	}

	return promResultsToTimeSeries(results)
}

func promResultsToTimeSeries(value model.Value) ([]*common.TimeSeries, error) {
	var (
		res       = []*common.TimeSeries{}
		valueType = value.Type()
	)

	switch valueType {
	case model.ValMatrix:
		matrix, ok := value.(model.Matrix)
		if !ok {
			return nil, fmt.Errorf("prom provider matrix value assert fail")
		}
		for _, sample := range matrix {
			if sample == nil {
				continue
			}

			ts := common.EmptyTimeSeries()
			for key, val := range sample.Metric {
				ts.Metadata = append(ts.Metadata, common.Metadata{Key: string(key), Value: string(val)})
			}
			for _, pair := range sample.Values {
				ts.Samples = append(ts.Samples, common.Sample{Value: float64(pair.Value), Timestamp: int64(pair.Timestamp)})
			}
			res = append(res, ts)
		}

		return res, nil
	default:
		return nil, fmt.Errorf("prom provider value type %v not supported yet", valueType.String())
	}
}

func (p *Provider) computeWindowShard(query string, window *v1.Range) *QueryShards {
	shardIndex := 0
	nextPoint := window.Start
	prePoint := nextPoint
	var shards []*v1.Range
	for {
		if nextPoint.After(window.End) {
			shards = append(shards, &v1.Range{
				Start: prePoint,
				End:   window.End,
				Step:  window.Step,
			})
			break
		}
		if shardIndex != 0 && shardIndex%p.maxPointsLimit == 0 {
			shards = append(shards, &v1.Range{
				Start: prePoint,
				End:   nextPoint.Add(-window.Step),
				Step:  window.Step,
			})
			prePoint = nextPoint
		}
		nextPoint = nextPoint.Add(window.Step)
		shardIndex++
	}

	return &QueryShards{
		query:   query,
		windows: shards,
	}
}
