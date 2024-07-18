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

import v1 "github.com/prometheus/client_golang/api/prometheus/v1"

const (
	workloadMaxCPUUsageTemplate = "max(sum(rate(container_cpu_usage_seconds_total{%s}[%s])) by (pod))"

	workloadMaxMemoryUsageTemplate = "max(sum(container_memory_working_set_bytes{%s}) by (pod))"

	defaultCPUUsageInterval = "2m"
)

var matchLabels = map[string]string{
	"namespace": "=",
	"pod":       "=~",
	"container": "!=",
}

type QueryShards struct {
	query   string
	windows []*v1.Range
}
