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
	"k8s.io/client-go/tools/cache"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/hintoptimizer"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/hintoptimizer/policy"
	hintoptimizerutil "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/hintoptimizer/util"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	cpuUtil "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/util"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/metricthreshold"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	"github.com/kubewharf/katalyst-core/pkg/util/strategygroup"
	metric_threshold "github.com/kubewharf/katalyst-core/pkg/util/threshold"
)

const HintOptimizerNameMetricBased = "metric_based"

type metricBasedHintOptimizer struct {
	mutex      sync.RWMutex
	conf       *config.Configuration
	emitter    metrics.MetricEmitter
	metaServer *metaserver.MetaServer
	state      state.State

	reservedCPUs      machine.CPUSet
	numaMetrics       map[int]cpuUtil.SubEntries
	metricSampleTime  time.Duration
	metricSampleCount int
}

func NewMetricBasedHintOptimizer(
	options policy.HintOptimizerFactoryOptions,
) (hintoptimizer.HintOptimizer, error) {
	numaMetrics := make(map[int]cpuUtil.SubEntries)
	numaNodes := options.MetaServer.CPUDetails.NUMANodes().ToSliceNoSortInt()
	for _, numaID := range numaNodes {
		numaMetrics[numaID] = make(cpuUtil.SubEntries)
	}
	return &metricBasedHintOptimizer{
		conf:              options.Conf,
		metaServer:        options.MetaServer,
		emitter:           options.Emitter,
		state:             options.State,
		reservedCPUs:      options.ReservedCPUs,
		numaMetrics:       numaMetrics,
		metricSampleTime:  10 * time.Minute,
		metricSampleCount: 10,
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

	if !o.enableMetricPreferredNUMAAllocation() {
		return pkgerrors.Wrapf(hintoptimizerutil.ErrHintOptimizerSkip, "metricPolicyEnabled is false")
	}

	_ = o.emitter.StoreInt64(util.MetricNameMetricBasedNUMAAllocationEnabled, 1, metrics.MetricTypeNameCount)
	err = o.populateHintsByMetricPolicy(hints, o.state.GetMachineState(), request.CPURequest)
	if err != nil {
		general.Errorf("populateHintsByMetricPolicy failed with error: %v", err)
		return pkgerrors.Wrapf(hintoptimizerutil.ErrHintOptimizerSkip, "failed %v", err)
	} else {
		_ = o.emitter.StoreInt64(util.MetricNameMetricBasedNUMAAllocationSuccess, 1, metrics.MetricTypeNameCount)
	}

	return nil
}

func (o *metricBasedHintOptimizer) enableMetricPreferredNUMAAllocation() bool {
	metricPolicyEnabled, _ := strategygroup.IsStrategyEnabledForNode(consts.StrategyNameMetricPreferredNUMAAllocation, o.conf.EnableMetricPreferredNumaAllocation, o.conf)
	general.Infof("metricPolicyEnabled: %v", metricPolicyEnabled)
	return metricPolicyEnabled
}

func (o *metricBasedHintOptimizer) Run(stopCh <-chan struct{}) error {
	// wait for metrics cache sync
	if !cache.WaitForCacheSync(stopCh, o.metaServer.MetricsFetcher.HasSynced) {
		return fmt.Errorf("wait for cache sync failed")
	}
	go wait.Until(o.collectNUMAMetrics, 30*time.Second, stopCh)
	return nil
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
	if !o.enableMetricPreferredNUMAAllocation() {
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
				subEntries[resourceName] = cpuUtil.CreateMetricRing(o.metricSampleCount)
			}

			subEntries[resourceName].Push(&cpuUtil.MetricSnapshot{
				Info: cpuUtil.MetricInfo{
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

	thresholds := metric_threshold.GetMetricThresholdsAll(o.metaServer, metricThreshold.Threshold)

	if thresholds == nil {
		return nil, fmt.Errorf("nil thresholds")
	}

	return thresholds, nil
}
