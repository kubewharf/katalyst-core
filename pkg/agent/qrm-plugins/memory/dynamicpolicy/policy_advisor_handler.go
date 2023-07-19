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

package dynamicpolicy

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"google.golang.org/grpc/status"
	v1 "k8s.io/api/core/v1"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/advisorsvc"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/memory/dynamicpolicy/memoryadvisor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/memory/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/asyncworker"
	cgroupcommon "github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	cgroupmgr "github.com/kubewharf/katalyst-core/pkg/util/cgroup/manager"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	"github.com/kubewharf/katalyst-core/pkg/util/process"
)

// initAdvisorClientConn initializes memory-advisor related connections
func (p *DynamicPolicy) initAdvisorClientConn() (err error) {
	memoryAdvisorConn, err := process.Dial(p.memoryAdvisorSocketAbsPath, 5*time.Second)
	if err != nil {
		err = fmt.Errorf("get memory advisor connection with socket: %s failed with error: %v", p.memoryAdvisorSocketAbsPath, err)
		return
	}

	p.advisorClient = advisorsvc.NewAdvisorServiceClient(memoryAdvisorConn)
	p.advisorConn = memoryAdvisorConn
	return nil
}

// lwMemoryAdvisorServer works as a client to connect with memory-advisor.
// it will wait to receive allocations from memory-advisor, and perform allocate actions
func (p *DynamicPolicy) lwMemoryAdvisorServer(stopCh <-chan struct{}) error {
	general.Infof("called")

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-stopCh
		general.Infof("received stop signal, stop calling ListAndWatch of MemoryAdvisorServer")
		cancel()
	}()

	stream, err := p.advisorClient.ListAndWatch(ctx, &advisorsvc.Empty{})
	if err != nil {
		return fmt.Errorf("call ListAndWatch of MemoryAdvisorServer failed with error: %v", err)
	}

	for {
		resp, err := stream.Recv()
		if err != nil {
			_ = p.emitter.StoreInt64(util.MetricNameLWMemoryAdvisorServerFailed, 1, metrics.MetricTypeNameRaw)
			return fmt.Errorf("receive ListAndWatch response of MemoryAdvisorServer failed with error: %v, grpc code: %v",
				err, status.Code(err))
		}

		err = p.handleAdvisorResp(resp)
		if err != nil {
			general.Errorf("handle ListAndWatch response of MemoryAdvisorServer failed with error: %v", err)
		}
	}
}

func (p *DynamicPolicy) handleAdvisorResp(advisorResp *advisorsvc.ListAndWatchResponse) (retErr error) {
	if advisorResp == nil {
		return fmt.Errorf("handleAdvisorResp got nil advisorResp")
	}

	general.Infof("called")
	_ = p.emitter.StoreInt64(util.MetricNameHandleAdvisorRespCalled, 1, metrics.MetricTypeNameRaw)
	p.Lock()
	defer func() {
		p.Unlock()
		if retErr != nil {
			_ = p.emitter.StoreInt64(util.MetricNameHandleAdvisorRespFailed, 1, metrics.MetricTypeNameRaw)
		}
	}()

	podResourceEntries := p.state.GetPodResourceEntries()

	handlers := memoryadvisor.GetRegisteredControlKnobHandlers()

	for entryName, entry := range advisorResp.PodEntries {
		if entry == nil {
			general.Warningf("entryName: %s has nil entry", entryName)
			continue
		}
		for subEntryName, calculationInfo := range entry.ContainerEntries {
			if calculationInfo == nil {
				general.Warningf("entryName: %s, subEntryName: %s has nil calculationInfo", entryName, subEntryName)
				continue
			} else if calculationInfo.CalculationResult == nil {
				general.Warningf("entryName: %s, subEntryName: %s has nil calculationInfo.CalculationResult", entryName, subEntryName)
				continue
			}

			for controlKnobName, controlKnobValue := range calculationInfo.CalculationResult.Values {
				general.InfoS("try to handle control knob",
					"entryName", entryName,
					"subEntryName", subEntryName,
					"controlKnobName", controlKnobName,
					"controlKnobValue", controlKnobValue)
				handler := handlers[memoryadvisor.MemoryControlKnobName(controlKnobName)]
				if handler != nil {
					err := handler(entryName, subEntryName, calculationInfo, podResourceEntries)

					if err != nil {
						general.ErrorS(err, "handle control knob failed",
							"entryName", entryName,
							"subEntryName", subEntryName,
							"controlKnobName", controlKnobName,
							"controlKnobValue", controlKnobValue)
					} else {
						general.ErrorS(err, "handle control knob successfully",
							"entryName", entryName,
							"subEntryName", subEntryName,
							"controlKnobName", controlKnobName,
							"controlKnobValue", controlKnobValue)
					}
				}
			}
		}
	}

	for _, calculationInfo := range advisorResp.ExtraEntries {
		if calculationInfo == nil {
			general.Warningf("advisorResp.ExtraEntries has nil calculationInfo")
			continue
		} else if calculationInfo.CgroupPath == "" {
			general.Warningf("advisorResp.ExtraEntries calculationInfo has empty CgroupPath")
			continue
		} else if calculationInfo.CalculationResult == nil {
			general.Warningf("advisorResp.ExtraEntry with CgroupPath: %s has nil CalculationResult", calculationInfo.CgroupPath)
			continue
		}

		for controlKnobName, controlKnobValue := range calculationInfo.CalculationResult.Values {
			general.InfoS("try to handle control knob",
				"cgroupPath", calculationInfo.CgroupPath,
				"controlKnobName", controlKnobName,
				"controlKnobValue", controlKnobValue)
			handler := handlers[memoryadvisor.MemoryControlKnobName(controlKnobName)]
			if handler != nil {
				err := handler("", "", calculationInfo, podResourceEntries)

				if err != nil {
					general.ErrorS(err, "handle control knob failed",
						"cgroupPath", calculationInfo.CgroupPath,
						"controlKnobName", controlKnobName,
						"controlKnobValue", controlKnobValue)
				} else {
					general.InfoS("handle control knob successfully",
						"cgroupPath", calculationInfo.CgroupPath,
						"controlKnobName", controlKnobName,
						"controlKnobValue", controlKnobValue)
				}
			}
		}
	}

	resourcesMachineState, err := state.GenerateMachineStateFromPodEntries(p.state.GetMachineInfo(), podResourceEntries, p.state.GetReservedMemory())
	if err != nil {
		return fmt.Errorf("calculate machineState by updated pod entries failed with error: %v", err)
	}

	p.state.SetPodResourceEntries(podResourceEntries)
	p.state.SetMachineState(resourcesMachineState)

	return nil
}

func (p *DynamicPolicy) handleAdvisorMemoryLimitInBytes(entryName, subEntryName string,
	calculationInfo *advisorsvc.CalculationInfo, podResourceEntries state.PodResourceEntries) error {

	calculatedLimitInBytes := calculationInfo.CalculationResult.Values[string(memoryadvisor.ControlKnobKeyMemoryLimitInBytes)]
	calculatedLimitInBytesInt64, err := strconv.ParseInt(calculatedLimitInBytes, 10, 64)

	if err != nil {
		return fmt.Errorf("parse %s: %s failed with error: %v", memoryadvisor.ControlKnobKeyMemoryLimitInBytes, calculatedLimitInBytes, err)
	}

	if calculationInfo.CgroupPath != "" {
		err = cgroupmgr.ApplyMemoryWithRelativePath(calculationInfo.CgroupPath, &cgroupcommon.MemoryData{
			LimitInBytes: calculatedLimitInBytesInt64,
		})

		if err != nil {
			return fmt.Errorf("apply %s: %d to cgroup: %s failed with error: %v",
				memoryadvisor.ControlKnobKeyMemoryLimitInBytes, calculatedLimitInBytesInt64,
				calculationInfo.CgroupPath, err)
		}

		return nil
	}

	allocationInfo := podResourceEntries[v1.ResourceMemory][entryName][subEntryName]

	if allocationInfo == nil {
		return fmt.Errorf("high level cgroup path and pod resource entry are both nil")
	} else if allocationInfo.ExtraControlKnobInfo == nil {
		allocationInfo.ExtraControlKnobInfo = make(map[string]commonstate.ControlKnobInfo)
	}

	allocationInfo.
		ExtraControlKnobInfo[string(memoryadvisor.ControlKnobKeyMemoryLimitInBytes)] = commonstate.ControlKnobInfo{
		ControlKnobValue: calculatedLimitInBytes,
		OciPropertyName:  util.OCIPropertyNameMemoryLimitInBytes,
	}

	return nil
}

func (p *DynamicPolicy) handleAdvisorDropCache(entryName, subEntryName string,
	calculationInfo *advisorsvc.CalculationInfo, podResourceEntries state.PodResourceEntries) error {

	dropCache := calculationInfo.CalculationResult.Values[string(memoryadvisor.ControlKnobKeyDropCache)]
	dropCacheBool, err := strconv.ParseBool(dropCache)
	if err != nil {
		return fmt.Errorf("parse %s: %s failed with error: %v", memoryadvisor.ControlKnobKeyDropCache, dropCache, err)
	}

	if calculationInfo.CgroupPath != "" {
		return fmt.Errorf("dropping cache at high level cgroup path %s isn't supported",
			memoryadvisor.ControlKnobKeyDropCache)
	}

	if !dropCacheBool {
		return nil
	}

	containerID, err := p.metaServer.GetContainerID(entryName, subEntryName)
	if err != nil {
		return fmt.Errorf("get container id of pod: %s container: %s failed with error: %v", entryName, subEntryName, err)
	}

	dropCacheWorkName := util.GetContainerAsyncWorkName(entryName, subEntryName,
		memoryPluginAsyncWorkTopicDropCache)
	// start a asynchronous work to drop cache for the container whose numaset changed and doesn't require numa_binding
	err = p.asyncWorkers.AddWork(dropCacheWorkName,
		&asyncworker.Work{
			Fn:          cgroupmgr.DropCacheWithTimeoutForContainer,
			Params:      []interface{}{entryName, containerID, dropCacheTimeoutSeconds},
			DeliveredAt: time.Now()})

	if err != nil {
		return fmt.Errorf("add work: %s pod: %s container: %s failed with error: %v", dropCacheWorkName, entryName, subEntryName, err)
	}

	return nil
}

func (p *DynamicPolicy) handleAdvisorCPUSetMems(entryName, subEntryName string,
	calculationInfo *advisorsvc.CalculationInfo, podResourceEntries state.PodResourceEntries) error {

	cpusetMemsStr := calculationInfo.CalculationResult.Values[string(memoryadvisor.ControlKnobKeyCPUSetMems)]
	cpusetMems, err := machine.Parse(cpusetMemsStr)
	if err != nil {
		return fmt.Errorf("parse %s: %s failed with error: %v", memoryadvisor.ControlKnobKeyCPUSetMems, cpusetMemsStr, err)
	}

	allocationInfo := podResourceEntries[v1.ResourceMemory][entryName][subEntryName]

	if calculationInfo.CgroupPath != "" {
		return fmt.Errorf("setting high level cgroup path cpuset.mems isn't supported")
	} else if allocationInfo.QoSLevel != apiconsts.PodAnnotationQoSLevelReclaimedCores {
		// cpuset.mems for numa_binding dedicated_cores isn't changed now
		// cpuset.mems for shared_cores is auto-adjusted in memory plugin
		return fmt.Errorf("setting cpuset.mems for container not with qosLevel: %s isn't supported",
			apiconsts.PodAnnotationQoSLevelReclaimedCores)
	}

	allocationInfo.NumaAllocationResult = cpusetMems
	allocationInfo.TopologyAwareAllocations = nil
	allocationInfo.AggregatedQuantity = 0

	return nil
}
