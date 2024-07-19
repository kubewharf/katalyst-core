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

package util

import "github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"

func InsertNPDScopedNodeMetrics(
	status *v1alpha1.NodeProfileDescriptorStatus,
	scopedNodeMetrics *v1alpha1.ScopedNodeMetrics,
) {
	if status == nil || scopedNodeMetrics == nil {
		return
	}

	if status.NodeMetrics == nil {
		status.NodeMetrics = []v1alpha1.ScopedNodeMetrics{}
	}

	for i := range status.NodeMetrics {
		if status.NodeMetrics[i].Scope == scopedNodeMetrics.Scope {
			status.NodeMetrics[i].Metrics = scopedNodeMetrics.Metrics
			return
		}
	}

	status.NodeMetrics = append(status.NodeMetrics, *scopedNodeMetrics)
}

func InsertNPDScopedPodMetrics(
	status *v1alpha1.NodeProfileDescriptorStatus,
	scopedPodMetrics *v1alpha1.ScopedPodMetrics,
) {
	if status == nil || scopedPodMetrics == nil {
		return
	}

	if status.PodMetrics == nil {
		status.PodMetrics = []v1alpha1.ScopedPodMetrics{}
	}

	for i := range status.PodMetrics {
		if status.PodMetrics[i].Scope == scopedPodMetrics.Scope {
			status.PodMetrics[i].PodMetrics = scopedPodMetrics.PodMetrics
			return
		}
	}

	status.PodMetrics = append(status.PodMetrics, *scopedPodMetrics)
}
