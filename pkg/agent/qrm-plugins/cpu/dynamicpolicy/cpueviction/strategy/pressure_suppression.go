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
	cpuutil "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/util"
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
	metricsHistory                            *cpuutil.NumaMetricHistory
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
		metricsHistory:       cpuutil.NewMetricHistory(evictionConfig.MetricRingSize),
		suppressionOverStats: make([]SuppressionOverStat, 0),
	}, nil
}

// Start starts the periodic sync loop of the plugin. It fails fast when the
// configured sync period is non-positive, so that a misconfigured dynamic
// config cannot silently starve the sync loop.
func (p *CPUPressureSuppression) Start(ctx context.Context) error {
	general.Infof("%s start", EvictionNameSuppression)
	period := time.Duration(p.suppressionUsageCPUPressureEvictionConfig.SyncPeriod) * time.Second
	if period <= 0 {
		return fmt.Errorf("%s: invalid sync period %d seconds, must be positive",
			EvictionNameSuppression, p.suppressionUsageCPUPressureEvictionConfig.SyncPeriod)
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
		general.Infof("[%s] suppression usage hard met, group %d, rate avg %v, threshold %v",
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
		general.Infof("[%s] suppression usage soft met, group %d, rate avg %v, threshold %v",
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

// GetTopEvictionPods returns the reclaimed pods of the over-threshold group
// sorted by actual CPU usage descending, filtered by the pod usage threshold
// and the pod suppression tolerance rate.
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

	// the sorted over stats guarantee that the first stat is the most severe
	// group, and it is consistent with the met type reported by ThresholdMet.
	targetStat := p.suppressionOverStats[0]
	targetGroupID := targetStat.GroupID
	groupRate := targetStat.RateAvg

	// filter active pods: reclaimed QoS and target group membership
	targetGroupPods := make([]*v1.Pod, 0)
	for _, pod := range native.FilterPods(request.ActivePods, p.conf.CheckReclaimedQoSForPod) {
		result, err := qos.GetActualNUMABindingResult(p.conf.QoSConfiguration, pod)
		if err != nil {
			general.Errorf("pod %s get numa binding result failed: %s", native.GenerateUniqObjectNameKey(pod), err)
			continue
		}

		if (targetGroupID == nonNUMABindingGroupID && result == -1) ||
			(targetGroupID != nonNUMABindingGroupID && result == targetGroupID) {
			targetGroupPods = append(targetGroupPods, pod)
		}
	}
	general.Infof("[%s] target group %d, group rate %v, target group pod count %v",
		EvictionNameSuppression, targetGroupID, groupRate, len(targetGroupPods))

	candidatePods := make([]*v1.Pod, 0)
	podCPUUsage := make(map[string]float64)
	for _, pod := range targetGroupPods {
		usage, err := helper.GetPodMetric(p.metaServer.MetricsFetcher, p.emitter, pod, consts.MetricCPUUsageContainer, -1)
		if err != nil {
			general.Warningf("[%s] failed to get pod %v cpu usage: %v", EvictionNameSuppression, native.GenerateUniqObjectNameKey(pod), err)
			continue
		}
		if usage <= config.PodCPUUsageEvictionThreshold {
			general.Infof("[%s] pod %v cpu usage %v is not over the eviction threshold %v",
				EvictionNameSuppression, native.GenerateUniqObjectNameKey(pod), usage, config.PodCPUUsageEvictionThreshold)
			continue
		}

		// the pod tolerance rate is the raw per-pod suppression tolerance rate
		// (not capped by the GetEvictPods path), respecting the per-pod user intent.
		toleranceRate, err := qos.GetPodCPUSuppressionToleranceRate(p.conf.QoSConfiguration, pod)
		if err != nil {
			general.Errorf("pod %s get cpu suppression tolerance rate failed: %s", native.GenerateUniqObjectNameKey(pod), err)
			continue
		}
		if toleranceRate >= groupRate {
			general.Infof("[%s] pod %v tolerance rate %v is not lower than the group suppression rate %v",
				EvictionNameSuppression, native.GenerateUniqObjectNameKey(pod), toleranceRate, groupRate)
			continue
		}

		candidatePods = append(candidatePods, pod)
		podCPUUsage[native.GenerateUniqObjectNameKey(pod)] = usage
	}

	if len(candidatePods) == 0 {
		general.Infof("[%s] got empty candidate pods after filter", EvictionNameSuppression)
		return &pluginapi.GetTopEvictionPodsResponse{}, nil
	}

	// sort by actual cpu usage descending, and break ties by the uniq object name
	sort.SliceStable(candidatePods, func(i, j int) bool {
		return podCPUUsage[native.GenerateUniqObjectNameKey(candidatePods[i])] > podCPUUsage[native.GenerateUniqObjectNameKey(candidatePods[j])]
	})

	// the soft tier (TopN == 0) also returns the top-1 pod as a soft eviction candidate
	retLen := request.TopN
	if retLen == 0 {
		retLen = 1
	}
	retLen = general.MinUInt64(retLen, uint64(len(candidatePods)))

	var deletionOptions *pluginapi.DeletionOptions
	if gracePeriod := config.GracePeriod; gracePeriod > 0 {
		deletionOptions = &pluginapi.DeletionOptions{
			GracePeriodSeconds: gracePeriod,
		}
	}

	return &pluginapi.GetTopEvictionPodsResponse{
		TargetPods:      candidatePods[:retLen],
		DeletionOptions: deletionOptions,
	}, nil
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
	if config == nil || !config.EnableSuppressionUsageEviction {
		general.Infof("%s plugin is disabled", EvictionNameSuppression)
		// clear stale over stats so that ThresholdMet and GetTopEvictionPods
		// cannot keep evicting pods based on outdated data
		p.suppressionOverStats = nil
		return
	}
	if config.MetricRingSize <= 0 {
		general.Errorf("invalid metric ring size %d", config.MetricRingSize)
		p.suppressionOverStats = nil
		return
	}
	// rebuild the metric history when the ring size changes via the dynamic
	// config, so that the denominator used by updateSuppressionOverStats
	// always matches the ring capacity
	if p.metricsHistory == nil || p.metricsHistory.RingSize != config.MetricRingSize {
		p.metricsHistory = cpuutil.NewMetricHistory(config.MetricRingSize)
	}

	// only reclaim pool supports suppression usage eviction
	entries := p.state.GetPodEntries()
	poolCPUSet, err := entries.GetCPUSetForPool(commonstate.PoolNameReclaim)
	if err != nil {
		general.Errorf("get reclaim pool failed: %s", err)
		p.suppressionOverStats = nil
		return
	}
	if poolCPUSet.Size() == 0 {
		general.Errorf("reclaim pool set size is empty")
		p.suppressionOverStats = nil
		return
	}

	pods, err := p.metaServer.GetPodList(ctx, func(_ *v1.Pod) bool { return true })
	if err != nil {
		general.Errorf("get pod list failed: %s", err)
		p.suppressionOverStats = nil
		return
	}
	reclaimPods := native.FilterPods(pods, p.conf.CheckReclaimedQoSForPod)

	// sync the non-actual-NUMA-binding group
	nonActualNUMABindingCPUSet := machine.NewCPUSet()
	nonActualNUMABindingNUMAs := p.state.GetMachineState().GetFilteredNUMASet(state.WrapAllocationMetaFilter((*commonstate.AllocationMeta).CheckReclaimedActualNUMABinding))
	for _, numaID := range nonActualNUMABindingNUMAs.ToSliceNoSortInt() {
		nonActualNUMABindingCPUSet = nonActualNUMABindingCPUSet.Union(poolCPUSet.Intersection(p.metaServer.CPUDetails.CPUsInNUMANodes(numaID)))
	}
	p.syncPoolGroup(nonNUMABindingGroupID, nonActualNUMABindingCPUSet, p.conf.ReclaimRelativeRootCgroupPath, reclaimPods, config)

	// sync each actual-NUMA-binding group
	for numaID, reclaimRelativeRootCgroupPath := range p.nonNUMABindingReclaimRelativeRootCgroupPaths {
		if !general.IsPathExists(common.GetAbsCgroupPath(common.DefaultSelectedSubsys, reclaimRelativeRootCgroupPath)) {
			continue
		}

		actualNUMABindingCPUSet := poolCPUSet.Intersection(p.metaServer.CPUDetails.CPUsInNUMANodes(numaID))
		p.syncPoolGroup(numaID, actualNUMABindingCPUSet, reclaimRelativeRootCgroupPath, reclaimPods, config)
	}

	// update over stats
	p.updateSuppressionOverStats()
}

// syncPoolGroup calculates the suppression rate and the CPU usage ratio of one
// reclaim pool group, and pushes them into the metric history.
func (p *CPUPressureSuppression) syncPoolGroup(groupID int, cpuset machine.CPUSet, cgroupPath string,
	reclaimPods []*v1.Pod, config *SuppressionUsageCPUPressureEvictionConfig,
) {
	reclaimMetrics, err := helper.GetReclaimMetrics(cpuset, cgroupPath, p.metaServer.MetricsFetcher)
	if err != nil {
		general.Errorf("[%s] group %d get reclaim metrics failed: %s", EvictionNameSuppression, groupID, err)
		return
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
	general.Infof("[%s] group %d suppression rate %v, usage ratio %v, cpuset %v, pod count %d",
		EvictionNameSuppression, groupID, rate, usageRatio, cpuset.String(), len(groupPods))

	p.metricsHistory.Push(groupID, cpuutil.FakePodUID, metricNameSuppressionRate, rate,
		config.SoftSuppressionRateThreshold, config.HardSuppressionRateThreshold)
	p.metricsHistory.Push(groupID, cpuutil.FakePodUID, metricNameSuppressionUsageRatio, usageRatio,
		config.SoftCPUUsageThreshold, config.HardCPUUsageThreshold)
}

// updateSuppressionOverStats judges the sustained over status of each group
// based on the metric history, and keeps the sorted over stats.
func (p *CPUPressureSuppression) updateSuppressionOverStats() {
	config := p.suppressionUsageCPUPressureEvictionConfig
	if config == nil {
		return
	}

	overStats := make([]SuppressionOverStat, 0)
	for groupID, groupHistory := range p.metricsHistory.Inner {
		groupInner := groupHistory[cpuutil.FakePodUID]
		if groupInner == nil {
			continue
		}

		rateRing, ok := groupInner[metricNameSuppressionRate]
		if !ok || rateRing.Len() == 0 {
			continue
		}
		usageRatioRing, ok := groupInner[metricNameSuppressionUsageRatio]
		if !ok || usageRatioRing.Len() == 0 {
			continue
		}

		rateSoftOverCount, rateHardOverCount := rateRing.Count()
		usageSoftOverCount, usageHardOverCount := usageRatioRing.Count()
		ringSize := float64(config.MetricRingSize)

		// a tier is over only when both the suppression rate and the pool CPU
		// usage ratio are sustained over their thresholds (AND condition)
		rateSoftOver := float64(rateSoftOverCount)/ringSize >= config.ThresholdMetPercentage
		rateHardOver := float64(rateHardOverCount)/ringSize >= config.ThresholdMetPercentage
		usageSoftOver := float64(usageSoftOverCount)/ringSize >= config.ThresholdMetPercentage
		usageHardOver := float64(usageHardOverCount)/ringSize >= config.ThresholdMetPercentage

		stat := SuppressionOverStat{
			GroupID:       groupID,
			RateAvg:       rateRing.Avg(),
			UsageRatioAvg: usageRatioRing.Avg(),
			IsSoftOver:    rateSoftOver && usageSoftOver,
			IsHardOver:    rateHardOver && usageHardOver,
		}
		if stat.IsSoftOver || stat.IsHardOver {
			general.Infof("[%s] group %d suppression over, rate avg %v, usage ratio avg %v, isSoftOver %v, isHardOver %v",
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
		PodCPUUsageEvictionThreshold:   config.PodCPUUsageEvictionThreshold,
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
