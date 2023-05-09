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

package headroompolicy

import (
	"context"
	"fmt"
	"math"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/sysadvisor/qosaware/resource/cpu/headroom"
	pkgconsts "github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/metric"
)

type PolicyAdaptive struct {
	*PolicyBase

	policyAdaptiveConfig *headroom.PolicyAdaptiveConfiguration
}

func NewPolicyAdaptive(regionName string, conf *config.Configuration, _ interface{}, metaReader metacache.MetaReader,
	metaServer *metaserver.MetaServer, emitter metrics.MetricEmitter) HeadroomPolicy {
	p := &PolicyAdaptive{
		PolicyBase:           NewPolicyBase(regionName, metaReader, metaServer, emitter),
		policyAdaptiveConfig: conf.CPUHeadroomPolicyConfiguration.PolicyAdaptive,
	}

	return p
}

func (p *PolicyAdaptive) Update() error {
	lastReclaimedCPU, err := p.getLastReclaimedCPU()
	if err != nil {
		return fmt.Errorf("get last reclaimed milli cpu failed: %v", err)
	}

	reclaimedPoolMetrics, err := p.getReclaimedPoolMetrics()
	if err != nil {
		return fmt.Errorf("calculate reclaimed cpu core utilization failed: %v", err)
	}

	p.headroom = p.calculateHeadroom(float64(reclaimedPoolMetrics.poolSize), reclaimedPoolMetrics.coreAvgUtilization,
		lastReclaimedCPU, float64(p.metaServer.MachineInfo.NumCores))
	return nil
}

func (p *PolicyAdaptive) GetHeadroom() (float64, error) {
	return p.headroom, nil
}

func (p *PolicyAdaptive) getLastReclaimedCPU() (float64, error) {
	cnr, err := p.metaServer.CNRFetcher.GetCNR(context.Background())
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

type poolMetrics struct {
	coreAvgUtilization float64
	poolSize           int
}

// getReclaimedPoolMetrics get reclaimed pool metrics, including the average utilization of each core in
// the reclaimed pool and the size of the pool
func (p *PolicyAdaptive) getReclaimedPoolMetrics() (*poolMetrics, error) {
	reclaimedInfo, ok := p.metaReader.GetPoolInfo(state.PoolNameReclaim)
	if !ok {
		return nil, fmt.Errorf("failed get reclaim pool info")
	}

	cpuSet := reclaimedInfo.TopologyAwareAssignments.MergeCPUSet()
	coreAvgUtilization := p.metaServer.AggregateCoreMetric(cpuSet, pkgconsts.MetricCPUUsage, metric.AggregatorAvg)
	return &poolMetrics{
		coreAvgUtilization: coreAvgUtilization / 100.,
		poolSize:           cpuSet.Size(),
	}, nil
}

// calculateHeadroom calculates headroom by taking into account the difference between the current
// and target core utilization of the reclaim pool
func (p *PolicyAdaptive) calculateHeadroom(reclaimedSupplyCPU, reclaimedCPUCoreUtilization,
	lastReclaimedCPU, nodeCPUCapacity float64) float64 {
	var (
		oversold, result float64
	)

	targetCoreUtilization := p.policyAdaptiveConfig.ReclaimedCPUTargetCoreUtilization
	maxCoreUtilization := p.policyAdaptiveConfig.ReclaimedCPUMaxCoreUtilization
	maxOversoldRatio := p.policyAdaptiveConfig.ReclaimedCPUMaxOversoldRate
	maxHeadroomCapacityRate := p.policyAdaptiveConfig.ReclaimedCPUMaxHeadroomCapacityRate

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
