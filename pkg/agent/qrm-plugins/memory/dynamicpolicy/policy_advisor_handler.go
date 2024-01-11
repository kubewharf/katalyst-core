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
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path"
	"strconv"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/status"
	v1 "k8s.io/api/core/v1"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"
	maputil "k8s.io/kubernetes/pkg/util/maps"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/advisorsvc"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/memory/dynamicpolicy/memoryadvisor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/memory/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util"
	"github.com/kubewharf/katalyst-core/pkg/config"
	dynamicconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/asyncworker"
	cgroupcommon "github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	cgroupmgr "github.com/kubewharf/katalyst-core/pkg/util/cgroup/manager"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	"github.com/kubewharf/katalyst-core/pkg/util/process"
)

const (
	memoryAdvisorLWRecvTimeMonitorName              = "memoryAdvisorLWRecvTimeMonitor"
	memoryAdvisorLWRecvTimeMonitorDurationThreshold = 30 * time.Second
	memoryAdvisorLWRecvTimeMonitorInterval          = 30 * time.Second
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

		if p.lwRecvTimeMonitor != nil {
			p.lwRecvTimeMonitor.UpdateRefreshTime()
		}

		if err != nil {
			_ = p.emitter.StoreInt64(util.MetricNameLWAdvisorServerFailed, 1, metrics.MetricTypeNameRaw)
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
					err := handler(nil, nil, nil,
						p.emitter, p.metaServer,
						entryName, subEntryName, calculationInfo, podResourceEntries)

					if err != nil {
						general.ErrorS(err, "handle control knob failed",
							"entryName", entryName,
							"subEntryName", subEntryName,
							"controlKnobName", controlKnobName,
							"controlKnobValue", controlKnobValue)

						_ = p.emitter.StoreInt64(util.MetricNameMemoryHandleAdvisorContainerEntryFailed, 1,
							metrics.MetricTypeNameRaw, metrics.ConvertMapToTags(map[string]string{
								"entryName":    entryName,
								"subEntryName": subEntryName,
							})...)
					} else {
						general.InfoS("handle control knob successfully",
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
				err := handler(nil, nil, nil,
					p.emitter, p.metaServer,
					"", "", calculationInfo, podResourceEntries)

				if err != nil {
					general.ErrorS(err, "handle control knob failed",
						"cgroupPath", calculationInfo.CgroupPath,
						"controlKnobName", controlKnobName,
						"controlKnobValue", controlKnobValue)

					_ = p.emitter.StoreInt64(util.MetricNameMemoryHandleAdvisorExtraEntryFailed, 1,
						metrics.MetricTypeNameRaw, metrics.ConvertMapToTags(map[string]string{
							"cgroupPath": calculationInfo.CgroupPath,
						})...)
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

func handleAdvisorMemoryLimitInBytes(
	_ *config.Configuration,
	_ interface{},
	_ *dynamicconfig.DynamicAgentConfiguration,
	emitter metrics.MetricEmitter,
	metaServer *metaserver.MetaServer,
	entryName, subEntryName string,
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

		_ = emitter.StoreInt64(util.MetricNameMemoryHandleAdvisorMemoryLimit, calculatedLimitInBytesInt64,
			metrics.MetricTypeNameRaw, metrics.ConvertMapToTags(map[string]string{
				"cgroupPath": calculationInfo.CgroupPath,
			})...)
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

	_ = emitter.StoreInt64(util.MetricNameMemoryHandleAdvisorMemoryLimit, calculatedLimitInBytesInt64,
		metrics.MetricTypeNameRaw, metrics.ConvertMapToTags(map[string]string{
			"entryName":    entryName,
			"subEntryName": subEntryName,
		})...)

	return nil
}

func (p *DynamicPolicy) handleAdvisorDropCache(
	_ *config.Configuration,
	_ interface{},
	_ *dynamicconfig.DynamicAgentConfiguration,
	emitter metrics.MetricEmitter,
	metaServer *metaserver.MetaServer,
	entryName, subEntryName string,
	calculationInfo *advisorsvc.CalculationInfo, podResourceEntries state.PodResourceEntries) error {

	dropCache := calculationInfo.CalculationResult.Values[string(memoryadvisor.ControlKnobKeyDropCache)]
	dropCacheBool, err := strconv.ParseBool(dropCache)
	if err != nil {
		return fmt.Errorf("parse %s: %s failed with error: %v", memoryadvisor.ControlKnobKeyDropCache, dropCache, err)
	} else if calculationInfo.CgroupPath != "" {
		return fmt.Errorf("dropping cache at high level cgroup path %s isn't supported",
			memoryadvisor.ControlKnobKeyDropCache)
	} else if !dropCacheBool {
		return nil
	}

	containerID, err := metaServer.GetContainerID(entryName, subEntryName)
	if err != nil {
		return fmt.Errorf("get container id of pod: %s container: %s failed with error: %v", entryName, subEntryName, err)
	}

	dropCacheWorkName := util.GetContainerAsyncWorkName(entryName, subEntryName, memoryPluginAsyncWorkTopicDropCache)
	// start a asynchronous work to drop cache for the container whose numaset changed and doesn't require numa_binding
	err = p.asyncWorkers.AddWork(dropCacheWorkName,
		&asyncworker.Work{
			Fn:          cgroupmgr.DropCacheWithTimeoutForContainer,
			Params:      []interface{}{entryName, containerID, dropCacheTimeoutSeconds},
			DeliveredAt: time.Now()})

	if err != nil {
		return fmt.Errorf("add work: %s pod: %s container: %s failed with error: %v", dropCacheWorkName, entryName, subEntryName, err)
	}

	_ = emitter.StoreInt64(util.MetricNameMemoryHandleAdvisorDropCache, 1,
		metrics.MetricTypeNameRaw, metrics.ConvertMapToTags(map[string]string{
			"entryName":    entryName,
			"subEntryName": subEntryName,
		})...)

	return nil
}

// pushMemoryAdvisor pushes state info to memory-advisor
func (p *DynamicPolicy) pushMemoryAdvisor() error {
	podEntries := p.state.GetPodResourceEntries()[v1.ResourceMemory]
	for _, entries := range podEntries {
		for _, allocationInfo := range entries {
			if allocationInfo == nil {
				continue
			}

			containerType, found := pluginapi.ContainerType_value[allocationInfo.ContainerType]
			if !found {
				return fmt.Errorf("sync pod: %s/%s, container: %s to memory advisor failed with error: containerType: %s not found",
					allocationInfo.PodNamespace, allocationInfo.PodName,
					allocationInfo.ContainerName, allocationInfo.ContainerType)
			}

			_, err := p.advisorClient.AddContainer(context.Background(), &advisorsvc.ContainerMetadata{
				PodUid:         allocationInfo.PodUid,
				PodNamespace:   allocationInfo.PodNamespace,
				PodName:        allocationInfo.PodName,
				ContainerName:  allocationInfo.ContainerName,
				ContainerType:  pluginapi.ContainerType(containerType),
				ContainerIndex: allocationInfo.ContainerIndex,
				Labels:         maputil.CopySS(allocationInfo.Labels),
				Annotations:    maputil.CopySS(allocationInfo.Annotations),
				QosLevel:       allocationInfo.QoSLevel,
			})
			if err != nil {
				return fmt.Errorf("sync pod: %s/%s, container: %s to memory advisor failed with error: %v",
					allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName, err)
			}
		}
	}

	return nil
}

func handleAdvisorCPUSetMems(
	_ *config.Configuration,
	_ interface{},
	_ *dynamicconfig.DynamicAgentConfiguration,
	emitter metrics.MetricEmitter,
	metaServer *metaserver.MetaServer,
	entryName, subEntryName string,
	calculationInfo *advisorsvc.CalculationInfo, podResourceEntries state.PodResourceEntries) error {

	cpusetMemsStr := calculationInfo.CalculationResult.Values[string(memoryadvisor.ControlKnobKeyCPUSetMems)]
	cpusetMems, err := machine.Parse(cpusetMemsStr)
	if err != nil {
		return fmt.Errorf("parse %s: %s failed with error: %v", memoryadvisor.ControlKnobKeyCPUSetMems, cpusetMemsStr, err)
	}

	allocationInfo := podResourceEntries[v1.ResourceMemory][entryName][subEntryName]

	if calculationInfo.CgroupPath != "" {
		return fmt.Errorf("setting high level cgroup path cpuset.mems isn't supported")
	} else if allocationInfo == nil {
		return fmt.Errorf("setting cpuset.mems for nil allocationInfo: %s/%s", entryName, subEntryName)
	} else if allocationInfo.QoSLevel != apiconsts.PodAnnotationQoSLevelReclaimedCores {
		// cpuset.mems for numa_binding dedicated_cores isn't changed now
		// cpuset.mems for shared_cores is auto-adjusted in memory plugin
		return fmt.Errorf("setting cpuset.mems for container not with qosLevel: %s isn't supported",
			apiconsts.PodAnnotationQoSLevelReclaimedCores)
	}

	allocationInfo.NumaAllocationResult = cpusetMems
	allocationInfo.TopologyAwareAllocations = nil
	allocationInfo.AggregatedQuantity = 0

	_ = emitter.StoreInt64(util.MetricNameMemoryHandleAdvisorCPUSetMems, 1,
		metrics.MetricTypeNameRaw, metrics.ConvertMapToTags(map[string]string{
			"entryName":    entryName,
			"subEntryName": subEntryName,
		})...)

	return nil
}

// serveForAdvisor starts a server for memory-advisor (as a client) to connect with
func (p *DynamicPolicy) serveForAdvisor(stopCh <-chan struct{}) {
	memoryPluginSocketDir := path.Dir(p.memoryPluginSocketAbsPath)

	err := general.EnsureDirectory(memoryPluginSocketDir)
	if err != nil {
		general.Errorf("ensure memoryPluginSocketDir: %s failed with error: %v", memoryPluginSocketDir, err)
		return
	}

	general.Infof("ensure memoryPluginSocketDir: %s successfully", memoryPluginSocketDir)
	if err := os.Remove(p.memoryPluginSocketAbsPath); err != nil && !os.IsNotExist(err) {
		general.Errorf("failed to remove %s: %v", p.memoryPluginSocketAbsPath, err)
		return
	}

	sock, err := net.Listen("unix", p.memoryPluginSocketAbsPath)
	if err != nil {
		general.Errorf("listen at socket: %s failed with err: %v", p.memoryPluginSocketAbsPath, err)
		return
	}
	general.Infof("listen at: %s successfully", p.memoryPluginSocketAbsPath)

	grpcServer := grpc.NewServer()
	advisorsvc.RegisterQRMServiceServer(grpcServer, p)

	exitCh := make(chan struct{})
	go func() {
		general.Infof("starting memory plugin checkpoint grpc server at socket: %s", p.memoryPluginSocketAbsPath)
		if err := grpcServer.Serve(sock); err != nil {
			general.Errorf("memory plugin checkpoint grpc server crashed with error: %v at socket: %s", err, p.memoryPluginSocketAbsPath)
		} else {
			general.Infof("memory plugin checkpoint grpc server at socket: %s exists normally", p.memoryPluginSocketAbsPath)
		}

		exitCh <- struct{}{}
	}()

	if conn, err := process.Dial(p.memoryPluginSocketAbsPath, 5*time.Second); err != nil {
		grpcServer.Stop()
		general.Errorf("dial check at socket: %s failed with err: %v", p.memoryPluginSocketAbsPath, err)
	} else {
		_ = conn.Close()
	}

	select {
	case <-exitCh:
		return
	case <-stopCh:
		grpcServer.Stop()
		return
	}
}

func (p *DynamicPolicy) ListContainers(context.Context, *advisorsvc.Empty) (*advisorsvc.ListContainersResponse, error) {
	general.Infof("called")

	resp := &advisorsvc.ListContainersResponse{}

	podEntries := p.state.GetPodResourceEntries()[v1.ResourceMemory]
	for _, entries := range podEntries {
		for _, allocationInfo := range entries {
			if allocationInfo == nil {
				continue
			}

			containerType, found := pluginapi.ContainerType_value[allocationInfo.ContainerType]
			if !found {
				return nil, fmt.Errorf("sync pod: %s/%s, container: %s to memory advisor failed with error: containerType: %s not found",
					allocationInfo.PodNamespace, allocationInfo.PodName,
					allocationInfo.ContainerName, allocationInfo.ContainerType)
			}

			resp.Containers = append(resp.Containers, &advisorsvc.ContainerMetadata{
				PodUid:         allocationInfo.PodUid,
				PodNamespace:   allocationInfo.PodNamespace,
				PodName:        allocationInfo.PodName,
				ContainerName:  allocationInfo.ContainerName,
				ContainerType:  pluginapi.ContainerType(containerType),
				ContainerIndex: allocationInfo.ContainerIndex,
				Labels:         maputil.CopySS(allocationInfo.Labels),
				Annotations:    maputil.CopySS(allocationInfo.Annotations),
				QosLevel:       allocationInfo.QoSLevel,
			})
		}
	}

	return resp, nil
}

// handleAdvisorMemoryProvisions handles memory provisions from memory-advisor
func (p *DynamicPolicy) handleAdvisorMemoryProvisions(_ *config.Configuration,
	_ interface{},
	_ *dynamicconfig.DynamicAgentConfiguration,
	emitter metrics.MetricEmitter,
	metaServer *metaserver.MetaServer,
	entryName, subEntryName string,
	calculationInfo *advisorsvc.CalculationInfo, podResourceEntries state.PodResourceEntries) error {
	// unmarshal calculationInfo
	memoryProvisions := &machine.MemoryDetails{}
	value := calculationInfo.CalculationResult.Values[string(memoryadvisor.ControlKnobReclaimedMemorySize)]
	err := json.Unmarshal([]byte(value), memoryProvisions)

	if err != nil {
		return fmt.Errorf("unmarshal %s: %s failed with error: %v",
			memoryadvisor.ControlKnobReclaimedMemorySize, value, err)
	}
	// Todo: another logic to handle memory provisions
	general.Infof("qrm: memoryProvisions: %v", memoryProvisions)
	return nil
}
