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

	MetricNumaMemoryBalance = "numa_memory_balance"
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

	MigratePagesThreshold = 0.7
)

type NumaInfo struct {
	*NumaLatencyInfo
	Distance int `json:"Distance"`
}

type PodSort struct {
	Pod       *v1.Pod
	SortValue float64
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
	EvictPods                    []EvictPod         `json:"evictPods"`
	BalancePods                  []BalancePod       `json:"balancePods"`
	BalanceReclaimedPods         []BalancePod       `json:"balanceReclaimedPods"`
	DestNumas                    []NumaInfo         `json:"destNumas"`
	ReclaimedDestNumas           []NumaInfo         `json:"reclaimedDestNumas"`
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

func (m *memoryBalancer) GetEvictPods(_ context.Context, request *pluginapi.GetEvictPodsRequest) (*pluginapi.GetEvictPodsResponse, error) {
	defer m.mutex.Unlock()
	m.mutex.Lock()

	//TODO check if the pod is in the request
	evictPods := make([]*pluginapi.EvictPod, 0)
	for poolName, poolBalanceInfo := range m.balanceInfo.pool2BalanceInfo {
		pods := poolBalanceInfo.EvictPods
		for i := range pods {
			for _, activePod := range request.ActivePods {
				if pods[i].UID == string(activePod.UID) {
					evictPods = append(evictPods, &pluginapi.EvictPod{
						Pod:                activePod,
						Reason:             fmt.Sprintf(EvictReason, poolBalanceInfo.SourceNuma.NumaID, poolName),
						ForceEvict:         true,
						EvictionPluginName: EvictionPluginNameMemoryBalancer,
					})
				}
			}
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
		balanceInfoStr, _ := json.Marshal(balanceInfo)
		general.Infof("get BalanceInfo for pool %v, info: %v", poolName, string(balanceInfoStr))
		balanceInfoMap[poolName] = balanceInfo
		_ = m.emitter.StoreInt64(MetricNumaMemoryBalance, 1, metrics.MetricTypeNameCount,
			metrics.MetricTag{Key: "need_balance", Val: strconv.FormatBool(balanceInfo.NeedBalance)},
			metrics.MetricTag{Key: "balance_level", Val: string(balanceInfo.BalanceLevel)},
			metrics.MetricTag{Key: "bandwidth_pressure", Val: strconv.FormatBool(balanceInfo.BandwidthPressure)},
			metrics.MetricTag{Key: "status", Val: string(balanceInfo.Status)},
			metrics.MetricTag{Key: "bandwidth_level", Val: string(balanceInfo.MaxLatencyNumaBandwidthLevel)},
			metrics.MetricTag{Key: "failed_reason", Val: balanceInfo.FailedReason},
		)
	}

	m.setBalanceInfo(balanceInfoMap, string(sessionID))
	return nil
}

func (m *memoryBalancer) getBalanceInfo(poolName string) (balanceInfo *PoolBalanceInfo, err error) {
	balanceInfo = &PoolBalanceInfo{
		EvictPods:            make([]EvictPod, 0),
		BalancePods:          make([]BalancePod, 0),
		BalanceReclaimedPods: make([]BalancePod, 0),
		NeedBalance:          false,
		RawNumaLatencyInfo:   make([]*NumaLatencyInfo, 0),
		Status:               BalanceStatusPreparing,
	}

	defer func() {
		if err != nil {
			balanceInfo.Status = BalanceStatusPrepareFailed
			balanceInfo.FailedReason = err.Error()
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
	// TODO but still need evict?
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

	reclaimedPods, poolPods, getPodErr := m.getCandidatePods(poolName)
	if getPodErr != nil {
		err = getPodErr
		return
	}

	canBalancePoolPod := false
	canBalanceReclaimedPod := false
	balanceInfo.DestNumas, balanceInfo.ReclaimedDestNumas = m.getDestNumaList(orderedDestNumaList, balanceInfo.SourceNuma, balanceInfo.BalanceLevel, balanceInfo.BandwidthPressure)
	balanceInfo.BalancePods, balanceInfo.BalanceReclaimedPods, balanceInfo.TotalRSS = m.getBalancePodsByRSS(poolPods, reclaimedPods, balanceInfo.SourceNuma)

	canBalancePoolPod = len(balanceInfo.DestNumas) > 0 && len(balanceInfo.BalancePods) > 0
	canBalanceReclaimedPod = len(balanceInfo.BalanceReclaimedPods) > 0 && len(balanceInfo.ReclaimedDestNumas) > 0
	general.Infof("destNumas:%+v, destReclaimedNumas:%+v, balancePods:%+v, balanceReclaimedPods:%+v, totalRSS: %+v",
		balanceInfo.DestNumas, balanceInfo.ReclaimedDestNumas, balanceInfo.BalancePods, balanceInfo.BalanceReclaimedPods, balanceInfo.TotalRSS)

	// if there are no pods can be balanced and balanceLevel is forceBalance, try to evict them
	if balanceInfo.BalanceLevel == ForceBalance && !canBalanceReclaimedPod && !canBalancePoolPod {
		general.Infof("can't found pods can be balanced, try to find pod to evict")
		balanceInfo.EvictPods = m.getEvictPods(reclaimedPods, balanceInfo.SourceNuma)
	}

	if !canBalancePoolPod && !canBalanceReclaimedPod && len(balanceInfo.EvictPods) == 0 {
		balanceInfo.Status = BalanceStatusPrepareFailed
		balanceInfo.FailedReason = "there is latency pressure but no pod can be balanced or evicted"
	}

	balanceInfo.Status = BalanceStatusPrepareSuccess
	return
}

func (m *memoryBalancer) getEvictPods(podList []*v1.Pod, sourceNuma *NumaLatencyInfo) []EvictPod {
	podToBeEvicted := m.getPodHasMaxRssOnSpecifiedNuma(sourceNuma.NumaID, podList)

	result := make([]EvictPod, 0)
	if podToBeEvicted != nil {
		result = m.packEvictPods([]*v1.Pod{podToBeEvicted})
	}

	return result
}

func (m *memoryBalancer) getDestNumaList(orderedDestNumaList []NumaInfo, sourceNuma *NumaLatencyInfo,
	balanceLevel BalanceLevel, bandwidthPressure bool) (poolPodDestNumaList []NumaInfo, reclaimedPodDestNumaList []NumaInfo) {
	// orderedDestNumaList is the destination numa list for non-reclaimed cores pod
	poolPodDestNumaList = m.getNumaListFilteredByLatencyGapRatio(
		orderedDestNumaList, sourceNuma, balanceLevel, bandwidthPressure)
	poolPodDestNumaList = m.getNumaListFilteredPingPongBalance(
		poolPodDestNumaList, sourceNuma)

	reclaimedPodDestNumaList = make([]NumaInfo, 0)
	// get destination pod for reclaimed cores pod in case reclaimed pool has different numa set with non-reclaimed pool
	reclaimedPoolInfo, found := m.metaReader.GetPoolInfo(state.PoolNameReclaim)
	if found {
		reclaimedNumaSet := machine.NewCPUSet(m.getPoolNumaIDs(reclaimedPoolInfo)...)
		for i, item := range poolPodDestNumaList {
			if reclaimedNumaSet.Contains(item.NumaID) {
				reclaimedPodDestNumaList = append(reclaimedPodDestNumaList, poolPodDestNumaList[i])
			}
		}
	}

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

func (m *memoryBalancer) getBalancePodsByRSS(poolPods []*v1.Pod, reclaimedPods []*v1.Pod, srcNuma *NumaLatencyInfo) (targetPoolPods []BalancePod, targetReclaimedPods []BalancePod, totalRSS float64) {
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
				SortValue: anon,
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
				SortValue: anon,
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

	targetPoolPods = make([]BalancePod, 0)
	targetReclaimedPods = make([]BalancePod, 0)

	for _, sortPod := range reclaimedPodSortList {
		if totalRSS+sortPod.SortValue > float64(m.conf.BalancedReclaimedPodsSourceNumaTotalRSSMax) {
			break
		}

		targetReclaimedPods = append(targetReclaimedPods, m.packBalancePods(sortPod.Pod, sortPod.SortValue))
		totalRSS += sortPod.SortValue
	}

	if totalRSS >= float64(m.conf.BalancedReclaimedPodsSingleRoundTotalRssThreshold) {
		return
	}

	for _, sortPod := range poolPodSortList {
		if totalRSS+sortPod.SortValue > float64(m.conf.BalancedPodsSourceNumaTotalRSSMax) {
			break
		}

		targetPoolPods = append(targetPoolPods, m.packBalancePods(sortPod.Pod, sortPod.SortValue))
		totalRSS += sortPod.SortValue
	}

	return
}

func (m *memoryBalancer) packEvictPods(pods []*v1.Pod) []EvictPod {
	result := make([]EvictPod, 0, len(pods))
	for i := range pods {
		result = append(result, EvictPod{Namespace: pods[i].Namespace, Name: pods[i].Name, UID: string(pods[i].UID)})
	}

	return result
}

func (m *memoryBalancer) packBalancePods(pod *v1.Pod, RSSOnSourceNuma float64) BalancePod {
	containers := make([]string, 0, len(pod.Spec.Containers))
	for i := range pod.Spec.Containers {
		containers = append(containers, pod.Spec.Containers[i].Name)
	}

	return BalancePod{Namespace: pod.Namespace,
		Name: pod.Name, UID: string(pod.UID), Containers: containers, RSSOnSourceNuma: RSSOnSourceNuma}
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

	if time.Since(m.lastBalanceInfo.balanceTime).Minutes() > PingPongDetectThresholdInMinutes {
		return false
	}

	for _, poolBalanceInfo := range m.lastBalanceInfo.pool2BalanceInfo {
		if dstNuma != poolBalanceInfo.SourceNuma.NumaID {
			continue
		}

		for i := range poolBalanceInfo.DestNumas {
			if srcNuma == poolBalanceInfo.DestNumas[i].NumaID {
				return true
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
		ExtraEntries: make([]types.ExtraMemoryAdvices, 0),
	}
	m.mutex.Lock()
	defer m.mutex.Unlock()
	if m.balanceInfo == nil {
		return
	}

	needBalance := false
	advice := types.NumaMemoryBalanceAdvice{
		PoolBalanceInfo: make(map[string]types.NumaMemoryBalancePoolAdvice),
	}
	for poolName, poolBalanceInfo := range m.balanceInfo.pool2BalanceInfo {
		if !poolBalanceInfo.NeedBalance || poolBalanceInfo.Status != BalanceStatusPrepareSuccess ||
			(len(poolBalanceInfo.BalancePods) == 0 && len(poolBalanceInfo.BalanceReclaimedPods) == 0) {
			continue
		}

		poolAdvice := types.NumaMemoryBalancePoolAdvice{
			BalanceInfo: make([]types.NumaMemoryMigrationInfo, 0),
			TotalRSS:    poolBalanceInfo.TotalRSS,
			Threshold:   MigratePagesThreshold,
		}

		needBalance = true
		reclaimedDestNumaSet := machine.NewCPUSet()
		for _, reclaimedDestNuma := range poolBalanceInfo.ReclaimedDestNumas {
			reclaimedDestNumaSet.Add(reclaimedDestNuma.NumaID)
		}

		// The dest numa order matters.
		for _, destNuma := range poolBalanceInfo.DestNumas {
			migrationInfo := types.NumaMemoryMigrationInfo{
				DestNuma:          destNuma.NumaID,
				SourceNuma:        poolBalanceInfo.SourceNuma.NumaID,
				MigrateContainers: make([]types.NumaMemoryBalanceContainerInfo, 0),
			}

			if reclaimedDestNumaSet.Contains(destNuma.NumaID) {
				for _, balanceReclaimedPod := range poolBalanceInfo.BalanceReclaimedPods {
					for _, container := range balanceReclaimedPod.Containers {
						migrationInfo.MigrateContainers = append(migrationInfo.MigrateContainers, types.NumaMemoryBalanceContainerInfo{
							PodUID:        balanceReclaimedPod.UID,
							ContainerName: container,
						})
					}
				}
			}

			for _, balancePod := range poolBalanceInfo.BalancePods {
				for _, container := range balancePod.Containers {
					migrationInfo.MigrateContainers = append(migrationInfo.MigrateContainers, types.NumaMemoryBalanceContainerInfo{
						PodUID:        balancePod.UID,
						ContainerName: container,
					})
				}
			}
			poolAdvice.BalanceInfo = append(poolAdvice.BalanceInfo, migrationInfo)
		}

		advice.PoolBalanceInfo[poolName] = poolAdvice
	}

	if len(advice.PoolBalanceInfo) > 0 {
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
	}

	if needBalance {
		m.lastBalanceInfo = m.balanceInfo
	}
	// avoid duplicate execution
	m.balanceInfo = nil

	return
}
