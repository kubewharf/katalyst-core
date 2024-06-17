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

package resource_portrait

import (
	"context"
	"fmt"
	"strings"
	"time"

	promapiv1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/prometheus/common/model"
	"k8s.io/klog/v2"

	apiconfig "github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	datasourceprometheus "github.com/kubewharf/katalyst-core/pkg/util/datasource/prometheus"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

const dataSourceClientName = "prom"

var datasourceRegistry = map[string]func(conf interface{}) (DataSourceClient, error){}

type DataSourceClient interface {
	Query(workloadName, workloadNamespace string, config *apiconfig.ResourcePortraitConfig) map[string][]model.SamplePair
}

func RegisterDataSourceClientInitFunc(name string, f func(conf interface{}) (DataSourceClient, error)) {
	datasourceRegistry[name] = f
}

func NewDataSourceClient(name string, conf interface{}) (DataSourceClient, error) {
	initFunc, ok := datasourceRegistry[name]
	if !ok {
		return nil, fmt.Errorf("no init function found for %s", name)
	}
	return initFunc(conf)
}

func init() {
	RegisterDataSourceClientInitFunc(dataSourceClientName, newPromClient)
}

type promClientImpl struct {
	promapiv1.API
}

var PresetWorkloadMetricQueryMapping = map[string]string{
	"cpu_utilization_cfs_throttled_seconds": "sum(rate(container_cpu_cfs_throttled_seconds_total{namespace=\"%s\",pod=~\"%s\",container!=\"\"}[%s]))",
	"cpu_utilization_user_seconds":          "sum(rate(container_cpu_user_seconds_total{namespace=\"%s\",pod=~\"%s\",container!=\"\"}[%s]))",
	"cpu_utilization_system_seconds":        "sum(rate(container_cpu_system_seconds_total{namespace=\"%s\",pod=~\"%s\",container!=\"\"}[%s]))",
	"cpu_utilization_usage_seconds":         "sum(rate(container_cpu_usage_seconds_total{namespace=\"%s\",pod=~\"%s\",container!=\"\"}[%s]))",
	"cpu_utilization_usage_seconds_avg":     "avg(rate(container_cpu_usage_seconds_total{namespace=\"%s\",pod=~\"%s\",container!=\"\"}[%s]))",
	"cpu_utilization_usage_seconds_max":     "max(rate(container_cpu_usage_seconds_total{namespace=\"%s\",pod=~\"%s\",container!=\"\"}[%s]))",
	"cpu_utilization":                       "sum(rate(container_cpu_usage_seconds_total{namespace=\"%s\",pod=~\"%s\",container!=\"\"}[%s]))",
	"memory_utilization":                    "sum(container_memory_working_set_bytes{namespace=\"%s\",pod=~\"%s\", container!=\"\"})",
	"memory_utilization_avg":                "avg(container_memory_working_set_bytes{namespace=\"%s\",pod=~\"%s\", container!=\"\"})",
	"memory_utilization_max":                "max(container_memory_working_set_bytes{namespace=\"%s\",pod=~\"%s\", container!=\"\"})",
	"network_packets_receive_packets":       "sum(rate(container_network_receive_packets_total{namespace=\"%s\",pod=~\"%s\",container!=\"\"}[%s]))",
	"network_packets_receive_dropped":       "sum(rate(container_network_receive_packets_dropped_total{namespace=\"%s\",pod=~\"%s\",container!=\"\"}[%s]))",
	"network_packets_receive_errors":        "sum(rate(container_network_receive_errors_total{namespace=\"%s\",pod=~\"%s\",container!=\"\"}[%s]))",
	"network_packets_transmit_packets":      "sum(rate(container_network_transmit_packets_total{namespace=\"%s\",pod=~\"%s\",container!=\"\"}[%s]))",
	"network_packets_transmit_dropped":      "sum(rate(container_network_transmit_packets_dropped_total{namespace=\"%s\",pod=~\"%s\",container!=\"\"}[%s]))",
	"network_packets_transmit_errors":       "sum(rate(container_network_transmit_errors_total{namespace=\"%s\",pod=~\"%s\",container!=\"\"}[%s]))",
	"network_io_receive":                    "sum(rate(container_network_receive_bytes_total{namespace=\"%s\",pod=~\"%s\",container!=\"\"}[%s]))",
	"network_io_transmit":                   "sum(rate(container_network_transmit_bytes_total{namespace=\"%s\",pod=~\"%s\",container!=\"\"}[%s]))",
	"disk_io_read":                          "sum(rate(container_fs_reads_bytes_total{namespace=\"%s\",pod=~\"%s\",container!=\"\"}[%s]))",
	"disk_io_write":                         "sum(rate(container_fs_writes_bytes_total{namespace=\"%s\",pod=~\"%s\",container!=\"\"}[%s]))",
}

func newPromClient(config interface{}) (DataSourceClient, error) {
	_, ok := config.(*datasourceprometheus.PromConfig)
	if !ok {
		return nil, fmt.Errorf("invalid prom config")
	}

	promClient, err := datasourceprometheus.NewPrometheusClient(config.(*datasourceprometheus.PromConfig))
	if err != nil {
		return nil, err
	}
	return &promClientImpl{promapiv1.NewAPI(promClient)}, nil
}

func (c *promClientImpl) Query(workloadName, workloadNamespace string, config *apiconfig.ResourcePortraitConfig) map[string][]model.SamplePair {
	if config == nil || (len(config.Metrics) == 0 && len(config.CustomMetrics) == 0) {
		klog.Warningf("[spd-resource-portrait] query func has no algorithm config or metric config is not found for %s/%s", workloadNamespace, workloadName)
		return nil
	}

	promqls := general.MergeMap(map[string]string{}, config.CustomMetrics)
	duration := time.Duration(config.AlgorithmConfig.TimeWindow.Input) * time.Second
	durationStr := duration.String()

	for _, metric := range config.Metrics {
		if promql := getPromqlFromMapping(metric, workloadName, workloadNamespace, durationStr); promql != "" {
			promqls[metric] = promql
		}
	}

	if len(promqls) == 0 {
		klog.Warningf("[spd-resource-portrait] there is no promqls for %s/%s", workloadNamespace, workloadName)
		return nil
	}

	end := time.Unix(time.Now().Unix()/60*60, 0)
	start := end.Add(-duration * time.Duration(config.AlgorithmConfig.TimeWindow.HistorySteps))
	r := promapiv1.Range{
		Start: start,
		End:   end,
		Step:  duration,
	}

	originMetrics := map[string][]model.SamplePair{}
	for metric, promql := range promqls {
		result, _, err := c.API.QueryRange(context.Background(), promql, r)
		if err != nil {
			klog.Warningf("[spd-resource-portrait] query promql %s error: %v", promql, err)
			continue
		}

		matrix, ok := result.(model.Matrix)
		if !ok || len(matrix) != 1 {
			klog.Warningf("[spd-resource-portrait] query promql %s return not be matrix or its length != 1", promql)
			continue
		}

		samples := []*model.SampleStream(matrix)[0].Values
		if len(samples) == 0 {
			klog.Warningf("[spd-resource-portrait] query promql %s return no data", promql)
			continue
		} else if len(samples) < (config.AlgorithmConfig.TimeWindow.HistorySteps + 1) {
			klog.Warningf("[spd-resource-portrait] query promql %s not get enough data: nums=%d", promql, len(samples))
			continue
		}
		originMetrics[metric] = samples
	}
	return originMetrics
}

func getPromqlFromMapping(metric, workload, ns, durationStr string) (promQL string) {
	if promql, ok := PresetWorkloadMetricQueryMapping[metric]; ok {
		if !strings.HasSuffix(workload, ".*") {
			workload += ".*"
		}

		switch strings.Count(promql, "%s") {
		case 2:
			promQL = fmt.Sprintf(promql, ns, workload)
		case 3:
			promQL = fmt.Sprintf(promql, ns, workload, durationStr)
		}
	}
	return
}
