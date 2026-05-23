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
	"k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/advisorsvc"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/memory/dynamicpolicy/memoryadvisor"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/reporter"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/memory"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/agent/utilcomponent/featuregatenegotiation"
	"github.com/kubewharf/katalyst-core/pkg/agent/utilcomponent/featuregatenegotiation/finders"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

const (
	memoryServerName           string = "memory-server"
	durationToWaitAddContainer        = time.Second * 30

	memoryServerLWHealthCheckName = "memory-server-lw"
)

var registerMemoryHealthCheckOnce sync.Once

type memoryServer struct {
	*baseServer
	hasListAndWatchLoop     atomic.Value
	headroomResourceManager reporter.HeadroomResourceManager
}

func NewMemoryServer(
	conf *config.Configuration,
	headroomResourceManager reporter.HeadroomResourceManager,
	metaCache metacache.MetaCache,
	metaServer *metaserver.MetaServer,
	advisor subResourceAdvisor,
	emitter metrics.MetricEmitter,
) (*memoryServer, error) {
	ms := &memoryServer{}
	ms.baseServer = newBaseServer(memoryServerName, conf, metaCache, metaServer, emitter, advisor, ms)
	ms.hasListAndWatchLoop.Store(false)
	ms.advisorSocketPath = conf.MemoryAdvisorSocketAbsPath
	ms.pluginSocketPath = conf.MemoryPluginSocketAbsPath
	ms.headroomResourceManager = headroomResourceManager
	ms.resourceRequestName = "MemoryRequest"
	return ms, nil
}

func (ms *memoryServer) createQRMClient() (advisorsvc.QRMServiceClient, io.Closer, error) {
	if !general.IsPathExists(ms.pluginSocketPath) {
		return nil, nil, fmt.Errorf("memory plugin socket path %s does not exist", ms.pluginSocketPath)
	}
	conn, err := ms.dial(ms.pluginSocketPath, ms.period)
	if err != nil {
		return nil, nil, fmt.Errorf("dial memory plugin socket failed: %w", err)
	}
	return advisorsvc.NewQRMServiceClient(conn), conn, nil
}

func (ms *memoryServer) RegisterAdvisorServer() {
	ms.mutex.Lock()
	defer ms.mutex.Unlock()

	grpcServer := grpc.NewServer()
	advisorsvc.RegisterAdvisorServiceServer(grpcServer, ms)
	ms.grpcServer = grpcServer
}

func (ms *memoryServer) populateMetaCache(memoryPluginClient advisorsvc.QRMServiceClient) error {
	general.Infof("start to populate metaCache")
	resp, err := memoryPluginClient.ListContainers(context.TODO(), &advisorsvc.Empty{})
	if err != nil {
		if !general.IsUnimplementedError(err) {
			return fmt.Errorf("list containers failed: %w", err)
		}

		// If ListContainers RPC method is not implemented, we need to wait for the QRM plugin to call AddContainer to update the metaCache.
		// Actually, this does not guarantee that all the containers will be fully walked through.
		general.Infof("waiting %v for qrm plugin to call AddContainer", durationToWaitAddContainer.String())
		time.Sleep(durationToWaitAddContainer)
	} else {
		for _, container := range resp.Containers {
			if err := ms.addContainer(container); err != nil {
				return fmt.Errorf("add container %s/%s failed: %w", container.PodUid, container.ContainerName, err)
			}
			general.InfoS("add container", "container", container.String())
		}
	}

	return nil
}

func (ms *memoryServer) GetAdvice(ctx context.Context, request *advisorsvc.GetAdviceRequest) (*advisorsvc.GetAdviceResponse, error) {
	// Register health check only when the QRM memory plugins actually calls the sysadvisor GetAdvice or ListAndWatch method
	registerMemoryHealthCheckOnce.Do(func() {
		memory.RegisterMemoryAdvisorHealthCheck()
	})

	startTime := time.Now()
	_ = ms.emitter.StoreInt64(ms.genMetricsName(metricServerGetAdviceCalled), 1, metrics.MetricTypeNameCount)
	general.Infof("get advice request: %v", general.ToString(request))

	if err := ms.updateMetaCacheInput(ctx, request); err != nil {
		general.Errorf("update meta cache failed: %v", err)
		return nil, fmt.Errorf("update meta cache failed: %w", err)
	}

	general.InfoS("updated meta cache input", "duration", time.Since(startTime))

	// generate both sys advisor supported and qrm wanted feature gates
	supportedWantedFeatureGates, err := featuregatenegotiation.GenerateSupportedWantedFeatureGates(request.WantedFeatureGates, finders.FeatureGateTypeMemory)
	if err != nil {
		return nil, err
	}

	general.InfofV(6, "QRM Memory Plugin wanted feature gates: %v, among them sysadvisor supported feature gates: %v", lo.Keys(request.WantedFeatureGates), lo.Keys(supportedWantedFeatureGates))

	result, err := ms.updateAdvisor(ctx, supportedWantedFeatureGates)
	if err != nil {
		general.Errorf("update advisor failed: %v", err)
		return nil, fmt.Errorf("update advisor failed: %w", err)
	}
	resp := &advisorsvc.GetAdviceResponse{
		PodEntries:            result.PodEntries,
		ExtraEntries:          result.ExtraEntries,
		SupportedFeatureGates: supportedWantedFeatureGates,
	}
	general.Infof("get advice response: %v", general.ToString(resp))
	general.InfoS("get advice", "duration", time.Since(startTime))
	return resp, nil
}

func (ms *memoryServer) updateMetaCacheInput(ctx context.Context, request *advisorsvc.GetAdviceRequest) error {
	startTime := time.Now()
	// lock meta cache to prevent race with cpu server
	ms.metaCache.Lock()
	general.InfoS("acquired lock", "duration", time.Since(startTime))
	defer ms.metaCache.Unlock()

	var errs []error
	for podUID, podEntry := range request.Entries {
		for containerName, containerEntry := range podEntry.Entries {
			if err := ms.addContainer(containerEntry); err != nil {
				errs = append(errs, fmt.Errorf("add container %s/%s failed: %w", podUID, containerName, err))
			}
		}
	}

	general.InfoS("added containers", "duration", time.Since(startTime))

	if err := ms.metaCache.RangeAndDeleteContainer(func(container *types.ContainerInfo) bool {
		info, ok := request.Entries[container.PodUID]
		if !ok {
			return true
		}
		if _, ok = info.Entries[container.ContainerName]; !ok {
			return true
		}
		return false
	}, 0); err != nil {
		errs = append(errs, fmt.Errorf("clean up containers failed: %w", err))
	}

	general.InfoS("cleaned up containers", "duration", time.Since(startTime))
	return errors.NewAggregate(errs)
}

func (ms *memoryServer) ListAndWatch(_ *advisorsvc.Empty, server advisorsvc.AdvisorService_ListAndWatchServer) error {
	// Register health check only when the QRM memory plugins actually calls the sysadvisor GetAdvice or ListAndWatch method
	registerMemoryHealthCheckOnce.Do(func() {
		memory.RegisterMemoryAdvisorHealthCheck()
	})
	_ = ms.emitter.StoreInt64(ms.genMetricsName(metricServerLWCalled), int64(ms.period.Seconds()), metrics.MetricTypeNameCount)

	if ms.hasListAndWatchLoop.Swap(true).(bool) {
		klog.Warningf("[qosaware-server-memory] another ListAndWatch loop is running")
		return fmt.Errorf("another ListAndWatch loop is running")
	}
	defer ms.hasListAndWatchLoop.Store(false)

	// list containers to make sure metaCache is populated before memory advisor updates.
	memoryPluginClient, conn, err := ms.createQRMClient()
	if err != nil {
		klog.Errorf("[qosaware-server-memory] create memory plugin client failed: %v", err)
		return fmt.Errorf("create memory plugin client failed: %w", err)
	}
	defer conn.Close()
	if err := ms.populateMetaCache(memoryPluginClient); err != nil {
		klog.Errorf("[qosaware-server-memory] populate metaCache failed: %v", err)
		return fmt.Errorf("populate metaCache failed: %w", err)
	}

	klog.Infof("[qosaware-server-memory] start to push memory advice")
	general.RegisterTemporaryHeartbeatCheck(memoryServerLWHealthCheckName, healthCheckTolerationDuration, general.HealthzCheckStateNotReady, healthCheckTolerationDuration)
	defer general.UnregisterTemporaryHeartbeatCheck(memoryServerLWHealthCheckName)

	timer := time.NewTimer(ms.period)
	defer timer.Stop()

	for {
		select {
		case <-server.Context().Done():
			klog.Infof("[qosaware-server-memory] lw stream server exited")
			return nil
		case <-ms.stopCh:
			klog.Infof("[qosaware-server-memory] lw stopped because %v stopped", ms.name)
			return nil
		case <-timer.C:
			klog.Infof("[qosaware-server-memory] trigger advisor update")
			if err := ms.getAndPushAdvice(server); err != nil {
				klog.Errorf("[qosaware-server-memory] get and push advice failed: %v", err)
				_ = general.UpdateHealthzStateByError(memoryServerLWHealthCheckName, err)
			} else {
				_ = general.UpdateHealthzStateByError(memoryServerLWHealthCheckName, nil)
			}
			timer.Reset(ms.period)
		}
	}
}

type memoryInternalResult struct {
	PodEntries   map[string]*advisorsvc.CalculationEntries
	ExtraEntries []*advisorsvc.CalculationInfo
}

func (ms *memoryServer) updateAdvisor(ctx context.Context, supportedWantedFeatureGates map[string]*advisorsvc.FeatureGate) (*memoryInternalResult, error) {
	advisorRespRaw, err := ms.resourceAdvisor.UpdateAndGetAdvice(ctx)
	if err != nil {
		return nil, fmt.Errorf("get memory advice failed: %w", err)
	}
	advisorResp, ok := advisorRespRaw.(*types.InternalMemoryCalculationResult)
	if !ok {
		return nil, fmt.Errorf("get memory advice failed: invalid type %T", advisorRespRaw)
	}
	klog.Infof("[qosaware-server-memory] get memory advice: %v", general.ToString(advisorResp))

	return ms.assembleResponse(advisorResp), nil
}

func (ms *memoryServer) getAndPushAdvice(server advisorsvc.AdvisorService_ListAndWatchServer) error {
	// old asynchronous communication interface does not support feature gate negotiation. If necessary, upgrade to the synchronization interface.
	emptyMap := map[string]*advisorsvc.FeatureGate{}
	result, err := ms.updateAdvisor(server.Context(), emptyMap)
	if err != nil {
		_ = ms.emitter.StoreInt64(ms.genMetricsName(metricServerAdvisorUpdateFailed), int64(ms.period.Seconds()), metrics.MetricTypeNameCount)
		return fmt.Errorf("update advisor failed: %w", err)
	}

	lwResp := &advisorsvc.ListAndWatchResponse{
		PodEntries:   result.PodEntries,
		ExtraEntries: result.ExtraEntries,
	}
	if err := server.Send(lwResp); err != nil {
		_ = ms.emitter.StoreInt64(ms.genMetricsName(metricServerLWSendResponseFailed), int64(ms.period.Seconds()), metrics.MetricTypeNameCount)
		return fmt.Errorf("send listWatch response failed: %w", err)
	}

	if klog.V(6).Enabled() {
		klog.Infof("[qosaware-server-memory] sent listWatch resp: %v", general.ToString(lwResp))
	}

	_ = ms.emitter.StoreInt64(ms.genMetricsName(metricServerLWSendResponseSucceeded), int64(ms.period.Seconds()), metrics.MetricTypeNameCount)
	return nil
}

// assmble per-numa headroom
func (ms *memoryServer) assembleHeadroom() *advisorsvc.CalculationInfo {
	numaAllocatable, err := ms.headroomResourceManager.GetNumaAllocatable()
	if err != nil {
		general.ErrorS(err, "get numa allocatable failed")
		return nil
	}

	numaHeadroom := make(memoryadvisor.MemoryNUMAHeadroom)
	for numaID, res := range numaAllocatable {
		numaHeadroom[numaID] = res.Value()
	}
	data, err := json.Marshal(numaHeadroom)
	if err != nil {
		general.ErrorS(err, "marshal numa headroom failed")
		return nil
	}

	calculationResult := &advisorsvc.CalculationResult{
		Values: map[string]string{
			string(memoryadvisor.ControlKnobKeyMemoryNUMAHeadroom): string(data),
		},
	}

	return &advisorsvc.CalculationInfo{
		CgroupPath:        "",
		CalculationResult: calculationResult,
	}
}

func (ms *memoryServer) assembleResponse(result *types.InternalMemoryCalculationResult) *memoryInternalResult {
	startTime := time.Now()
	defer func() {
		general.InfoS("finished", "duration", time.Since(startTime))
	}()
	if result == nil {
		return nil
	}

	resp := memoryInternalResult{
		PodEntries:   make(map[string]*advisorsvc.CalculationEntries),
		ExtraEntries: make([]*advisorsvc.CalculationInfo, 0),
	}

	for _, advice := range result.ContainerEntries {
		podEntry, ok := resp.PodEntries[advice.PodUID]
		if !ok {
			podEntry = &advisorsvc.CalculationEntries{
				ContainerEntries: map[string]*advisorsvc.CalculationInfo{},
			}
			resp.PodEntries[advice.PodUID] = podEntry
		}
		calculationInfo, ok := podEntry.ContainerEntries[advice.ContainerName]
		if !ok {
			calculationInfo = &advisorsvc.CalculationInfo{
				CalculationResult: &advisorsvc.CalculationResult{
					Values: make(map[string]string),
				},
			}
			podEntry.ContainerEntries[advice.ContainerName] = calculationInfo
		}
		for k, v := range advice.Values {
			calculationInfo.CalculationResult.Values[k] = v
		}
	}

	for _, advice := range result.ExtraEntries {
		found := false
		for _, entry := range resp.ExtraEntries {
			if advice.CgroupPath == entry.CgroupPath {
				found = true
				for k, v := range advice.Values {
					entry.CalculationResult.Values[k] = v
				}
				break
			}
		}
		if !found {
			calculationInfo := &advisorsvc.CalculationInfo{
				CgroupPath: advice.CgroupPath,
				CalculationResult: &advisorsvc.CalculationResult{
					Values: general.DeepCopyMap(advice.Values),
				},
			}
			resp.ExtraEntries = append(resp.ExtraEntries, calculationInfo)
		}
	}

	extraNumaHeadroom := ms.assembleHeadroom()
	if extraNumaHeadroom != nil {
		resp.ExtraEntries = append(resp.ExtraEntries, extraNumaHeadroom)
	}

	return &resp
}
