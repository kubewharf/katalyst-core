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

package datasource

import (
	"fmt"
	"sort"
	"strings"

	v1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
)

// Sample is a single timestamped value of the metric.
type Sample struct {
	Value     float64
	Timestamp int64
}

// TimeSeries represents a metric with given labels, with its values possibly changing in time.
type TimeSeries struct {
	Labels  map[string]string
	Samples []Sample
}

// WorkloadKind is k8s resource kind
type WorkloadKind string

// Resource Name
const (
	// workload kind is deployment
	WorkloadDeployment WorkloadKind = "Deployment"
)

type Metric struct {
	// to be extended when new datasource is added
	Namespace     string
	Kind          string
	APIVersion    string
	WorkloadName  string
	PodName       string
	ContainerName string
	Resource      v1.ResourceName
	Selectors     string
}

func (m *Metric) SetSelector(selectors map[string]string) {
	keys := make([]string, 0, len(selectors))
	for key := range selectors {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	var formattedParams []string
	for _, key := range keys {
		value := selectors[key]
		formattedParam := fmt.Sprintf("%s=\"%s\"", key, value)
		formattedParams = append(formattedParams, formattedParam)
	}
	m.Selectors = strings.Join(formattedParams, ",")
	klog.V(4).InfoS("Setting selectors", "selectors", m.Selectors)
}

type Query struct {
	// to be extended when new datasource is added
	Prometheus *PrometheusQuery
}
type PrometheusQuery struct {
	Query string
}

func NewTimeSeries() *TimeSeries {
	return &TimeSeries{
		Labels:  make(map[string]string),
		Samples: make([]Sample, 0),
	}
}

func (ts *TimeSeries) AppendLabel(key, val string) {
	ts.Labels[key] = val
}

func (ts *TimeSeries) AppendSample(timestamp int64, val float64) {
	ts.Samples = append(ts.Samples, Sample{Timestamp: timestamp, Value: val})
}
