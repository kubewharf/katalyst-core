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

package mbm

import (
	"context"
	"time"

	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/mbm/strategy"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/types"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/external/mbm"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

const (
	MemoryBandwidthManagement = "mbm"

	// controlling hype-parameters in percentage
	MEMORY_BANDWIDTH_PHYSICAL_NUMA_PAINPOINT       = 105
	MEMORY_BANDWIDTH_PHYSICAL_NUMA_SWEETPOINT      = 95
	MEMORY_BANDWIDTH_PHYSICAL_NUMA_UNTHROTTLEPOINT = 80

	MEMORY_BANDWIDTH_INCREASE_MIN = 256 // the minimum incremental value
	MEMORY_BANDWIDTH_DECREASE_MIN = 512 // the minimum decremental value
)

type NUMAStater interface {
	GetMachineState() state.NUMANodeMap
}

type Controller struct {
	metricEmitter metrics.MetricEmitter
	metricReader  types.MetricsReader
	numaStater    NUMAStater
	mbAdjust      mbm.MBAdjuster

	numaThrottled sets.Int

	packageMap         map[int][]int // package id --> numa nodes in the package
	interval           time.Duration
	bandwidthThreshold int64
	minDeductionStep   float64
	minIncreaseStep    float64
}

func (c Controller) Run(ctx context.Context) {
	general.Infof("mbm controller is starting")
	wait.Until(c.run, c.interval, ctx.Done())
}

func (c Controller) run() {
	for p, nodes := range c.packageMap {
		c.processPackage(p, nodes)
	}
}

// getActiveNodeSets divides the nodes into workload-associated sets
// only nodes having active workload assigned shall be considered
func (c Controller) getActiveNodeSets(nodes []int) map[string]sets.Int {
	nodeSets := make(map[string]sets.Int)
	states := c.numaStater.GetMachineState()
	for _, node := range nodes {
		nodeState := states[node]
		for podUID := range nodeState.PodEntries {
			if _, ok := nodeSets[podUID]; !ok {
				nodeSets[podUID] = sets.Int{}
			}
			nodeSets[podUID].Insert(node)
		}
	}
	return nodeSets
}

// todo: to leverage the active group MB of whole package to save metric store fetching
func (c Controller) getNodeMBMetrics(nodes sets.Int) (strategy.GroupMB, error) {
	mbs := make(map[int]float64)
	for node := range nodes {
		nodeMB, err := c.metricReader.GetNumaMetric(node, "mb-mbps")
		if err != nil {
			return nil, err
		}
		mbs[node] = nodeMB.Value
	}
	return mbs, nil
}

func (c Controller) getActiveGroupMBs(nodes []int) (toSkip bool, mbs strategy.GroupMBs, err error) {
	activeGroups := c.getActiveNodeSets(nodes)
	if len(activeGroups) <= 1 {
		// no need to take any action; neither an error
		toSkip = true
		return
	}

	mbs, err = c.getGroupMBMetrics(activeGroups)
	return
}

func (c Controller) getGroupMBMetrics(actives map[string]sets.Int) (strategy.GroupMBs, error) {
	groups := make(strategy.GroupMBs, 0)
	for _, nodes := range actives {
		groupMB, err := c.getNodeMBMetrics(nodes)
		if err != nil {
			return nil, err
		}
		groups = append(groups, groupMB)
	}
	return groups, nil
}

func (c Controller) processPackage(packageID int, nodes []int) {
	// get the metrics
	currMetric, err := c.metricReader.GetPackageMetric(packageID, "rw-mbps")
	if err != nil {
		c.metricEmitter.StoreInt64("package metric reading error", 1, metrics.MetricTypeNameCount)
		return
	}

	mbwPackage := int64(currMetric.Value)

	// workloads are hi-prio (lo-prio not in scope of this stage)
	// adjust mem bandwidth based on mbw metrics, if applicable
	if mbwPackage >= c.bandwidthThreshold/100*MEMORY_BANDWIDTH_PHYSICAL_NUMA_PAINPOINT {
		// over the threshold - to throttle noisy neighbors
		// identify the numa-node-set (nodes of one workload) of this package
		toSkip, activeGroupMBs, err := c.getActiveGroupMBs(nodes)
		if err != nil {
			// error happened; skip for now
			c.metricEmitter.StoreInt64("numa metric reading error", 1, metrics.MetricTypeNameCount)
			return
		}

		if toSkip {
			return
		}

		shareAllocator := strategy.NewAllocator()
		shares := shareAllocator.AllocateDeductions(activeGroupMBs, float64(mbwPackage), float64(c.bandwidthThreshold))
		for _, group := range shares {
			for node, share := range group {
				// ignore if the deduction step is too small
				if share >= c.minDeductionStep {
					c.numaThrottled.Insert(node)
					if err := c.mbAdjust.AdjustNumaMB(node,
						uint64(activeGroupMBs.NodeMBAverage()),
						uint64(share),
						mbm.MEMORY_BANDWIDTH_CONTROL_REDUCE,
					); err == nil {
						c.numaThrottled.Insert(node)
					}
				}
			}
		}
		return
	}

	if mbwPackage <= c.bandwidthThreshold/100*MEMORY_BANDWIDTH_PHYSICAL_NUMA_SWEETPOINT &&
		mbwPackage > c.bandwidthThreshold/100*MEMORY_BANDWIDTH_PHYSICAL_NUMA_UNTHROTTLEPOINT {
		// below the threshold a little - to grant some nodes' a little more bandwidth
		// we take precautious and gradual steps when releasing the throttle
		activeGroups := c.getActiveNodeSets(nodes)
		activeGroupMBs, err := c.getGroupMBMetrics(activeGroups)
		if err != nil {
			// error happened; skip for now
			return
		}

		mbThrottleds, err := c.getNodeMBMetrics(c.numaThrottled)
		if err != nil {
			// error happened; skip for now
			return
		}

		shareAllocator := strategy.NewAllocator()
		shares := shareAllocator.AllocateIncreases(mbThrottleds, activeGroupMBs, float64(mbwPackage), float64(c.bandwidthThreshold))

		for node, share := range shares {
			if share >= c.minIncreaseStep {
				// ok to skip if error happened this time
				_ = c.mbAdjust.AdjustNumaMB(node, uint64(mbThrottleds.NodeMBAverage()), uint64(share), mbm.MEMORY_BANDWIDTH_CONTROL_RAISE)
			}
		}

		return
	}

	if mbwPackage < c.bandwidthThreshold/100*MEMORY_BANDWIDTH_PHYSICAL_NUMA_UNTHROTTLEPOINT {
		// under the watermark significantly - well it seems ok to un-throttle
		for _, node := range nodes {
			if err := c.mbAdjust.AdjustNumaMB(node, 0, 0, mbm.MEMORY_BANDWIDTH_CONTROL_UNTHROTTLE); err == nil {
				c.numaThrottled.Delete(node)
			}
		}
		return
	}

	// around the threshold bar; no need to take action this time
	return
}

func NewController(metricEmitter metrics.MetricEmitter, metricReader types.MetricsReader,
	stater NUMAStater, mbAdjuster mbm.MBAdjuster,
	interval time.Duration, bandwidthThreshold int, packageMap map[int][]int,
) *Controller {
	return &Controller{
		metricEmitter:      metricEmitter.WithTags(MemoryBandwidthManagement),
		metricReader:       metricReader,
		numaStater:         stater,
		mbAdjust:           mbAdjuster,
		packageMap:         packageMap,
		interval:           interval,
		bandwidthThreshold: int64(bandwidthThreshold),
		minDeductionStep:   MEMORY_BANDWIDTH_DECREASE_MIN,
		minIncreaseStep:    MEMORY_BANDWIDTH_INCREASE_MIN,
	}
}
