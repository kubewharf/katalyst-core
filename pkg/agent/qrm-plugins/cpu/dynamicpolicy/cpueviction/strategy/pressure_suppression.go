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

package strategy

import (
	"context"
	"fmt"
	"math"
	"sync"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/kubewharf/katalyst-api/pkg/protocol/evictionplugin/v1alpha1"
	pluginapi "github.com/kubewharf/katalyst-api/pkg/protocol/evictionplugin/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/adminqos/eviction"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/helper"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
	"github.com/kubewharf/katalyst-core/pkg/util/qos"
)

const EvictionNameSuppression = "cpu-pressure-suppression-plugin"

type CPUPressureSuppression struct {
	conf                                         *config.Configuration
	state                                        state.ReadonlyState
	metaServer                                   *metaserver.MetaServer
	nonNUMABindingReclaimRelativeRootCgroupPaths map[int]string

	lastToleranceTime sync.Map
}

func NewCPUPressureSuppressionEviction(_ metrics.MetricEmitter, metaServer *metaserver.MetaServer,
	conf *config.Configuration, state state.ReadonlyState,
) (CPUPressureEviction, error) {
	return &CPUPressureSuppression{
		conf:       conf,
		state:      state,
		metaServer: metaServer,
		nonNUMABindingReclaimRelativeRootCgroupPaths: common.GetNUMABindingReclaimRelativeRootCgroupPaths(conf.ReclaimRelativeRootCgroupPath,
			metaServer.CPUDetails.NUMANodes().ToSliceNoSortInt()),
	}, nil
}

func (p *CPUPressureSuppression) Start(context.Context) error { return nil }
func (p *CPUPressureSuppression) Name() string                { return EvictionNameSuppression }
func (p *CPUPressureSuppression) ThresholdMet(_ context.Context, _ *pluginapi.GetThresholdMetRequest) (*pluginapi.ThresholdMetResponse, error) {
	return &pluginapi.ThresholdMetResponse{}, nil
}

func (p *CPUPressureSuppression) GetTopEvictionPods(_ context.Context, _ *pluginapi.GetTopEvictionPodsRequest) (*pluginapi.GetTopEvictionPodsResponse, error) {
	return &pluginapi.GetTopEvictionPodsResponse{}, nil
}

func (p *CPUPressureSuppression) GetEvictPods(_ context.Context, request *pluginapi.GetEvictPodsRequest) (*pluginapi.GetEvictPodsResponse, error) {
	if request == nil {
		return nil, fmt.Errorf("GetEvictPods got nil request")
	}

	dynamicConfig := p.conf.GetDynamicConfiguration()
	if !dynamicConfig.EnableSuppressionEviction {
		return &pluginapi.GetEvictPodsResponse{}, nil
	}
	general.InfoS("cpu suppression enabled")

	// only reclaim pool support suppression tolerance eviction
	entries := p.state.GetPodEntries()
	poolCPUSet, err := entries.GetCPUSetForPool(commonstate.PoolNameReclaim)
	if err != nil {
		return nil, fmt.Errorf("get reclaim pool failed: %s", err)
	}

	// skip evict pods if pool size is zero
	poolSize := poolCPUSet.Size()
	if poolSize == 0 {
		general.Errorf("reclaim pool set size is empty")
		return &pluginapi.GetEvictPodsResponse{}, nil
	}

	filteredPods := native.FilterPods(request.ActivePods, p.conf.CheckReclaimedQoSForPod)
	if len(filteredPods) == 0 {
		return &pluginapi.GetEvictPodsResponse{}, nil
	}

	// prioritize evicting the pod whose cpu request is larger and priority is lower
	general.NewMultiSorter(
		general.ReverseCmpFunc(native.PodCPURequestCmpFunc),
		general.ReverseCmpFunc(native.PodPriorityCmpFunc),
		native.PodUniqKeyCmpFunc,
	).Sort(native.NewPodSourceImpList(filteredPods))

	now := time.Now()
	evictPods := make([]*v1alpha1.EvictPod, 0)
	nonActualNUMABindingPods, err := p.evictNonActualNUMABindingPods(now, filteredPods, poolCPUSet, dynamicConfig.CPUPressureEvictionConfiguration)
	if err != nil {
		return nil, err
	}
	evictPods = append(evictPods, nonActualNUMABindingPods...)

	actualNUMABindingPods, err := p.evictActualNUMABindingPods(now, filteredPods, poolCPUSet, dynamicConfig.CPUPressureEvictionConfiguration)
	if err != nil {
		return nil, err
	}
	evictPods = append(evictPods, actualNUMABindingPods...)

	// clear inactive filtered pod from lastToleranceTime
	filteredPodsMap := native.GetPodKeyMap(filteredPods, native.GenerateUniqObjectNameKey)
	p.lastToleranceTime.Range(func(key, _ interface{}) bool {
		if _, ok := filteredPodsMap[key.(string)]; !ok {
			p.lastToleranceTime.Delete(key)
		}
		return true
	})

	return &pluginapi.GetEvictPodsResponse{EvictPods: evictPods}, nil
}

func (p *CPUPressureSuppression) evictNonActualNUMABindingPods(now time.Time, filteredPods []*v1.Pod, poolCPUSet machine.CPUSet,
	evictionConfiguration *eviction.CPUPressureEvictionConfiguration,
) ([]*v1alpha1.EvictPod, error) {
	nonActualNUMABindingCPUSet := machine.NewCPUSet()
	nonActualNUMABindingNUMAs := p.state.GetMachineState().GetFilteredNUMASet(state.WrapAllocationMetaFilter((*commonstate.AllocationMeta).CheckReclaimedActualNUMABinding))
	for _, numaID := range nonActualNUMABindingNUMAs.ToSliceNoSortInt() {
		nonActualNUMABindingCPUSet = nonActualNUMABindingCPUSet.Union(poolCPUSet.Intersection(p.metaServer.CPUDetails.CPUsInNUMANodes(numaID)))
	}

	// get reclaim metrics
	reclaimMetrics, err := helper.GetReclaimMetrics(nonActualNUMABindingCPUSet, p.conf.ReclaimRelativeRootCgroupPath, p.metaServer.MetricsFetcher)
	if err != nil {
		return nil, fmt.Errorf("get reclaim metrics failed: %s", err)
	}

	filterPods := native.FilterPods(filteredPods, func(pod *v1.Pod) (bool, error) {
		result, err := qos.GetActualNUMABindingResult(p.conf.QoSConfiguration, pod)
		if err != nil {
			return false, err
		}

		return result == -1, nil
	})

	general.InfoS("filterPods", "cpuSet",
		nonActualNUMABindingCPUSet.String(), "podCount", len(filterPods))
	return p.evictPodsByReclaimMetrics(now, filterPods, reclaimMetrics, evictionConfiguration)
}

func (p *CPUPressureSuppression) evictActualNUMABindingPods(now time.Time, filteredPods []*v1.Pod, poolCPUSet machine.CPUSet,
	evictionConfiguration *eviction.CPUPressureEvictionConfiguration,
) ([]*v1alpha1.EvictPod, error) {
	var evictPods []*v1alpha1.EvictPod
	for numaID, reclaimRelativeRootCgroupPath := range p.nonNUMABindingReclaimRelativeRootCgroupPaths {
		if !general.IsPathExists(common.GetAbsCgroupPath(common.DefaultSelectedSubsys, reclaimRelativeRootCgroupPath)) {
			continue
		}

		actualNUMABindingCPUSet := poolCPUSet.Intersection(p.metaServer.CPUDetails.CPUsInNUMANodes(numaID))

		// get reclaim metrics
		reclaimMetrics, err := helper.GetReclaimMetrics(actualNUMABindingCPUSet,
			reclaimRelativeRootCgroupPath, p.metaServer.MetricsFetcher)
		if err != nil {
			return nil, fmt.Errorf("get reclaim metrics failed: %s", err)
		}

		filterPods := native.FilterPods(filteredPods, func(pod *v1.Pod) (bool, error) {
			result, err := qos.GetActualNUMABindingResult(p.conf.QoSConfiguration, pod)
			if err != nil {
				return false, err
			}

			return result == numaID, nil
		})

		general.InfoS("filterPods", "numaID", numaID, "cpuSet",
			actualNUMABindingCPUSet.String(), "podCount", len(filterPods))
		pods, err := p.evictPodsByReclaimMetrics(now, filterPods, reclaimMetrics, evictionConfiguration)
		if err != nil {
			return nil, err
		}

		evictPods = append(evictPods, pods...)
	}

	return evictPods, nil
}

func (p *CPUPressureSuppression) evictPodsByReclaimMetrics(now time.Time, filteredPods []*v1.Pod,
	reclaimMetrics *helper.ReclaimMetrics, evictionConfiguration *eviction.CPUPressureEvictionConfiguration,
) ([]*v1alpha1.EvictPod, error) {
	totalCPURequest := resource.Quantity{}
	for _, pod := range filteredPods {
		totalCPURequest.Add(native.CPUQuantityGetter()(native.SumUpPodRequestResources(pod)))
	}

	general.InfoS("info", "reclaim cpu request", totalCPURequest.String(), "reclaimMetrics", reclaimMetrics)

	var evictPods []*v1alpha1.EvictPod
	for _, pod := range filteredPods {
		key := native.GenerateUniqObjectNameKey(pod)
		poolSuppressionRate := 0.0
		if reclaimMetrics.ReclaimedCoresSupply == 0 {
			poolSuppressionRate = math.MaxFloat64
		} else {
			poolSuppressionRate = float64(totalCPURequest.Value()) / reclaimMetrics.ReclaimedCoresSupply
		}
		// TODO: consider the case that this Pod is throttled by other Pods

		if podToleranceRate := p.getPodToleranceRate(pod, evictionConfiguration.MaxSuppressionToleranceRate); podToleranceRate < poolSuppressionRate {
			last, _ := p.lastToleranceTime.LoadOrStore(key, now)
			lastDuration := now.Sub(last.(time.Time))
			general.Infof("current pool suppression rate %.2f, "+
				"and it is over than suppression tolerance rate %.2f of pod %s, last duration: %s secs", poolSuppressionRate,
				podToleranceRate, key, now.Sub(last.(time.Time)))

			// a pod will only be evicted if its cpu suppression lasts longer than minToleranceDuration
			if lastDuration > evictionConfiguration.MinSuppressionToleranceDuration {
				evictPod := &v1alpha1.EvictPod{
					Pod: pod,
					Reason: fmt.Sprintf("current pool suppression rate %.2f is over than the "+
						"pod suppression tolerance rate %.2f", poolSuppressionRate, podToleranceRate),
				}
				if evictionConfiguration.GracePeriod > 0 {
					evictPod.DeletionOptions = &v1alpha1.DeletionOptions{
						GracePeriodSeconds: evictionConfiguration.GracePeriod,
					}
				}
				evictPods = append(evictPods, evictPod)
				totalCPURequest.Sub(native.CPUQuantityGetter()(native.SumUpPodRequestResources(pod)))
			}
		} else {
			p.lastToleranceTime.Delete(key)
		}
	}

	return evictPods, nil
}

// getPodToleranceRate returns pod suppression tolerance rate,
// and it is limited by max cpu suppression tolerance rate.
func (p *CPUPressureSuppression) getPodToleranceRate(pod *v1.Pod, maxToleranceRate float64) float64 {
	rate, err := qos.GetPodCPUSuppressionToleranceRate(p.conf.QoSConfiguration, pod)
	if err != nil {
		general.Errorf("pod %s get cpu suppression tolerance rate failed: %s",
			native.GenerateUniqObjectNameKey(pod), err)
		return maxToleranceRate
	} else {
		return math.Min(rate, maxToleranceRate)
	}
}
