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
	"time"

	"google.golang.org/grpc"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/retry"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/advisorsvc"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/memory/dynamicpolicy/memoryadvisor"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/reporter"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

const (
	memoryServerName                 string = "memory-server"
	durationToWaitAddContainer              = time.Second * 30
	durationToWaitListAndWatchCalled        = time.Second * 5

	memoryServerHealthCheckName = "memory-server-lw"
)

type memoryServer struct {
	*baseServer
	memoryPluginClient      advisorsvc.QRMServiceClient
	listAndWatchCalled      bool
	headroomResourceManager reporter.HeadroomResourceManager
}

func NewMemoryServer(recvCh chan types.InternalMemoryCalculationResult, sendCh chan types.TriggerInfo, conf *config.Configuration,
	headroomResourceManager reporter.HeadroomResourceManager, metaCache metacache.MetaCache, metaServer *metaserver.MetaServer, emitter metrics.MetricEmitter,
) (*memoryServer, error) {
	ms := &memoryServer{}
	ms.baseServer = newBaseServer(memoryServerName, conf, recvCh, sendCh, metaCache, metaServer, emitter, ms)
	ms.advisorSocketPath = conf.MemoryAdvisorSocketAbsPath
	ms.pluginSocketPath = conf.MemoryPluginSocketAbsPath
	ms.headroomResourceManager = headroomResourceManager
	ms.resourceRequestName = "MemoryRequest"
	return ms, nil
}

func (ms *memoryServer) RegisterAdvisorServer() {
	ms.mutex.Lock()
	defer ms.mutex.Unlock()

	grpcServer := grpc.NewServer()
	advisorsvc.RegisterAdvisorServiceServer(grpcServer, ms)
	ms.grpcServer = grpcServer
}

// Start is override to list containers when starting up
func (ms *memoryServer) Start() error {
	if err := ms.baseServer.Start(); err != nil {
		return err
	}

	// list containers to make sure metaCache is populated before memory advisor updating.
	// For CPU advisor, sanity checks will be performed before updating, such as checking the existence of the reserve pool.
	// TODO: list containers and pool entries before updating cpu advisor, same as this approach here.
	if err := ms.StartListContainers(); err != nil {
		return err
	}

	return nil
}

func (ms *memoryServer) StartListContainers() error {
	conn, err := ms.dial(ms.pluginSocketPath, ms.period)
	if err != nil {
		klog.ErrorS(err, "dial memory plugin failed", "memory plugin socket path", ms.pluginSocketPath)
		goto unimplementedError
	}

	ms.memoryPluginClient = advisorsvc.NewQRMServiceClient(conn)

	err = retry.OnError(retry.DefaultRetry, func(err error) bool {
		return !general.IsUnimplementedError(err)
	}, func() error {
		if err := ms.listContainers(); err != nil {
			_ = ms.metaCache.ClearContainers()
			return err
		}
		return nil
	})
	if err == nil {
		return nil
	}

unimplementedError:
	go func() {
		wait.PollUntil(durationToWaitListAndWatchCalled, func() (done bool, err error) { return ms.listAndWatchCalled, nil }, ms.stopCh)

		// Is listContainer RPC is not implemented, we need to wait for the QRM to call addContainer to update the metaCache.
		// Actually, this does not guarantee that all the containers will be fully walked through.
		general.Infof("wait %v to get add container query", durationToWaitAddContainer.String())
		time.Sleep(durationToWaitAddContainer)
		ms.sendCh <- types.TriggerInfo{TimeStamp: time.Now()}
	}()
	return nil
}

func (ms *memoryServer) listContainers() error {
	resp, err := ms.memoryPluginClient.ListContainers(context.TODO(), &advisorsvc.Empty{})
	if err != nil {
		return err
	}
	for _, container := range resp.Containers {
		if err := ms.addContainer(container); err != nil {
			general.ErrorS(err, "add container failed", "podUID", container.PodUid, "containerName", container.ContainerName)
			return err
		}
		general.InfoS("add container", "container", container.String())
	}
	go func() {
		ms.sendCh <- types.TriggerInfo{TimeStamp: time.Now()}
	}()
	return nil
}

func (ms *memoryServer) ListAndWatch(_ *advisorsvc.Empty, server advisorsvc.AdvisorService_ListAndWatchServer) error {
	_ = ms.emitter.StoreInt64(ms.genMetricsName(metricServerLWCalled), int64(ms.period.Seconds()), metrics.MetricTypeNameCount)
	general.RegisterTemporaryHeartbeatCheck(memoryServerHealthCheckName, healthCheckTolerationDuration, general.HealthzCheckStateNotReady, healthCheckTolerationDuration)
	defer general.UnregisterTemporaryHeartbeatCheck(memoryServerHealthCheckName)

	recvCh, ok := ms.recvCh.(chan types.InternalMemoryCalculationResult)
	if !ok {
		return fmt.Errorf("recvCh convert failed")
	}
	ms.listAndWatchCalled = true

	maxDropLength := len(recvCh)
	klog.Infof("[qosaware-server-memory] drop all old memory advices in channel (max: %d)", maxDropLength)
	for i := 0; i < maxDropLength; i++ {
		select {
		case <-recvCh:
		default:
			klog.Infof("[qosaware-server-memory] all old memory advice in channel is dropped (max: %d)", i)
			break
		}
	}

	klog.Infof("[qosaware-server-memory] start to push memory advice")
	for {
		select {
		case <-server.Context().Done():
			klog.Infof("[qosaware-server-memory] lw stream server exited")
			return nil
		case <-ms.stopCh:
			klog.Infof("[qosaware-server-memory] lw stopped because %v stopped", ms.name)
			return nil
		case advisorResp, more := <-recvCh:
			if !more {
				klog.Infof("[qosaware-server-memory] %v recv channel is closed", ms.name)
				return nil
			}
			if advisorResp.TimeStamp.Add(ms.period * 2).Before(time.Now()) {
				klog.Warningf("[qosaware-server-memory] advisorResp is expired")
				continue
			}
			resp := ms.assembleResponse(&advisorResp)
			if resp != nil {
				if err := server.Send(resp); err != nil {
					klog.Warningf("[qosaware-server-memory] send response failed: %v", err)
					_ = ms.emitter.StoreInt64(ms.genMetricsName(metricServerLWSendResponseFailed), int64(ms.period.Seconds()), metrics.MetricTypeNameCount)
					_ = general.UpdateHealthzStateByError(memoryServerHealthCheckName, err)
					return err
				}

				klog.Infof("[qosaware-server-memory] send calculation result: %v", general.ToString(resp))
				_ = general.UpdateHealthzStateByError(memoryServerHealthCheckName, nil)
				_ = ms.emitter.StoreInt64(ms.genMetricsName(metricServerLWSendResponseSucceeded), int64(ms.period.Seconds()), metrics.MetricTypeNameCount)
			}
		}
	}
}

func (ms *memoryServer) assembleResponse(result *types.InternalMemoryCalculationResult) *advisorsvc.ListAndWatchResponse {
	if result == nil {
		return nil
	}

	// assmble per-numa headroom
	numaAllocatable, err := ms.headroomResourceManager.GetNumaAllocatable()
	if err != nil {
		general.ErrorS(err, "get numa allocatable failed")
		return nil
	}

	numaHeadroom := make(map[int]float64)
	for numaID, res := range numaAllocatable {
		numaHeadroom[numaID] = float64(res.Value())
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
	extraNumaHeadRoom := &advisorsvc.CalculationInfo{
		CgroupPath:        "",
		CalculationResult: calculationResult,
	}

	resp := advisorsvc.ListAndWatchResponse{
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

	resp.ExtraEntries = append(resp.ExtraEntries, extraNumaHeadRoom)
	return &resp
}
