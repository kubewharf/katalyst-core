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
	"time"

	promapiv1 "github.com/prometheus/client_golang/api/prometheus/v1"
	datamodel "github.com/prometheus/common/model"
	v1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/controller/resource-recommend/datasource"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	datasourcetypes "github.com/kubewharf/katalyst-core/pkg/util/resource-recommend/types/datasource"
)

type prometheus struct {
	promAPIClient promapiv1.API
	config        *PromConfig
}

type PromDatasource interface {
	datasource.Datasource
	GetPromClient() promapiv1.API
}

// NewPrometheus return a prometheus data source
func NewPrometheus(config *PromConfig) (PromDatasource, error) {
	client, err := NewPrometheusClient(config)
	if err != nil {
		return nil, err
	}
	promAPIClient := promapiv1.NewAPI(client)

	return &prometheus{promAPIClient: promAPIClient, config: config}, nil
}

func (p *prometheus) GetPromClient() promapiv1.API {
	return p.promAPIClient
}

func (p *prometheus) ConvertMetricToQuery(metric datasourcetypes.Metric) (*datasourcetypes.Query, error) {
	extraFilters := GetExtraFilters(metric.Selectors, p.config.BaseFilter)
	var queryExpr string
	switch metric.Resource {
	case v1.ResourceCPU:
		queryExpr = GetContainerCpuUsageQueryExp(metric.Namespace, metric.WorkloadName, metric.Kind, metric.ContainerName, extraFilters)
	case v1.ResourceMemory:
		queryExpr = GetContainerMemUsageQueryExp(metric.Namespace, metric.WorkloadName, metric.Kind, metric.ContainerName, extraFilters)
	default:
		return nil, fmt.Errorf("query for resource type %v is not supported", metric.Resource)
	}
	convertedQuery := datasourcetypes.PrometheusQuery{
		Query: queryExpr,
	}
	return &datasourcetypes.Query{
		Prometheus: &convertedQuery,
	}, nil
}

func (p *prometheus) QueryTimeSeries(query *datasourcetypes.Query, start time.Time, end time.Time, step time.Duration) (*datasourcetypes.TimeSeries, error) {
	klog.InfoS("QueryTimeSeries", "query", general.StructToString(query), "start", start, "end", end)
	timeoutCtx, cancelFunc := context.WithTimeout(context.Background(), p.config.Timeout)
	defer cancelFunc()
	timeSeries, err := p.queryRangeSync(timeoutCtx, query.Prometheus.Query, start, end, step)
	if err != nil {
		klog.ErrorS(err, "query", query, "start", start, "end", end)
		return nil, err
	}
	return timeSeries, nil
}

// queryRangeSync range query prometheus in sync way
func (p *prometheus) queryRangeSync(ctx context.Context, query string, start, end time.Time, step time.Duration) (*datasourcetypes.TimeSeries, error) {
	r := promapiv1.Range{
		Start: start,
		End:   end,
		Step:  step,
	}
	klog.InfoS("Prom query", "query", query)
	var ts *datasourcetypes.TimeSeries
	results, warnings, err := p.promAPIClient.QueryRange(ctx, query, r)
	if len(warnings) != 0 {
		klog.InfoS("Prom query range warnings", "warnings", warnings)
	}
	if err != nil {
		return ts, err
	}
	klog.V(5).InfoS("Prom query range result", "query", query, "result", results.String(), "resultsType", results.Type())

	return p.convertPromResultsToTimeSeries(results)
}

func (p *prometheus) convertPromResultsToTimeSeries(value datamodel.Value) (*datasourcetypes.TimeSeries, error) {
	results := datasourcetypes.NewTimeSeries()
	typeValue := value.Type()
	switch typeValue {
	case datamodel.ValMatrix:
		if matrix, ok := value.(datamodel.Matrix); ok {
			for _, sampleStream := range matrix {
				if sampleStream == nil {
					continue
				}
				for key, val := range sampleStream.Metric {
					results.AppendLabel(string(key), string(val))
				}
				for _, pair := range sampleStream.Values {
					results.AppendSample(int64(pair.Timestamp/1000), float64(pair.Value))
				}
			}
			return results, nil
		} else {
			return results, fmt.Errorf("prometheus value type is %v, but assert failed", typeValue)
		}

	case datamodel.ValVector:
		if vector, ok := value.(datamodel.Vector); ok {
			for _, sample := range vector {
				if sample == nil {
					continue
				}
				for key, val := range sample.Metric {
					results.AppendLabel(string(key), string(val))
				}
				results.AppendSample(int64(sample.Timestamp/1000), float64(sample.Value))
			}
			return results, nil
		} else {
			return results, fmt.Errorf("prometheus value type is %v, but assert failed", typeValue)
		}
	}
	return results, fmt.Errorf("prometheus return unsupported model value type %v", typeValue)
}
