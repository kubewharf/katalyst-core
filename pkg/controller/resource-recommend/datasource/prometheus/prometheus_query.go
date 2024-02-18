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
	"fmt"

	datasourcetypes "github.com/kubewharf/katalyst-core/pkg/util/resource-recommend/types/datasource"
)

const (
	// ContainerCpuUsageQueryExpr is used to query container cpu usage by promql
	ContainerCpuUsageQueryExpr = `rate(container_cpu_usage_seconds_total{container!="POD",namespace="%s",pod=~"%s",container="%s"%s}[30s])`
	// ContainerMemUsageQueryExpr is used to query container cpu usage by promql
	ContainerMemUsageQueryExpr = `container_memory_working_set_bytes{container!="POD",namespace="%s",pod=~"%s",container="%s"%s}`
)

const (
	WorkloadSuffixRuleForDeployment = `[a-z0-9]+-[a-z0-9]{5}$`
)

func GetContainerCpuUsageQueryExp(namespace string, workloadName string, kind string, containerName string, extraFilters string) string {
	if len(extraFilters) != 0 {
		extraLabels := fmt.Sprintf(",%s", extraFilters)
		return fmt.Sprintf(ContainerCpuUsageQueryExpr, namespace, convertWorkloadNameToPods(workloadName, kind), containerName, extraLabels)
	}
	return fmt.Sprintf(ContainerCpuUsageQueryExpr, namespace, convertWorkloadNameToPods(workloadName, kind), containerName, extraFilters)
}

func GetContainerMemUsageQueryExp(namespace string, workloadName string, kind string, containerName string, extraFilters string) string {
	if len(extraFilters) != 0 {
		extraLabels := fmt.Sprintf(",%s", extraFilters)
		return fmt.Sprintf(ContainerMemUsageQueryExpr, namespace, convertWorkloadNameToPods(workloadName, kind), containerName, extraLabels)
	}
	return fmt.Sprintf(ContainerMemUsageQueryExpr, namespace, convertWorkloadNameToPods(workloadName, kind), containerName, extraFilters)
}

func convertWorkloadNameToPods(workloadName string, workloadKind string) string {
	switch workloadKind {
	case string(datasourcetypes.WorkloadDeployment):
		return fmt.Sprintf("^%s-%s", workloadName, WorkloadSuffixRuleForDeployment)
	}
	return fmt.Sprintf("^%s-%s", workloadName, `.*`)
}

func GetExtraFilters(extraFilters string, baseFilter string) string {
	if extraFilters == "" {
		return baseFilter
	}
	if baseFilter == "" {
		return extraFilters
	}
	return extraFilters + "," + baseFilter
}
