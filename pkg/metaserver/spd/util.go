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

	pkgerrors "github.com/pkg/errors"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	workloadapi "github.com/kubewharf/katalyst-api/pkg/apis/workload/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/util"
)

const (
	defaultMemoryBandwidthRequest = 0
)

// GetContainerMemoryBandwidthRequest gets the memory bandwidth request for pod with given cpu request
func GetContainerMemoryBandwidthRequest(profilingManager ServiceProfilingManager, podMeta metav1.ObjectMeta, cpuRequest int) (int, error) {
	mbwRequest := defaultMemoryBandwidthRequest
	memoryBandwidthMetrics, err := profilingManager.ServiceAggregateMetrics(context.Background(), podMeta, consts.SPDAggMetricNameMemoryBandwidth,
		false, workloadapi.Avg, workloadapi.Sum)
	if err != nil && !IsSPDNameOrResourceNotFound(err) {
		return 0, err
	} else if err == nil && memoryBandwidthMetrics != nil {
		memoryBandwidth, err := util.AggregateMetrics(memoryBandwidthMetrics, workloadapi.Max)
		if err != nil {
			return 0, err
		}
		mbwRequest = int(memoryBandwidth.Value()) * cpuRequest
	}

	return mbwRequest, nil
}

// GetContainerServiceProfileRequest gets the memory bandwidth request for pod with given cpu request
func GetContainerServiceProfileRequest(profilingManager ServiceProfilingManager, podMeta metav1.ObjectMeta,
	name v1.ResourceName,
) ([]resource.Quantity, error) {
	metrics, err := profilingManager.ServiceAggregateMetrics(context.Background(), podMeta, name,
		false, workloadapi.Avg, workloadapi.Sum)
	if err != nil && !errors.IsNotFound(err) {
		return nil, err
	} else if err == nil && metrics != nil {
		return metrics, nil
	}

	return nil, nil
}

// IsSPDNameOrResourceNotFound returns true if the given error is caused by SPD name not found or SPD not found.
func IsSPDNameOrResourceNotFound(err error) bool {
	return errors.IsNotFound(err) || IsSPDNameNotFound(err)
}

// IsSPDNameNotFound returns true if the given error is caused by SPD name not found.
func IsSPDNameNotFound(err error) bool {
	return pkgerrors.Is(err, SPDNameNotFoundError)
}
