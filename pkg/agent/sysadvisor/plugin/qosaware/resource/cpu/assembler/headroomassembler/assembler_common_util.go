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

package headroomassembler

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/helper"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
)

func (ha *HeadroomAssemblerCommon) getUtilBasedHeadroom(dynamicConfig *dynamic.Configuration,
	reclaimedMetrics *poolMetrics,
) (resource.Quantity, error) {
	if reclaimedMetrics == nil {
		return resource.Quantity{}, fmt.Errorf("invalid reclaimed metrics")
	}

	lastReclaimedCPU, err := ha.getLastReclaimedCPU()
	if err != nil {
		return resource.Quantity{}, err
	}

	headroom, err := helper.EstimateUtilBasedCapacity(
		helper.UtilBasedCapacityOptions{
			TargetUtilization: dynamicConfig.TargetReclaimedCoreUtilization,
			MaxUtilization:    dynamicConfig.MaxReclaimedCoreUtilization,
			MaxOversoldRate:   dynamicConfig.MaxOversoldRate,
			MaxCapacity:       dynamicConfig.MaxHeadroomCapacityRate * float64(ha.metaServer.MachineInfo.NumCores),
		},
		float64(reclaimedMetrics.poolSize),
		reclaimedMetrics.coreAvgUtil,
		lastReclaimedCPU,
	)
	if err != nil {
		return resource.Quantity{}, err
	}

	return *resource.NewQuantity(int64(headroom), resource.DecimalSI), nil
}

func (ha *HeadroomAssemblerCommon) getLastReclaimedCPU() (float64, error) {
	cnr, err := ha.metaServer.CNRFetcher.GetCNR(context.Background())
	if err != nil {
		return 0, err
	}

	if cnr.Status.Resources.Allocatable != nil {
		if reclaimedMilliCPU, ok := (*cnr.Status.Resources.Allocatable)[consts.ReclaimedResourceMilliCPU]; ok {
			return float64(reclaimedMilliCPU.Value()) / 1000, nil
		}
	}

	klog.Errorf("cnr status resource allocatable reclaimed milli cpu not found")
	return 0, nil
}
