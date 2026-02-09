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

package resourcepackage

import (
	"testing"

	v1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
)

func Test_ConvertResourcePackagesToNPDMetrics(t *testing.T) {
	t.Parallel()

	var min v1alpha1.Aggregator = "min"
	pinnedTrue := true
	pinnedFalse := false

	cases := []struct {
		name            string
		resourcePackage []ResourcePackageMetric
		want            []v1alpha1.ScopedNodeMetrics
	}{
		{
			name: "normal",
			resourcePackage: []ResourcePackageMetric{
				{
					NumaID: "0",
					ResourcePackages: []ResourcePackageItem{
						{
							ResourcePackage: v1alpha1.ResourcePackage{
								PackageName: "x2",
								Allocatable: &v1.ResourceList{
									v1.ResourceCPU:    resource.MustParse("32"),
									v1.ResourceMemory: resource.MustParse("64Gi"),
								},
							},
						},
						{
							ResourcePackage: v1alpha1.ResourcePackage{
								PackageName: "x8",
								Allocatable: &v1.ResourceList{
									v1.ResourceCPU:    resource.MustParse("64"),
									v1.ResourceMemory: resource.MustParse("512Gi"),
								},
							},
						},
					},
				},
				{
					NumaID: "1",
					ResourcePackages: []ResourcePackageItem{
						{
							ResourcePackage: v1alpha1.ResourcePackage{
								PackageName: "x4",
								Allocatable: &v1.ResourceList{
									v1.ResourceCPU:    resource.MustParse("8"),
									v1.ResourceMemory: resource.MustParse("32Gi"),
								},
							},
						},
						{
							ResourcePackage: v1alpha1.ResourcePackage{
								PackageName: "x6",
								Allocatable: &v1.ResourceList{
									v1.ResourceCPU:    resource.MustParse("8"),
									v1.ResourceMemory: resource.MustParse("48Gi"),
								},
							},
						},
					},
				},
			},
			want: []v1alpha1.ScopedNodeMetrics{
				{
					Scope: "resource-package",
					Metrics: []v1alpha1.MetricValue{
						{
							MetricName: "cpu",
							MetricLabels: map[string]string{
								"package-name": "x2",
								"numa-id":      "0",
							},
							Aggregator: &min,
							Value:      resource.MustParse("32"),
						},
						{
							MetricName: "memory",
							MetricLabels: map[string]string{
								"package-name": "x2",
								"numa-id":      "0",
							},
							Aggregator: &min,
							Value:      resource.MustParse("64Gi"),
						},
						{
							MetricName: "cpu",
							MetricLabels: map[string]string{
								"package-name": "x8",
								"numa-id":      "0",
							},
							Aggregator: &min,
							Value:      resource.MustParse("64"),
						},
						{
							MetricName: "memory",
							MetricLabels: map[string]string{
								"package-name": "x8",
								"numa-id":      "0",
							},
							Aggregator: &min,
							Value:      resource.MustParse("512Gi"),
						},
						{
							MetricName: "cpu",
							MetricLabels: map[string]string{
								"package-name": "x4",
								"numa-id":      "1",
							},
							Aggregator: &min,
							Value:      resource.MustParse("8"),
						},
						{
							MetricName: "memory",
							MetricLabels: map[string]string{
								"package-name": "x4",
								"numa-id":      "1",
							},
							Aggregator: &min,
							Value:      resource.MustParse("32Gi"),
						},
						{
							MetricName: "cpu",
							MetricLabels: map[string]string{
								"package-name": "x6",
								"numa-id":      "1",
							},
							Aggregator: &min,
							Value:      resource.MustParse("8"),
						},
						{
							MetricName: "memory",
							MetricLabels: map[string]string{
								"package-name": "x6",
								"numa-id":      "1",
							},
							Aggregator: &min,
							Value:      resource.MustParse("48Gi"),
						},
					},
				},
			},
		},
		{
			name:            "nil",
			resourcePackage: nil,
			want: []v1alpha1.ScopedNodeMetrics{
				{
					Scope: "resource-package",
				},
			},
		},
		{
			name: "pinned-cpuset",
			resourcePackage: []ResourcePackageMetric{
				{
					NumaID: "0",
					ResourcePackages: []ResourcePackageItem{
						{
							ResourcePackage: v1alpha1.ResourcePackage{
								PackageName: "x1",
								Allocatable: &v1.ResourceList{
									v1.ResourceCPU: resource.MustParse("4"),
								},
							},
							Config: &ResourcePackageConfig{
								PinnedCPUSet: &pinnedTrue,
							},
						},
						{
							ResourcePackage: v1alpha1.ResourcePackage{
								PackageName: "x2",
								Allocatable: &v1.ResourceList{
									v1.ResourceCPU: resource.MustParse("8"),
								},
							},
							Config: &ResourcePackageConfig{
								PinnedCPUSet: &pinnedFalse,
							},
						},
					},
				},
			},
			want: []v1alpha1.ScopedNodeMetrics{
				{
					Scope: "resource-package",
					Metrics: []v1alpha1.MetricValue{
						{
							MetricName: "cpu",
							MetricLabels: map[string]string{
								"package-name":  "x1",
								"numa-id":       "0",
								"pinned-cpuset": "true",
							},
							Aggregator: &min,
							Value:      resource.MustParse("4"),
						},
						{
							MetricName: "cpu",
							MetricLabels: map[string]string{
								"package-name":  "x2",
								"numa-id":       "0",
								"pinned-cpuset": "false",
							},
							Aggregator: &min,
							Value:      resource.MustParse("8"),
						},
					},
				},
			},
		},
	}

	for _, tt := range cases {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := ConvertResourcePackagesToNPDMetrics(tt.resourcePackage, metav1.Time{})
			if !apiequality.Semantic.DeepEqual(got, tt.want) {
				t.Errorf("got %v\nwant %v", got, tt.want)
			}

			pkgs := ConvertNPDMetricsToResourcePackages(got)
			if !apiequality.Semantic.DeepEqual(pkgs, tt.resourcePackage) {
				t.Errorf("ConvertResourcePackagesToNPDMetrics() got %v, want %v", pkgs, tt.resourcePackage)
			}
		})
	}
}
