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
	"k8s.io/apimachinery/pkg/util/uuid"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"
	maputil "k8s.io/kubernetes/pkg/util/maps"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/advisorsvc"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	memconsts "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/memory/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/memory/dynamicpolicy/memoryadvisor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/memory/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	dynamicconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/asyncworker"
	"github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	cgroupcommon "github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	cgroupmgr "github.com/kubewharf/katalyst-core/pkg/util/cgroup/manager"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	"github.com/kubewharf/katalyst-core/pkg/util/process"
)

const (
	memoryAdvisorHealthMonitorName     = "memoryAdvisorHealthMonitor"
	memoryAdvisorUnhealthyThreshold    = 5 * time.Minute
	memoryAdvisorHealthyThreshold      = 2 * time.Minute
	memoryAdvisorHealthyCount          = 2
	memoryAdvisorHealthMonitorInterval = 30 * time.Second
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
			_ = p.emitter.StoreInt64(util.MetricNameLWAdvisorServerFailed, 1, metrics.MetricTypeNameRaw)
			return fmt.Errorf("receive ListAndWatch response of MemoryAdvisorServer failed with error: %v, grpc code: %v",
				err, status.Code(err))
		}

		err = p.handleAdvisorResp(resp)
		if err != nil {
			general.Errorf("handle ListAndWatch response of MemoryAdvisorServer failed with error: %v", err)
		}
		_ = general.UpdateHealthzStateByError(memconsts.CommunicateWithAdvisor, err)

		if p.advisorMonitor != nil && err == nil {
			p.advisorMonitor.UpdateRefreshTime()
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

func (p *DynamicPolicy) handleAdvisorMemoryLimitInBytes(
	_ *config.Configuration,
	_ interface{},
	_ *dynamicconfig.DynamicAgentConfiguration,
	emitter metrics.MetricEmitter,
	metaServer *metaserver.MetaServer,
	entryName, subEntryName string,
	calculationInfo *advisorsvc.CalculationInfo, podResourceEntries state.PodResourceEntries,
) error {
	calculatedLimitInBytes := calculationInfo.CalculationResult.Values[string(memoryadvisor.ControlKnobKeyMemoryLimitInBytes)]
	calculatedLimitInBytesInt64, err := strconv.ParseInt(calculatedLimitInBytes, 10, 64)
	if err != nil {
		return fmt.Errorf("parse %s: %s failed with error: %v", memoryadvisor.ControlKnobKeyMemoryLimitInBytes, calculatedLimitInBytes, err)
	}

	if calculationInfo.CgroupPath != "" {
		setExtraCGMemLimitWorkName := util.GetAsyncWorkNameByPrefix(calculationInfo.CgroupPath, memoryPluginAsyncWorkTopicSetExtraCGMemLimit)
		err = p.asyncWorkers.AddWork(setExtraCGMemLimitWorkName,
			&asyncworker.Work{
				Fn:          cgroupmgr.SetExtraCGMemLimitWithTimeoutAndRelCGPath,
				Params:      []interface{}{calculationInfo.CgroupPath, setExtraCGMemLimitTimeoutSeconds, calculatedLimitInBytesInt64},
				DeliveredAt: time.Now(),
			}, asyncworker.DuplicateWorkPolicyDiscard)
		if err != nil {
			return fmt.Errorf("add work: %s pod: %s container: %s failed with error: %v",
				setExtraCGMemLimitWorkName, entryName, subEntryName, err)
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
	calculationInfo *advisorsvc.CalculationInfo, podResourceEntries state.PodResourceEntries,
) error {
	var err error
	defer func() {
		_ = general.UpdateHealthzStateByError(memconsts.DropCache, err)
	}()

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

	container, err := p.metaServer.GetContainerSpec(entryName, subEntryName)
	if err != nil || container == nil {
		return fmt.Errorf("get container spec for pod: %s, container: %s failed with error: %v", entryName, subEntryName, err)
	}

	dropCacheWorkName := util.GetContainerAsyncWorkName(entryName, subEntryName, memoryPluginAsyncWorkTopicDropCache)
	// start a asynchronous work to drop cache for the container whose numaset changed and doesn't require numa_binding
	err = p.asyncWorkers.AddWork(dropCacheWorkName,
		&asyncworker.Work{
			Fn:          cgroupmgr.DropCacheWithTimeoutForContainer,
			Params:      []interface{}{entryName, containerID, dropCacheTimeoutSeconds, GetFullyDropCacheBytes(container)},
			DeliveredAt: time.Now(),
		}, asyncworker.DuplicateWorkPolicyOverride)
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
	calculationInfo *advisorsvc.CalculationInfo, podResourceEntries state.PodResourceEntries,
) error {
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
	} else if !allocationInfo.CheckReclaimed() {
		// cpuset.mems for numa_binding dedicated_cores isn't changed now
		// cpuset.mems for shared_cores is auto-adjusted in memory plugin
		return fmt.Errorf("setting cpuset.mems for container not with qosLevel: %s isn't supported",
			apiconsts.PodAnnotationQoSLevelReclaimedCores)
	}

	allocationInfo.NumaAllocationResult = cpusetMems
	allocationInfo.TopologyAwareAllocations = nil

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
	calculationInfo *advisorsvc.CalculationInfo, podResourceEntries state.PodResourceEntries,
) error {
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

func (p *DynamicPolicy) handleNumaMemoryBalance(_ *config.Configuration,
	_ interface{},
	_ *dynamicconfig.DynamicAgentConfiguration,
	emitter metrics.MetricEmitter,
	metaServer *metaserver.MetaServer,
	entryName, subEntryName string,
	calculationInfo *advisorsvc.CalculationInfo, podResourceEntries state.PodResourceEntries,
) error {
	advice := &types.NumaMemoryBalanceAdvice{}
	value := calculationInfo.CalculationResult.Values[string(memoryadvisor.ControlKnobKeyBalanceNumaMemory)]
	err := json.Unmarshal([]byte(value), advice)
	if err != nil {
		return fmt.Errorf("unmarshal %s: %s failed with error: %v",
			memoryadvisor.ControlKnobKeyBalanceNumaMemory, value, err)
	}

	migratePagesWorkName := fmt.Sprintf("%v", memoryadvisor.ControlKnobKeyBalanceNumaMemory)

	// start an asynchronous work to migrate pages for containers
	err = p.asyncWorkers.AddWork(migratePagesWorkName,
		&asyncworker.Work{
			Fn:          p.doNumaMemoryBalance,
			Params:      []interface{}{*advice},
			DeliveredAt: time.Now(),
		}, asyncworker.DuplicateWorkPolicyDiscard)
	if err != nil {
		general.Errorf("add work: %s failed with error: %v", migratePagesWorkName, err)
	}

	_ = emitter.StoreInt64(util.MetricNameMemoryNumaBalance, 1, metrics.MetricTypeNameRaw)

	return nil
}

type containerMigrateStat struct {
	ContainerID string
	CgroupPath  string
	RssBefore   uint64
	RssAfter    uint64
}

func (p *DynamicPolicy) doNumaMemoryBalance(ctx context.Context, advice types.NumaMemoryBalanceAdvice) error {
	startTime := time.Now()
	defer func() {
		cost := time.Now().Sub(startTime)
		general.Infof("numa memory balance cost %v", cost)
	}()

	keyFunc := func(podUID, containerName string) string {
		return fmt.Sprintf("%s-%s", podUID, containerName)
	}

	getAnonOnNumaFunc := func(absCGPath string, numaID int) (uint64, error) {
		numaMemory, err := cgroupmgr.GetManager().GetNumaMemory(absCGPath)
		numaMemoryJson, _ := json.Marshal(numaMemory)
		general.Infof("get cgroup %v numa stat:%+v", absCGPath, string(numaMemoryJson))
		if err != nil {
			return 0, err
		}

		if _, ok := numaMemory[numaID]; !ok {
			return 0, fmt.Errorf("can't find numa memory for numa ID %v", numaID)
		}

		return numaMemory[numaID].Anon, nil
	}

	containerStats := make(map[string]*containerMigrateStat)
	for _, containerInfo := range advice.MigrateContainers {
		containerID, err := p.metaServer.GetContainerID(containerInfo.PodUID, containerInfo.ContainerName)
		if err != nil {
			general.Errorf("get container id of pod: %s container: %s failed with error: %v", containerInfo.PodUID, containerInfo.ContainerName, err)
			continue
		}

		memoryAbsCGPath, err := cgroupcommon.GetContainerAbsCgroupPath(cgroupcommon.CgroupSubsysMemory, containerInfo.PodUID, containerID)
		if err != nil {
			general.Errorf("GetContainerAbsCgroupPath of pod: %s container: %s failed with error: %v", containerInfo.PodUID, containerInfo.ContainerName, err)
			continue
		}

		key := keyFunc(containerInfo.PodUID, containerInfo.ContainerName)
		containerStats[key] = &containerMigrateStat{
			ContainerID: containerID,
			CgroupPath:  memoryAbsCGPath,
		}
	}

	var migrateSuccess bool
	var rssDecreased uint64
	for _, destNuma := range advice.DestNumaList {
		for _, containerInfo := range advice.MigrateContainers {
			containerKey := keyFunc(containerInfo.PodUID, containerInfo.ContainerName)
			if _, ok := containerStats[containerKey]; !ok {
				continue
			}

			stats := containerStats[containerKey]
			rssBefore, err := getAnonOnNumaFunc(stats.CgroupPath, advice.SourceNuma)
			if err != nil {
				general.Errorf("getAnonOnNumaFunc failed for container[%v/%v] numa [%v],err: %v",
					containerInfo.PodUID, containerInfo.ContainerName, advice.SourceNuma, err)
			}
			stats.RssBefore = rssBefore

			containerNumaSet := machine.NewCPUSet(containerInfo.DestNumaList...)
			if containerNumaSet.Contains(destNuma) {
				err = MigratePagesForContainer(ctx, containerInfo.PodUID, stats.ContainerID, p.topology.NumNUMANodes,
					machine.NewCPUSet(advice.SourceNuma), machine.NewCPUSet(destNuma))
				if err != nil {
					general.Errorf("MigratePagesForContainer failed for container[%v/%v] source_numa [%v],dest_numa [%v],err: %v",
						containerInfo.PodUID, containerInfo.ContainerName, advice.SourceNuma, destNuma, err)
				}
			} else {
				general.Infof("skip migrate container %v/%v memory from %v to %v", containerInfo.PodUID, containerInfo.ContainerName, advice.SourceNuma, advice.DestNumaList)
			}

			rssAfter, err := getAnonOnNumaFunc(stats.CgroupPath, advice.SourceNuma)
			if err != nil {
				general.Errorf("getAnonOnNumaFunc failed for container[%v/%v] numa [%v],err: %v",
					containerInfo.PodUID, containerInfo.ContainerName, advice.SourceNuma, err)
			}
			stats.RssAfter = rssAfter

			if rssAfter > rssBefore {
				general.Infof("rssAfter: %d greater than rssBefore: %d", rssAfter, rssBefore)
				rssAfter = rssBefore
			}

			rssDiff := rssBefore - rssAfter
			rssDecreased += rssDiff
		}

		var totalRSSAfter uint64
		var totalRSSBefore uint64
		containerStatJson, _ := json.Marshal(containerStats)
		general.Infof("containerStats: %+v", string(containerStatJson))
		for _, stat := range containerStats {
			totalRSSAfter += stat.RssAfter
			totalRSSBefore += stat.RssBefore
		}
		general.Infof("numa memory balance migration succeed, advice total rss: %v, threshold: %v, sourceNuma:%v, targetNuma:%v, total rss before:%v, total rss after: %v",
			advice.TotalRSS, advice.Threshold, advice.SourceNuma, destNuma, totalRSSBefore, totalRSSAfter)

		if float64(totalRSSAfter)/advice.TotalRSS < (1-advice.Threshold) || float64(rssDecreased)/advice.TotalRSS >= advice.Threshold {
			migrateSuccess = true
			break
		}
	}

	_ = p.emitter.StoreInt64(util.MetricNameMemoryNumaBalanceResult, 1, metrics.MetricTypeNameRaw,
		metrics.MetricTag{Key: "success", Val: strconv.FormatBool(migrateSuccess)})
	return nil
}

// handleAdvisorMemoryOffloading handles memory offloading from memory-advisor
func (p *DynamicPolicy) handleAdvisorMemoryOffloading(_ *config.Configuration,
	_ interface{},
	_ *dynamicconfig.DynamicAgentConfiguration,
	emitter metrics.MetricEmitter,
	metaServer *metaserver.MetaServer,
	entryName, subEntryName string,
	calculationInfo *advisorsvc.CalculationInfo, podResourceEntries state.PodResourceEntries,
) error {
	var absCGPath string
	var memoryOffloadingWorkName string
	memoryOffloadingSizeInBytes := calculationInfo.CalculationResult.Values[string(memoryadvisor.ControlKnowKeyMemoryOffloading)]
	memoryOffloadingSizeInBytesInt64, err := strconv.ParseInt(memoryOffloadingSizeInBytes, 10, 64)
	if err != nil {
		return fmt.Errorf("parse %s: %s failed with error: %v", memoryadvisor.ControlKnowKeyMemoryOffloading, memoryOffloadingSizeInBytes, err)
	}

	if calculationInfo.CgroupPath == "" {
		memoryOffloadingWorkName = util.GetContainerAsyncWorkName(entryName, subEntryName, memoryPluginAsyncWorkTopicMemoryOffloading)
		containerID, err := metaServer.GetContainerID(entryName, subEntryName)
		if err != nil {
			return fmt.Errorf("GetContainerID failed with error: %v", err)
		}
		absCGPath, err = common.GetContainerAbsCgroupPath(common.CgroupSubsysMemory, entryName, containerID)
		if err != nil {
			return fmt.Errorf("GetContainerAbsCgroupPath failed with error: %v", err)
		}
	} else {
		memoryOffloadingWorkName = util.GetCgroupAsyncWorkName(calculationInfo.CgroupPath, memoryPluginAsyncWorkTopicMemoryOffloading)
		absCGPath = common.GetAbsCgroupPath(common.CgroupSubsysMemory, calculationInfo.CgroupPath)
	}

	// set swap max before trigger memory offloading
	swapMax := calculationInfo.CalculationResult.Values[string(memoryadvisor.ControlKnobKeySwapMax)]
	if swapMax == consts.ControlKnobON {
		err := cgroupmgr.SetSwapMaxWithAbsolutePathRecursive(absCGPath)
		if err != nil {
			general.Infof("Failed to set swap max, err: %v", err)
		}
	} else {
		err := cgroupmgr.DisableSwapMaxWithAbsolutePathRecursive(absCGPath)
		if err != nil {
			general.Infof("Failed to disable swap, err: %v", err)
		}
	}

	_, mems, err := cgroupmgr.GetEffectiveCPUSetWithAbsolutePath(absCGPath)
	if err != nil {
		return fmt.Errorf("GetEffectiveCPUSetWithAbsolutePath failed with error: %v", err)
	}

	// start a asynchronous work to execute memory offloading
	err = p.defaultAsyncLimitedWorkers.AddWork(
		&asyncworker.Work{
			Name:        memoryOffloadingWorkName,
			UID:         uuid.NewUUID(),
			Fn:          cgroupmgr.MemoryOffloadingWithAbsolutePath,
			Params:      []interface{}{absCGPath, memoryOffloadingSizeInBytesInt64, mems},
			DeliveredAt: time.Now(),
		}, asyncworker.DuplicateWorkPolicyOverride)
	if err != nil {
		return fmt.Errorf("add work: %s pod: %s container: %s cgroup: %s failed with error: %v", memoryOffloadingWorkName, entryName, subEntryName, absCGPath, err)
	}

	_ = emitter.StoreInt64(util.MetricNameMemoryHandlerAdvisorMemoryOffload, memoryOffloadingSizeInBytesInt64,
		metrics.MetricTypeNameRaw, metrics.ConvertMapToTags(map[string]string{
			"entryName":    entryName,
			"subEntryName": subEntryName,
			"cgroupPath":   calculationInfo.CgroupPath,
		})...)
	return nil
}
