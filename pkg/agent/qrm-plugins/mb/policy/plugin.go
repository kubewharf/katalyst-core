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

package policy

import (
	"context"
	"fmt"
	"sync"
	"time"

	"golang.org/x/exp/maps"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-api/pkg/plugins/skeleton"

	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/agent"
	cpustate "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/advisor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/advisor/priority"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/allocator"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/domain"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/monitor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/plan"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/reader"
	"github.com/kubewharf/katalyst-core/pkg/config"
	metrictypes "github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/types"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	metricspool "github.com/kubewharf/katalyst-core/pkg/metrics/metrics-pool"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

const (
	name       = "qrm_mb_plugin_generic_policy"
	metricName = name
	interval   = time.Second * 1

	emitInterval30s = time.Second * 30

	metricMBMLoadSuppressed = "mbm_load_suppressed"
	metricMBMCCDSuppressed  = "mbm_ccd_suppressed"
	metricMBMHealthStatus   = "mbm_health_status"
)

type MBPlugin struct {
	sync.Mutex
	started bool

	chStop  chan struct{}
	emitter metrics.MetricEmitter

	ccdToDomain map[int]int
	xDomGroups  sets.String
	domains     domain.Domains

	reader        reader.MBReader
	advisor       advisor.Advisor
	planAllocator allocator.PlanAllocator

	metricFetcher metrictypes.MetricsFetcher
	conf          *config.Configuration
	agentCtx      *agent.GenericContext
	machineInfo   *machine.KatalystMachineInfo
	podFetcher    pod.PodFetcher
}

func (m *MBPlugin) Name() string {
	return name
}

func (m *MBPlugin) Start() (err error) {
	m.Lock()
	defer func() {
		if err == nil {
			m.started = true
		}
		m.Unlock()
	}()

	if m.started {
		return
	}

	general.Infof("mbm: plugin started")

	// todo: consider option not to reset resctrl FS on start to avoid hiccup between deployment updates
	general.Infof("mbm: to reset resctrl FS on start")
	ccds := sets.NewInt(maps.Keys(m.ccdToDomain)...)
	if m.chStop == nil {
		m.chStop = make(chan struct{})
	}
	if err = m.planAllocator.Reset(context.Background(), ccds); err != nil {
		general.Errorf("mbm: reset resctrl FS on start failed: %v", err)
		go wait.Until(m.emitResctrlResetFailure, emitInterval30s, m.chStop)
		return nil
	}

	if m.conf.ResetResctrlOnly {
		general.Infof("mbm: not intended to manage mem bandwidth; to end immediately after resctrl state reset")
		return nil
	}

	if !cache.WaitForCacheSync(m.chStop, m.metricFetcher.HasSynced) {
		general.Errorf("mbm: wait for metric fetcher cache sync failed")
		return fmt.Errorf("mbm plugin failed to wait for metric fetcher cache sync")
	}

	// determine mb capacities of qos groups after metric fetch has been fully synced
	groupCapacities, defaultMBDomainCapacity, err := initGroupCapacities(m.metricFetcher,
		m.agentCtx.KatalystMachineInfo.ExtraTopologyInfo.SiblingNumaAvgMBWCapacityMap,
		m.agentCtx.KatalystMachineInfo.ExtraTopologyInfo.SiblingNumaInfo,
		m.conf.MBQRMPluginConfig.DomainGroupAwareCapacityPCT,
	)
	if err != nil {
		general.Errorf("mbm: failed to determine group capacities: %v; not to enable mbm", err)
		return nil
	}

	// ensure the extra resctrl groups registered with their priorities
	for group, weight := range m.conf.ExtraGroupPriorities {
		general.Infof("mbm: registering extra group %s, priority %d", group, weight)
		priority.GetInstance().AddWeight(group, weight)
	}

	// initializing advisor field is deferred as qos group mb capacities is known now
	general.Infof("mbm: create advior able to treat multiple resctrl groups with identical priority")
	m.advisor = advisor.New(m.emitter, m.domains,
		m.conf.MinCCDMB, m.conf.MaxCCDMB,
		defaultMBDomainCapacity, m.conf.MBCapLimitPercent,
		m.conf.CrossDomainGroups, m.conf.MBQRMPluginConfig.NoThrottleGroups,
		groupCapacities,
	)

	if len(m.conf.CCDCapGroups) > 0 && m.conf.CCDCapKp > 0 {
		m.advisor = advisor.NewPControllerAdvisor(
			m.conf.CCDCapKp,
			m.conf.MinCCDMB,
			m.conf.MaxCCDMB,
			m.conf.CCDCapGroups,
			m.advisor,
		)
		general.Infof("[mbm] P-Controller advisor enabled with groups=%v, Kp=%.2f", m.conf.CCDCapGroups, m.conf.CCDCapKp)
	}

	go func() {
		wait.Until(m.run, interval, m.chStop)

		// todo: consider option not to reset resctrl FS on exit to avoid hiccup between deployment updates
		general.Infof("mbm: plugin timer stopped; to reset resctrl FS on cleanup")
		if err := m.planAllocator.Reset(context.Background(), ccds); err != nil {
			general.Errorf("mbm: reset resctrl FS on stop failed: %v", err)
		}
	}()

	go wait.Until(m.emitSuppressionMetrics, emitInterval30s, m.chStop)

	return nil
}

func (m *MBPlugin) Stop() error {
	m.Lock()
	defer func() {
		m.started = false
		m.Unlock()
	}()

	// plugin.Stop may be called before plugin.Start or multiple times,
	// we should ensure cancel function exist
	if !m.started {
		return nil
	}

	general.Infof("mbm: plugin stopped")
	if m.chStop != nil {
		close(m.chStop)
		m.chStop = nil
	}
	return nil
}

func getGroupMonStat(mbData *reader.MBData) monitor.GroupMBStats {
	if mbData == nil {
		return nil
	}

	return mbData.MBBody
}

func (m *MBPlugin) run() {
	general.InfofV(6, "[mbm] plugin run start")

	mbData, err := m.reader.GetMBData()
	if err != nil {
		general.Errorf("[mbm] failed to get mb data: %v", err)
		return
	}
	if mbData == nil {
		general.Warningf("[mbm] got empty mb data")
		return
	}

	// Keep physical CLOS IDs unchanged throughout the MB pipeline. "shared-50"
	// is the default shared CLOS, while "share-50" is an explicit subgroup; a
	// global prefix conversion would merge two independently managed groups.

	if klog.V(6).Enabled() {
		general.Infof("[mbm] [reader] resctrl reported grouped ccd mb stat: %#v", mbData.MBBody)
	}

	statOutgoing := getGroupMonStat(mbData)

	monData, err := monitor.NewDomainStats(statOutgoing, m.ccdToDomain, m.xDomGroups)
	if err != nil {
		general.Errorf("[mbm] failed to run fetching mb stats: %v", err)
		return
	}
	if klog.V(6).Enabled() {
		general.Infof("[mbm] [mon] domain specific group ccd mb stat: %s", monData)
	}

	ctx := context.Background()
	var mbPlan *plan.MBPlan
	mbPlan, err = m.advisor.GetPlan(ctx, monData)
	if err != nil {
		general.Errorf("[mbm] failed to run getting plan: %v", err)
		return
	}
	if klog.V(6).Enabled() {
		general.Infof("[mbm] mb plan update: %s", mbPlan)
	}

	if err := m.planAllocator.Allocate(ctx, mbPlan); err != nil {
		general.Errorf("[mbm] failed to run allocating plan: %v", err)
		return
	}

	general.InfofV(6, "[mbm] plugin run end")
}

func (m *MBPlugin) buildCCDToPodsMap() map[int]map[string]*corev1.Pod {
	cpuState, err := cpustate.GetReadonlyState()
	if err != nil {
		general.Warningf("[mbm] cpu readonly state unavailable: %v", err)
		return nil
	}

	podEntries := cpuState.GetPodEntries()

	if klog.V(6).Enabled() {
		general.Infof("[mbm] cpuState.GetPodEntries(): %v", podEntries)
	}

	return buildCCDToPodsMapFromState(
		podEntries,
		m.machineInfo.CPUDetails,
		m.podFetcher.GetPod,
	)
}

type getPodFunc func(ctx context.Context, podUID string) (*corev1.Pod, error)

func buildCCDToPodsMapFromState(podEntries cpustate.PodEntries, cpuTopology machine.CPUDetails, getPod getPodFunc) map[int]map[string]*corev1.Pod {
	ccdToPods := make(map[int]map[string]*corev1.Pod)
	podIndexer := make(map[string]*corev1.Pod)
	for podUID, containerEntries := range podEntries {
		if containerEntries == nil || containerEntries.IsPoolEntry() {
			continue
		}

		for _, allocationInfo := range containerEntries {
			if allocationInfo == nil || !allocationInfo.CheckMainContainer() {
				continue
			}
			cpus := allocationInfo.AllocationResult.ToSliceInt()

			podInfo, ok := podIndexer[podUID]
			if !ok {
				var err error
				podInfo, err = getPod(context.Background(), podUID)
				if err != nil || podInfo == nil {
					continue
				}
				podIndexer[podUID] = podInfo
			}

			for _, cpu := range cpus {
				ccdID := cpuTopology[cpu].L3CacheID
				if ccdToPods[ccdID] == nil {
					ccdToPods[ccdID] = make(map[string]*corev1.Pod)
				}
				ccdToPods[ccdID][podUID] = podInfo
			}
		}
	}
	return ccdToPods
}

func (m *MBPlugin) emitSuppressedCCDsMetrics(suppressedCCDs []advisor.SuppressedCCD) {
	for _, ccd := range suppressedCCDs {
		tags := map[string]string{
			"domain": fmt.Sprintf("%d", ccd.DomID),
			"group":  ccd.Group,
			"ccd":    fmt.Sprintf("%d", ccd.CCDID),
			"type":   ccd.SuppressionType,
		}
		_ = m.emitter.StoreInt64(metricMBMCCDSuppressed, 1, metrics.MetricTypeNameRaw, metrics.ConvertMapToTags(tags)...)
	}
}

func (m *MBPlugin) emitSuppressedPodsMetrics(suppressedCCDs []advisor.SuppressedCCD, ccdToPods map[int]map[string]*corev1.Pod) {
	// reuse GenericEvictionConfiguration.PodMetricLabels as MB plugin does not have its own
	podMetricLabels := m.conf.GenericEvictionConfiguration.PodMetricLabels
	seen := make(map[string]struct{})
	for _, s := range suppressedCCDs {
		pods, ok := ccdToPods[s.CCDID]
		if !ok {
			continue
		}
		for _, p := range pods {
			key := string(p.UID) + "/" + s.SuppressionType
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}

			emitPodSuppressed(m.emitter, p.Namespace, p.Name, s.SuppressionType, p.Labels, podMetricLabels)
		}
	}
}

func (m *MBPlugin) emitSuppressionMetrics() {
	if klog.V(6).Enabled() {
		general.Infof("[mbm] metrics: start emitting suppression metrics")
	}

	suppressedCCDs := m.advisor.GetSuppressedCCDs()
	if klog.V(6).Enabled() {
		general.Infof("[mbm] metrics: suppressed ccds: %v", suppressedCCDs)
	}
	if len(suppressedCCDs) == 0 {
		return
	}

	m.emitSuppressedCCDsMetrics(suppressedCCDs)

	ccdToPods := m.buildCCDToPodsMap()
	if klog.V(6).Enabled() {
		general.Infof("[mbm] metrics: ccds to pod mappings: %v", ccdToPods)
	}
	if ccdToPods == nil {
		return
	}

	m.emitSuppressedPodsMetrics(suppressedCCDs, ccdToPods)
	if klog.V(6).Enabled() {
		general.Infof("[mbm] metrics: done emitting suppression metrics")
	}
}

func (m *MBPlugin) emitResctrlResetFailure() {
	_ = m.emitter.StoreInt64(metricMBMHealthStatus, 1, metrics.MetricTypeNameRaw,
		metrics.MetricTag{Key: "healthy", Val: "false"},
		metrics.MetricTag{Key: "component", Val: "resctrl"},
		metrics.MetricTag{Key: "reason", Val: "reset_failure"},
	)
}

func emitPodSuppressed(emitter metrics.MetricEmitter, namespace, podName, suppressionType string, labels map[string]string, podMetricLabels sets.String) {
	tags := []metrics.MetricTag{
		{Key: "suppressed_namespace", Val: namespace},
		{Key: "suppressed_pod", Val: podName},
		{Key: "suppression_type", Val: suppressionType},
	}
	for _, label := range podMetricLabels.List() {
		value, ok := labels[label]
		if ok {
			tags = append(tags, metrics.MetricTag{Key: label, Val: value})
		}
	}
	_ = emitter.StoreInt64(metricMBMLoadSuppressed, 1, metrics.MetricTypeNameRaw, tags...)
}

func newMBPlugin(agentCtx *agent.GenericContext, conf *config.Configuration,
	domains domain.Domains, metricFetcher metrictypes.MetricsFetcher,
	planAllocator allocator.PlanAllocator, emitPool metricspool.MetricsEmitterPool,
) skeleton.GenericPlugin {
	ccdMappings := domains.GetCCDMapping()
	general.Infof("[mbm] initialization: ccd-to-domain mapping %v", ccdMappings)
	emitter := emitPool.GetDefaultMetricsEmitter().WithTags(metricName)

	// note: advisor not initialed yet until later at runtime
	return &MBPlugin{
		emitter:       emitter,
		ccdToDomain:   ccdMappings,
		xDomGroups:    sets.NewString(conf.CrossDomainGroups...),
		domains:       domains,
		reader:        reader.New(metricFetcher),
		planAllocator: planAllocator,
		metricFetcher: metricFetcher,
		conf:          conf,
		agentCtx:      agentCtx,
		machineInfo:   agentCtx.KatalystMachineInfo,
		podFetcher:    agentCtx.MetaServer.PodFetcher,
	}
}
