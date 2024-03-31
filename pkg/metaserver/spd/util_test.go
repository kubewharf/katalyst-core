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

package spd

import (
	"testing"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/metrics/pkg/apis/metrics/v1beta1"

	workloadapi "github.com/kubewharf/katalyst-api/pkg/apis/workload/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/consts"
)

func TestGetContainerMemoryBandwidthRequest(t *testing.T) {
	t.Parallel()

	type args struct {
		profilingManager ServiceProfilingManager
		podMeta          metav1.ObjectMeta
		cpuRequest       int
	}
	tests := []struct {
		name    string
		args    args
		want    int
		wantErr bool
	}{
		{
			name: "test two container",
			args: args{
				profilingManager: NewServiceProfilingManager(DummySPDFetcher{
					SPD: &workloadapi.ServiceProfileDescriptor{
						Status: workloadapi.ServiceProfileDescriptorStatus{
							AggMetrics: []workloadapi.AggPodMetrics{
								{
									Aggregator: workloadapi.Avg,
									Items: []v1beta1.PodMetrics{
										{
											Timestamp: metav1.NewTime(time.Date(1970, 0, 0, 0, 0, 0, 0, time.UTC)),
											Window:    metav1.Duration{Duration: time.Hour},
											Containers: []v1beta1.ContainerMetrics{
												{
													Name: "container-1",
													Usage: map[v1.ResourceName]resource.Quantity{
														consts.SPDAggMetricNameMemoryBandwidth: resource.MustParse("10"),
													},
												},
												{
													Name: "container-2",
													Usage: map[v1.ResourceName]resource.Quantity{
														consts.SPDAggMetricNameMemoryBandwidth: resource.MustParse("10"),
													},
												},
											},
										},
										{
											Timestamp: metav1.NewTime(time.Date(1970, 0, 0, 1, 0, 0, 0, time.UTC)),
											Window:    metav1.Duration{Duration: time.Hour},
											Containers: []v1beta1.ContainerMetrics{
												{
													Name: "container-1",
													Usage: map[v1.ResourceName]resource.Quantity{
														consts.SPDAggMetricNameMemoryBandwidth: resource.MustParse("20"),
													},
												},
												{
													Name: "container-2",
													Usage: map[v1.ResourceName]resource.Quantity{
														consts.SPDAggMetricNameMemoryBandwidth: resource.MustParse("20"),
													},
												},
											},
										},
									},
								},
							},
						},
					},
				}),
				podMeta:    metav1.ObjectMeta{},
				cpuRequest: 10,
			},
			want:    400,
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := GetContainerMemoryBandwidthRequest(tt.args.profilingManager, tt.args.podMeta, tt.args.cpuRequest)
			if (err != nil) != tt.wantErr {
				t.Errorf("GetContainerMemoryBandwidthRequest() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("GetContainerMemoryBandwidthRequest() got = %v, want %v", got, tt.want)
			}
		})
	}
}
