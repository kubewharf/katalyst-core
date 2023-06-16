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
	"math"

	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

func (ha *HeadroomAssemblerCommon) getUtilBasedHeadroom(dynamicConfig *dynamic.Configuration,
	reclaimedMetrics *poolMetrics) (resource.Quantity, error) {
	if reclaimedMetrics == nil {
		return resource.Quantity{}, fmt.Errorf("invalid reclaimed metrics")
	}

	lastReclaimedCPU, err := ha.getLastReclaimedCPU()
	if err != nil {
		return resource.Quantity{}, err
	}

	headroom := ha.calculateHeadroom(dynamicConfig, float64(reclaimedMetrics.poolSize),
		reclaimedMetrics.coreAvgUtil, lastReclaimedCPU, float64(ha.metaServer.MachineInfo.NumCores))

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

	return 0, fmt.Errorf("cnr status resource allocatable reclaimed milli cpu not found")
}

// calculateHeadroom calculates headroom by taking into account the difference between the current
// and target core utilization of the reclaim pool
func (ha *HeadroomAssemblerCommon) calculateHeadroom(dynamicConfig *dynamic.Configuration,
	reclaimedSupplyCPU, reclaimedCPUCoreUtilization,
	lastReclaimedCPU, nodeCPUCapacity float64) float64 {
	var (
		oversold, result float64
	)

	targetCoreUtilization := dynamicConfig.TargetReclaimedCoreUtilization
	maxCoreUtilization := dynamicConfig.MaxReclaimedCoreUtilization
	maxOversoldRatio := dynamicConfig.MaxOversoldRate
	maxHeadroomCapacityRate := dynamicConfig.MaxHeadroomCapacityRate

	defer func() {
		general.Infof("reclaimed supply %.2f, reclaimed core utilization: %.2f (target: %.2f, max: %.2f), "+
			"last reclaimed: %.2f, oversold: %.2f, max oversold ratio: %.2f, final result: %.2f (max rate: %.2f, capacity: %.2f)",
			reclaimedSupplyCPU, reclaimedCPUCoreUtilization, targetCoreUtilization, maxCoreUtilization, lastReclaimedCPU,
			oversold, maxOversoldRatio, result, maxHeadroomCapacityRate, nodeCPUCapacity)
	}()

	// calculate the cpu resources that can be oversold to the reclaimed_cores workload, and consider that
	// the utilization rate of reclaimed cores is proportional to the cpu headroom.
	// if the maximum reclaimed core utilization is greater than zero, the oversold can be negative to reduce
	// cpu reporting reclaimed to avoid too many reclaimed_cores workloads being scheduled to that machine.
	if targetCoreUtilization > reclaimedCPUCoreUtilization {
		oversold = reclaimedSupplyCPU * (targetCoreUtilization - reclaimedCPUCoreUtilization)
	} else if maxCoreUtilization > 0 && reclaimedCPUCoreUtilization > maxCoreUtilization {
		oversold = reclaimedSupplyCPU * (maxCoreUtilization - reclaimedCPUCoreUtilization)
	}

	result = math.Max(lastReclaimedCPU+oversold, reclaimedSupplyCPU)
	result = math.Min(result, reclaimedSupplyCPU*maxOversoldRatio)
	if maxHeadroomCapacityRate > 0 {
		result = math.Min(result, nodeCPUCapacity*maxHeadroomCapacityRate)
	}

	return result
}
