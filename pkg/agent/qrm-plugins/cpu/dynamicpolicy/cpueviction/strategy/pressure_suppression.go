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
	"sort"
	"sync"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/kubewharf/katalyst-api/pkg/protocol/evictionplugin/v1alpha1"
	pluginapi "github.com/kubewharf/katalyst-api/pkg/protocol/evictionplugin/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/adminqos/eviction"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/helper"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	"github.com/kubewharf/katalyst-core/pkg/util/metricring"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
	"github.com/kubewharf/katalyst-core/pkg/util/qos"
)

// EvictionNameSuppression is the name of the cpu pressure suppression eviction plugin.
const EvictionNameSuppression = "cpu-pressure-suppression-plugin"

const (
	// metricNameSuppressionRate is the metric name of the pool suppression rate
	// stored in the metric history.
	metricNameSuppressionRate = "suppression_rate"
	// metricNameSuppressionUsageRatio is the metric name of the pool CPU usage
	// ratio stored in the metric history.
	metricNameSuppressionUsageRatio = "suppression_usage_ratio"

	// nonNUMABindingGroupID is the group id of the non-actual-NUMA-binding group
	// in the metric history.
	nonNUMABindingGroupID = -1

	// evictionScopeSuppressionUsage is the eviction scope reported by the
	// suppression usage eviction channel.
	evictionScopeSuppressionUsage = "cpu"

	// defaultSuppressionUsageSyncPeriodSeconds is the fallback sync period (in
	// seconds) when the configured SyncPeriod is non-positive, so an unset or
	// unavailable dynamic config cannot stop the whole plugin from starting.
	defaultSuppressionUsageSyncPeriodSeconds = 15
)

// CPUPressureSuppression is the eviction plugin that periodically evaluates
// the suppression rate and the actual CPU usage of each reclaim pool group,
// and evicts reclaimed pods from over-threshold groups.
type CPUPressureSuppression struct {
	sync.RWMutex
	conf                                         *config.Configuration
	state                                        state.ReadonlyState
	emitter                                      metrics.MetricEmitter
	metaServer                                   *metaserver.MetaServer
	nonNUMABindingReclaimRelativeRootCgroupPaths map[int]string

	lastToleranceTime sync.Map

	// suppression usage eviction channel (independent from the GetEvictPods path)
	suppressionUsageCPUPressureEvictionConfig *SuppressionUsageCPUPressureEvictionConfig
	metricsHistory                            *metricring.NumaMultiMetricRing
	suppressionOverStats                      []SuppressionOverStat
}

// SuppressionOverStat is the per-group suppression usage stat maintained by sync.
type SuppressionOverStat struct {
	GroupID       int
	RateAvg       float64
	UsageRatioAvg float64
	IsSoftOver    bool
	IsHardOver    bool
}

// NewCPUPressureSuppressionEviction creates a CPUPressureSuppression plugin.
func NewCPUPressureSuppressionEviction(emitter metrics.MetricEmitter, metaServer *metaserver.MetaServer,
	conf *config.Configuration, state state.ReadonlyState,
) (CPUPressureEviction, error) {
	evictionConfig := getSuppressionUsageCPUPressureEvictionConfig(conf.GetDynamicConfiguration())
	return &CPUPressureSuppression{
		conf:       conf,
		state:      state,
		emitter:    emitter,
		metaServer: metaServer,
		nonNUMABindingReclaimRelativeRootCgroupPaths: common.GetNUMABindingReclaimRelativeRootCgroupPaths(conf.ReclaimRelativeRootCgroupPath,
			metaServer.CPUDetails.NUMANodes().ToSliceNoSortInt()),
		suppressionUsageCPUPressureEvictionConfig: evictionConfig,
		metricsHistory:       metricring.NewNumaMultiMetricRing(evictionConfig.MetricRingSize),
		suppressionOverStats: make([]SuppressionOverStat, 0),
	}, nil
}

// Start starts the periodic sync loop of the plugin. A non-positive sync period
// only means the dynamic config is unavailable or unset; it falls back to a
// positive default instead of failing Start, because a returned error would make
// the wrapper leave the plugin unstarted, taking down the legacy GetEvictPods path
// as well.
func (p *CPUPressureSuppression) Start(ctx context.Context) error {
	general.Infof("%s start", EvictionNameSuppression)
	period := time.Duration(p.suppressionUsageCPUPressureEvictionConfig.SyncPeriod) * time.Second
	if period <= 0 {
		general.Warningf("%s: invalid sync period %d seconds, fall back to %d seconds",
			EvictionNameSuppression, p.suppressionUsageCPUPressureEvictionConfig.SyncPeriod,
			defaultSuppressionUsageSyncPeriodSeconds)
		period = time.Duration(defaultSuppressionUsageSyncPeriodSeconds) * time.Second
	}
	go wait.UntilWithContext(ctx, p.sync, period)
	return nil
}

// Name returns the name of the plugin.
func (p *CPUPressureSuppression) Name() string { return EvictionNameSuppression }

// ThresholdMet reports whether the most severe reclaim pool group has
// sustained over its soft or hard suppression usage thresholds.
func (p *CPUPressureSuppression) ThresholdMet(_ context.Context, _ *pluginapi.GetThresholdMetRequest) (*pluginapi.ThresholdMetResponse, error) {
	p.RLock()
	defer p.RUnlock()

	config := p.suppressionUsageCPUPressureEvictionConfig
	if config == nil || !config.EnableSuppressionUsageEviction {
		general.Infof("%s plugin is disabled", EvictionNameSuppression)
		return &pluginapi.ThresholdMetResponse{
			MetType: pluginapi.ThresholdMetType_NOT_MET,
		}, nil
	}

	if len(p.suppressionOverStats) == 0 {
		general.Infof("[%s] no suppression over load currently", EvictionNameSuppression)
		return &pluginapi.ThresholdMetResponse{
			MetType: pluginapi.ThresholdMetType_NOT_MET,
		}, nil
	}

	// over stats are sorted with hard over first and rate descending,
	// so the first stat is the most severe one.
	stat := p.suppressionOverStats[0]
	if stat.IsHardOver {
		general.Infof("[%s] suppression usage hard met, group %d, rate avg %.2f, threshold %.2f",
			EvictionNameSuppression, stat.GroupID, stat.RateAvg, config.HardSuppressionRateThreshold)
		return &pluginapi.ThresholdMetResponse{
			ThresholdValue:    config.HardSuppressionRateThreshold,
			ObservedValue:     stat.RateAvg,
			ThresholdOperator: pluginapi.ThresholdOperator_GREATER_THAN,
			MetType:           pluginapi.ThresholdMetType_HARD_MET,
			EvictionScope:     evictionScopeSuppressionUsage,
		}, nil
	}
	if stat.IsSoftOver {
		general.Infof("[%s] suppression usage soft met, group %d, rate avg %.2f, threshold %.2f",
			EvictionNameSuppression, stat.GroupID, stat.RateAvg, config.SoftSuppressionRateThreshold)
		return &pluginapi.ThresholdMetResponse{
			ThresholdValue:    config.SoftSuppressionRateThreshold,
			ObservedValue:     stat.RateAvg,
			ThresholdOperator: pluginapi.ThresholdOperator_GREATER_THAN,
			MetType:           pluginapi.ThresholdMetType_SOFT_MET,
			EvictionScope:     evictionScopeSuppressionUsage,
		}, nil
	}

	return &pluginapi.ThresholdMetResponse{
		MetType: pluginapi.ThresholdMetType_NOT_MET,
	}, nil
}

// GetTopEvictionPods walks the sorted over stats in severity order, evaluates
// each group against its real-time suppression rate, and returns the TopN pods
// from the first group that yields eligible candidates; groups that have fallen
// back below their tier suppression-rate threshold, or that filter out to no
// eligible pod, are skipped and the walk continues.
func (p *CPUPressureSuppression) GetTopEvictionPods(_ context.Context, request *pluginapi.GetTopEvictionPodsRequest) (*pluginapi.GetTopEvictionPodsResponse, error) {
	if request == nil {
		return nil, fmt.Errorf("GetTopEvictionPods got nil request")
	}
	if len(request.ActivePods) == 0 {
		general.Warningf("[%s] got empty active pods list", EvictionNameSuppression)
		return &pluginapi.GetTopEvictionPodsResponse{}, nil
	}

	p.RLock()
	defer p.RUnlock()

	config := p.suppressionUsageCPUPressureEvictionConfig
	if config == nil || !config.EnableSuppressionUsageEviction {
		general.Infof("%s plugin is disabled", EvictionNameSuppression)
		return &pluginapi.GetTopEvictionPodsResponse{}, nil
	}

	if len(p.suppressionOverStats) == 0 {
		general.Infof("[%s] no suppression over load currently", EvictionNameSuppression)
		return &pluginapi.GetTopEvictionPodsResponse{}, nil
	}

	// soft tier (TopN == 0) also returns the top-1 pod as a candidate
	retLen := request.TopN
	if retLen == 0 {
		retLen = 1
	}

	poolCPUSet, err := p.state.GetPodEntries().GetCPUSetForPool(commonstate.PoolNameReclaim)
	if err != nil {
		general.Errorf("get reclaim pool failed: %s", err)
		return nil, err
	}

	evictionConfiguration := p.conf.GetDynamicConfiguration().CPUPressureEvictionConfiguration

	// over stats are sorted hard-over first, then rate descending; return from
	// the first group that yields eligible candidates.
	for _, stat := range p.suppressionOverStats {
		candidatePods, podCPUUsage, err := p.collectSuppressionGroupCandidates(stat, poolCPUSet, request.ActivePods, evictionConfiguration)
		if err != nil {
			return nil, err
		}
		if len(candidatePods) == 0 {
			continue
		}

		// sort by actual cpu usage descending, and break ties by the uniq object
		// name ascending so equal-usage candidates have a deterministic order
		sort.SliceStable(candidatePods, func(i, j int) bool {
			keyI := native.GenerateUniqObjectNameKey(candidatePods[i])
			keyJ := native.GenerateUniqObjectNameKey(candidatePods[j])
			usageI, usageJ := podCPUUsage[keyI], podCPUUsage[keyJ]
			if usageI != usageJ {
				return usageI > usageJ
			}
			return keyI < keyJ
		})

		n := general.MinUInt64(retLen, uint64(len(candidatePods)))

		var deletionOptions *pluginapi.DeletionOptions
		if gracePeriod := config.GracePeriod; gracePeriod > 0 {
			deletionOptions = &pluginapi.DeletionOptions{
				GracePeriodSeconds: gracePeriod,
			}
		}

		general.Infof("[%s] group %d yields %d eligible pods, return %d",
			EvictionNameSuppression, stat.GroupID, len(candidatePods), n)
		return &pluginapi.GetTopEvictionPodsResponse{
			TargetPods:      candidatePods[:n],
			DeletionOptions: deletionOptions,
		}, nil
	}

	return &pluginapi.GetTopEvictionPodsResponse{}, nil
}

// collectSuppressionGroupCandidates resolves one over-stat group, computes its
// real-time suppression rate from freshly fetched reclaim metrics, applies the
// tier suppression-rate gate, and returns the pods whose tolerance rate is
// below the real-time rate along with their actual cpu usage for ranking. A
// non-nil error is returned when reclaim metrics cannot be fetched. An empty
// pod slice (and nil error) means the group should be skipped: it has fallen
// below its tier threshold or has no eligible pod.
func (p *CPUPressureSuppression) collectSuppressionGroupCandidates(
	stat SuppressionOverStat, poolCPUSet machine.CPUSet, activePods []*v1.Pod,
	evictionConfiguration *eviction.CPUPressureEvictionConfiguration,
) ([]*v1.Pod, map[string]float64, error) {
	cgroupCPUs, cgroupPath, err := p.resolveGroupCgroup(stat.GroupID, poolCPUSet)
	if err != nil {
		return nil, nil, err
	}

	reclaimMetrics, err := helper.GetReclaimMetrics(cgroupCPUs, cgroupPath, p.metaServer.MetricsFetcher)
	if err != nil {
		return nil, nil, fmt.Errorf("group %d get reclaim metrics failed: %w", stat.GroupID, err)
	}

	// filter active pods: reclaimed QoS and this group's NUMA membership, while
	// accumulating total cpu request for the real-time suppression rate.
	totalCPURequest := resource.Quantity{}
	matchedPods := make([]*v1.Pod, 0)
	for _, pod := range native.FilterPods(activePods, p.conf.CheckReclaimedQoSForPod) {
		result, err := qos.GetActualNUMABindingResult(p.conf.QoSConfiguration, pod)
		if err != nil {
			general.Errorf("pod %s get numa binding result failed: %s", native.GenerateUniqObjectNameKey(pod), err)
			continue
		}
		if (stat.GroupID == nonNUMABindingGroupID && result == -1) ||
			(stat.GroupID != nonNUMABindingGroupID && result == stat.GroupID) {
			matchedPods = append(matchedPods, pod)
			totalCPURequest.Add(native.CPUQuantityGetter()(native.SumUpPodRequestResources(pod)))
		}
	}

	realTimeRate := computePoolSuppressionRate(totalCPURequest, reclaimMetrics.ReclaimedCoresSupply)

	// suppression-rate gate only: a hard-over group must still exceed the hard
	// suppression rate threshold, a soft-over group the soft one; otherwise skip.
	config := p.suppressionUsageCPUPressureEvictionConfig
	var rateThreshold float64
	if stat.IsHardOver {
		rateThreshold = config.HardSuppressionRateThreshold
	} else {
		rateThreshold = config.SoftSuppressionRateThreshold
	}
	if realTimeRate <= rateThreshold {
		general.Infof("[%s] group %d real-time suppression rate %.2f <= threshold %.2f, skip",
			EvictionNameSuppression, stat.GroupID, realTimeRate, rateThreshold)
		return nil, nil, nil
	}

	candidatePods := make([]*v1.Pod, 0)
	podCPUUsage := make(map[string]float64)
	for _, pod := range matchedPods {
		usage, err := helper.GetPodMetric(p.metaServer.MetricsFetcher, p.emitter, pod, consts.MetricCPUUsageContainer, -1)
		if err != nil {
			general.Warningf("[%s] failed to get pod %v cpu usage: %v", EvictionNameSuppression, native.GenerateUniqObjectNameKey(pod), err)
			continue
		}

		// reuse the shared tolerance-rate helper so that pods without an explicit
		// annotation fall back to the configured max suppression tolerance rate
		// instead of MaxFloat64, and the per-pod rate is capped by the max.
		toleranceRate := p.getPodToleranceRate(pod, evictionConfiguration.MaxSuppressionToleranceRate)
		if toleranceRate >= realTimeRate {
			general.Infof("[%s] pod %v tolerance rate %.2f is not lower than the group suppression rate %.2f",
				EvictionNameSuppression, native.GenerateUniqObjectNameKey(pod), toleranceRate, realTimeRate)
			continue
		}

		candidatePods = append(candidatePods, pod)
		podCPUUsage[native.GenerateUniqObjectNameKey(pod)] = usage
	}

	return candidatePods, podCPUUsage, nil
}

// resolveGroupCgroup returns the cpuset and cgroup path used to fetch reclaim
// metrics for the given group id.
func (p *CPUPressureSuppression) resolveGroupCgroup(groupID int, poolCPUSet machine.CPUSet) (machine.CPUSet, string, error) {
	if groupID == nonNUMABindingGroupID {
		cgroupCPUs := machine.NewCPUSet()
		nonActualNUMABindingNUMAs := p.state.GetMachineState().GetFilteredNUMASet(
			state.WrapAllocationMetaFilter((*commonstate.AllocationMeta).CheckReclaimedActualNUMABinding))
		for _, numaID := range nonActualNUMABindingNUMAs.ToSliceNoSortInt() {
			cgroupCPUs = cgroupCPUs.Union(poolCPUSet.Intersection(p.metaServer.CPUDetails.CPUsInNUMANodes(numaID)))
		}
		return cgroupCPUs, p.conf.ReclaimRelativeRootCgroupPath, nil
	}

	cgroupCPUs := poolCPUSet.Intersection(p.metaServer.CPUDetails.CPUsInNUMANodes(groupID))
	cgroupPath, ok := p.nonNUMABindingReclaimRelativeRootCgroupPaths[groupID]
	if !ok {
		return machine.CPUSet{}, "", fmt.Errorf("no reclaim cgroup path for numa group %d", groupID)
	}
	return cgroupCPUs, cgroupPath, nil
}

// GetEvictPods evicts reclaimed pods of the reclaim pool based on the
// suppression rate and the reclaimed cores supply.
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

// evictNonActualNUMABindingPods selects reclaimed pods that are not bound to
// actual NUMA nodes for eviction.
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

// evictActualNUMABindingPods selects reclaimed pods bound to each actual NUMA
// node for eviction.
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

// evictPodsByReclaimMetrics evicts reclaimed pods whose suppression tolerance
// rate is below the current pool suppression rate.
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
		poolSuppressionRate := computePoolSuppressionRate(totalCPURequest, reclaimMetrics.ReclaimedCoresSupply)
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

// sync periodically refreshes the suppression usage stats of each reclaim
// pool group, which is consumed by ThresholdMet and GetTopEvictionPods.
func (p *CPUPressureSuppression) sync(ctx context.Context) {
	p.Lock()
	defer p.Unlock()

	// sync eviction config
	p.suppressionUsageCPUPressureEvictionConfig = getSuppressionUsageCPUPressureEvictionConfig(p.conf.GetDynamicConfiguration())
	config := p.suppressionUsageCPUPressureEvictionConfig

	// validate the config and gather the pool cpuset and reclaim pods; on any
	// failure, clear stale over stats so ThresholdMet and GetTopEvictionPods
	// cannot keep evicting pods based on outdated data
	poolCPUSet, reclaimPods, err := p.prepareSuppressionSync(ctx, config)
	if err != nil {
		p.suppressionOverStats = nil
		return
	}

	syncedGroups := make(map[int]bool)

	// sync the non-actual-NUMA-binding group
	nonActualNUMABindingCPUSet := machine.NewCPUSet()
	nonActualNUMABindingNUMAs := p.state.GetMachineState().GetFilteredNUMASet(state.WrapAllocationMetaFilter((*commonstate.AllocationMeta).CheckReclaimedActualNUMABinding))
	for _, numaID := range nonActualNUMABindingNUMAs.ToSliceNoSortInt() {
		nonActualNUMABindingCPUSet = nonActualNUMABindingCPUSet.Union(poolCPUSet.Intersection(p.metaServer.CPUDetails.CPUsInNUMANodes(numaID)))
	}
	if p.syncPoolGroup(nonNUMABindingGroupID, nonActualNUMABindingCPUSet, p.conf.ReclaimRelativeRootCgroupPath, reclaimPods) {
		syncedGroups[nonNUMABindingGroupID] = true
	}

	// sync each actual-NUMA-binding group
	for numaID, reclaimRelativeRootCgroupPath := range p.nonNUMABindingReclaimRelativeRootCgroupPaths {
		if !general.IsPathExists(common.GetAbsCgroupPath(common.DefaultSelectedSubsys, reclaimRelativeRootCgroupPath)) {
			continue
		}

		actualNUMABindingCPUSet := poolCPUSet.Intersection(p.metaServer.CPUDetails.CPUsInNUMANodes(numaID))
		if p.syncPoolGroup(numaID, actualNUMABindingCPUSet, reclaimRelativeRootCgroupPath, reclaimPods) {
			syncedGroups[numaID] = true
		}
	}

	// drop rings of groups that failed to sync or disappeared this round, so
	// their stale samples cannot contribute to the over stats
	if p.metricsHistory != nil {
		for groupID := range p.metricsHistory.Inner {
			if !syncedGroups[groupID] {
				delete(p.metricsHistory.Inner, groupID)
			}
		}
	}

	// update over stats
	p.updateSuppressionOverStats()
}

// prepareSuppressionSync validates the eviction config and gathers the reclaim
// pool cpuset and reclaim pods for this round. It logs the reason internally and
// returns a non-nil error when the round cannot produce fresh stats; the caller
// clears over stats on error.
func (p *CPUPressureSuppression) prepareSuppressionSync(ctx context.Context, config *SuppressionUsageCPUPressureEvictionConfig) (machine.CPUSet, []*v1.Pod, error) {
	if config == nil || !config.EnableSuppressionUsageEviction {
		general.Infof("%s plugin is disabled", EvictionNameSuppression)
		// clear accumulated samples so re-enabling with the same ring size cannot
		// reuse stale history; keep the previously configured ring capacity when
		// rebuilding the empty ring
		p.resetMetricsHistory()
		return machine.CPUSet{}, nil, fmt.Errorf("suppression usage eviction disabled")
	}
	if config.MetricRingSize <= 0 {
		general.Errorf("invalid metric ring size %d", config.MetricRingSize)
		return machine.CPUSet{}, nil, fmt.Errorf("invalid metric ring size %d", config.MetricRingSize)
	}
	// the CRD path copies ThresholdMetPercentage without validation, so reject a
	// non-positive or greater-than-1 value here: <=0 would make the sustained
	// check always pass (hardCount/ringSize >= 0), while >1 silently disables it
	if config.ThresholdMetPercentage <= 0 || config.ThresholdMetPercentage > 1 {
		general.Errorf("invalid threshold met percentage %v, must be in (0, 1]", config.ThresholdMetPercentage)
		return machine.CPUSet{}, nil, fmt.Errorf("invalid threshold met percentage %v, must be in (0, 1]", config.ThresholdMetPercentage)
	}

	// validate the soft/hard threshold relationship and ranges: the hard
	// suppression-rate threshold must not be below the soft one (otherwise the
	// hard tier can be met before the soft tier), and the CPU usage thresholds
	// are ratios that must stay within [0, 1].
	if config.SoftSuppressionRateThreshold < 0 || config.HardSuppressionRateThreshold < 0 {
		general.Errorf("invalid suppression rate threshold: soft=%v hard=%v, must be non-negative",
			config.SoftSuppressionRateThreshold, config.HardSuppressionRateThreshold)
		return machine.CPUSet{}, nil, fmt.Errorf("invalid suppression rate threshold: soft=%v hard=%v, must be non-negative",
			config.SoftSuppressionRateThreshold, config.HardSuppressionRateThreshold)
	}
	if config.HardSuppressionRateThreshold < config.SoftSuppressionRateThreshold {
		general.Errorf("invalid suppression rate thresholds: hard %v < soft %v, hard must be no less than soft",
			config.HardSuppressionRateThreshold, config.SoftSuppressionRateThreshold)
		return machine.CPUSet{}, nil, fmt.Errorf("invalid suppression rate thresholds: hard %v < soft %v, hard must be no less than soft",
			config.HardSuppressionRateThreshold, config.SoftSuppressionRateThreshold)
	}
	if config.SoftCPUUsageThreshold < 0 || config.SoftCPUUsageThreshold > 1 ||
		config.HardCPUUsageThreshold < 0 || config.HardCPUUsageThreshold > 1 {
		general.Errorf("invalid cpu usage threshold: soft=%v hard=%v, must be within [0, 1]",
			config.SoftCPUUsageThreshold, config.HardCPUUsageThreshold)
		return machine.CPUSet{}, nil, fmt.Errorf("invalid cpu usage threshold: soft=%v hard=%v, must be within [0, 1]",
			config.SoftCPUUsageThreshold, config.HardCPUUsageThreshold)
	}

	// rebuild the metric history when the ring size changes via the dynamic
	// config, so that the denominator used by updateSuppressionOverStats
	// always matches the ring capacity
	if p.metricsHistory == nil || p.metricsHistory.RingSize != config.MetricRingSize {
		p.metricsHistory = metricring.NewNumaMultiMetricRing(config.MetricRingSize)
	}

	// only reclaim pool supports suppression usage eviction
	entries := p.state.GetPodEntries()
	poolCPUSet, err := entries.GetCPUSetForPool(commonstate.PoolNameReclaim)
	if err != nil {
		general.Errorf("get reclaim pool failed: %s", err)
		return machine.CPUSet{}, nil, err
	}
	if poolCPUSet.Size() == 0 {
		general.Errorf("reclaim pool set size is empty")
		return machine.CPUSet{}, nil, fmt.Errorf("reclaim pool set is empty")
	}

	pods, err := p.metaServer.GetPodList(ctx, func(_ *v1.Pod) bool { return true })
	if err != nil {
		general.Errorf("get pod list failed: %s", err)
		return machine.CPUSet{}, nil, err
	}
	reclaimPods := native.FilterPods(pods, p.conf.CheckReclaimedQoSForPod)
	return poolCPUSet, reclaimPods, nil
}

// resetMetricsHistory drops all buffered samples while preserving the previously
// configured ring capacity. It is used when the channel is disabled so stale
// samples cannot be reused after re-enabling with the same ring size.
func (p *CPUPressureSuppression) resetMetricsHistory() {
	if p.metricsHistory == nil {
		return
	}
	p.metricsHistory = metricring.NewNumaMultiMetricRing(p.metricsHistory.RingSize)
}

// syncPoolGroup calculates the suppression rate and the CPU usage ratio of one
// reclaim pool group, and pushes them into the metric history. It returns true
// only when the group synced successfully this round, so the caller can drop
// rings of groups that failed or disappeared and avoid stale samples.
func (p *CPUPressureSuppression) syncPoolGroup(groupID int, cpuset machine.CPUSet, cgroupPath string,
	reclaimPods []*v1.Pod,
) bool {
	reclaimMetrics, err := helper.GetReclaimMetrics(cpuset, cgroupPath, p.metaServer.MetricsFetcher)
	if err != nil {
		general.Errorf("[%s] group %d get reclaim metrics failed: %s", EvictionNameSuppression, groupID, err)
		return false
	}

	groupPods := make([]*v1.Pod, 0)
	for _, pod := range reclaimPods {
		result, err := qos.GetActualNUMABindingResult(p.conf.QoSConfiguration, pod)
		if err != nil {
			general.Errorf("pod %s get numa binding result failed: %s", native.GenerateUniqObjectNameKey(pod), err)
			continue
		}

		if (groupID == nonNUMABindingGroupID && result == -1) ||
			(groupID != nonNUMABindingGroupID && result == groupID) {
			groupPods = append(groupPods, pod)
		}
	}

	totalCPURequest := resource.Quantity{}
	for _, pod := range groupPods {
		totalCPURequest.Add(native.CPUQuantityGetter()(native.SumUpPodRequestResources(pod)))
	}

	supply := reclaimMetrics.ReclaimedCoresSupply
	rate := computePoolSuppressionRate(totalCPURequest, supply)
	usageRatio := 0.0
	if supply > 0 {
		usageRatio = reclaimMetrics.CgroupCPUUsage / supply
	}
	general.Infof("[%s] group %d suppression rate %.2f, usage ratio %.2f, cpuset %v, pod count %d",
		EvictionNameSuppression, groupID, rate, usageRatio, cpuset.String(), len(groupPods))

	// push the synchronized suppression rate and usage ratio as one sample, so
	// that the joint threshold check reads both values from the same moment
	p.metricsHistory.PushNuma(groupID, map[string]float64{
		metricNameSuppressionRate:       rate,
		metricNameSuppressionUsageRatio: usageRatio,
	})
	return true
}

// updateSuppressionOverStats judges the sustained over status of each group
// based on the metric history, and keeps the sorted over stats. A tier counts
// only when the suppression rate and the CPU usage ratio of the SAME sample
// both exceed their thresholds, so a staggered over (rate over in the first
// half, usage over in the second half) cannot pass the joint check.
func (p *CPUPressureSuppression) updateSuppressionOverStats() {
	config := p.suppressionUsageCPUPressureEvictionConfig
	if config == nil {
		return
	}

	overStats := make([]SuppressionOverStat, 0)
	for groupID, groupHistory := range p.metricsHistory.Inner {
		ring, ok := groupHistory[""]
		if !ok || ring.Len() == 0 {
			continue
		}

		// joint soft met: this sample exceeds both soft thresholds; joint hard
		// met: this sample exceeds both hard thresholds. Thresholds are passed
		// as predicates so changing them at runtime does not require rebuilding
		// the history.
		softCount := ring.CountWhere(func(values map[string]float64) bool {
			return values[metricNameSuppressionRate] > config.SoftSuppressionRateThreshold &&
				values[metricNameSuppressionUsageRatio] > config.SoftCPUUsageThreshold
		})
		hardCount := ring.CountWhere(func(values map[string]float64) bool {
			return values[metricNameSuppressionRate] > config.HardSuppressionRateThreshold &&
				values[metricNameSuppressionUsageRatio] > config.HardCPUUsageThreshold
		})

		ringSize := float64(config.MetricRingSize)
		stat := SuppressionOverStat{
			GroupID:       groupID,
			RateAvg:       ring.Avg(metricNameSuppressionRate),
			UsageRatioAvg: ring.Avg(metricNameSuppressionUsageRatio),
			IsSoftOver:    float64(softCount)/ringSize >= config.ThresholdMetPercentage,
			IsHardOver:    float64(hardCount)/ringSize >= config.ThresholdMetPercentage,
		}
		if stat.IsSoftOver || stat.IsHardOver {
			general.Infof("[%s] group %d suppression over, rate avg %.2f, usage ratio avg %.2f, isSoftOver %v, isHardOver %v",
				EvictionNameSuppression, groupID, stat.RateAvg, stat.UsageRatioAvg, stat.IsSoftOver, stat.IsHardOver)
			overStats = append(overStats, stat)
		}
	}

	// sort by severity: hard over first, then rate descending, then usage ratio descending
	sort.SliceStable(overStats, func(i, j int) bool {
		if overStats[i].IsHardOver != overStats[j].IsHardOver {
			return overStats[i].IsHardOver
		}
		if overStats[i].RateAvg != overStats[j].RateAvg {
			return overStats[i].RateAvg > overStats[j].RateAvg
		}
		return overStats[i].UsageRatioAvg > overStats[j].UsageRatioAvg
	})

	p.suppressionOverStats = overStats
}

// getSuppressionUsageCPUPressureEvictionConfig extracts the suppression usage
// cpu pressure eviction config from the dynamic configuration, or returns an
// empty config when the dynamic configuration is unavailable.
func getSuppressionUsageCPUPressureEvictionConfig(conf *dynamic.Configuration) *SuppressionUsageCPUPressureEvictionConfig {
	if conf == nil || conf.AdminQoSConfiguration == nil || conf.EvictionConfiguration == nil ||
		conf.CPUPressureEvictionConfiguration == nil {
		return &SuppressionUsageCPUPressureEvictionConfig{}
	}

	config := conf.CPUPressureEvictionConfiguration.SuppressionUsageCPUPressureEvictionConfiguration
	return &SuppressionUsageCPUPressureEvictionConfig{
		EnableSuppressionUsageEviction: config.EnableSuppressionUsageEviction,
		SyncPeriod:                     config.SyncPeriod,
		MetricRingSize:                 config.MetricRingSize,
		ThresholdMetPercentage:         config.ThresholdMetPercentage,
		SoftSuppressionRateThreshold:   config.SoftSuppressionRateThreshold,
		SoftCPUUsageThreshold:          config.SoftCPUUsageThreshold,
		HardSuppressionRateThreshold:   config.HardSuppressionRateThreshold,
		HardCPUUsageThreshold:          config.HardCPUUsageThreshold,
		GracePeriod:                    config.GracePeriod,
	}
}

// computePoolSuppressionRate returns the pool suppression rate, which is the ratio
// of the total cpu request of the pool to the reclaimed cores supply. If the supply
// is zero, the suppression rate is the max float64.
func computePoolSuppressionRate(totalCPURequest resource.Quantity, reclaimedCoresSupply float64) float64 {
	if reclaimedCoresSupply == 0 {
		return math.MaxFloat64
	}
	// use MilliValue to preserve fractional CPU requests (e.g. 1.5 cores)
	// instead of Value() which rounds up to whole cores
	return float64(totalCPURequest.MilliValue()) / 1000.0 / reclaimedCoresSupply
}
