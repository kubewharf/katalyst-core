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
	"fmt"
	"time"

	"google.golang.org/grpc"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/advisorsvc"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

const (
	memoryServerName string = "memory-server"
)

type memoryServer struct {
	*baseServer
}

func NewMemoryServer(recvCh chan types.InternalMemoryCalculationResult, sendCh chan struct{}, conf *config.Configuration,
	metaCache metacache.MetaCache, emitter metrics.MetricEmitter) (*memoryServer, error) {
	ms := &memoryServer{}
	ms.baseServer = newBaseServer(memoryServerName, conf, recvCh, sendCh, metaCache, emitter, ms)
	ms.advisorSocketPath = conf.MemoryAdvisorSocketAbsPath
	ms.resourceRequestName = "MemoryRequest"
	return ms, nil
}

func (ms *memoryServer) RegisterAdvisorServer() {
	grpcServer := grpc.NewServer()
	advisorsvc.RegisterAdvisorServiceServer(grpcServer, ms)
	ms.grpcServer = grpcServer
}

func (ms *memoryServer) ListAndWatch(_ *advisorsvc.Empty, server advisorsvc.AdvisorService_ListAndWatchServer) error {
	_ = ms.emitter.StoreInt64(ms.genMetricsName(metricServerLWCalled), int64(ms.period.Seconds()), metrics.MetricTypeNameCount)

	recvCh, ok := ms.recvCh.(chan types.InternalMemoryCalculationResult)
	if !ok {
		return fmt.Errorf("recvCh convert failed")
	}

	for {
		select {
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
					return err
				}

				klog.Infof("[qosaware-server-memory] send calculation result: %v", general.ToString(resp))
				_ = ms.emitter.StoreInt64(ms.genMetricsName(metricServerLWSendResponseSucceeded), int64(ms.period.Seconds()), metrics.MetricTypeNameCount)
			}
		}
	}
}

func (ms *memoryServer) assembleResponse(result *types.InternalMemoryCalculationResult) *advisorsvc.ListAndWatchResponse {
	resp := advisorsvc.ListAndWatchResponse{
		PodEntries:   make(map[string]*advisorsvc.CalculationEntries),
		ExtraEntries: make([]*advisorsvc.CalculationInfo, 0),
	}
	if result == nil {
		return nil
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

	return &resp
}
