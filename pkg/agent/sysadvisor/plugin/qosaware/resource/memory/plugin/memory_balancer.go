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

package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-api/pkg/plugins/registration"
	"github.com/kubewharf/katalyst-api/pkg/plugins/skeleton"
	pluginapi "github.com/kubewharf/katalyst-api/pkg/protocol/evictionplugin/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/memory/dynamicpolicy/memoryadvisor"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/helper"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

const (
	NumaMemoryBalancer               = "numa_memory_balancer"
	EvictionPluginNameMemoryBalancer = "numa_memory_balancer"

	EvictReason = "the memory node %v in pool %v is under bandwidth pressure"
)

var supportedPools = []string{
	state.PoolNameShare,
}

var unsupportedPools = []string{
	state.PoolNameDedicated,
	state.PoolNameReserve,
	state.PoolNameReclaim,
}

type BalanceStatus string

const (
	BalanceStatusPreparing      BalanceStatus = "preparing"
	BalanceStatusPrepareSuccess BalanceStatus = "prepareSuccess"
	BalanceStatusPrepareFailed  BalanceStatus = "prepareFailed"
)

type BalanceLevel string

const (
	ForceBalance BalanceLevel = "forceBalance"
	GraceBalance BalanceLevel = "graceBalance"
)

type BandwidthLevel string

const (
	BandwidthLevelLow    BandwidthLevel = "low"
	BandwidthLevelMedium BandwidthLevel = "medium"
	BandwidthLevelHigh   BandwidthLevel = "high"
)

const (
	BandwidthHighThreshold   float64 = 0.8
	BandwidthMediumThreshold float64 = 0.5

	PingPongDetectThresholdInMinutes = 15
)

type NumaInfo struct {
	*NumaLatencyInfo
	Distance int `json:"Distance"`
}

type PodSort struct {
	Pod       *v1.Pod
	SortValue uint64
}

type NumaLatencyInfo struct {
	NumaID             int     `json:"NumaID"`
	ReadLatency        float64 `json:"ReadLatency"`
	ReadWriteBandwidth float64 `json:"ReadWriteBandwidth"`
}

type BalancePod struct {
	pod          *v1.Pod
	srcNuma      int
	destNumaList []int
}

type EvictPod struct {
	pod     *v1.Pod
	srcNuma int
}

type memoryBalancerWrapper struct {
	started        bool
	memoryBalancer *memoryBalancer
	evictionPlugin skeleton.GenericPlugin
}

func NewMemoryBalancer(conf *config.Configuration, _ interface{}, metaReader metacache.MetaReader, metaServer *metaserver.MetaServer, emitter metrics.MetricEmitter) MemoryAdvisorPlugin {
	balancer := &memoryBalancer{
		conf:       conf,
		metaReader: metaReader,
		metaServer: metaServer,
		emitter:    emitter,
	}
	evictionPlugin, _ := skeleton.NewRegistrationPluginWrapper(balancer,
		[]string{conf.PluginRegistrationDir},
		func(key string, value int64) {
			_ = emitter.StoreInt64(key, value, metrics.MetricTypeNameCount, metrics.ConvertMapToTags(map[string]string{
				"pluginName": EvictionPluginNameMemoryBalancer,
				"pluginType": registration.EvictionPlugin,
			})...)
		})

	return &memoryBalancerWrapper{
		started:        false,
		memoryBalancer: balancer,
		evictionPlugin: evictionPlugin,
	}
}

func (m *memoryBalancerWrapper) Reconcile(status *types.MemoryPressureStatus) error {
	if !m.started {
		err := m.evictionPlugin.Start()
		if err != nil {
			general.Errorf("start memory balancer eviction plugin failed,err:%v", err)
		} else {
			general.Infof("")
			m.started = true
		}
	}

	return m.memoryBalancer.Reconcile(status)
}

func (m *memoryBalancerWrapper) GetAdvices() types.InternalMemoryCalculationResult {
	return m.memoryBalancer.GetAdvices()
}

type PoolBalanceInfo struct {
	evictPods                    []*EvictPod
	balancePods                  []*BalancePod
	needBalance                  bool
	bandwidthPressure            bool
	balanceLevel                 BalanceLevel
	maxLatencyNuma               *NumaLatencyInfo
	maxBandwidthNuma             *NumaLatencyInfo
	rawNumaLatencyInfo           []*NumaLatencyInfo
	maxLatencyNumaBandwidthLevel BandwidthLevel
	sourceNuma                   *NumaLatencyInfo
	status                       BalanceStatus
	failedReason                 string
}

type BalanceInfo struct {
	pool2BalanceInfo map[string]*PoolBalanceInfo
	sessionID        string
	balanceTime      time.Time
}

type memoryBalancer struct {
	pluginapi.UnimplementedEvictionPluginServer
	conf       *config.Configuration
	mutex      sync.RWMutex
	metaReader metacache.MetaReader
	metaServer *metaserver.MetaServer
	emitter    metrics.MetricEmitter

	lastBalanceInfo *BalanceInfo
	balanceInfo     *BalanceInfo
}

func (m *memoryBalancer) Name() string {
	return EvictionPluginNameMemoryBalancer
}

func (m *memoryBalancer) Start() error {
	return nil
}

func (m *memoryBalancer) Stop() error {
	return nil
}

func (m *memoryBalancer) GetToken(_ context.Context, _ *pluginapi.Empty) (*pluginapi.GetTokenResponse, error) {
	return &pluginapi.GetTokenResponse{Token: ""}, nil
}

func (m *memoryBalancer) ThresholdMet(_ context.Context, _ *pluginapi.Empty) (*pluginapi.ThresholdMetResponse, error) {
	return &pluginapi.ThresholdMetResponse{}, nil
}

func (m *memoryBalancer) GetTopEvictionPods(_ context.Context, _ *pluginapi.GetTopEvictionPodsRequest) (*pluginapi.GetTopEvictionPodsResponse, error) {
	return &pluginapi.GetTopEvictionPodsResponse{}, nil
}

func (m *memoryBalancer) GetEvictPods(_ context.Context, _ *pluginapi.GetEvictPodsRequest) (*pluginapi.GetEvictPodsResponse, error) {
	defer m.mutex.Unlock()
	m.mutex.Lock()

	//TODO check if the pod is in the request
	evictPods := make([]*pluginapi.EvictPod, 0)
	for poolName := range m.balanceInfo.pool2BalanceInfo {
		pods := m.balanceInfo.pool2BalanceInfo[poolName].evictPods
		for i := range pods {
			evictPods = append(evictPods, &pluginapi.EvictPod{
				Pod:                pods[i].pod,
				Reason:             fmt.Sprintf(EvictReason, pods[i].srcNuma, poolName),
				ForceEvict:         true,
				EvictionPluginName: EvictionPluginNameMemoryBalancer,
			})
		}
	}
	// TODO clear evictPods to avoid evict same pod repeatedly
	return &pluginapi.GetEvictPodsResponse{EvictPods: evictPods}, nil
}

func (m *memoryBalancer) Reconcile(_ *types.MemoryPressureStatus) error {
	sessionID := uuid.NewUUID()
	balanceInfoMap := make(map[string]*PoolBalanceInfo)
	for _, poolName := range supportedPools {
		balanceInfo, err := m.getBalanceInfo(poolName)
		if err != nil {
			general.Errorf("failed to get balance info,err:%v", err)
			continue
		}

		balanceInfoMap[poolName] = balanceInfo
	}

	m.setBalanceInfo(balanceInfoMap, string(sessionID))
	return nil
}

func (m *memoryBalancer) getBalanceInfo(poolName string) (balanceInfo *PoolBalanceInfo, err error) {
	balanceInfo = &PoolBalanceInfo{
		evictPods:          make([]*EvictPod, 0),
		balancePods:        make([]*BalancePod, 0),
		needBalance:        false,
		rawNumaLatencyInfo: make([]*NumaLatencyInfo, 0),
		status:             BalanceStatusPreparing,
	}

	defer func() {
		if err != nil {
			balanceInfo.status = BalanceStatusPrepareFailed
			balanceInfo.failedReason = err.Error()
		}
	}()

	for _, unsupportedPool := range unsupportedPools {
		if poolName == unsupportedPool {
			err = fmt.Errorf("unsupported pool %v", poolName)
			return
		}
	}

	poolInfo, found := m.metaReader.GetPoolInfo(poolName)
	if !found {
		err = fmt.Errorf("failed to find pool info for %v", poolName)
		return
	}

	poolNumaList := m.getPoolNumaIDs(poolInfo)
	if len(poolNumaList) == 0 {
		err = fmt.Errorf("pool %v has no numa", poolName)
		return
	}

	for _, numaID := range poolNumaList {
		info, getLatencyErr := m.getNumaLatencyInfo(numaID)
		if getLatencyErr != nil {
			err = getLatencyErr
			return
		}
		balanceInfo.rawNumaLatencyInfo = append(balanceInfo.rawNumaLatencyInfo, info)

		if balanceInfo.maxLatencyNuma == nil || balanceInfo.maxBandwidthNuma.ReadLatency < info.ReadLatency {
			balanceInfo.maxLatencyNuma = info
		}

		if balanceInfo.maxBandwidthNuma == nil || balanceInfo.maxBandwidthNuma.ReadWriteBandwidth < info.ReadWriteBandwidth {
			balanceInfo.maxBandwidthNuma = info
		}
	}

	if balanceInfo.maxLatencyNuma.ReadLatency == 0 || balanceInfo.maxBandwidthNuma.ReadWriteBandwidth == 0 {
		err = fmt.Errorf("all numas read latency or bandwidth are 0")
		return
	}

	numaDistanceList, distanceErr := m.getNumaDistanceList(balanceInfo.maxLatencyNuma.NumaID)
	if distanceErr != nil {
		err = distanceErr
		return
	}

	// numa list sort by latency
	orderedDestNumaList := m.getNumaListOrderedByDistanceAndLatency(balanceInfo.rawNumaLatencyInfo, numaDistanceList)
	balanceInfo.needBalance, balanceInfo.balanceLevel = m.checkLatencyPressure(balanceInfo.maxLatencyNuma, orderedDestNumaList)

	if !balanceInfo.needBalance {
		return
	}

	// only one numa in this pool, no numa can be migrated to
	// TODO but still need evict?
	if len(orderedDestNumaList) == 0 {
		balanceInfo.needBalance = false
		return
	}

	balanceInfo.maxLatencyNumaBandwidthLevel = m.getBandwidthLevel(balanceInfo.maxLatencyNuma, balanceInfo.maxBandwidthNuma)
	balanceInfo.bandwidthPressure, balanceInfo.needBalance, balanceInfo.sourceNuma, orderedDestNumaList, err =
		m.regulateByBandwidthLevel(balanceInfo.maxLatencyNuma, balanceInfo.maxBandwidthNuma, balanceInfo.rawNumaLatencyInfo,
			balanceInfo.balanceLevel, balanceInfo.maxLatencyNumaBandwidthLevel, orderedDestNumaList)

	// double check, grace balance may be canceled because of maxLatencyNuma is not high bandwidth
	if !balanceInfo.needBalance {
		return
	}

	reclaimedPods, poolPods, getPodErr := m.getCandidatePods(poolName)
	if getPodErr != nil {
		err = getPodErr
		return
	}

	// if there are reclaimed pods in src numa and balanceLevel is forceBalance, try to evict them
	if balanceInfo.balanceLevel == ForceBalance {
		podToBeEvicted, podErr := m.getPodHasMaxRssOnSpecifiedNuma(balanceInfo.sourceNuma.NumaID, reclaimedPods)
		if podErr != nil {
			err = podErr
			return
		}
		// just evict reclaimed pod this turn
		if podToBeEvicted != nil {
			balanceInfo.evictPods = m.packEvictPods([]*v1.Pod{podToBeEvicted}, balanceInfo.sourceNuma)
			balanceInfo.status = BalanceStatusPrepareSuccess
			return balanceInfo, nil
		}
	}

	balanceInfo.balancePods, err = m.getBalancedPods(poolPods, reclaimedPods, balanceInfo.sourceNuma, balanceInfo.balanceLevel, balanceInfo.bandwidthPressure, orderedDestNumaList)
	if err != nil {
		return
	}

	balanceInfo.status = BalanceStatusPrepareSuccess
	return
}

// getBalancedPods returns the pods whose memory can be migrated to relief the source numa's bandwidth pressure, it contains the pods in the target pool and  the pods in reclaimed pool
func (m *memoryBalancer) getBalancedPods(poolPods, reclaimedPods []*v1.Pod, sourceNuma *NumaLatencyInfo, balanceLevel BalanceLevel, bandwidthPressure bool, orderedDestNumaList []NumaInfo) ([]*BalancePod, error) {
	targetPoolPods, targetReclaimedPods := m.getBalancePodsByRSS(poolPods, reclaimedPods, sourceNuma)

	// orderedDestNumaList is the destination numa list for non-reclaimed cores pod
	orderedDestNumaList = m.getNumaListFilteredByLatencyGapRatio(
		orderedDestNumaList, sourceNuma, balanceLevel, bandwidthPressure)
	orderedDestNumaList = m.getNumaListFilteredPingPongBalance(
		orderedDestNumaList, sourceNuma)

	result := make([]*BalancePod, 0)

	if bandwidthPressure {
		// if the balance is caused by bandwidth pressure, we don't need to select dest NUMAs for non-reclaimed cores
		// pod and reclaimed cores pod separately since the destination NUMAs has determined when regulation by
		// bandwidth pressure level
		if len(orderedDestNumaList) == 0 || (len(targetPoolPods) == 0 && len(targetReclaimedPods) == 0) {
			return result, nil
		}

		result = append(result, m.packBalancePods(targetReclaimedPods, sourceNuma, orderedDestNumaList)...)
		result = append(result, m.packBalancePods(targetPoolPods, sourceNuma, orderedDestNumaList)...)
	} else {
		if len(targetReclaimedPods) > 0 {
			// get destination pod for reclaimed cores pod in case reclaimed pool has different numa set with non-reclaimed pool
			reclaimedPoolInfo, found := m.metaReader.GetPoolInfo(state.PoolNameReclaim)
			if found {
				reclaimedNumaList := m.getPoolNumaIDs(reclaimedPoolInfo)
				reclaimedOrderedDestNumaList, getDstNumaErr := m.getDestNumaOrderByDistanceAndLatency(
					sourceNuma, reclaimedNumaList)
				if getDstNumaErr != nil {
					general.Errorf("get ordered optional dest numa list for reclaimed pool failed,skip it, err:%v", getDstNumaErr)
				} else {
					reclaimedOrderedDestNumaList = m.getNumaListFilteredByLatencyGapRatio(
						reclaimedOrderedDestNumaList, sourceNuma, balanceLevel, bandwidthPressure)
					reclaimedOrderedDestNumaList = m.getNumaListFilteredPingPongBalance(
						reclaimedOrderedDestNumaList, sourceNuma)
				}
				result = append(result, m.packBalancePods(targetReclaimedPods, sourceNuma, reclaimedOrderedDestNumaList)...)
			} else {
				general.Errorf("can't find reclaimed pool info,skip balance reclaimed core pod")
			}
		}

		result = append(result, m.packBalancePods(targetPoolPods, sourceNuma, orderedDestNumaList)...)
	}

	return result, nil
}

func (m *memoryBalancer) getCandidatePods(poolName string) (reclaimedPods, poolPods []*v1.Pod, err error) {
	var getPodErr error
	reclaimedPods, getPodErr = m.getPodsInPool(state.PoolNameReclaim)
	if getPodErr != nil {
		err = getPodErr
		return
	}

	poolPods, getPodErr = m.getPodsInPool(poolName)
	if getPodErr != nil {
		err = getPodErr
	}
	return
}

// regulateByBandwidthLevel adjust the strategy by the bandwidth pressure level
func (m *memoryBalancer) regulateByBandwidthLevel(maxLatencyNuma, maxBandwidthNuma *NumaLatencyInfo, rawNumaLatencyInfos []*NumaLatencyInfo,
	balanceLevel BalanceLevel, maxLatencyNumaBandwidthLevel BandwidthLevel, orderedDestNuma []NumaInfo) (
	bandwidthPressure, needBalance bool, sourceNuma *NumaLatencyInfo, regulatedOrderedDestNuma []NumaInfo, err error) {

	regulatedOrderedDestNuma = orderedDestNuma
	needBalance = true
	if maxLatencyNumaBandwidthLevel == BandwidthLevelHigh {
		sourceNuma = maxLatencyNuma
	} else {
		// GraceBalance need to be performed only when max latency numa is high bandwidth
		if balanceLevel == GraceBalance {
			needBalance = false
		} else {
			// when max latency numa has medium bandwidth or low bandwidth, it's high probability caused by cross-numa access from cpus belong
			// to the max latency numa, or max latency numa has low bandwidth
			// now we will balance rss from the numa which has the max bandwidth, and the dest numas will be decided by max latency numa bandwidth level
			sourceNuma = maxBandwidthNuma
			bandwidthPressure = true

			// max latency numa has medium bandwidth, it's high probability caused by cross-numa access from cpus in max lantecy numa,
			// now we have to balance rss from the numa which has the max bandwidth to the numa which has the min bandwidth
			if maxLatencyNumaBandwidthLevel == BandwidthLevelMedium {
				var numaDistanceList []machine.NumaDistanceInfo
				numaDistanceList, err = m.getNumaDistanceList(sourceNuma.NumaID)
				if err != nil {
					return
				}

				// numas sort by bandwidth
				regulatedOrderedDestNuma = m.getNumaListOrderedByBandwidth(rawNumaLatencyInfos, numaDistanceList)
				regulatedOrderedDestNuma = regulatedOrderedDestNuma[:1]
			}

			// amd has the probem that occasionally max latency numa has low bandwidth, but the latency is faked high,
			// the actual latency of cpu access this node is not high, now we have to balance rss from the numa which has
			// the max bandwidth to this numa which has faked high latency
			if maxLatencyNumaBandwidthLevel == BandwidthLevelLow {
				regulatedOrderedDestNuma = []NumaInfo{
					{
						NumaLatencyInfo: maxLatencyNuma,
						Distance:        10,
					},
				}
			}
		}
	}

	return
}

func (m *memoryBalancer) getPoolNumaIDs(poolInfo *types.PoolInfo) []int {
	poolNumaList := machine.NewCPUSet()
	for numaID := range poolInfo.TopologyAwareAssignments {
		if poolInfo.TopologyAwareAssignments[numaID].Size() > 0 {
			poolNumaList.Add(numaID)
		}
	}

	return poolNumaList.ToSliceInt()
}

func (m *memoryBalancer) getBandwidthLevel(maxLatencyNuma *NumaLatencyInfo, maxBandwidthNuma *NumaLatencyInfo) BandwidthLevel {
	if maxLatencyNuma == maxBandwidthNuma ||
		maxLatencyNuma.ReadWriteBandwidth/maxBandwidthNuma.ReadWriteBandwidth > BandwidthHighThreshold {
		return BandwidthLevelHigh
	} else if maxLatencyNuma.ReadWriteBandwidth/maxBandwidthNuma.ReadWriteBandwidth > BandwidthMediumThreshold {
		return BandwidthLevelMedium
	} else {
		return BandwidthLevelLow
	}
}

func (m *memoryBalancer) checkLatencyPressure(maxLatencyNuma *NumaLatencyInfo, orderedOptionalNuma []NumaInfo) (needBalance bool, balanceLevel BalanceLevel) {
	if maxLatencyNuma.ReadLatency > m.conf.ForceBalanceReadLatencyThreshold {
		needBalance = true
		balanceLevel = ForceBalance
	} else if maxLatencyNuma.ReadLatency > m.conf.GraceBalanceReadLatencyThreshold {
		needBalance = true
		balanceLevel = GraceBalance
	} else {
		// max latency numa has possible migrated numas
		if len(orderedOptionalNuma) > 0 {
			// when the gap between the max latency and the min latency of conditional nodes greater than this threshold,
			// then trigger grace balance,
			// conditions which conditional nodes must meet:
			//   1) distance from the node which has the max latency is less-equal GraceBalanceNumaDistanceMax
			//   2) which are nearest to the node which has the max latency, even theoretically maybe there are
			//      multiple distances less-equal GraceBalanceNumaDistanceMax
			nearestNuma := orderedOptionalNuma[0]
			if nearestNuma.Distance <= int(m.conf.GraceBalanceNumaDistanceMax) &&
				(maxLatencyNuma.ReadLatency-nearestNuma.ReadLatency) > m.conf.GraceBalanceReadLatencyGapThreshold {
				needBalance = true
				balanceLevel = GraceBalance
			}
		}
	}

	return
}

func (m *memoryBalancer) getDestNumaOrderByDistanceAndLatency(srcNuma *NumaLatencyInfo, numaIDs []int) ([]NumaInfo, error) {
	rawNumaLatencyInfo := make([]*NumaLatencyInfo, 0)
	for _, numaID := range numaIDs {
		info, err := m.getNumaLatencyInfo(numaID)
		if err != nil {
			return nil, err
		}
		rawNumaLatencyInfo = append(rawNumaLatencyInfo, info)
	}

	numaDistanceList, err := m.getNumaDistanceList(srcNuma.NumaID)
	if err != nil {
		return nil, err
	}

	// numas sort by latency
	return m.getNumaListOrderedByDistanceAndLatency(rawNumaLatencyInfo, numaDistanceList), nil
}

func (m *memoryBalancer) getNumaLatencyInfo(numaID int) (*NumaLatencyInfo, error) {
	readLatency, err := helper.GetNumaMetric(m.metaServer.MetricsFetcher, m.emitter, m.conf.ReadLatencyMetric, numaID)
	if err != nil {
		return nil, err
	}
	// unit GiB
	memBandwidth, err := helper.GetNumaMetric(m.metaServer.MetricsFetcher, m.emitter, consts.MetricMemBandwidthNuma, numaID)
	if err != nil {
		return nil, err
	}
	return &NumaLatencyInfo{
		NumaID:             numaID,
		ReadLatency:        readLatency,
		ReadWriteBandwidth: memBandwidth,
	}, nil
}

func (m *memoryBalancer) getBalancePodsByRSS(poolPods []*v1.Pod, reclaimedPods []*v1.Pod, srcNuma *NumaLatencyInfo) (targetPoolPods []*v1.Pod, targetReclaimedPods []*v1.Pod) {
	poolPodSortList := make([]PodSort, 0, len(poolPods))
	reclaimedPodSortList := make([]PodSort, 0, len(reclaimedPods))

	for i := range reclaimedPods {
		pod := reclaimedPods[i]
		anon, err := helper.GetPodMetric(m.metaServer.MetricsFetcher, m.emitter, pod, consts.MetricsMemAnonPerNumaContainer, srcNuma.NumaID)
		if err != nil {
			general.Errorf("failed to find metric %v for pod %v/%v", consts.MetricsMemAnonPerNumaContainer, pod.Namespace, pod.Name)
			continue
		}

		if int64(anon) > m.conf.BalancedReclaimedPodSourceNumaRSSMin && int64(anon) < m.conf.BalancedReclaimedPodSourceNumaRSSMax {
			reclaimedPodSortList = append(reclaimedPodSortList, PodSort{
				Pod:       pod,
				SortValue: uint64(anon),
			})
		}
	}

	for i := range poolPods {
		pod := poolPods[i]
		anon, err := helper.GetPodMetric(m.metaServer.MetricsFetcher, m.emitter, pod, consts.MetricsMemAnonPerNumaContainer, srcNuma.NumaID)
		if err != nil {
			general.Errorf("failed to find metric %v for pod %v/%v", consts.MetricsMemAnonPerNumaContainer, pod.Namespace, pod.Name)
			continue
		}

		if int64(anon) > m.conf.BalancedPodSourceNumaRSSMin && int64(anon) < m.conf.BalancedPodSourceNumaRSSMax {
			poolPodSortList = append(poolPodSortList, PodSort{
				Pod:       pod,
				SortValue: uint64(anon),
			})
		}
	}

	if len(poolPodSortList) > 1 {
		// sort in ascending order for pods in target pool
		sort.Slice(poolPodSortList, func(i, j int) bool {
			return poolPodSortList[i].SortValue < poolPodSortList[j].SortValue
		})
	}
	if len(reclaimedPodSortList) > 1 {
		// sort in ascending order for reclaimed pods
		sort.Slice(reclaimedPodSortList, func(i, j int) bool {
			return reclaimedPodSortList[i].SortValue < reclaimedPodSortList[j].SortValue
		})
	}

	targetPoolPods = make([]*v1.Pod, 0)
	targetReclaimedPods = make([]*v1.Pod, 0)
	var totalRSS uint64

	for _, sortPod := range reclaimedPodSortList {
		if totalRSS+sortPod.SortValue > uint64(m.conf.BalancedReclaimedPodsSourceNumaTotalRSSMax) {
			break
		}

		targetReclaimedPods = append(targetReclaimedPods, sortPod.Pod)
		totalRSS += sortPod.SortValue
	}

	if totalRSS >= uint64(m.conf.BalancedReclaimedPodsSingleRoundTotalRssThreshold) {
		return
	}

	for _, sortPod := range poolPodSortList {
		if totalRSS+sortPod.SortValue > uint64(m.conf.BalancedPodsSourceNumaTotalRSSMax) {
			break
		}

		targetPoolPods = append(targetPoolPods, sortPod.Pod)
		totalRSS += sortPod.SortValue
	}

	return
}

func (m *memoryBalancer) packEvictPods(pods []*v1.Pod, sourceNuma *NumaLatencyInfo) []*EvictPod {
	result := make([]*EvictPod, 0, len(pods))
	for i := range pods {
		result = append(result, &EvictPod{pod: pods[i], srcNuma: sourceNuma.NumaID})
	}

	return result
}

func (m *memoryBalancer) packBalancePods(poolPods []*v1.Pod, sourceNuma *NumaLatencyInfo, targetNumas []NumaInfo) []*BalancePod {
	result := make([]*BalancePod, 0, len(poolPods))

	if len(poolPods) > 0 && len(targetNumas) > 0 {
		targetNumaIDs := m.getNumaIDs(targetNumas)
		for i := range poolPods {
			result = append(result, &BalancePod{
				pod:          poolPods[i],
				srcNuma:      sourceNuma.NumaID,
				destNumaList: targetNumaIDs,
			})
		}
	}

	return result
}

func (m *memoryBalancer) getNumaIDs(numaInfos []NumaInfo) []int {
	numaIDs := make([]int, 0, len(numaInfos))
	for i := range numaInfos {
		numaIDs = append(numaIDs, numaInfos[i].NumaID)
	}

	return numaIDs
}

func (m *memoryBalancer) setBalanceInfo(balanceMap map[string]*PoolBalanceInfo, currentSessionID string) {
	defer m.mutex.Unlock()
	m.mutex.Lock()

	m.balanceInfo = &BalanceInfo{
		pool2BalanceInfo: balanceMap,
		sessionID:        currentSessionID,
		balanceTime:      time.Now(),
	}
}

func (m *memoryBalancer) getPodsInPool(poolName string) ([]*v1.Pod, error) {
	result := make([]*v1.Pod, 0)
	errList := make([]error, 0)
	m.metaReader.RangeContainer(func(podUID string, containerName string, containerInfo *types.ContainerInfo) bool {
		if containerInfo == nil {
			return true
		}

		if containerInfo.ContainerType != v1alpha1.ContainerType_MAIN {
			return true
		}

		if containerInfo.OwnerPoolName == poolName {
			pod, err := m.metaServer.GetPod(context.TODO(), podUID)
			if err != nil {
				errList = append(errList, err)
				return true
			}
			result = append(result, pod)
		}

		return true
	})

	return result, errors.NewAggregate(errList)
}

func (m *memoryBalancer) getPodHasMaxRssOnSpecifiedNuma(numaID int, pods []*v1.Pod) (*v1.Pod, error) {
	var portSortList []PodSort
	for _, podInfo := range pods {
		anon, err := helper.GetPodMetric(m.metaServer.MetricsFetcher, m.emitter, podInfo, consts.MetricsMemAnonPerNumaContainer, numaID)
		if err != nil {
			general.Errorf("failed to get %v for pod %v/%v on numa %v", consts.MetricsMemAnonPerNumaContainer, podInfo.Namespace, podInfo.Name, numaID)
			continue
		}

		portSortList = append(portSortList, PodSort{
			Pod:       podInfo,
			SortValue: uint64(anon),
		})
	}

	if len(portSortList) == 0 {
		return nil, nil
	}

	sort.Slice(portSortList, func(i, j int) bool {
		return portSortList[i].SortValue > portSortList[j].SortValue
	})
	return portSortList[0].Pod, nil
}

func (m *memoryBalancer) getNumaListFilteredByLatencyGapRatio(orderedNumaList []NumaInfo, srcNuma *NumaLatencyInfo, balanceLevel BalanceLevel, sortByBandwidth bool) []NumaInfo {
	var numaList []NumaInfo
	var gapRatio float64
	for _, n := range orderedNumaList {
		if balanceLevel == GraceBalance && n.Distance > int(m.conf.GraceBalanceNumaDistanceMax) {
			break
		}

		if n.Distance <= int(m.conf.GraceBalanceNumaDistanceMax) {
			gapRatio = m.conf.GraceBalanceGapRatio
		} else {
			gapRatio = m.conf.ForceBalanceGapRatio
		}

		if sortByBandwidth {
			if n.ReadWriteBandwidth*(1+gapRatio) <= srcNuma.ReadWriteBandwidth {
				numaList = append(numaList, n)
			}
		} else {
			if n.ReadLatency*(1+gapRatio) <= srcNuma.ReadLatency {
				numaList = append(numaList, n)
			}
		}
	}
	return numaList
}

func (m *memoryBalancer) getNumaListFilteredPingPongBalance(orderedNumaList []NumaInfo, srcNuma *NumaLatencyInfo) []NumaInfo {
	result := make([]NumaInfo, 0, len(orderedNumaList))

	for i := range orderedNumaList {
		if m.isPingPongBalance(srcNuma.NumaID, orderedNumaList[i].NumaID) {
			continue
		}
		result = append(result, orderedNumaList[i])
	}

	return result
}

func (m *memoryBalancer) isPingPongBalance(srcNuma int, dstNuma int) bool {
	if m.lastBalanceInfo == nil {
		return false
	}

	if time.Since(m.lastBalanceInfo.balanceTime).Minutes() > PingPongDetectThresholdInMinutes {
		return false
	}

	for _, poolBalanceInfo := range m.lastBalanceInfo.pool2BalanceInfo {
		if dstNuma != poolBalanceInfo.sourceNuma.NumaID {
			continue
		}

		for i := range poolBalanceInfo.balancePods {
			for j := range poolBalanceInfo.balancePods[i].destNumaList {
				if srcNuma == poolBalanceInfo.balancePods[i].destNumaList[j] {
					return true
				}
			}
		}
	}

	return false
}

func (m *memoryBalancer) getNumaListOrderedByDistanceAndLatency(numaLatencies []*NumaLatencyInfo, numaDistanceList []machine.NumaDistanceInfo) []NumaInfo {
	var numaInfoList = m.buildNumaInfoList(numaLatencies, numaDistanceList)
	sort.Slice(numaInfoList, func(i, j int) bool {
		if numaInfoList[i].Distance != numaInfoList[j].Distance {
			return numaInfoList[i].Distance < numaInfoList[j].Distance
		}
		return numaInfoList[i].ReadLatency < numaInfoList[j].ReadLatency
	})
	return numaInfoList
}

func (m *memoryBalancer) getNumaListOrderedByBandwidth(numaLatencies []*NumaLatencyInfo, numaDistanceList []machine.NumaDistanceInfo) []NumaInfo {
	var numaInfoList = m.buildNumaInfoList(numaLatencies, numaDistanceList)
	sort.Slice(numaInfoList, func(i, j int) bool {
		return numaInfoList[i].ReadWriteBandwidth < numaInfoList[j].ReadWriteBandwidth
	})
	return numaInfoList
}

func (m *memoryBalancer) buildNumaInfoList(numaLatencies []*NumaLatencyInfo, numaDistanceList []machine.NumaDistanceInfo) []NumaInfo {
	var numaInfoList []NumaInfo
	for _, nd := range numaDistanceList {
		for _, nl := range numaLatencies {
			if nd.NumaID == nl.NumaID {
				numaInfoList = append(numaInfoList, NumaInfo{
					NumaLatencyInfo: nl,
					Distance:        nd.Distance,
				})
				break
			}
		}
	}
	return numaInfoList
}

func (m *memoryBalancer) getNumaDistanceList(numaID int) ([]machine.NumaDistanceInfo, error) {
	numaDistanceList, ok := m.metaServer.ExtraTopologyInfo.NumaDistanceMap[numaID]
	if !ok {
		return nil, fmt.Errorf("failed to find numa id(%d)in numa distance array(%+v)", numaID, m.metaServer.ExtraTopologyInfo.NumaDistanceMap)
	}
	return numaDistanceList, nil
}

func (m *memoryBalancer) GetAdvices() (result types.InternalMemoryCalculationResult) {
	result = types.InternalMemoryCalculationResult{
		ContainerEntries: make([]types.ContainerMemoryAdvices, 0),
	}
	m.mutex.Lock()
	defer m.mutex.Unlock()
	if m.balanceInfo == nil {
		return
	}

	needBalance := false
	for _, poolBalanceInfo := range m.balanceInfo.pool2BalanceInfo {
		if !poolBalanceInfo.needBalance || poolBalanceInfo.status != BalanceStatusPrepareSuccess ||
			len(poolBalanceInfo.balancePods) == 0 {
			continue
		}

		needBalance = true
		for _, balancePod := range poolBalanceInfo.balancePods {
			advice := types.NumaMemoryBalanceAdvice{
				SourceNuma: balancePod.srcNuma,
				DestNumas:  balancePod.destNumaList,
			}

			adviceBytes, err := json.Marshal(advice)
			if err != nil {
				general.Errorf("fail to marshall numa memory balance advice:%v,err:%v", advice, err)
				continue
			}

			for _, container := range balancePod.pod.Spec.Containers {
				result.ContainerEntries = append(result.ContainerEntries, types.ContainerMemoryAdvices{
					PodUID:        string(balancePod.pod.UID),
					ContainerName: container.Name,
					Values: map[string]string{
						string(memoryadvisor.ControlKnobKeyBalanceNumaMemory): string(adviceBytes),
					},
				})
			}
		}
	}

	if needBalance {
		m.lastBalanceInfo = m.balanceInfo
	}
	// avoid duplicate execution
	m.balanceInfo = nil

	return
}
