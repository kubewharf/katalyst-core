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
	"context"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	workloadapi "github.com/kubewharf/katalyst-api/pkg/apis/workload/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/consts"
)

const (
	defaultMemoryBandwidthRequest = 0
)

// GetContainerMemoryBandwidthRequest gets the memory bandwidth request for pod with given cpu request
func GetContainerMemoryBandwidthRequest(profilingManager ServiceProfilingManager, podMeta metav1.ObjectMeta, cpuRequest int) (int, error) {
	mbwRequest := defaultMemoryBandwidthRequest
	memoryBandwidth, err := profilingManager.ServiceAggregateMetrics(context.Background(), podMeta, consts.SPDAggMetricNameMemoryBandwidth,
		false, workloadapi.Avg, workloadapi.Sum, workloadapi.Max)
	if err != nil && !errors.IsNotFound(err) {
		return 0, err
	} else if err == nil && memoryBandwidth != nil {
		mbwRequest = int(memoryBandwidth.Value()) * cpuRequest
	}

	return mbwRequest, nil
}
