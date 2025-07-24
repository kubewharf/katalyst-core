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

package metricbased

import (
	"fmt"
	"sync"
	"time"

	pkgerrors "github.com/pkg/errors"
	"k8s.io/apimachinery/pkg/util/wait"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/cpueviction/strategy"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/hintoptimizer"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/hintoptimizer/policy"
	hintoptimizerutil "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/hintoptimizer/util"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/metricthreshold"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/helper"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	"github.com/kubewharf/katalyst-core/pkg/util/strategygroup"
	"sort"
)

const HintOptimizerNameMetricBased = "metric_based"

type metricBasedHintOptimizer struct {
	mutex      sync.RWMutex
	conf       *config.Configuration
	emitter    metrics.MetricEmitter
	metaServer *metaserver.MetaServer
	state      state.State

	reservedCPUs      machine.CPUSet
	numaMetrics       map[int]strategy.SubEntries
	metricSampleTime  time.Duration
	metricSampleCount int

	requestScoreThreshold      float64
	usageThresholdExpandFactor float64
}

func NewMetricBasedHintOptimizer(
	options policy.HintOptimizerFactoryOptions,
) (hintoptimizer.HintOptimizer, error) {
	numaMetrics := make(map[int]strategy.SubEntries)
	numaNodes := options.MetaServer.CPUDetails.NUMANodes().ToSliceNoSortInt()
	for _, numaID := range numaNodes {
		numaMetrics[numaID] = make(strategy.SubEntries)
	}
	return &metricBasedHintOptimizer{
		conf:                       options.Conf,
		metaServer:                 options.MetaServer,
		emitter:                    options.Emitter,
		state:                      options.State,
		reservedCPUs:               options.ReservedCPUs,
		numaMetrics:                numaMetrics,
		metricSampleTime:           10 * time.Minute,
		metricSampleCount:          10,
		requestScoreThreshold:      0.8,
		usageThresholdExpandFactor: 1.2,
	}, nil
}

func (o *metricBasedHintOptimizer) OptimizeHints(
	request hintoptimizer.Request,
	hints *pluginapi.ListOfTopologyHints,
) error {
	err := hintoptimizerutil.GenericOptimizeHintsCheck(request, hints)
	if err != nil {
		general.Errorf("GenericOptimizeHintsCheck failed with error: %v", err)
		return err
	}
	//
	//if !o.enableMetricPreferredNUMAAllocation() {
	//	return pkgerrors.Wrapf(hintoptimizerutil.ErrHintOptimizerSkip, "metricPolicyEnabled is false")
	//}

	numaNodes, err := hintoptimizerutil.GetSingleNUMATopologyHintNUMANodes(hints.Hints)
	if err != nil {
		return err
	}

	hybridPolicyEnabled, err := strategygroup.IsStrategyEnabledForNode(consts.StrategyNameHybridNUMAAllocation, true, o.conf)
	if err != nil {
		general.Warningf("IsStrategyEnabledForNode failed with error: %v", err)
	}
	general.Infof("hybridPolicyEnabled: %v", hybridPolicyEnabled)

	metricPolicyEnabled, _ := strategygroup.IsStrategyEnabledForNode(consts.StrategyNameMetricPreferredNUMAAllocation, o.conf.EnableMetricPreferredNumaAllocation, o.conf)
	general.Infof("metricPolicyEnabled: %v", metricPolicyEnabled)

	if !metricPolicyEnabled && !hybridPolicyEnabled {
		return pkgerrors.Wrapf(hintoptimizerutil.ErrHintOptimizerSkip, "metricPolicyEnabled is false")
	}

	if hybridPolicyEnabled {
		_ = o.emitter.StoreInt64(util.MetricNameHybridNUMAAllocationEnabled, 1, metrics.MetricTypeNameCount)
		o.populateHintsByHybridPolicy(numaNodes, hints, o.state.GetMachineState(), request.CPURequest)

		if err != nil {
			general.Errorf("populateHintsByHybridPolicy failed with error: %v", err)
		} else {
			_ = o.emitter.StoreInt64(util.MetricNameHybridNUMAAllocationSuccess, 1, metrics.MetricTypeNameCount)
		}
	} else {
		_ = o.emitter.StoreInt64(util.MetricNameMetricBasedNUMAAllocationEnabled, 1, metrics.MetricTypeNameCount)
		err = o.populateHintsByMetricPolicy(hints, o.state.GetMachineState(), request.CPURequest)
		if err != nil {
			general.Errorf("populateHintsByMetricPolicy failed with error: %v", err)
			return pkgerrors.Wrapf(hintoptimizerutil.ErrHintOptimizerSkip, "failed %v", err)
		} else {
			_ = o.emitter.StoreInt64(util.MetricNameMetricBasedNUMAAllocationSuccess, 1, metrics.MetricTypeNameCount)
		}
	}

	return nil
}

func (o *metricBasedHintOptimizer) enableMetricPreferredNUMAAllocation() bool {
	metricPolicyEnabled, _ := strategygroup.IsStrategyEnabledForNode(consts.StrategyNameMetricPreferredNUMAAllocation, o.conf.EnableMetricPreferredNumaAllocation, o.conf)
	general.Infof("metricPolicyEnabled: %v", metricPolicyEnabled)
	return metricPolicyEnabled
}

func (o *metricBasedHintOptimizer) enableHybridPreferredNUMAAllocation() bool {
	hybridPolicyEnabled, _ := strategygroup.IsStrategyEnabledForNode(consts.StrategyNameHybridNUMAAllocation, true, o.conf)
	general.Infof("hybridPolicyEnabled: %v", hybridPolicyEnabled)
	return hybridPolicyEnabled
}

func (o *metricBasedHintOptimizer) Run(stopCh <-chan struct{}) {
	wait.Until(o.collectNUMAMetrics, 30*time.Second, stopCh)
}

func (o *metricBasedHintOptimizer) populateHintsByMetricPolicy(
	hints *pluginapi.ListOfTopologyHints, machineState state.NUMANodeMap, request float64,
) error {
	thresholdNameToValue, err := o.getNUMAMetricThresholdNameToValue()
	if err != nil {
		return fmt.Errorf("getNUMAMetricThresholdNameToValue faield with error: %v", err)
	}

	for _, hint := range hints.Hints {
		numaNodes := hint.Nodes
		prefer := hint.Preferred
		for _, numaID := range numaNodes {
			for thresholdName, resourceName := range metricthreshold.ThresholdNameToResourceName {
				threshold, found := thresholdNameToValue[thresholdName]
				if !found {
					return fmt.Errorf("threshold %s not found", thresholdName)
				}

				overThreshold, err := o.isNUMAOverThreshold(int(numaID), threshold, request, resourceName, machineState)
				if err != nil {
					return fmt.Errorf("get overThreshold for numa: %d failed: %v", numaID, err)
				}

				if overThreshold {
					prefer = false
					break
				}
			}
			if !prefer {
				_ = o.emitter.StoreInt64(util.MetricNameNUMAMetricOverThreshold, 1, metrics.MetricTypeNameRaw,
					metrics.MetricTag{Key: "numa_id", Val: fmt.Sprintf("%d", numaID)})
				break
			}
		}

		general.Infof("numaNodes: %+v, prefer: %v", numaNodes, prefer)
		hint.Preferred = prefer
	}
	return nil
}

func (o *metricBasedHintOptimizer) isNUMAOverThreshold(numa int, threshold, request float64, resourceName string, machineState state.NUMANodeMap) (bool, error) {
	o.mutex.RLock()
	defer o.mutex.RUnlock()

	if machineState == nil || machineState[numa] == nil {
		return false, fmt.Errorf("invalid machineState")
	} else if o.numaMetrics[numa][resourceName] == nil {
		return false, fmt.Errorf("invalid machineState")
	}

	used, err := o.numaMetrics[numa][resourceName].AvgAfterTimestampWithCountBound(time.Now().UnixNano()-o.metricSampleTime.Nanoseconds(), o.metricSampleCount)
	if err != nil {
		return false, fmt.Errorf("get numa metric failed: %v", err)
	}

	used += request

	allocatable := float64(machineState[numa].GetFilteredDefaultCPUSet(nil, nil).Difference(o.reservedCPUs).Size())

	if allocatable == 0 {
		return false, fmt.Errorf("invalid allocatable")
	}

	general.Infof("numa: %d, resourceName: %s, request: %.2f, used: %.2f, allocatable: %.2f, ratio: %.2f, threshold: %.2f, overThreshold: %v",
		numa, resourceName, request,
		used, allocatable, used/allocatable,
		threshold, (used/allocatable) >= threshold)

	return (used / allocatable) >= threshold, nil
}

func (o *metricBasedHintOptimizer) collectNUMAMetrics() {
	if !o.enableMetricPreferredNUMAAllocation() && !o.enableHybridPreferredNUMAAllocation() {
		return
	}

	_ = o.emitter.StoreInt64(util.MetricNameCollectNUMAMetrics, 1, metrics.MetricTypeNameRaw)
	collectTime := time.Now().UnixNano()
	machineState := o.state.GetMachineState()
	o.mutex.Lock()
	defer o.mutex.Unlock()
	for numaID, subEntries := range o.numaMetrics {
		for _, resourceName := range metricthreshold.ThresholdNameToResourceName {
			value, err := o.getNUMAMetric(numaID, resourceName, machineState)
			if err != nil {
				general.Errorf("getNUMAMetric failed with error: %v", err)
				continue
			}

			if subEntries[resourceName] == nil {
				subEntries[resourceName] = strategy.CreateMetricRing(o.metricSampleCount)
			}

			subEntries[resourceName].Push(&strategy.MetricSnapshot{
				Info: strategy.MetricInfo{
					Value: value,
				},
				Time: collectTime,
			})

			general.Infof("numa: %d, resourceName: %s, value: %.2f, windowSize: %d", numaID, resourceName, value, subEntries[resourceName].Len())
		}
	}
}

func (o *metricBasedHintOptimizer) getNUMAMetric(numa int, resourceName string, machineState state.NUMANodeMap) (float64, error) {
	if machineState == nil || machineState[numa] == nil {
		return 0.0, fmt.Errorf("invalid machineState")
	}

	snbEntries := machineState[numa].PodEntries.GetFilteredPodEntries(state.WrapAllocationMetaFilter((*commonstate.AllocationMeta).CheckSharedNUMABinding))

	sum := 0.0
	for podUID, containerEntries := range snbEntries {
		for containerName := range containerEntries {
			data, err := o.metaServer.GetContainerMetric(podUID, containerName, resourceName)
			if err != nil {
				return 0.0, fmt.Errorf("fetch metric for container failed: %v", err)
			}

			sum += data.Value
		}
	}

	return sum, nil
}

func (o *metricBasedHintOptimizer) getNUMAMetricThresholdNameToValue() (map[string]float64, error) {
	if o.conf.DynamicAgentConfiguration == nil {
		return nil, fmt.Errorf("nil dynamicConf")
	} else if o.metaServer == nil {
		return nil, fmt.Errorf("nil metaServer")
	}

	metricThreshold := o.conf.DynamicAgentConfiguration.GetDynamicConfiguration().MetricThresholdConfiguration
	if metricThreshold == nil {
		return nil, fmt.Errorf("nil metricThreshold")
	}

	res := make(map[string]float64, len(metricthreshold.ThresholdNameToResourceName))
	for thresholdName := range metricthreshold.ThresholdNameToResourceName {
		thresholdValue, err := o.getNUMAMetricThreshold(thresholdName, metricThreshold)
		if err != nil {
			return nil, fmt.Errorf("getNUMAMetricThreshold failed for %s, %v", thresholdName, err)
		}

		res[thresholdName] = thresholdValue
	}

	return res, nil
}

func (o *metricBasedHintOptimizer) getNUMAMetricThreshold(thresholdName string, metricThreshold *metricthreshold.MetricThresholdConfiguration) (float64, error) {
	if metricThreshold == nil {
		return 0.0, fmt.Errorf("nil metricThreshold")
	}

	cpuCodeName := helper.GetCpuCodeName(o.metaServer.MetricsFetcher)
	isVM, _ := helper.GetIsVM(o.metaServer.MetricsFetcher)
	if value, found := metricThreshold.Threshold[cpuCodeName][isVM][thresholdName]; found {
		return value, nil
	} else {
		return 0.0, fmt.Errorf("threshold: %s isn't found cpuCodeName: %s and isVM: %v", thresholdName, cpuCodeName, isVM)
	}
}

func (o *metricBasedHintOptimizer) populateHintsByHybridPolicy(numaNodes []int,
	hints *pluginapi.ListOfTopologyHints, machineState state.NUMANodeMap, request float64) {
	hintFilters := []Filter{
		o.requestSufficientFilter(),
	}
	preferFilters := []Filter{
		//o.usageFilter(),
	}
	scorers := []Scorer{
		o.hybridRequestScorer(),
		//c.hybridUsageScorer(),
	}

	o.populateHintsByFramework(numaNodes, hints, machineState, request, hintFilters, preferFilters, scorers)
}

func (o *metricBasedHintOptimizer) populateHintsByFramework(numaNodes []int, hints *pluginapi.ListOfTopologyHints,
	machineState state.NUMANodeMap, request float64, hintFilters []Filter, preferFilters []Filter, scorers []Scorer,
) {
	var numaScoreList NumaScoreList

	// todo: set the hints to empty now, and support post-filtering hints later
	hints.Hints = make([]*pluginapi.TopologyHint, 0, len(numaNodes))

	for _, numaID := range numaNodes {
		if pass := filterNuma(hintFilters, request, numaID, machineState); !pass {
			continue
		}

		hints.Hints = append(hints.Hints, &pluginapi.TopologyHint{
			Nodes: []uint64{uint64(numaID)},
		})

		if pass := filterNuma(preferFilters, request, numaID, machineState); !pass {
			continue
		}

		scoreSum := 0
		for _, scorer := range scorers {
			score := scorer.ScoreFunc(request, numaID, machineState)
			scoreSum += score
		}
		score := scoreSum / len(scorers)

		numaScoreList = append(numaScoreList, NumaScore{
			Name:  numaID,
			Index: len(hints.Hints) - 1,
			Score: int64(score),
		})
	}

	if len(numaScoreList) == 0 {
		return
	}

	sort.Slice(numaScoreList, func(i, j int) bool {
		return numaScoreList[i].Score > numaScoreList[j].Score
	})
	targetNuma := numaScoreList[0]
	general.InfoS("Select preferred numa", "request", request, "numa", targetNuma.Name,
		"score", targetNuma.Score, "hintIndex", targetNuma.Index)

	hints.Hints[targetNuma.Index].Preferred = true
}

func filterNuma(hintFilters []Filter, request float64, nodeID int, machineState state.NUMANodeMap) bool {
	for _, filter := range hintFilters {
		if !filter(request, nodeID, machineState) {
			return false
		}
	}
	return true
}

func (o *metricBasedHintOptimizer) getNumaUsage(numa int, machineState state.NUMANodeMap) (float64, error) {
	if machineState == nil || machineState[numa] == nil {
		return 0, fmt.Errorf("invalid machineState")
	}
	if o.numaMetrics[numa][consts.MetricCPUUsageContainer] == nil {
		return 0, fmt.Errorf("invalid machineState")
	}

	used, err := o.numaMetrics[numa][consts.MetricCPUUsageContainer].AvgAfterTimestampWithCountBound(time.Now().UnixNano()-10*time.Minute.Nanoseconds(), 10)
	if err != nil {
		return 0, fmt.Errorf("get numa metric failed: %v", err)
	}
	return used, nil
}

func (o *metricBasedHintOptimizer) getNUMACpuUsageThreshold(metricThreshold *metricthreshold.MetricThresholdConfiguration,
	usageThresholdExpandFactor float64) (float64, error) {
	threshold, err := o.getNUMAMetricThreshold(metricthreshold.NUMACPUUsageRatioThreshold, metricThreshold)
	if err != nil {
		return 0, err
	}
	return threshold * usageThresholdExpandFactor, nil
}
