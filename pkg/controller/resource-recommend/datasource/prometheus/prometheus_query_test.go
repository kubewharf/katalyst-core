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
	"testing"

	datasourcetypes "github.com/kubewharf/katalyst-core/pkg/util/resource-recommend/types/datasource"
)

func TestGetContainerCpuUsageQueryExp(t *testing.T) {
	type args struct {
		namespace     string
		workloadName  string
		kind          string
		containerName string
	}
	tests := []struct {
		name string
		args args
		want string
	}{
		{
			name: "Deployment workload kind",
			args: args{
				namespace:     "my-namespace",
				workloadName:  "my-deployment",
				kind:          string(datasourcetypes.WorkloadDeployment),
				containerName: "test",
			},
			want: "rate(container_cpu_usage_seconds_total{container!=\"POD\",namespace=\"my-namespace\",pod=~\"^my-deployment-[a-z0-9]+-[a-z0-9]{5}$\",container=\"test\"}[30s])",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := GetContainerCpuUsageQueryExp(tt.args.namespace, tt.args.workloadName, tt.args.kind, tt.args.containerName, ""); got != tt.want {
				t.Errorf("GetContainerCpuUsageQueryExp() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGetContainerMemUsageQueryExp(t *testing.T) {
	type args struct {
		namespace     string
		workloadName  string
		kind          string
		containerName string
		extraFilter   string
	}
	tests := []struct {
		name string
		args args
		want string
	}{
		{
			name: "Deployment workload kind",
			args: args{
				namespace:     "my-namespace",
				workloadName:  "my-deployment",
				kind:          string(datasourcetypes.WorkloadDeployment),
				containerName: "test",
				extraFilter:   "",
			},
			want: "container_memory_working_set_bytes{container!=\"POD\",namespace=\"my-namespace\",pod=~\"^my-deployment-[a-z0-9]+-[a-z0-9]{5}$\",container=\"test\"}",
		},
		{
			name: "Deployment workload kind",
			args: args{
				namespace:     "my-namespace",
				workloadName:  "my-deployment",
				kind:          string(datasourcetypes.WorkloadDeployment),
				containerName: "test2",
				extraFilter:   "key1=\"value\"",
			},
			want: "container_memory_working_set_bytes{container!=\"POD\",namespace=\"my-namespace\",pod=~\"^my-deployment-[a-z0-9]+-[a-z0-9]{5}$\",container=\"test2\",key1=\"value\"}",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := GetContainerMemUsageQueryExp(tt.args.namespace, tt.args.workloadName, tt.args.kind, tt.args.containerName, tt.args.extraFilter); got != tt.want {
				t.Errorf("GetContainerMemUsageQueryExp() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_convertWorkloadNameToPods(t *testing.T) {
	type args struct {
		workloadName string
		workloadKind string
	}
	tests := []struct {
		name string
		args args
		want string
	}{
		{
			name: "Deployment workload kind",
			args: args{
				workloadName: "my-deployment",
				workloadKind: string(datasourcetypes.WorkloadDeployment),
			},
			want: "^my-deployment-[a-z0-9]+-[a-z0-9]{5}$",
		},
		{
			name: "Unknown workload kind",
			args: args{
				workloadName: "my-workload",
				workloadKind: "unknown",
			},
			want: "^my-workload-.*",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := convertWorkloadNameToPods(tt.args.workloadName, tt.args.workloadKind); got != tt.want {
				t.Errorf("convertWorkloadNameToPods() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGetExtraFilters(t *testing.T) {
	type args struct {
		extraFilters string
		baseFilter   string
	}
	tests := []struct {
		name string
		args args
		want string
	}{
		{
			name: "all is empty",
			args: args{
				extraFilters: "",
				baseFilter:   "",
			},
			want: "",
		},
		{
			name: "extraFilters is empty",
			args: args{
				extraFilters: "",
				baseFilter:   "cluster=\"cfeaf782fasdfe\"",
			},
			want: "cluster=\"cfeaf782fasdfe\"",
		},
		{
			name: "baseFilter is empty",
			args: args{
				extraFilters: "service=\"Katalyst\"",
				baseFilter:   "",
			},
			want: "service=\"Katalyst\"",
		},
		{
			name: "all is not empty",
			args: args{
				extraFilters: "service=\"Katalyst\"",
				baseFilter:   "cluster=\"cfeaf782fasdfe\"",
			},
			want: "service=\"Katalyst\",cluster=\"cfeaf782fasdfe\"",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := GetExtraFilters(tt.args.extraFilters, tt.args.baseFilter); got != tt.want {
				t.Errorf("GetExtraFilters() = %v, want %v", got, tt.want)
			}
		})
	}
}
