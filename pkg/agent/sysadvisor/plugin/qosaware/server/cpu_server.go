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

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/samber/lo"
	"google.golang.org/grpc"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/advisorsvc"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/cpuadvisor"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/reporter"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/cpu"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/agent/utilcomponent/featuregatenegotiation"
	"github.com/kubewharf/katalyst-core/pkg/agent/utilcomponent/featuregatenegotiation/finders"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

const (
	cpuServerName string = "cpu-server"

	cpuServerLWHealthCheckName = "cpu-server-lw"

	DefaultCFSCPUPeriod = 100000
)

var registerCPUAdvisorHealthCheckOnce sync.Once

type cpuServer struct {
	*baseServer
	startTime               time.Time
	hasListAndWatchLoop     atomic.Value
	headroomResourceManager reporter.HeadroomResourceManager
}

func NewCPUServer(
	conf *config.Configuration,
	headroomResourceManager reporter.HeadroomResourceManager,
	metaCache metacache.MetaCache,
	metaServer *metaserver.MetaServer,
	advisor subResourceAdvisor,
	emitter metrics.MetricEmitter,
) (*cpuServer, error) {
	cs := &cpuServer{}
	cs.baseServer = newBaseServer(cpuServerName, conf, metaCache, metaServer, emitter, advisor, cs)
	cs.hasListAndWatchLoop.Store(false)
	cs.startTime = time.Now()
	cs.advisorSocketPath = conf.CPUAdvisorSocketAbsPath
	cs.pluginSocketPath = conf.CPUPluginSocketAbsPath
	cs.headroomResourceManager = headroomResourceManager
	cs.resourceRequestName = "CPURequest"
	return cs, nil
}

func (cs *cpuServer) createQRMClient() (cpuadvisor.CPUPluginClient, io.Closer, error) {
	if !general.IsPathExists(cs.pluginSocketPath) {
		return nil, nil, fmt.Errorf("memory plugin socket path %s does not exist", cs.pluginSocketPath)
	}
	conn, err := cs.dial(cs.pluginSocketPath, cs.period)
	if err != nil {
		return nil, nil, fmt.Errorf("dial memory plugin socket failed: %w", err)
	}
	return cpuadvisor.NewCPUPluginClient(conn), conn, nil
}

func (cs *cpuServer) RegisterAdvisorServer() {
	cs.mutex.Lock()
	defer cs.mutex.Unlock()

	grpcServer := grpc.NewServer()
	cpuadvisor.RegisterCPUAdvisorServer(grpcServer, cs)
	cs.grpcServer = grpcServer
}

func (cs *cpuServer) GetAdvice(ctx context.Context, request *cpuadvisor.GetAdviceRequest) (*cpuadvisor.GetAdviceResponse, error) {
	// Register health check only when the QRM cpu plugins actually calls the sysadvisor GetAdvice or ListAndWatch method
	registerCPUAdvisorHealthCheckOnce.Do(func() {
		cpu.RegisterCPUAdvisorHealthCheck()
	})

	startTime := time.Now()
	_ = cs.emitter.StoreInt64(cs.genMetricsName(metricServerGetAdviceCalled), 1, metrics.MetricTypeNameCount)
	general.Infof("get advice request: %v", general.ToString(request))

	if err := cs.updateMetaCacheInput(ctx, request); err != nil {
		general.Errorf("update meta cache failed: %v", err)
		return nil, fmt.Errorf("update meta cache failed: %w", err)
	}

	general.InfoS("updated meta cache input", "duration", time.Since(startTime))

	// generate both sys advisor supported and qrm wanted feature gates
	supportedWantedFeatureGates, err := featuregatenegotiation.GenerateSupportedWantedFeatureGates(request.WantedFeatureGates, finders.FeatureGateTypeCPU)
	if err != nil {
		return nil, err
	}

	general.InfofV(6, "QRM CPU Plugin wanted feature gates: %v, among them sysadvisor supported feature gates: %v", lo.Keys(request.WantedFeatureGates), lo.Keys(supportedWantedFeatureGates))
	result, err := cs.updateAdvisor(supportedWantedFeatureGates)
	if err != nil {
		general.Errorf("update advisor failed: %v", err)
		return nil, fmt.Errorf("update advisor failed: %w", err)
	}
	resp := &cpuadvisor.GetAdviceResponse{
		Entries:                               result.Entries,
		AllowSharedCoresOverlapReclaimedCores: result.AllowSharedCoresOverlapReclaimedCores,
		ExtraEntries:                          result.ExtraEntries,
		SupportedFeatureGates:                 supportedWantedFeatureGates,
	}
	general.Infof("get advice response: %v", general.ToString(resp))
	general.InfoS("get advice", "duration", time.Since(startTime))
	return resp, nil
}

func (cs *cpuServer) ListAndWatch(_ *advisorsvc.Empty, server cpuadvisor.CPUAdvisor_ListAndWatchServer) error {
	// Register health check only when the QRM cpu plugins actually calls the sysadvisor GetAdvice or ListAndWatch method
	registerCPUAdvisorHealthCheckOnce.Do(func() {
		cpu.RegisterCPUAdvisorHealthCheck()
	})

	_ = cs.emitter.StoreInt64(cs.genMetricsName(metricServerLWCalled), int64(cs.period.Seconds()), metrics.MetricTypeNameCount)

	if cs.hasListAndWatchLoop.Swap(true).(bool) {
		klog.Warningf("[qosaware-server-cpu] another ListAndWatch loop is running")
		return fmt.Errorf("another ListAndWatch loop is running")
	}
	defer cs.hasListAndWatchLoop.Store(false)

	cpuPluginClient, conn, err := cs.createQRMClient()
	if err != nil {
		_ = cs.emitter.StoreInt64(cs.genMetricsName(metricServerLWGetCheckpointFailed), int64(cs.period.Seconds()), metrics.MetricTypeNameCount)
		klog.Errorf("[qosaware-server-cpu] create cpu plugin client failed: %v", err)
		return fmt.Errorf("create cpu plugin client failed: %w", err)
	}
	defer conn.Close()

	klog.Infof("[qosaware-server-cpu] start to push cpu advices")
	general.RegisterTemporaryHeartbeatCheck(cpuServerLWHealthCheckName, healthCheckTolerationDuration, general.HealthzCheckStateNotReady, healthCheckTolerationDuration)
	defer general.UnregisterTemporaryHeartbeatCheck(cpuServerLWHealthCheckName)

	timer := time.NewTimer(cs.period)
	defer timer.Stop()

	for {
		select {
		case <-server.Context().Done():
			klog.Infof("[qosaware-server-cpu] lw stream server exited")
			return nil
		case <-cs.stopCh:
			klog.Infof("[qosaware-server-cpu] lw stopped because cpu server stopped")
			return nil
		case <-timer.C:
			klog.Infof("[qosaware-server-cpu] trigger advisor update")
			if err := cs.getAndPushAdvice(cpuPluginClient, server); err != nil {
				klog.Errorf("[qosaware-server-cpu] get and push advice failed: %v", err)
				_ = general.UpdateHealthzStateByError(cpuServerLWHealthCheckName, err)
			} else {
				_ = general.UpdateHealthzStateByError(cpuServerLWHealthCheckName, nil)
			}
			timer.Reset(cs.period)
		}
	}
}

func (cs *cpuServer) getAndSyncCheckpoint(ctx context.Context, client cpuadvisor.CPUPluginClient) error {
	safeTime := time.Now().UnixNano()

	// get checkpoint
	getCheckpointResp, err := client.GetCheckpoint(ctx, &cpuadvisor.GetCheckpointRequest{})
	if err != nil {
		_ = cs.emitter.StoreInt64(cs.genMetricsName(metricServerLWGetCheckpointFailed), int64(cs.period.Seconds()), metrics.MetricTypeNameCount)
		return fmt.Errorf("get checkpoint failed: %w", err)
	} else if getCheckpointResp == nil {
		_ = cs.emitter.StoreInt64(cs.genMetricsName(metricServerLWGetCheckpointFailed), int64(cs.period.Seconds()), metrics.MetricTypeNameCount)
		return fmt.Errorf("got nil checkpoint")
	}

	if klog.V(6).Enabled() {
		klog.Infof("[qosaware-server-cpu] got checkpoint: %v", general.ToString(getCheckpointResp.Entries))
	}

	_ = cs.emitter.StoreInt64(cs.genMetricsName(metricServerLWGetCheckpointSucceeded), int64(cs.period.Seconds()), metrics.MetricTypeNameCount)

	cs.syncCheckpoint(ctx, getCheckpointResp, safeTime)
	return nil
}

func (cs *cpuServer) shouldTriggerAdvisorUpdate() bool {
	// TODO: do we still need this check?
	// skip pushing advice during startup
	if time.Now().Before(cs.startTime.Add(types.StartUpPeriod)) {
		klog.Infof("[qosaware-cpu] skip pushing advice: starting up")
		return false
	}

	// sanity check: if reserve pool exists
	reservePoolInfo, ok := cs.metaCache.GetPoolInfo(commonstate.PoolNameReserve)
	if !ok || reservePoolInfo == nil {
		klog.Errorf("[qosaware-cpu] skip pushing advice: reserve pool does not exist")
		return false
	}

	return true
}

// Deprecated: getAndPushAdvice implements the legacy asynchronous bidirectional communication model between
// qrm plugins and sys-advisor. This is kept for backward compatibility.
// TODO: remove this function after all qrm plugins are migrated to the new synchronous model
func (cs *cpuServer) getAndPushAdvice(client cpuadvisor.CPUPluginClient, server cpuadvisor.CPUAdvisor_ListAndWatchServer) error {
	if err := cs.getAndSyncCheckpoint(server.Context(), client); err != nil {
		return err
	}

	if !cs.shouldTriggerAdvisorUpdate() {
		return nil
	}

	// old asynchronous communication interface does not support feature gate negotiation. If necessary, upgrade to the synchronization interface.
	emptyMap := map[string]*advisorsvc.FeatureGate{}
	result, err := cs.updateAdvisor(emptyMap)
	if err != nil {
		return err
	}
	lwResp := &cpuadvisor.ListAndWatchResponse{
		Entries:                               result.Entries,
		AllowSharedCoresOverlapReclaimedCores: result.AllowSharedCoresOverlapReclaimedCores,
		ExtraEntries:                          result.ExtraEntries,
	}
	if err := server.Send(lwResp); err != nil {
		_ = cs.emitter.StoreInt64(cs.genMetricsName(metricServerLWSendResponseFailed), int64(cs.period.Seconds()), metrics.MetricTypeNameCount)
		return fmt.Errorf("send listWatch response failed: %w", err)
	}

	if klog.V(6).Enabled() {
		klog.Infof("[qosaware-server-cpu] sent listWatch resp: %v", general.ToString(lwResp))
	}

	_ = cs.emitter.StoreInt64(cs.genMetricsName(metricServerLWSendResponseSucceeded), int64(cs.period.Seconds()), metrics.MetricTypeNameCount)
	return nil
}

func (cs *cpuServer) updateAdvisor(featureGates map[string]*advisorsvc.FeatureGate) (*cpuInternalResult, error) {
	// update feature gates in meta cache
	err := cs.metaCache.SetSupportedWantedFeatureGates(featureGates)
	if err != nil {
		_ = cs.emitter.StoreInt64(cs.genMetricsName(metricServerAdvisorUpdateFailed), int64(cs.period.Seconds()), metrics.MetricTypeNameCount)
		return nil, fmt.Errorf("set feature gates failed: %w", err)
	}

	// trigger advisor update and get latest advice
	advisorRespRaw, err := cs.resourceAdvisor.UpdateAndGetAdvice()
	if err != nil {
		_ = cs.emitter.StoreInt64(cs.genMetricsName(metricServerAdvisorUpdateFailed), int64(cs.period.Seconds()), metrics.MetricTypeNameCount)
		return nil, fmt.Errorf("get advice failed: %w", err)
	}
	advisorResp, ok := advisorRespRaw.(*types.InternalCPUCalculationResult)
	if !ok {
		_ = cs.emitter.StoreInt64(cs.genMetricsName(metricServerAdvisorUpdateFailed), int64(cs.period.Seconds()), metrics.MetricTypeNameCount)
		return nil, fmt.Errorf("get advice failed: invalid type: %T", advisorRespRaw)
	}

	klog.Infof("[qosaware-server-cpu] get advisor update: %+v", general.ToString(advisorResp))

	return cs.assembleResponse(advisorResp), nil
}

type cpuInternalResult struct {
	Entries                               map[string]*cpuadvisor.CalculationEntries
	AllowSharedCoresOverlapReclaimedCores bool
	ExtraEntries                          []*advisorsvc.CalculationInfo
}

func (cs *cpuServer) assembleResponse(advisorResp *types.InternalCPUCalculationResult) *cpuInternalResult {
	startTime := time.Now()
	defer func() {
		general.InfoS("finished", "duration", time.Since(startTime))
	}()
	calculationEntriesMap := make(map[string]*cpuadvisor.CalculationEntries)
	blockID2Blocks := NewBlockSet()

	cs.assemblePoolEntries(advisorResp, calculationEntriesMap, blockID2Blocks)

	// Assemble pod entries
	f := func(podUID string, containerName string, ci *types.ContainerInfo) bool {
		if err := cs.assemblePodEntries(calculationEntriesMap, blockID2Blocks, podUID, ci); err != nil {
			klog.Errorf("[qosaware-server-cpu] assemblePodEntries for pod %s/%s uid %s err: %v", ci.PodNamespace, ci.PodName, ci.PodUID, err)
		}
		return true
	}
	cs.metaCache.RangeContainer(f)

	extraEntries := cs.assembleCgroupConfig(advisorResp)
	extraNumaHeadRoom := cs.assembleHeadroom()
	if extraNumaHeadRoom != nil {
		extraEntries = append(extraEntries, extraNumaHeadRoom)
	}
	// Send result
	resp := &cpuInternalResult{
		Entries:                               calculationEntriesMap,
		ExtraEntries:                          extraEntries,
		AllowSharedCoresOverlapReclaimedCores: advisorResp.AllowSharedCoresOverlapReclaimedCores,
	}

	return resp
}

// assemble cgroup config
func (cs *cpuServer) assembleCgroupConfig(advisorResp *types.InternalCPUCalculationResult) (extraEntries []*advisorsvc.CalculationInfo) {
	for poolName, entries := range advisorResp.PoolEntries {
		if poolName != commonstate.PoolNameReclaim {
			continue
		}

		// range from fakeNUMAID
		for numaID := -1; numaID < cs.metaServer.NumNUMANodes; numaID++ {
			quota := int64(-1)
			cpuResource, ok := entries[numaID]
			if !ok {
				continue
			}
			if cpuResource.Quota > 0 {
				quota = int64(cpuResource.Quota * DefaultCFSCPUPeriod)
			}
			resourceConf := &common.CgroupResources{
				CpuQuota:  quota,
				CpuPeriod: DefaultCFSCPUPeriod,
			}
			bytes, err := json.Marshal(resourceConf)
			if err != nil {
				klog.ErrorS(err, "")
				continue
			}

			extraEntries = append(extraEntries, &advisorsvc.CalculationInfo{
				CgroupPath: common.GetReclaimRelativeRootCgroupPath(cs.reclaimRelativeRootCgroupPath, numaID),
				CalculationResult: &advisorsvc.CalculationResult{
					Values: map[string]string{
						string(cpuadvisor.ControlKnobKeyCgroupConfig): string(bytes),
					},
				},
			})
		}
	}
	return
}

// assemble per-numa headroom
func (cs *cpuServer) assembleHeadroom() *advisorsvc.CalculationInfo {
	numaAllocatable, err := cs.headroomResourceManager.GetNumaAllocatable()
	if err != nil {
		klog.Errorf("get numa allocatable failed: %v", err)
		return nil
	}

	numaHeadroom := make(cpuadvisor.CPUNUMAHeadroom)
	for numaID, res := range numaAllocatable {
		numaHeadroom[numaID] = float64(res.Value()) / 1000.0
	}
	data, err := json.Marshal(numaHeadroom)
	if err != nil {
		klog.Errorf("marshal headroom failed: %v", err)
		return nil
	}

	calculationResult := &advisorsvc.CalculationResult{
		Values: map[string]string{
			string(cpuadvisor.ControlKnobKeyCPUNUMAHeadroom): string(data),
		},
	}

	return &advisorsvc.CalculationInfo{
		CgroupPath:        "",
		CalculationResult: calculationResult,
	}
}

func (cs *cpuServer) updateMetaCacheInput(ctx context.Context, req *cpuadvisor.GetAdviceRequest) error {
	startTime := time.Now()
	// lock meta cache to prevent race with cpu server
	cs.metaCache.Lock()
	general.InfoS("acquired lock", "duration", time.Since(startTime))
	defer cs.metaCache.Unlock()

	var errs []error
	livingPoolNameSet := sets.NewString()

	// update pool entries first, which are needed for updating container entries
	for entryName, entry := range req.Entries {
		poolInfo, ok := entry.Entries[commonstate.FakedContainerName]
		if !ok {
			continue
		}
		poolName := entryName
		livingPoolNameSet.Insert(poolName)
		if err := cs.createOrUpdatePoolInfo(
			poolName,
			poolInfo.AllocationInfo.OwnerPoolName,
			poolInfo.AllocationInfo.TopologyAwareAssignments,
			poolInfo.AllocationInfo.OriginalTopologyAwareAssignments,
		); err != nil {
			errs = append(errs, fmt.Errorf("update pool info failed: %w", err))
		}
	}

	general.InfoS("updated pool entries", "duration", time.Since(startTime))

	// update container entries after pool entries
	for entryName, entry := range req.Entries {
		if _, ok := entry.Entries[commonstate.FakedContainerName]; ok {
			continue
		}
		podUID := entryName
		pod, err := cs.metaServer.GetPod(ctx, podUID)
		if err != nil {
			errs = append(errs, fmt.Errorf("get pod info for %s failed: %w", podUID, err))
			continue
		}

		for containerName, info := range entry.Entries {
			if err := cs.createOrUpdateContainerInfo(podUID, containerName, pod, info); err != nil {
				errs = append(errs, fmt.Errorf("update container info for %s/%s failed: %w", podUID, containerName, err))
				_ = cs.emitter.StoreInt64(
					cs.genMetricsName(metricServerCheckpointUpdateContainerFailed), 1, metrics.MetricTypeNameCount,
					metrics.MetricTag{Key: "podUID", Val: podUID},
					metrics.MetricTag{Key: "containerName", Val: containerName},
				)
			}
		}
	}

	general.InfoS("updated container entries", "duration", time.Since(startTime))

	// clean up containers that no longer exist
	if err := cs.metaCache.RangeAndDeleteContainer(func(containerInfo *types.ContainerInfo) bool {
		info, ok := req.Entries[containerInfo.PodUID]
		if !ok {
			return true
		}
		if _, ok := info.Entries[containerInfo.ContainerName]; !ok {
			return true
		}
		return false
	}, 0, // there is no need for safeTime check
	); err != nil {
		errs = append(errs, fmt.Errorf("clean up containers failed: %w", err))
	}

	general.InfoS("cleaned up container entries", "duration", time.Since(startTime))

	// add all containers' original owner pools to livingPoolNameSet
	cs.metaCache.RangeContainer(func(podUID string, containerName string, containerInfo *types.ContainerInfo) bool {
		livingPoolNameSet.Insert(containerInfo.OriginOwnerPoolName)
		return true
	},
	)

	if err := cs.metaCache.GCPoolEntries(livingPoolNameSet); err != nil {
		errs = append(errs, fmt.Errorf("gc pool entries failed: %w", err))
	}

	general.InfoS("cleaned up pool entries", "duration", time.Since(startTime))
	return errors.NewAggregate(errs)
}

// Deprecated: to be removed after all qrm plugins are migrated to the new synchronous model
func (cs *cpuServer) syncCheckpoint(ctx context.Context, resp *cpuadvisor.GetCheckpointResponse, safeTime int64) {
	livingPoolNameSet := sets.NewString()

	// parse pool entries first, which are needed for parsing container entries
	for entryName, entry := range resp.Entries {
		if poolInfo, ok := entry.Entries[commonstate.FakedContainerName]; ok {
			poolName := entryName
			livingPoolNameSet.Insert(poolName)
			if err := cs.createOrUpdatePoolInfo(
				poolName, poolInfo.OwnerPoolName, poolInfo.TopologyAwareAssignments, poolInfo.OriginalTopologyAwareAssignments,
			); err != nil {
				klog.Errorf("[qosaware-server-cpu] update pool info with error: %v", err)
			}
		}
	}

	// parse container entries after pool entries
	for entryName, entry := range resp.Entries {
		if _, ok := entry.Entries[commonstate.FakedContainerName]; !ok {
			podUID := entryName
			pod, err := cs.metaServer.GetPod(ctx, podUID)
			if err != nil {
				klog.Errorf("[qosaware-server-cpu] get pod info with error: %v", err)
				continue
			}

			for containerName, info := range entry.Entries {
				if err := cs.updateContainerInfo(podUID, containerName, pod, info); err != nil {
					klog.Errorf("[qosaware-server-cpu] update container info with error: %v", err)
					_ = cs.emitter.StoreInt64(cs.genMetricsName(metricServerCheckpointUpdateContainerFailed), 1, metrics.MetricTypeNameCount,
						metrics.MetricTag{Key: "podUID", Val: podUID},
						metrics.MetricTag{Key: "containerName", Val: containerName})
				}
			}
		}
	}

	// clean up the containers not existed in resp.Entries
	_ = cs.metaCache.RangeAndDeleteContainer(func(containerInfo *types.ContainerInfo) bool {
		info, ok := resp.Entries[containerInfo.PodUID]
		if !ok {
			return true
		}
		if _, ok = info.Entries[containerInfo.ContainerName]; !ok {
			return true
		}
		return false
	}, safeTime)

	// complement living containers' original owner pools for pool gc
	// todo: deprecate original owner pool and generate owner pool by realtime container status
	cs.metaCache.RangeContainer(func(podUID string, containerName string, containerInfo *types.ContainerInfo) bool {
		livingPoolNameSet.Insert(containerInfo.OriginOwnerPoolName)
		return true
	})

	// gc pool entries
	_ = cs.metaCache.GCPoolEntries(livingPoolNameSet)
}

// TODO: are poolName and ownerPoolName the same?
func (cs *cpuServer) createOrUpdatePoolInfo(
	poolName string,
	ownerPoolName string,
	topologyAwareAssignments map[uint64]string,
	originalTopologyAwareAssignments map[uint64]string,
) error {
	pi, ok := cs.metaCache.GetPoolInfo(poolName)
	if !ok {
		pi = &types.PoolInfo{
			PoolName: ownerPoolName,
		}
	}
	pi.TopologyAwareAssignments = machine.TransformCPUAssignmentFormat(topologyAwareAssignments)
	pi.OriginalTopologyAwareAssignments = machine.TransformCPUAssignmentFormat(originalTopologyAwareAssignments)

	return cs.metaCache.SetPoolInfo(poolName, pi)
}

// The new update method for container info to replace setContainerInfoBasedOnAllocationInfo
func (cs *cpuServer) setContainerInfoBasedOnContainerAllocationInfo(
	pod *v1.Pod,
	ci *types.ContainerInfo,
	info *cpuadvisor.ContainerAllocationInfo,
) error {
	if err := cs.setContainerInfoBasedOnAllocationInfo(pod, ci, info.AllocationInfo); err != nil {
		return err
	}

	ci.Labels = info.Metadata.Labels
	ci.Annotations = info.Metadata.Annotations

	if info.Metadata.UseMilliQuantity {
		ci.CPURequest = float64(info.Metadata.RequestMilliQuantity) / 1000.0
	} else {
		ci.CPURequest = float64(info.Metadata.RequestQuantity)
	}

	if info.Metadata.QosLevel == consts.PodAnnotationQoSLevelSharedCores &&
		info.Metadata.Annotations[consts.PodAnnotationMemoryEnhancementNumaBinding] == consts.PodAnnotationMemoryEnhancementNumaBindingEnable {
		originOwnerPoolName, err := commonstate.GetSpecifiedNUMABindingPoolName(info.Metadata.QosLevel, info.Metadata.Annotations)
		if err != nil {
			return fmt.Errorf("get specified numa binding pool name failed: %w", err)
		}
		ci.OriginOwnerPoolName = originOwnerPoolName
	} else {
		ci.OriginOwnerPoolName = commonstate.GetSpecifiedPoolName(info.Metadata.QosLevel, info.Metadata.Annotations[consts.PodAnnotationCPUEnhancementCPUSet])
	}

	return nil
}

// Deprecated: to be removed after all qrm plugins are migrated to the new synchronous model
func (cs *cpuServer) setContainerInfoBasedOnAllocationInfo(
	pod *v1.Pod,
	ci *types.ContainerInfo,
	info *cpuadvisor.AllocationInfo,
) error {
	ci.RampUp = info.RampUp
	ci.TopologyAwareAssignments = machine.TransformCPUAssignmentFormat(info.TopologyAwareAssignments)
	ci.OriginalTopologyAwareAssignments = machine.TransformCPUAssignmentFormat(info.OriginalTopologyAwareAssignments)
	ci.OwnerPoolName = info.OwnerPoolName

	// get qos level name according to the qos conf
	qosLevel, err := cs.qosConf.GetQoSLevelForPod(pod)
	if err != nil {
		return fmt.Errorf("get qos level failed: %w", err)
	}
	if ci.QoSLevel != qosLevel {
		general.Infof("qos level of %v/%v has change from %s to %s", ci.PodUID, ci.ContainerName, ci.QoSLevel, qosLevel)
		ci.QoSLevel = qosLevel
	}

	if ci.OriginOwnerPoolName == "" {
		ci.OriginOwnerPoolName = ci.OwnerPoolName
	}

	// fill in topology aware assignment for containers with owner pool
	if info.TopologyAwareAssignments == nil {
		if len(ci.OwnerPoolName) > 0 {
			if poolInfo, ok := cs.metaCache.GetPoolInfo(ci.OwnerPoolName); ok {
				ci.TopologyAwareAssignments = poolInfo.TopologyAwareAssignments.Clone()
			}
		}
	}

	return nil
}

func (cs *cpuServer) createOrUpdateContainerInfo(
	podUID string,
	containerName string,
	pod *v1.Pod,
	info *cpuadvisor.ContainerAllocationInfo,
) error {
	ci, ok := cs.metaCache.GetContainerInfo(podUID, containerName)
	if !ok {
		ci = &types.ContainerInfo{
			PodUID:         podUID,
			PodNamespace:   info.Metadata.PodNamespace,
			PodName:        info.Metadata.PodName,
			ContainerName:  containerName,
			ContainerType:  info.Metadata.ContainerType,
			ContainerIndex: int(info.Metadata.ContainerIndex),
			Labels:         info.Metadata.Labels,
			Annotations:    info.Metadata.Annotations,
			QoSLevel:       info.Metadata.QosLevel,
		}

		if info.Metadata.UseMilliQuantity {
			ci.CPURequest = float64(info.Metadata.RequestMilliQuantity) / 1000.0
		} else {
			ci.CPURequest = float64(info.Metadata.RequestQuantity)
		}

		if err := cs.setContainerInfoBasedOnContainerAllocationInfo(pod, ci, info); err != nil {
			return fmt.Errorf("set container info for new container %v/%v failed: %w", podUID, containerName, err)
		}
		// use AddContainer instead of SetContainer to set the creation time in meta cache (is this necessary?)
		if err := cs.metaCache.AddContainer(podUID, containerName, ci); err != nil {
			return fmt.Errorf("add container %v/%v failed: %w", podUID, containerName, err)
		}
		return nil
	}

	if err := cs.setContainerInfoBasedOnContainerAllocationInfo(pod, ci, info); err != nil {
		return fmt.Errorf("set container info for existing container %v/%v failed: %w", podUID, containerName, err)
	}
	if err := cs.metaCache.SetContainerInfo(podUID, containerName, ci); err != nil {
		return fmt.Errorf("update container info %v/%v failed: %w", podUID, containerName, err)
	}
	return nil
}

func (cs *cpuServer) updateContainerInfo(
	podUID string,
	containerName string,
	pod *v1.Pod,
	info *cpuadvisor.AllocationInfo,
) error {
	ci, ok := cs.metaCache.GetContainerInfo(podUID, containerName)
	if !ok {
		return fmt.Errorf("container %v/%v not exist", podUID, containerName)
	}

	if err := cs.setContainerInfoBasedOnAllocationInfo(pod, ci, info); err != nil {
		return fmt.Errorf("update container info %v/%v failed: %w", podUID, containerName, err)
	}

	// Need to set back because of deep copy
	return cs.metaCache.SetContainerInfo(podUID, containerName, ci)
}

// assemblePoolEntries fills up calculationEntriesMap and blockSet based on cpu.InternalCPUCalculationResult
// - for each [pool, numa] set, there exists a new Block (and corresponding internalBlock)
func (cs *cpuServer) assemblePoolEntries(advisorResp *types.InternalCPUCalculationResult, calculationEntriesMap map[string]*cpuadvisor.CalculationEntries, bs blockSet) {
	for poolName, entries := range advisorResp.PoolEntries {
		// join reclaim pool lastly
		if poolName == commonstate.PoolNameReclaim && advisorResp.AllowSharedCoresOverlapReclaimedCores {
			continue
		}
		poolEntry := NewPoolCalculationEntries(poolName)
		for numaID, cpu := range entries {
			block := NewBlock(uint64(cpu.Size), "")
			numaCalculationResult := &cpuadvisor.NumaCalculationResult{Blocks: []*cpuadvisor.Block{block}}

			innerBlock := NewInnerBlock(block, int64(numaID), poolName, nil, numaCalculationResult)
			innerBlock.join(block.BlockId, bs)

			poolEntry.Entries[commonstate.FakedContainerName].CalculationResultsByNumas[int64(numaID)] = numaCalculationResult
		}
		calculationEntriesMap[poolName] = poolEntry
	}

	if reclaimEntries, ok := advisorResp.PoolEntries[commonstate.PoolNameReclaim]; ok && advisorResp.AllowSharedCoresOverlapReclaimedCores {
		poolEntry := NewPoolCalculationEntries(commonstate.PoolNameReclaim)
		for numaID, reclaimCPU := range reclaimEntries {

			overlapSize := advisorResp.GetPoolOverlapInfo(commonstate.PoolNameReclaim, numaID)
			if len(overlapSize) == 0 {
				// If share pool not exists, join reclaim pool directly
				block := NewBlock(uint64(reclaimCPU.Size), "")
				numaCalculationResult := &cpuadvisor.NumaCalculationResult{Blocks: []*cpuadvisor.Block{block}}

				innerBlock := NewInnerBlock(block, int64(numaID), commonstate.PoolNameReclaim, nil, numaCalculationResult)
				innerBlock.join(block.BlockId, bs)

				poolEntry.Entries[commonstate.FakedContainerName].CalculationResultsByNumas[int64(numaID)] = numaCalculationResult
			} else {
				numaCalculationResult := &cpuadvisor.NumaCalculationResult{Blocks: []*cpuadvisor.Block{}}
				for sharedPoolName, reclaimedSize := range overlapSize {
					block := NewBlock(uint64(reclaimedSize), "")

					sharedPoolCalculationResults, ok := getNumaCalculationResult(calculationEntriesMap, sharedPoolName, commonstate.FakedContainerName, int64(numaID))
					if ok && len(sharedPoolCalculationResults.Blocks) == 1 {
						innerBlock := NewInnerBlock(block, int64(numaID), commonstate.PoolNameReclaim, nil, numaCalculationResult)
						numaCalculationResult.Blocks = append(numaCalculationResult.Blocks, block)
						innerBlock.join(sharedPoolCalculationResults.Blocks[0].BlockId, bs)
					}
					poolEntry.Entries[commonstate.FakedContainerName].CalculationResultsByNumas[int64(numaID)] = numaCalculationResult
				}
			}
		}
		calculationEntriesMap[commonstate.PoolNameReclaim] = poolEntry
	}

	// Since the interrupt pool does not require advisor calculation, it is still necessary to fill the pool to
	// ensure that the original data of the interrupt pool is not overwritten.
	if poolInfo, ok := cs.metaCache.GetPoolInfo(commonstate.PoolNameInterrupt); ok && poolInfo != nil {
		poolEntry := NewPoolCalculationEntries(commonstate.PoolNameInterrupt)
		calculationEntriesMap[commonstate.PoolNameInterrupt] = poolEntry
	} else {
		general.Warningf("cpu server meta cache does not exist interrupt pool")
	}
}

// assemblePoolEntries fills up calculationEntriesMap and blockSet based on types.ContainerInfo
//
// todo this logic should be refined to make sure we will assemble entries from	internalCalculationInfo rather than walking through containerInfo
func (cs *cpuServer) assemblePodEntries(calculationEntriesMap map[string]*cpuadvisor.CalculationEntries,
	bs blockSet, podUID string, ci *types.ContainerInfo,
) error {
	calculationInfo := &cpuadvisor.CalculationInfo{
		OwnerPoolName:             ci.OwnerPoolName,
		CalculationResultsByNumas: nil,
	}

	// if isolation is locking in, pass isolation-region name (equals isolation owner-pool) instead of owner pool
	if ci.Isolated {
		if ci.RegionNames.Len() == 1 && ci.OwnerPoolName != ci.RegionNames.List()[0] {
			calculationInfo.OwnerPoolName = ci.RegionNames.List()[0]
		}
	}
	// if isolation is locking out, pass original owner pool instead of owner pool
	if !ci.Isolated && ci.OwnerPoolName != ci.OriginOwnerPoolName {
		calculationInfo.OwnerPoolName = ci.OriginOwnerPoolName
	}

	if ci.QoSLevel == consts.PodAnnotationQoSLevelSharedCores || ci.QoSLevel == consts.PodAnnotationQoSLevelReclaimedCores {
		if calculationInfo.OwnerPoolName == "" {
			klog.Warningf("container %s/%s pool name is empty", ci.PodUID, ci.ContainerName)
			return nil
		}
		if _, ok := calculationEntriesMap[calculationInfo.OwnerPoolName]; !ok {
			klog.Warningf("container %s/%s refer a non-existed pool: %s", ci.PodUID, ci.ContainerName, ci.OwnerPoolName)
			return nil
		}
	}

	// currently, only pods in "dedicated_nums with numa binding" has topology aware allocations
	if ci.IsDedicatedNumaBinding() {
		calculationResultsByNumas := make(map[int64]*cpuadvisor.NumaCalculationResult)

		for numaID, cpuset := range ci.TopologyAwareAssignments {
			numaCalculationResult := &cpuadvisor.NumaCalculationResult{Blocks: []*cpuadvisor.Block{}}

			// the same podUID appears twice iff there exists multiple containers in one pod;
			// in this case, reuse the same blocks as the last container.
			// i.e. sidecar container will always follow up with the main container.
			if podEntries, ok := calculationEntriesMap[podUID]; ok {
				for _, containerEntry := range podEntries.Entries {
					if result, ok := containerEntry.CalculationResultsByNumas[int64(numaID)]; ok {
						for _, block := range result.Blocks {
							newBlock := NewBlock(block.Result, block.BlockId)
							newInnerBlock := NewInnerBlock(newBlock, int64(numaID), "", ci, numaCalculationResult)
							numaCalculationResult.Blocks = append(numaCalculationResult.Blocks, newBlock)
							newInnerBlock.join(block.BlockId, bs)
						}
						break
					}
				}
			} else {
				// if this podUID appears firstly, we should generate a new Block

				reclaimPoolCalculationResults, ok := getNumaCalculationResult(calculationEntriesMap, commonstate.PoolNameReclaim,
					commonstate.FakedContainerName, int64(numaID))
				if !ok {
					// if no reclaimed pool exists, return the generated Block

					block := NewBlock(uint64(cpuset.Size()), "")
					innerBlock := NewInnerBlock(block, int64(numaID), "", ci, numaCalculationResult)
					numaCalculationResult.Blocks = append(numaCalculationResult.Blocks, block)
					innerBlock.join(block.BlockId, bs)
				} else {
					// if reclaimed pool exists, join the generated Block with Block in reclaimed pool

					for _, block := range reclaimPoolCalculationResults.Blocks {
						// todo assume only one reclaimed block exists in a certain numa
						if block.OverlapTargets == nil || len(block.OverlapTargets) == 0 {
							newBlock := NewBlock(uint64(cpuset.Size()), "")
							innerBlock := NewInnerBlock(newBlock, int64(numaID), "", ci, numaCalculationResult)
							numaCalculationResult.Blocks = append(numaCalculationResult.Blocks, newBlock)
							innerBlock.join(block.BlockId, bs)
						}
					}
				}
			}

			calculationResultsByNumas[int64(numaID)] = numaCalculationResult
		}

		calculationInfo.CalculationResultsByNumas = calculationResultsByNumas
	}

	calculationEntries, ok := calculationEntriesMap[podUID]
	if !ok {
		calculationEntriesMap[podUID] = &cpuadvisor.CalculationEntries{
			Entries: make(map[string]*cpuadvisor.CalculationInfo),
		}
		calculationEntries = calculationEntriesMap[podUID]
	}
	calculationEntries.Entries[ci.ContainerName] = calculationInfo

	return nil
}
