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
	"strconv"
	"sync"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/errors"
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

	EvictReason = "the memory node %v is under bandwidth pressure"

	MetricNumaMemoryBalance = "numa_memory_balance"
)

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

	MigratePagesThreshold = 0.7
	BalanceInterval       = 30 * time.Second
)

type NumaInfo struct {
	*NumaLatencyInfo
	Distance int `json:"Distance"`
}

type PodSort struct {
	Pod          *v1.Pod
	DestNumaList []int
	SortValue    float64
}

type NumaLatencyInfo struct {
	NumaID             int     `json:"NumaID"`
	ReadLatency        float64 `json:"ReadLatency"`
	ReadWriteBandwidth float64 `json:"ReadWriteBandwidth"`
}

type BalancePod struct {
	Namespace       string   `json:"namespace"`
	Name            string   `json:"name"`
	UID             string   `json:"UID"`
	Containers      []string `json:"containers"`
	RSSOnSourceNuma float64  `json:"RSSOnSourceNuma"`
	DestNumas       []int    `json:"destNumas"`
}

type EvictPod struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	UID       string `json:"UID"`
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
			general.Infof("start memory balancer eviction plugin")
			m.started = true
		}
	}

	return m.memoryBalancer.Reconcile(status)
}

func (m *memoryBalancerWrapper) GetAdvices() types.InternalMemoryCalculationResult {
	return m.memoryBalancer.GetAdvices()
}

type BalanceInfo struct {
	EvictPods                    []EvictPod         `json:"evictPods"`
	BalancePods                  []BalancePod       `json:"balancePods"`
	DestNumas                    []NumaInfo         `json:"destNumas"`
	NeedBalance                  bool               `json:"needBalance"`
	BandwidthPressure            bool               `json:"bandwidthPressure"`
	BalanceLevel                 BalanceLevel       `json:"balanceLevel"`
	TotalRSS                     float64            `json:"totalRSS"`
	MaxLatencyNuma               *NumaLatencyInfo   `json:"maxLatencyNuma"`
	MaxBandwidthNuma             *NumaLatencyInfo   `json:"maxBandwidthNuma"`
	RawNumaLatencyInfo           []*NumaLatencyInfo `json:"rawNumaLatencyInfo"`
	MaxLatencyNumaBandwidthLevel BandwidthLevel     `json:"maxLatencyNumaBandwidthLevel"`
	SourceNuma                   *NumaLatencyInfo   `json:"sourceNuma"`
	Status                       BalanceStatus      `json:"status"`
	FailedReason                 string             `json:"failedReason"`
	DetectTime                   time.Time          `json:"detectTime"`
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

func (m *memoryBalancer) GetEvictPods(_ context.Context, request *pluginapi.GetEvictPodsRequest) (*pluginapi.GetEvictPodsResponse, error) {
	defer m.mutex.Unlock()
	m.mutex.Lock()

	evictPods := make([]*pluginapi.EvictPod, 0)
	if m.balanceInfo == nil {
		return &pluginapi.GetEvictPodsResponse{}, nil
	}

	pods := m.balanceInfo.EvictPods
	for i := range m.balanceInfo.EvictPods {
		for _, activePod := range request.ActivePods {
			if pods[i].UID == string(activePod.UID) {
				evictPods = append(evictPods, &pluginapi.EvictPod{
					Pod:                activePod,
					Reason:             fmt.Sprintf(EvictReason, m.balanceInfo.SourceNuma.NumaID),
					ForceEvict:         true,
					EvictionPluginName: EvictionPluginNameMemoryBalancer,
				})
			}
		}
	}

	m.balanceInfo.EvictPods = make([]EvictPod, 0)
	return &pluginapi.GetEvictPodsResponse{EvictPods: evictPods}, nil
}

func (m *memoryBalancer) Reconcile(_ *types.MemoryPressureStatus) error {
	if m.lastBalanceInfo != nil && time.Now().Sub(m.lastBalanceInfo.DetectTime) < BalanceInterval {
		general.Infof("balance occurs recently,skip this turn")
		return nil
	}

	balanceInfo, err := m.getBalanceInfo()
	balanceInfoStr, _ := json.Marshal(balanceInfo)
	general.Infof("get BalanceInfo info: %v", string(balanceInfoStr))
	_ = m.emitter.StoreInt64(MetricNumaMemoryBalance, 1, metrics.MetricTypeNameRaw,
		metrics.MetricTag{Key: "need_balance", Val: strconv.FormatBool(balanceInfo.NeedBalance)},
		metrics.MetricTag{Key: "balance_level", Val: string(balanceInfo.BalanceLevel)},
		metrics.MetricTag{Key: "bandwidth_pressure", Val: strconv.FormatBool(balanceInfo.BandwidthPressure)},
		metrics.MetricTag{Key: "status", Val: string(balanceInfo.Status)},
		metrics.MetricTag{Key: "bandwidth_level", Val: string(balanceInfo.MaxLatencyNumaBandwidthLevel)},
		metrics.MetricTag{Key: "failed_reason", Val: balanceInfo.FailedReason},
	)
	if err != nil {
		general.Errorf("failed to get balance info,err:%v", err)
		return err
	}

	m.setBalanceInfo(balanceInfo)
	return nil
}

func (m *memoryBalancer) getBalanceInfo() (balanceInfo *BalanceInfo, err error) {
	balanceInfo = &BalanceInfo{
		EvictPods:          make([]EvictPod, 0),
		BalancePods:        make([]BalancePod, 0),
		NeedBalance:        false,
		RawNumaLatencyInfo: make([]*NumaLatencyInfo, 0),
		Status:             BalanceStatusPreparing,
		DetectTime:         time.Now(),
	}

	defer func() {
		if err != nil {
			balanceInfo.Status = BalanceStatusPrepareFailed
			balanceInfo.FailedReason = err.Error()
		}
	}()

	for numaID := 0; numaID < m.metaServer.CPUTopology.NumNUMANodes; numaID++ {
		info, getLatencyErr := m.getNumaLatencyInfo(numaID)
		if getLatencyErr != nil {
			err = getLatencyErr
			return
		}
		balanceInfo.RawNumaLatencyInfo = append(balanceInfo.RawNumaLatencyInfo, info)

		if balanceInfo.MaxLatencyNuma == nil || balanceInfo.MaxBandwidthNuma.ReadLatency < info.ReadLatency {
			balanceInfo.MaxLatencyNuma = info
		}

		if balanceInfo.MaxBandwidthNuma == nil || balanceInfo.MaxBandwidthNuma.ReadWriteBandwidth < info.ReadWriteBandwidth {
			balanceInfo.MaxBandwidthNuma = info
		}
	}

	if balanceInfo.MaxLatencyNuma.ReadLatency == 0 || balanceInfo.MaxBandwidthNuma.ReadWriteBandwidth == 0 {
		err = fmt.Errorf("all numas read latency or bandwidth are 0")
		return
	}

	numaDistanceList, distanceErr := m.getNumaDistanceList(balanceInfo.MaxLatencyNuma.NumaID)
	if distanceErr != nil {
		err = distanceErr
		return
	}

	// numa list sort by latency
	orderedDestNumaList := m.getNumaListOrderedByDistanceAndLatency(balanceInfo.RawNumaLatencyInfo, numaDistanceList)
	balanceInfo.NeedBalance, balanceInfo.BalanceLevel = m.checkLatencyPressure(balanceInfo.MaxLatencyNuma, orderedDestNumaList)

	if !balanceInfo.NeedBalance {
		return
	}

	// only one numa in this pool, no numa can be migrated to
	if len(orderedDestNumaList) == 0 {
		err = fmt.Errorf("failed to find any dest numa")
		return
	}

	balanceInfo.MaxLatencyNumaBandwidthLevel = m.getBandwidthLevel(balanceInfo.MaxLatencyNuma, balanceInfo.MaxBandwidthNuma)
	balanceInfo.BandwidthPressure, balanceInfo.NeedBalance, balanceInfo.SourceNuma, orderedDestNumaList, err =
		m.regulateByBandwidthLevel(balanceInfo.MaxLatencyNuma, balanceInfo.MaxBandwidthNuma, balanceInfo.RawNumaLatencyInfo,
			balanceInfo.BalanceLevel, balanceInfo.MaxLatencyNumaBandwidthLevel, orderedDestNumaList)

	// double check, grace balance may be canceled because of maxLatencyNuma is not high bandwidth
	if !balanceInfo.NeedBalance {
		return
	}

	balanceInfo.DestNumas = m.getDestNumaList(orderedDestNumaList, balanceInfo.SourceNuma, balanceInfo.BalanceLevel, balanceInfo.BandwidthPressure)
	if len(balanceInfo.DestNumas) > 0 {
		balanceInfo.BalancePods, balanceInfo.TotalRSS, err = m.getBalancePods(m.conf.SupportedPools, balanceInfo.SourceNuma, balanceInfo.DestNumas)
		if err != nil {
			return
		}
	}

	canBalancePod := len(balanceInfo.DestNumas) > 0 && len(balanceInfo.BalancePods) > 0
	general.Infof("destNumas:%+v, balancePods:%+v, totalRSS: %+v", balanceInfo.DestNumas, balanceInfo.BalancePods, balanceInfo.TotalRSS)

	// if there are no pods can be balanced and balanceLevel is forceBalance, try to evict them
	if balanceInfo.BalanceLevel == ForceBalance && !canBalancePod {
		general.Infof("can't found pods can be balanced, try to find pod to evict")
		balanceInfo.EvictPods, err = m.getEvictPods(balanceInfo.SourceNuma)
		if err != nil {
			return
		}
	}

	if !canBalancePod && len(balanceInfo.EvictPods) == 0 {
		balanceInfo.Status = BalanceStatusPrepareFailed
		balanceInfo.FailedReason = "there is latency pressure but no pod can be balanced or evicted"
	}

	balanceInfo.Status = BalanceStatusPrepareSuccess
	return
}

func (m *memoryBalancer) getEvictPods(sourceNuma *NumaLatencyInfo) ([]EvictPod, error) {
	reclaimedPods, err := m.getPodsInPool(state.PoolNameReclaim)
	if err != nil {
		return nil, err
	}

	podToBeEvicted := m.getPodHasMaxRssOnSpecifiedNuma(sourceNuma.NumaID, reclaimedPods)

	result := make([]EvictPod, 0)
	if podToBeEvicted != nil {
		result = m.packEvictPods([]*v1.Pod{podToBeEvicted})
	}

	return result, nil
}

func (m *memoryBalancer) getDestNumaList(orderedDestNumaList []NumaInfo, sourceNuma *NumaLatencyInfo,
	balanceLevel BalanceLevel, bandwidthPressure bool) (poolPodDestNumaList []NumaInfo) {
	// orderedDestNumaList is the destination numa list for non-reclaimed cores pod
	poolPodDestNumaList = m.getNumaListFilteredByLatencyGapRatio(
		orderedDestNumaList, sourceNuma, balanceLevel, bandwidthPressure)
	poolPodDestNumaList = m.getNumaListFilteredPingPongBalance(
		poolPodDestNumaList, sourceNuma)

	return
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

func (m *memoryBalancer) getBalancePodsForPool(poolName string, srcNuma *NumaLatencyInfo, destNumas []NumaInfo, leftBoundary, rightBoundary int64) ([]PodSort, error) {
	result := make([]PodSort, 0)
	destNumaSet := machine.NewCPUSet(m.getNumaIDs(destNumas)...)
	poolInfo, found := m.metaReader.GetPoolInfo(poolName)
	if !found {
		general.Infof("pool %v not found in meta cache,skip it", poolName)
		return result, nil
	}

	pods, err := m.getPodsInPool(poolName)
	if err != nil {
		return result, err
	}

	poolNumaSet := machine.NewCPUSet(m.getPoolNumaIDs(poolInfo)...)
	general.Infof("get balanced pod for pool %v, pool numas:%v, destNumas: %v, leftBoundary: %v, rightBoundary: %v",
		poolName, poolNumaSet, destNumaSet, general.FormatMemoryQuantity(float64(leftBoundary)), general.FormatMemoryQuantity(float64(rightBoundary)))

	if destNumaSet.Intersection(poolNumaSet).Size() > 0 {
		for i := range pods {
			pod := pods[i]
			anon, err := helper.GetPodMetric(m.metaServer.MetricsFetcher, m.emitter, pod, consts.MetricsMemAnonPerNumaContainer, srcNuma.NumaID)
			if err != nil {
				general.Errorf("failed to find metric %v for pod %v/%v", consts.MetricsMemAnonPerNumaContainer, pod.Namespace, pod.Name)
				continue
			}

			general.Infof("pod %v/%v has anon %v on numa %v", pod.Namespace, pod.Name, general.FormatMemoryQuantity(anon), srcNuma.NumaID)
			if int64(anon) > leftBoundary && int64(anon) < rightBoundary {
				result = append(result, PodSort{
					Pod:          pod,
					DestNumaList: poolNumaSet.ToSliceInt(),
					SortValue:    anon,
				})
			}
		}
	} else {
		general.Infof("pool %v has no intersection with dest numas, skip it", poolName)
	}
	return result, nil
}

func (m *memoryBalancer) getBalancePods(supportPools []string, srcNuma *NumaLatencyInfo, destNumas []NumaInfo) ([]BalancePod, float64, error) {
	var totalRSS float64 = 0
	poolPodSortList := make([]PodSort, 0)
	reclaimedPodSortList, err := m.getBalancePodsForPool(state.PoolNameReclaim, srcNuma, destNumas, m.conf.BalancedReclaimedPodSourceNumaRSSMin, m.conf.BalancedReclaimedPodSourceNumaRSSMax)
	if err != nil {
		return nil, totalRSS, err
	}

	for _, poolName := range supportPools {
		podSortList, err := m.getBalancePodsForPool(poolName, srcNuma, destNumas, m.conf.BalancedPodSourceNumaRSSMin, m.conf.BalancedPodSourceNumaRSSMax)
		if err != nil {
			return nil, totalRSS, err
		}

		poolPodSortList = append(poolPodSortList, podSortList...)
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

	targetPods := make([]BalancePod, 0)

	for _, sortPod := range reclaimedPodSortList {
		if totalRSS+sortPod.SortValue > float64(m.conf.BalancedReclaimedPodsSourceNumaTotalRSSMax) {
			break
		}

		targetPods = append(targetPods, m.packBalancePods(sortPod.Pod, sortPod.DestNumaList, sortPod.SortValue))
		totalRSS += sortPod.SortValue
	}

	if totalRSS >= float64(m.conf.BalancedReclaimedPodsSingleRoundTotalRssThreshold) {
		general.Infof("balance reclaimed pod total rss is greater than threshold %v", float64(m.conf.BalancedReclaimedPodsSingleRoundTotalRssThreshold))
		return targetPods, totalRSS, nil
	}

	for _, sortPod := range poolPodSortList {
		if totalRSS+sortPod.SortValue > float64(m.conf.BalancedPodsSourceNumaTotalRSSMax) {
			break
		}

		targetPods = append(targetPods, m.packBalancePods(sortPod.Pod, sortPod.DestNumaList, sortPod.SortValue))
		totalRSS += sortPod.SortValue
	}

	return targetPods, totalRSS, nil
}

func (m *memoryBalancer) packEvictPods(pods []*v1.Pod) []EvictPod {
	result := make([]EvictPod, 0, len(pods))
	for i := range pods {
		result = append(result, EvictPod{Namespace: pods[i].Namespace, Name: pods[i].Name, UID: string(pods[i].UID)})
	}

	return result
}

func (m *memoryBalancer) packBalancePods(pod *v1.Pod, destNumas []int, RSSOnSourceNuma float64) BalancePod {
	containers := make([]string, 0, len(pod.Spec.Containers))
	for i := range pod.Spec.Containers {
		containers = append(containers, pod.Spec.Containers[i].Name)
	}

	return BalancePod{Namespace: pod.Namespace,
		Name: pod.Name, UID: string(pod.UID), Containers: containers, RSSOnSourceNuma: RSSOnSourceNuma, DestNumas: destNumas}
}

func (m *memoryBalancer) getNumaIDs(numaInfos []NumaInfo) []int {
	numaIDs := make([]int, 0, len(numaInfos))
	for i := range numaInfos {
		numaIDs = append(numaIDs, numaInfos[i].NumaID)
	}

	return numaIDs
}

func (m *memoryBalancer) setBalanceInfo(balanceInfo *BalanceInfo) {
	defer m.mutex.Unlock()
	m.mutex.Lock()

	m.balanceInfo = balanceInfo
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

func (m *memoryBalancer) getPodHasMaxRssOnSpecifiedNuma(numaID int, pods []*v1.Pod) *v1.Pod {
	var portSortList []PodSort
	for _, podInfo := range pods {
		anon, err := helper.GetPodMetric(m.metaServer.MetricsFetcher, m.emitter, podInfo, consts.MetricsMemAnonPerNumaContainer, numaID)
		if err != nil {
			general.Errorf("failed to get metric %v for pod %v/%v on numa %v", consts.MetricsMemAnonPerNumaContainer, podInfo.Namespace, podInfo.Name, numaID)
			continue
		}

		portSortList = append(portSortList, PodSort{
			Pod:       podInfo,
			SortValue: anon,
		})
	}

	if len(portSortList) == 0 {
		return nil
	}

	sort.Slice(portSortList, func(i, j int) bool {
		return portSortList[i].SortValue > portSortList[j].SortValue
	})
	return portSortList[0].Pod
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

	if time.Since(m.lastBalanceInfo.DetectTime).Minutes() > PingPongDetectThresholdInMinutes {
		return false
	}

	if dstNuma != m.lastBalanceInfo.SourceNuma.NumaID {
		return false
	}

	for i := range m.lastBalanceInfo.DestNumas {
		if srcNuma == m.lastBalanceInfo.DestNumas[i].NumaID {
			return true
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
		ExtraEntries: make([]types.ExtraMemoryAdvices, 0),
	}
	m.mutex.Lock()
	defer m.mutex.Unlock()
	if m.balanceInfo == nil {
		return
	}

	if !m.balanceInfo.NeedBalance || m.balanceInfo.Status != BalanceStatusPrepareSuccess ||
		len(m.balanceInfo.BalancePods) == 0 || len(m.balanceInfo.DestNumas) == 0 {
		return
	}

	advice := types.NumaMemoryBalanceAdvice{
		SourceNuma:        m.balanceInfo.SourceNuma.NumaID,
		DestNumaList:      m.getNumaIDs(m.balanceInfo.DestNumas),
		TotalRSS:          m.balanceInfo.TotalRSS,
		Threshold:         MigratePagesThreshold,
		MigrateContainers: make([]types.NumaMemoryBalanceContainerInfo, 0),
	}

	for _, balancePod := range m.balanceInfo.BalancePods {
		for _, container := range balancePod.Containers {
			advice.MigrateContainers = append(advice.MigrateContainers, types.NumaMemoryBalanceContainerInfo{
				PodUID:        balancePod.UID,
				ContainerName: container,
				DestNumaList:  balancePod.DestNumas,
			})
		}
	}

	adviceStr, err := json.Marshal(advice)
	if err != nil {
		general.Errorf("marshal numa memory balance advice failed: %v", err)
		return
	}
	result.ExtraEntries = append(result.ExtraEntries, types.ExtraMemoryAdvices{
		Values: map[string]string{
			string(memoryadvisor.ControlKnobKeyBalanceNumaMemory): string(adviceStr),
		},
	})

	if m.balanceInfo.NeedBalance {
		m.lastBalanceInfo = m.balanceInfo
	}
	// avoid duplicate execution
	m.balanceInfo = nil

	return
}
