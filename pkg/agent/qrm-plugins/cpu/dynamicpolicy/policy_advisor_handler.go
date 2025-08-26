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

	"github.com/samber/lo"
	"google.golang.org/grpc"
	"google.golang.org/grpc/status"
	"k8s.io/apimachinery/pkg/util/wait"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"
	maputil "k8s.io/kubernetes/pkg/util/maps"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/advisorsvc"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	cpuconsts "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/calculator"
	advisorapi "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/cpuadvisor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util"
	"github.com/kubewharf/katalyst-core/pkg/agent/utilcomponent/featuregatenegotiation"
	"github.com/kubewharf/katalyst-core/pkg/agent/utilcomponent/featuregatenegotiation/finders"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	"github.com/kubewharf/katalyst-core/pkg/util/metric"
	"github.com/kubewharf/katalyst-core/pkg/util/process"
)

const (
	cpuAdvisorHealthMonitorName     = "cpuAdvisorHealthMonitor"
	cpuAdvisorUnhealthyThreshold    = 5 * time.Minute
	cpuAdvisorHealthyThreshold      = 2 * time.Minute
	cpuAdvisorHealthyCount          = 2
	cpuAdvisorHealthMonitorInterval = 30 * time.Second
)

/* in the below, cpu-plugin works in server-mode, while cpu-advisor works in client-mode */

// serveForAdvisor starts a server for cpu-advisor (as a client) to connect with
func (p *DynamicPolicy) serveForAdvisor(stopCh <-chan struct{}) {
	cpuPluginSocketDir := path.Dir(p.cpuPluginSocketAbsPath)

	err := general.EnsureDirectory(cpuPluginSocketDir)
	if err != nil {
		general.Errorf("ensure cpuPluginSocketDir: %s failed with error: %v", cpuPluginSocketDir, err)
		return
	}

	general.Infof("ensure cpuPluginSocketDir: %s successfully", cpuPluginSocketDir)
	if err := os.Remove(p.cpuPluginSocketAbsPath); err != nil && !os.IsNotExist(err) {
		general.Errorf("failed to remove %s: %v", p.cpuPluginSocketAbsPath, err)
		return
	}

	sock, err := net.Listen("unix", p.cpuPluginSocketAbsPath)
	if err != nil {
		general.Errorf("listen at socket: %s failed with err: %v", p.cpuPluginSocketAbsPath, err)
		return
	}
	general.Infof("listen at: %s successfully", p.cpuPluginSocketAbsPath)

	grpcServer := grpc.NewServer()
	advisorapi.RegisterCPUPluginServer(grpcServer, p)

	exitCh := make(chan struct{})
	go func() {
		general.Infof("starting cpu plugin checkpoint grpc server at socket: %s", p.cpuPluginSocketAbsPath)
		if err := grpcServer.Serve(sock); err != nil {
			general.Errorf("cpu plugin checkpoint grpc server crashed with error: %v at socket: %s", err, p.cpuPluginSocketAbsPath)
		} else {
			general.Infof("cpu plugin checkpoint grpc server at socket: %s exists normally", p.cpuPluginSocketAbsPath)
		}

		exitCh <- struct{}{}
	}()

	if conn, err := process.Dial(p.cpuPluginSocketAbsPath, 5*time.Second); err != nil {
		grpcServer.Stop()
		general.Errorf("dial check at socket: %s failed with err: %v", p.cpuPluginSocketAbsPath, err)
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

// GetCheckpoint works with serveForAdvisor to provide ckp for cpu-advisor
func (p *DynamicPolicy) GetCheckpoint(_ context.Context,
	req *advisorapi.GetCheckpointRequest,
) (*advisorapi.GetCheckpointResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("GetCheckpoint got nil req")
	}
	general.Infof("called")

	p.RLock()
	defer p.RUnlock()

	stateEntries := p.state.GetPodEntries()
	chkEntries := make(map[string]*advisorapi.AllocationEntries)
	for uid, containerEntries := range stateEntries {
		if chkEntries[uid] == nil {
			chkEntries[uid] = &advisorapi.AllocationEntries{}
		}

		for entryName, allocationInfo := range containerEntries {
			if allocationInfo == nil {
				continue
			}

			if chkEntries[uid].Entries == nil {
				chkEntries[uid].Entries = make(map[string]*advisorapi.AllocationInfo)
			}

			chkEntries[uid].Entries[entryName] = &advisorapi.AllocationInfo{
				RampUp:        allocationInfo.RampUp,
				OwnerPoolName: allocationInfo.OwnerPoolName,
			}

			ownerPoolName := allocationInfo.GetOwnerPoolName()
			if ownerPoolName == commonstate.EmptyOwnerPoolName {
				general.Warningf("pod: %s/%s container: %s get empty owner pool name",
					allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName)
				if allocationInfo.CheckSideCar() {
					ownerPoolName = containerEntries.GetMainContainerPoolName()

					general.Warningf("set pod: %s/%s sidecar container: %s owner pool name: %s same to its main container",
						allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName,
						ownerPoolName)
				}
			}

			chkEntries[uid].Entries[entryName].OwnerPoolName = ownerPoolName

			// not set topology-aware assignments for shared_cores,
			// since their topology-aware assignments are same to the pools they are in.
			if !allocationInfo.CheckShared() || containerEntries.IsPoolEntry() {
				chkEntries[uid].Entries[entryName].TopologyAwareAssignments = machine.ParseCPUAssignmentFormat(allocationInfo.TopologyAwareAssignments)
				chkEntries[uid].Entries[entryName].OriginalTopologyAwareAssignments = machine.ParseCPUAssignmentFormat(allocationInfo.OriginalTopologyAwareAssignments)
			}
		}
	}

	return &advisorapi.GetCheckpointResponse{
		Entries: chkEntries,
	}, nil
}

/* in the below, cpu-plugin works in client-mode, while cpu-advisor works in server-mode */

// pushCPUAdvisor pushes state info to cpu-advisor
func (p *DynamicPolicy) pushCPUAdvisor() error {
	podEntries := p.state.GetPodEntries()
	for _, entries := range podEntries {
		if entries.IsPoolEntry() {
			continue
		}

		for _, allocationInfo := range entries {
			if allocationInfo == nil {
				continue
			}

			containerType, found := pluginapi.ContainerType_value[allocationInfo.ContainerType]
			if !found {
				return fmt.Errorf("sync pod: %s/%s, container: %s to cpu advisor failed with error: containerType: %s not found",
					allocationInfo.PodNamespace, allocationInfo.PodName,
					allocationInfo.ContainerName, allocationInfo.ContainerType)
			}

			_, err := p.advisorClient.AddContainer(context.Background(), &advisorsvc.ContainerMetadata{
				PodUid:               allocationInfo.PodUid,
				PodNamespace:         allocationInfo.PodNamespace,
				PodName:              allocationInfo.PodName,
				ContainerName:        allocationInfo.ContainerName,
				ContainerType:        pluginapi.ContainerType(containerType),
				ContainerIndex:       allocationInfo.ContainerIndex,
				Labels:               maputil.CopySS(allocationInfo.Labels),
				Annotations:          maputil.CopySS(allocationInfo.Annotations),
				QosLevel:             allocationInfo.QoSLevel,
				RequestQuantity:      uint64(allocationInfo.RequestQuantity),
				RequestMilliQuantity: uint64(allocationInfo.RequestQuantity * 1000),
				UseMilliQuantity:     true,
			})
			if err != nil {
				return fmt.Errorf("sync pod: %s/%s, container: %s to cpu advisor failed with error: %v",
					allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName, err)
			}
		}
	}

	return nil
}

func (p *DynamicPolicy) createGetAdviceRequest() (*advisorapi.GetAdviceRequest, error) {
	stateEntries := p.state.GetPodEntries()
	chkEntries := make(map[string]*advisorapi.ContainerAllocationInfoEntries)
	for uid, containerEntries := range stateEntries {
		if chkEntries[uid] == nil {
			chkEntries[uid] = &advisorapi.ContainerAllocationInfoEntries{
				Entries: make(map[string]*advisorapi.ContainerAllocationInfo),
			}
		}

		for entryName, allocationInfo := range containerEntries {
			if allocationInfo == nil {
				continue
			}

			info := &advisorapi.ContainerAllocationInfo{
				Metadata: &advisorsvc.ContainerMetadata{
					PodUid:               allocationInfo.PodUid,
					PodNamespace:         allocationInfo.PodNamespace,
					PodName:              allocationInfo.PodName,
					ContainerName:        allocationInfo.ContainerName,
					ContainerIndex:       allocationInfo.ContainerIndex,
					Labels:               maputil.CopySS(allocationInfo.Labels),
					Annotations:          maputil.CopySS(allocationInfo.Annotations),
					QosLevel:             allocationInfo.QoSLevel,
					RequestQuantity:      uint64(allocationInfo.RequestQuantity),
					RequestMilliQuantity: uint64(allocationInfo.RequestQuantity * 1000),
					UseMilliQuantity:     true,
				},
				AllocationInfo: &advisorapi.AllocationInfo{
					RampUp:        allocationInfo.RampUp,
					OwnerPoolName: allocationInfo.OwnerPoolName,
				},
			}

			if !containerEntries.IsPoolEntry() {
				// Only fill in the container type for non-pool entries.
				containerType, found := pluginapi.ContainerType_value[allocationInfo.ContainerType]
				if !found {
					return nil, fmt.Errorf("container type %q for container %s/%s not found", allocationInfo.ContainerType, uid, entryName)
				}
				info.Metadata.ContainerType = pluginapi.ContainerType(containerType)

				ownerPoolName := allocationInfo.GetOwnerPoolName()
				if ownerPoolName == commonstate.EmptyOwnerPoolName {
					general.Warningf("pod: %s/%s container: %s get empty owner pool name",
						allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName)
					if allocationInfo.CheckSideCar() {
						ownerPoolName = containerEntries.GetMainContainerPoolName()

						general.Warningf("set pod: %s/%s sidecar container: %s owner pool name: %s same to its main container",
							allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName,
							ownerPoolName)
					}
				}
				info.AllocationInfo.OwnerPoolName = ownerPoolName

				// Copy annotations missing in sidecar container from main container.
				// This should have been done during sidecar allocation, but we do it again here
				// to ensure backward compatibility with checkpoint written by older versions of the plugin.
				if allocationInfo.CheckSideCar() {
					mainContainerInfo := containerEntries.GetMainContainerEntry()
					if mainContainerInfo != nil {
						for key, value := range mainContainerInfo.Annotations {
							if _, ok := info.Metadata.Annotations[key]; !ok {
								info.Metadata.Annotations[key] = value
							}
						}
					}
				}
			}

			// not set topology-aware assignments for shared_cores,
			// since their topology-aware assignments are same to the pools they are in.
			if !allocationInfo.CheckShared() || containerEntries.IsPoolEntry() {
				info.AllocationInfo.TopologyAwareAssignments = machine.ParseCPUAssignmentFormat(allocationInfo.TopologyAwareAssignments)
				info.AllocationInfo.OriginalTopologyAwareAssignments = machine.ParseCPUAssignmentFormat(allocationInfo.OriginalTopologyAwareAssignments)
			}

			chkEntries[uid].Entries[entryName] = info
		}
	}

	if lo.IsNil(p.featureGateManager) {
		return nil, fmt.Errorf("featureGateManager is nil")
	}
	wantedFeatureGates, err := p.featureGateManager.GetWantedFeatureGates(finders.FeatureGateTypeCPU)
	if err != nil {
		return nil, err
	}

	general.InfofV(6, "CPU plugin desire negotiation feature gates: %#v", wantedFeatureGates)

	return &advisorapi.GetAdviceRequest{
		Entries:            chkEntries,
		WantedFeatureGates: wantedFeatureGates,
	}, nil
}

func (p *DynamicPolicy) getAdviceFromAdvisor(ctx context.Context) (isImplemented bool, err error) {
	startTime := time.Now()
	general.Infof("called")
	defer func() {
		general.InfoS("finished", "duration", time.Since(startTime))
	}()

	request, err := p.createGetAdviceRequest()
	if err != nil {
		return false, fmt.Errorf("create GetAdviceRequest failed with error: %w", err)
	}
	resp, err := p.advisorClient.GetAdvice(ctx, request)
	if err != nil {
		if general.IsUnimplementedError(err) {
			return false, nil
		}
		return true, fmt.Errorf("GetAdvice failed with error: %w", err)
	}

	general.InfofV(6, "QRM CPU plugin wanted feature gates: %v, sysadvisor supported feature gates: %v", lo.Keys(request.WantedFeatureGates), lo.Keys(resp.SupportedFeatureGates))
	// check if there are feature gates wanted by QRM that are not supported by cpu sysadvisor
	wantedButNotSupportedFeatureGates := featuregatenegotiation.GetWantedButNotSupportedFeatureGates(request.WantedFeatureGates, resp.SupportedFeatureGates)
	for _, featureGate := range wantedButNotSupportedFeatureGates {
		if featureGate.MustMutuallySupported {
			return true, fmt.Errorf("feature gate %s which must be mutually supported is not supported by cpu-advisor", featureGate.Name)
		}
	}

	err = p.allocateByCPUAdvisor(request, &advisorapi.ListAndWatchResponse{
		Entries:                               resp.Entries,
		AllowSharedCoresOverlapReclaimedCores: resp.AllowSharedCoresOverlapReclaimedCores,
		ExtraEntries:                          resp.ExtraEntries,
	}, resp.SupportedFeatureGates)
	if err != nil {
		return true, fmt.Errorf("allocate by GetAdvice response failed with error: %w", err)
	}

	if len(wantedButNotSupportedFeatureGates) > 0 {
		general.Warningf("feature gates wanted by QRM that are not supported by cpu sysadvisor: %v", lo.Keys(wantedButNotSupportedFeatureGates))
		return true, featuregatenegotiation.FeatureGatesNotSupportedError{WantedButNotSupportedFeatureGates: wantedButNotSupportedFeatureGates}
	}

	return true, nil
}

// getAdviceFromAdvisorLoop gets advice from cpu-advisor periodically.
// qrm-plugin works in client-mode, while cpu-advisor works in server-mode.
// All information required by cpu-advisor is included in the request, and the response is returned synchronously.
// This new communication mode is meant to replace the legacy list-and-watch mode.
// This function only returns if stopCh is closed, or if the advisor does not implement GetAdvice,
// in which case we should fall back to the legacy list-and-watch mode.
func (p *DynamicPolicy) getAdviceFromAdvisorLoop(stopCh <-chan struct{}) {
	general.Infof("called")

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-stopCh
		general.Infof("received stop signal, stop calling GetAdvice on CPUAdvisorServer")
		cancel()
	}()

	_ = wait.PollImmediateUntilWithContext(ctx, p.getAdviceInterval, func(ctx context.Context) (bool, error) {
		isImplemented, err := p.getAdviceFromAdvisor(ctx)
		if err != nil {
			if featuregatenegotiation.IsFeatureGatesNotSupportedError(err) {
				_ = p.emitter.StoreInt64(util.MetricNameGetAdviceFeatureNotSupported, 1,
					metrics.MetricTypeNameRaw,
					metrics.MetricTag{
						Key: "error_message", Val: metric.MetricTagValueFormat(err),
					})
				general.Warningf("get advice from cpu advisor found not supported feature gate: %v", err)
			} else {
				_ = p.emitter.StoreInt64(util.MetricNameGetAdviceFailed, 1, metrics.MetricTypeNameRaw)
				general.Errorf("get advice from cpu advisor failed with error: %v", err)
			}
		} else if !isImplemented {
			general.Infof("CPUAdvisorServer does not implement GetAdvice")
			return true, nil
		}

		_ = general.UpdateHealthzStateByError(cpuconsts.CommunicateWithAdvisor, err)

		if p.advisorMonitor != nil && err == nil {
			p.advisorMonitor.UpdateRefreshTime()
		}
		return false, nil
	})
}

// lwCPUAdvisorServer works as a client to connect with cpu-advisor.
// it will wait to receive allocations from cpu-advisor, and perform allocate actions
func (p *DynamicPolicy) lwCPUAdvisorServer(stopCh <-chan struct{}) error {
	general.Infof("called")

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-stopCh
		general.Infof("received stop signal, stop calling ListAndWatch of CPUAdvisorServer")
		cancel()
	}()

	stream, err := p.advisorClient.ListAndWatch(ctx, &advisorsvc.Empty{})
	if err != nil {
		return fmt.Errorf("call ListAndWatch of CPUAdvisorServer failed with error: %v", err)
	}

	for {
		resp, err := stream.Recv()
		if err != nil {
			_ = p.emitter.StoreInt64(util.MetricNameLWAdvisorServerFailed, 1, metrics.MetricTypeNameRaw)
			return fmt.Errorf("receive ListAndWatch response of CPUAdvisorServer failed with error: %v, grpc code: %v",
				err, status.Code(err))
		}

		// old asynchronous communication interface does not support feature gate negotiation. If necessary, upgrade to the synchronization interface.
		emptyMap := map[string]*advisorsvc.FeatureGate{}
		err = p.allocateByCPUAdvisor(nil, resp, emptyMap)
		if err != nil {
			general.Errorf("allocate by ListAndWatch response of CPUAdvisorServer failed with error: %v", err)
		}

		_ = general.UpdateHealthzStateByError(cpuconsts.CommunicateWithAdvisor, err)

		if p.advisorMonitor != nil && err == nil {
			p.advisorMonitor.UpdateRefreshTime()
		}
	}
}

// allocateByCPUAdvisor perform allocate actions based on allocation response from cpu-advisor.
// supportedFeatureGates means the feature gates than both qrm wanted and sysadvisor supported
func (p *DynamicPolicy) allocateByCPUAdvisor(
	req *advisorapi.GetAdviceRequest,
	resp *advisorapi.ListAndWatchResponse,
	featureGates map[string]*advisorsvc.FeatureGate,
) (err error) {
	if resp == nil {
		return fmt.Errorf("allocateByCPUAdvisor got nil qos aware lw response")
	}

	startTime := time.Now()
	general.Infof("allocateByCPUAdvisor is called")
	_ = p.emitter.StoreInt64(util.MetricNameHandleAdvisorRespCalled, 1, metrics.MetricTypeNameRaw)
	p.Lock()
	defer func() {
		p.Unlock()
		if err != nil {
			_ = p.emitter.StoreInt64(util.MetricNameHandleAdvisorRespFailed, 1, metrics.MetricTypeNameRaw)
		}
		general.InfoS("finished", "duration", time.Since(startTime))
	}()

	if req != nil {
		vErr := p.advisorValidator.ValidateRequest(req)
		if vErr != nil {
			return fmt.Errorf("ValidateCPUAdvisorReq failed with error: %v", vErr)
		}
	}
	vErr := p.advisorValidator.Validate(resp)
	if vErr != nil {
		return fmt.Errorf("ValidateCPUAdvisorResp failed with error: %v", vErr)
	}

	blockToCPUSet, aErr := p.generateBlockCPUSet(resp)
	if aErr != nil {
		return fmt.Errorf("generateBlockCPUSet failed with error: %v", aErr)
	}

	applyErr := p.applyBlocks(blockToCPUSet, resp)
	if applyErr != nil {
		return fmt.Errorf("applyBlocks failed with error: %v", applyErr)
	}

	applyErr = p.applyNUMAHeadroom(resp)
	if applyErr != nil {
		return fmt.Errorf("applyNUMAHeadroom failed with error: %v", applyErr)
	}

	applyErr = p.applyCgroupConfigs(resp)
	if applyErr != nil {
		return fmt.Errorf("applyCgroupConfigs failed with error: %v", applyErr)
	}

	curAllowSharedCoresOverlapReclaimedCores := p.state.GetAllowSharedCoresOverlapReclaimedCores()

	if curAllowSharedCoresOverlapReclaimedCores != resp.AllowSharedCoresOverlapReclaimedCores {
		general.Infof("set allowSharedCoresOverlapReclaimedCores from %v to %v",
			curAllowSharedCoresOverlapReclaimedCores, resp.AllowSharedCoresOverlapReclaimedCores)
		p.state.SetAllowSharedCoresOverlapReclaimedCores(resp.AllowSharedCoresOverlapReclaimedCores, true)
	}

	return nil
}

func (p *DynamicPolicy) applyCgroupConfigs(resp *advisorapi.ListAndWatchResponse) error {
	for _, calculationInfo := range resp.ExtraEntries {
		if !general.IsPathExists(common.GetAbsCgroupPath(common.DefaultSelectedSubsys, calculationInfo.CgroupPath)) {
			general.Infof("cgroup path not exist, skip applyCgroupConfigs: %s", common.GetAbsCgroupPath(common.DefaultSelectedSubsys, calculationInfo.CgroupPath))
			continue
		}

		cgConf, ok := calculationInfo.CalculationResult.Values[string(advisorapi.ControlKnobKeyCgroupConfig)]
		if !ok {
			continue
		}

		resources := &common.CgroupResources{}
		err := json.Unmarshal([]byte(cgConf), resources)
		if err != nil {
			return fmt.Errorf("unmarshal %s: %s failed with error: %v",
				advisorapi.ControlKnobKeyCgroupConfig, cgConf, err)
		}

		resources.SkipDevices = true
		resources.SkipFreezeOnSet = true

		err = common.ApplyCgroupConfigs(calculationInfo.CgroupPath, resources)
		if err != nil {
			return fmt.Errorf("ApplyCgroupConfigs failed: %s, %v", calculationInfo.CgroupPath, err)
		}
	}

	return nil
}

// generateBlockCPUSet generates BlockCPUSet from cpu-advisor response
// and the logic contains three main steps
//  1. handle blocks for static pools
//  2. handle blocks with specified NUMA ids (probably be blocks for
//     numa_binding dedicated_cores containers and reclaimed_cores containers colocated with them)
//  3. handle blocks without specified NUMA id (probably be blocks for
//     not numa_binding dedicated_cores containers and pools of shared_cores and reclaimed_cores containers)
func (p *DynamicPolicy) generateBlockCPUSet(resp *advisorapi.ListAndWatchResponse) (advisorapi.BlockCPUSet, error) {
	if resp == nil {
		return nil, fmt.Errorf("got nil resp")
	}

	numaToBlocks, err := resp.GetBlocks()
	if err != nil {
		return nil, err
	}

	machineInfo := p.machineInfo
	topology := machineInfo.CPUTopology
	availableCPUs := topology.CPUDetails.CPUs()

	// walk through static pools to construct blockCPUSet (for static pool),
	// and calculate availableCPUs after deducting static pools
	blockCPUSet := advisorapi.NewBlockCPUSet()
	for _, poolName := range state.StaticPools.List() {
		allocationInfo := p.state.GetAllocationInfo(poolName, commonstate.FakedContainerName)
		if allocationInfo == nil {
			continue
		}

		blocks, ok := resp.GeEntryNUMABlocks(poolName, commonstate.FakedContainerName, commonstate.FakedNUMAID)
		if !ok || len(blocks) != 1 {
			return nil, fmt.Errorf("blocks of pool: %s is invalid", poolName)
		}

		blockID := blocks[0].BlockId
		blockCPUSet[blockID] = allocationInfo.AllocationResult.Clone()
		availableCPUs = availableCPUs.Difference(blockCPUSet[blockID])
	}

	// walk through forbidden pools to construct blockCPUSet (for forbidden pool),
	// and calculate availableCPUs after deducting forbidden pools
	for _, poolName := range state.ForbiddenPools.List() {
		allocationInfo := p.state.GetAllocationInfo(poolName, commonstate.FakedContainerName)
		if allocationInfo == nil {
			continue
		}

		availableCPUs = availableCPUs.Difference(allocationInfo.AllocationResult.Clone())
	}

	// walk through all blocks with specified NUMA ids
	// for each block, add them into blockCPUSet (if not exist) and renew availableCPUs
	for numaID, blocks := range numaToBlocks {
		if numaID == commonstate.FakedNUMAID {
			continue
		}

		numaAvailableCPUs := availableCPUs.Intersection(topology.CPUDetails.CPUsInNUMANodes(numaID))
		for _, block := range blocks {
			if block == nil {
				general.Warningf("got nil block")
				continue
			}

			blockID := block.BlockId

			if _, found := blockCPUSet[blockID]; found {
				general.Warningf("block: %v already allocated", blockID)
				continue
			}

			blockResult, err := general.CovertUInt64ToInt(block.Result)
			if err != nil {
				return nil, fmt.Errorf("parse block: %s result failed with error: %v",
					blockID, err)
			}

			cpuset, err := calculator.TakeByTopology(machineInfo, numaAvailableCPUs, blockResult)
			if err != nil {
				return nil, fmt.Errorf("allocate cpuset for NUMA Aware block: %s in NUMA: %d failed with error: %v, numaAvailableCPUs: %d(%s), blockResult: %d",
					blockID, numaID, err, numaAvailableCPUs.Size(), numaAvailableCPUs.String(), blockResult)
			}

			blockCPUSet[blockID] = cpuset
			numaAvailableCPUs = numaAvailableCPUs.Difference(cpuset)
			availableCPUs = availableCPUs.Difference(cpuset)
		}
	}

	// walk through all blocks without specified NUMA id
	// for each block, add them into blockCPUSet (if not exist) and renew availableCPUs
	for _, block := range numaToBlocks[commonstate.FakedNUMAID] {
		if block == nil {
			general.Warningf("got nil block")
			continue
		}

		blockID := block.BlockId

		if _, found := blockCPUSet[blockID]; found {
			general.Warningf("block: %s already allocated", blockID)
			continue
		}

		blockResult, err := general.CovertUInt64ToInt(block.Result)
		if err != nil {
			return nil, fmt.Errorf("parse block: %s result failed with error: %v",
				blockID, err)
		}

		// use NUMA balance strategy to aviod changing memset as much as possible
		// for blocks with faked NUMA id
		var cpuset machine.CPUSet
		cpuset, availableCPUs, err = calculator.TakeByNUMABalance(machineInfo, availableCPUs, blockResult)
		if err != nil {
			return nil, fmt.Errorf("allocate cpuset for non NUMA Aware block: %s failed with error: %v, availableCPUs: %d(%s), blockResult: %d",
				blockID, err, availableCPUs.Size(), availableCPUs.String(), blockResult)
		}

		blockCPUSet[blockID] = cpuset
		availableCPUs = availableCPUs.Difference(cpuset)
	}

	return blockCPUSet, nil
}

// applyBlocks allocate based on BlockCPUSet
// and the logic contains three main steps
// 1. construct entries for dedicated containers and pools
// 2. ensure reclaimed pool exists
// 3. construct entries for shared and reclaimed containers
func (p *DynamicPolicy) applyBlocks(blockCPUSet advisorapi.BlockCPUSet, resp *advisorapi.ListAndWatchResponse) error {
	if resp == nil {
		return fmt.Errorf("applyBlocks got nil resp")
	}

	curEntries := p.state.GetPodEntries()
	newEntries := make(state.PodEntries)
	dedicatedCPUSet := machine.NewCPUSet()
	pooledUnionDedicatedCPUSet := machine.NewCPUSet()

	// calculate NUMAs without actual numa_binding reclaimed pods
	nonReclaimActualBindingNUMAs := p.state.GetMachineState().GetFilteredNUMASet(state.WrapAllocationMetaFilter((*commonstate.AllocationMeta).CheckReclaimedActualNUMABinding))

	// deal with blocks of dedicated_cores and pools
	for entryName, entry := range resp.Entries {
		if entryName == commonstate.PoolNameInterrupt {
			continue
		}

		for subEntryName, calculationInfo := range entry.Entries {
			if calculationInfo == nil {
				general.Warningf("got nil calculationInfo entry: %s, subEntry: %s", entryName, subEntryName)
				continue
			} else if !(subEntryName == commonstate.FakedContainerName || calculationInfo.OwnerPoolName == commonstate.PoolNameDedicated) {
				continue
			}

			// construct cpuset for this entry by union all blocks for it
			entryCPUSet, err := calculationInfo.GetCPUSet(entryName, subEntryName, blockCPUSet)
			if err != nil {
				return err
			}

			// transform cpuset into topologyAwareAssignments
			topologyAwareAssignments, err := machine.GetNumaAwareAssignments(p.machineInfo.CPUTopology, entryCPUSet)
			if err != nil {
				return fmt.Errorf("unable to calculate topologyAwareAssignments for entry: %s, subEntry: %s, entry cpuset: %s, error: %v",
					entryName, subEntryName, entryCPUSet.String(), err)
			}

			// if allocation already exists, update them; otherwise, construct new a new one
			allocationInfo := curEntries[entryName][subEntryName].Clone()
			if allocationInfo == nil {
				// currently, cpu advisor can only create new pools,
				// all container entries or entries with owner pool name dedicated can't be created by cpu advisor
				if calculationInfo.OwnerPoolName == commonstate.PoolNameDedicated || subEntryName != commonstate.FakedContainerName {
					return fmt.Errorf("no-pool entry isn't found in plugin cache, entry: %s, subEntry: %s", entryName, subEntryName)
				} else if entryName != calculationInfo.OwnerPoolName {
					return fmt.Errorf("pool entryName: %s and OwnerPoolName: %s mismatch", entryName, calculationInfo.OwnerPoolName)
				}

				general.Infof("create new pool: %s cpuset result %s", entryName, entryCPUSet.String())
				allocationInfo = &state.AllocationInfo{
					AllocationMeta: commonstate.AllocationMeta{
						PodUid:        entryName,
						OwnerPoolName: entryName,
					},
					AllocationResult:                 entryCPUSet.Clone(),
					OriginalAllocationResult:         entryCPUSet.Clone(),
					TopologyAwareAssignments:         topologyAwareAssignments,
					OriginalTopologyAwareAssignments: machine.DeepcopyCPUAssignment(topologyAwareAssignments),
				}
			} else {
				general.Infof("entry: %s, subEntryName: %s cpuset allocation result transform from %s(size: %d) to %s(size: %d)",
					entryName, subEntryName,
					allocationInfo.AllocationResult.String(), allocationInfo.AllocationResult.Size(),
					entryCPUSet.String(), entryCPUSet.Size())

				allocationInfo.OwnerPoolName = calculationInfo.OwnerPoolName
				allocationInfo.AllocationResult = entryCPUSet.Clone()
				allocationInfo.OriginalAllocationResult = entryCPUSet.Clone()
				allocationInfo.TopologyAwareAssignments = topologyAwareAssignments
				allocationInfo.OriginalTopologyAwareAssignments = machine.DeepcopyCPUAssignment(topologyAwareAssignments)
			}

			if newEntries[entryName] == nil {
				newEntries[entryName] = make(state.ContainerEntries)
			}
			newEntries[entryName][subEntryName] = allocationInfo
			pooledUnionDedicatedCPUSet = pooledUnionDedicatedCPUSet.Union(allocationInfo.AllocationResult)

			// ramp-up finishes immediately for dedicated
			if allocationInfo.OwnerPoolName == commonstate.PoolNameDedicated {
				dedicatedCPUSet = dedicatedCPUSet.Union(allocationInfo.AllocationResult)
				general.Infof("try to apply dedicated_cores: %s/%s %s: %s",
					allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName, allocationInfo.AllocationResult.String())

				if allocationInfo.RampUp {
					allocationInfo.RampUp = false
					general.Infof("pod: %s/%s, container: %s ramp up finished", allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName)
				}
			} else {
				for numaID, cpus := range allocationInfo.TopologyAwareAssignments {
					_ = p.emitter.StoreInt64(util.MetricNamePoolSize, int64(cpus.Size()),
						metrics.MetricTypeNameRaw, metrics.MetricTag{Key: "poolName", Val: allocationInfo.OwnerPoolName},
						metrics.MetricTag{Key: "pool_type", Val: commonstate.GetPoolType(allocationInfo.OwnerPoolName)},
						metrics.MetricTag{Key: "numa_id", Val: strconv.Itoa(numaID)})
					general.Infof("try to apply pool %s numa %d: %s", allocationInfo.OwnerPoolName, numaID, cpus.String())
				}
			}
		}
	}

	// revise reclaim pool size to avoid reclaimed_cores and numa_binding dedicated_cores containers
	// in NUMAs without cpuset actual binding
	err := p.reviseReclaimPool(newEntries, nonReclaimActualBindingNUMAs, pooledUnionDedicatedCPUSet)
	if err != nil {
		return err
	}

	// calculate rampUpCPUs
	sharedBindingNUMAs, err := resp.GetSharedBindingNUMAs()
	if err != nil {
		return fmt.Errorf("GetSharedBindingNUMAs failed with error: %v", err)
	}
	sharedBindingNUMACPUs := p.machineInfo.CPUDetails.CPUsInNUMANodes(sharedBindingNUMAs.UnsortedList()...)
	// rampUpCPUs include reclaim pool in NUMAs without NUMA_binding cpus
	rampUpCPUs := p.machineInfo.CPUDetails.CPUs().
		Difference(p.reservedCPUs).
		Difference(dedicatedCPUSet).
		Difference(sharedBindingNUMACPUs)

	rampUpCPUsTopologyAwareAssignments, err := machine.GetNumaAwareAssignments(p.machineInfo.CPUTopology, rampUpCPUs)
	if err != nil {
		return fmt.Errorf("unable to calculate topologyAwareAssignments for rampUpCPUs, result cpuset: %s, error: %v",
			rampUpCPUs.String(), err)
	}

	// deal with blocks of reclaimed_cores and share_cores
	for podUID, containerEntries := range curEntries {
		if containerEntries.IsPoolEntry() {
			continue
		}

	containerLoop:
		for containerName, allocationInfo := range containerEntries {
			if allocationInfo == nil {
				general.Errorf("pod: %s, container: %s has nil allocationInfo", podUID, containerName)
				continue
			}

			if newEntries[podUID][containerName] != nil {
				continue
			}

			if newEntries[podUID] == nil {
				newEntries[podUID] = make(state.ContainerEntries)
			}
			newEntries[podUID][containerName] = allocationInfo.Clone()
			// adapt to old checkpoint without RequestQuantity property
			newEntries[podUID][containerName].RequestQuantity = p.getContainerRequestedCores(allocationInfo)

			switch allocationInfo.QoSLevel {
			case consts.PodAnnotationQoSLevelDedicatedCores:
				errMsg := fmt.Sprintf("dedicated_cores blocks aren't applied, pod: %s/%s, container: %s",
					allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName)
				general.Errorf(errMsg)
				return fmt.Errorf(errMsg)
			case consts.PodAnnotationQoSLevelSystemCores:
				poolCPUSet, topologyAwareAssignments, err := p.getSystemPoolCPUSetAndNumaAwareAssignments(newEntries, allocationInfo)
				if err != nil {
					return fmt.Errorf("pod: %s/%s, container: %s is system_cores, "+
						"getSystemPoolCPUSetAndNumaAwareAssignments failed with error: %v",
						allocationInfo.PodNamespace, allocationInfo.PodName,
						allocationInfo.ContainerName, err)
				}

				newEntries[podUID][containerName].AllocationResult = poolCPUSet
				newEntries[podUID][containerName].OriginalAllocationResult = poolCPUSet.Clone()
				newEntries[podUID][containerName].TopologyAwareAssignments = topologyAwareAssignments
				newEntries[podUID][containerName].OriginalTopologyAwareAssignments = machine.DeepcopyCPUAssignment(topologyAwareAssignments)
			case consts.PodAnnotationQoSLevelSharedCores:
				ownerPoolName := p.getOwnerPoolNameFromAdvisor(allocationInfo, resp)
				if allocationInfo.RampUp {
					general.Infof("pod: %s/%s container: %s is in ramp up, set its allocation result from %s to rampUpCPUs :%s",
						allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName, allocationInfo.AllocationResult.String(), rampUpCPUs.String())

					if rampUpCPUs.IsEmpty() {
						general.Warningf("rampUpCPUs is empty. pod: %s/%s container: %s reuses its allocation result: %s",
							allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName, allocationInfo.AllocationResult.String())
						continue containerLoop
					}

					newEntries[podUID][containerName].OwnerPoolName = commonstate.EmptyOwnerPoolName
					newEntries[podUID][containerName].AllocationResult = rampUpCPUs.Clone()
					newEntries[podUID][containerName].OriginalAllocationResult = rampUpCPUs.Clone()
					newEntries[podUID][containerName].TopologyAwareAssignments = machine.DeepcopyCPUAssignment(rampUpCPUsTopologyAwareAssignments)
					newEntries[podUID][containerName].OriginalTopologyAwareAssignments = machine.DeepcopyCPUAssignment(rampUpCPUsTopologyAwareAssignments)
				} else {
					poolEntry, err := p.getAllocationPoolEntry(allocationInfo, ownerPoolName, newEntries)
					if err != nil {
						return err
					}

					if allocationInfo.CheckSharedNUMABinding() {
						poolEntry.QoSLevel = apiconsts.PodAnnotationQoSLevelSharedCores
						// set SharedNUMABinding declarations to pool entry containing SharedNUMABinding containers,
						// in order to differentiate them from non-binding share cores pools during GetFilteredPoolsCPUSetMap.
						poolEntry.Annotations = general.MergeMap(poolEntry.Annotations, map[string]string{
							apiconsts.PodAnnotationMemoryEnhancementNumaBinding: apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable,
						})
					}

					general.Infof("put pod: %s/%s container: %s to pool: %s, set its allocation result from %s to %s",
						allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName, ownerPoolName, allocationInfo.AllocationResult.String(), poolEntry.AllocationResult.String())

					newEntries[podUID][containerName].OwnerPoolName = ownerPoolName
					newEntries[podUID][containerName].AllocationResult = poolEntry.AllocationResult.Clone()
					newEntries[podUID][containerName].OriginalAllocationResult = poolEntry.OriginalAllocationResult.Clone()
					newEntries[podUID][containerName].TopologyAwareAssignments = machine.DeepcopyCPUAssignment(poolEntry.TopologyAwareAssignments)
					newEntries[podUID][containerName].OriginalTopologyAwareAssignments = machine.DeepcopyCPUAssignment(poolEntry.TopologyAwareAssignments)
				}
			case consts.PodAnnotationQoSLevelReclaimedCores:
				ownerPoolName := p.getOwnerPoolNameFromAdvisor(allocationInfo, resp)
				poolEntry, err := p.getAllocationPoolEntry(allocationInfo, ownerPoolName, newEntries)
				if err != nil {
					return err
				}

				err = p.updateReclaimAllocationResultByPoolEntry(newEntries[podUID][containerName], poolEntry, nonReclaimActualBindingNUMAs)
				if err != nil {
					return err
				}
			default:
				return fmt.Errorf("invalid qosLevel: %s for pod: %s/%s container: %s",
					allocationInfo.QoSLevel, allocationInfo.PodNamespace,
					allocationInfo.PodName, allocationInfo.ContainerName)
			}
		}
	}

	// deal with interrupt pools
	if subEntry, ok := curEntries[commonstate.PoolNameInterrupt]; ok {
		newEntries[commonstate.PoolNameInterrupt] = make(state.ContainerEntries)
		if ai, ok := subEntry[commonstate.FakedContainerName]; ok && ai != nil {
			newEntries[commonstate.PoolNameInterrupt][commonstate.FakedContainerName] = ai.Clone()
		}
	}

	// use pod entries generated above to generate machine state info, and store in local state
	newMachineState, err := generateMachineStateFromPodEntries(p.machineInfo.CPUTopology, newEntries)
	if err != nil {
		return fmt.Errorf("calculate machineState by newPodEntries failed with error: %v", err)
	}
	p.state.SetPodEntries(newEntries, false)
	p.state.SetMachineState(newMachineState, false)
	if err := p.state.StoreState(); err != nil {
		general.ErrorS(err, "store state failed")
	}
	return nil
}

func (p *DynamicPolicy) getOwnerPoolNameFromAdvisor(allocationInfo *state.AllocationInfo, resp *advisorapi.ListAndWatchResponse) string {
	ownerPoolName := allocationInfo.GetOwnerPoolName()
	if calculationInfo, ok := resp.GetCalculationInfo(allocationInfo.PodUid, allocationInfo.ContainerName); ok {
		general.Infof("cpu advisor put pod: %s/%s, container: %s from %s to %s",
			allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName, ownerPoolName, calculationInfo.OwnerPoolName)

		ownerPoolName = calculationInfo.OwnerPoolName
	} else {
		general.Warningf("cpu advisor doesn't return entry for pod: %s/%s, container: %s, qosLevel: %s",
			allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName, allocationInfo.QoSLevel)
	}
	return ownerPoolName
}

func (p *DynamicPolicy) applyNUMAHeadroom(resp *advisorapi.ListAndWatchResponse) error {
	if resp == nil {
		return fmt.Errorf("applyNUMAHeadroom got nil resp")
	}

	for _, calculationInfo := range resp.ExtraEntries {
		if calculationInfo == nil {
			general.Warningf("resp.ExtraEntries has nil calculationInfo")
			continue
		} else if calculationInfo.CalculationResult == nil {
			general.Warningf("resp.ExtraEntries has nil CalculationResult")
			continue
		}

		cpuNUMAHeadroomValue, ok := calculationInfo.CalculationResult.Values[string(advisorapi.ControlKnobKeyCPUNUMAHeadroom)]
		if !ok {
			general.Warningf("resp.ExtraEntry has no cpu_numa_headroom value")
			continue
		}

		cpuNUMAHeadroom := &advisorapi.CPUNUMAHeadroom{}
		err := json.Unmarshal([]byte(cpuNUMAHeadroomValue), cpuNUMAHeadroom)
		if err != nil {
			return fmt.Errorf("unmarshal %s: %s failed with error: %v",
				advisorapi.ControlKnobKeyCPUNUMAHeadroom, cpuNUMAHeadroomValue, err)
		}

		p.state.SetNUMAHeadroom(*cpuNUMAHeadroom, true)
		general.Infof("cpuNUMAHeadroom: %v", cpuNUMAHeadroom)
	}

	return nil
}

func (p *DynamicPolicy) reviseReclaimPool(newEntries state.PodEntries, nonReclaimActualBindingNUMAs, pooledUnionDedicatedCPUSet machine.CPUSet) error {
	forbiddenCPUs, err := state.GetUnitedPoolsCPUs(state.ForbiddenPools, p.state.GetPodEntries())
	if err != nil {
		return fmt.Errorf("GetUnitedPoolsCPUs for forbidden pools failed with error: %v", err)
	}

	// if there is no block for state.PoolNameReclaim pool,
	// we must make it existing here even if cause overlap
	if newEntries.CheckPoolEmpty(commonstate.PoolNameReclaim) {
		reclaimPoolCPUSet := p.machineInfo.CPUDetails.CPUs().Difference(p.reservedCPUs).Difference(pooledUnionDedicatedCPUSet).Difference(forbiddenCPUs)
		if reclaimPoolCPUSet.IsEmpty() {
			reclaimPoolCPUSet = p.reservedReclaimedCPUSet.Clone()
			general.Infof("fallback takeByNUMABalance for reclaimPoolCPUSet: %s", reclaimPoolCPUSet.String())
		}

		topologyAwareAssignments, err := machine.GetNumaAwareAssignments(p.machineInfo.CPUTopology, reclaimPoolCPUSet)
		if err != nil {
			return fmt.Errorf("unable to calculate topologyAwareAssignments for pool: %s, "+
				"result cpuset: %s, error: %v", commonstate.PoolNameReclaim, reclaimPoolCPUSet.String(), err)
		}

		if newEntries[commonstate.PoolNameReclaim] == nil {
			newEntries[commonstate.PoolNameReclaim] = make(state.ContainerEntries)
		}
		newEntries[commonstate.PoolNameReclaim][commonstate.FakedContainerName] = &state.AllocationInfo{
			AllocationMeta: commonstate.AllocationMeta{
				PodUid:        commonstate.PoolNameReclaim,
				OwnerPoolName: commonstate.PoolNameReclaim,
			},
			AllocationResult:                 reclaimPoolCPUSet.Clone(),
			OriginalAllocationResult:         reclaimPoolCPUSet.Clone(),
			TopologyAwareAssignments:         topologyAwareAssignments,
			OriginalTopologyAwareAssignments: machine.DeepcopyCPUAssignment(topologyAwareAssignments),
		}

		general.Infof("set reclaim allocationResult: %s, reclaim topologyAwareAssignments: %v",
			newEntries[commonstate.PoolNameReclaim][commonstate.FakedContainerName].AllocationResult.String(),
			newEntries[commonstate.PoolNameReclaim][commonstate.FakedContainerName].TopologyAwareAssignments)
	} else {
		general.Infof("detected reclaim allocationResult: %s, reclaim topologyAwareAssignments: %v",
			newEntries[commonstate.PoolNameReclaim][commonstate.FakedContainerName].AllocationResult.String(),
			newEntries[commonstate.PoolNameReclaim][commonstate.FakedContainerName].TopologyAwareAssignments)
	}

	reclaimPool := newEntries[commonstate.PoolNameReclaim][commonstate.FakedContainerName]

	// revise reclaim pool for RNB NUMAs
	for _, numaID := range p.machineInfo.CPUDetails.NUMANodes().ToSliceInt() {
		if nonReclaimActualBindingNUMAs.Contains(numaID) {
			continue
		}

		if reclaimPool.TopologyAwareAssignments[numaID].IsEmpty() {
			reclaimPool.AllocationResult = reclaimPool.AllocationResult.Union(p.reservedReclaimedTopologyAwareAssignments[numaID])
			reclaimPool.OriginalAllocationResult = reclaimPool.OriginalAllocationResult.Union(p.reservedReclaimedTopologyAwareAssignments[numaID])
			reclaimPool.TopologyAwareAssignments[numaID] = p.reservedReclaimedTopologyAwareAssignments[numaID].Clone()
			reclaimPool.OriginalTopologyAwareAssignments[numaID] = p.reservedReclaimedTopologyAwareAssignments[numaID].Clone()
		}
	}

	// revise reclaim pool for non-RNB NUMAs
	nonReclaimActualBindingNUMAsAllocation := machine.NewCPUSet()
	for _, numaID := range p.machineInfo.CPUDetails.NUMANodes().ToSliceInt() {
		if nonReclaimActualBindingNUMAs.Contains(numaID) {
			nonReclaimActualBindingNUMAsAllocation = nonReclaimActualBindingNUMAsAllocation.Union(reclaimPool.TopologyAwareAssignments[numaID])
		}
	}

	if nonReclaimActualBindingNUMAsAllocation.IsEmpty() {
		for _, numaID := range nonReclaimActualBindingNUMAsAllocation.ToSliceInt() {
			reclaimPool.AllocationResult = reclaimPool.AllocationResult.Union(p.reservedReclaimedTopologyAwareAssignments[numaID])
			reclaimPool.OriginalAllocationResult = reclaimPool.OriginalAllocationResult.Union(p.reservedReclaimedTopologyAwareAssignments[numaID])
			reclaimPool.TopologyAwareAssignments[numaID] = p.reservedReclaimedTopologyAwareAssignments[numaID].Clone()
			reclaimPool.OriginalTopologyAwareAssignments[numaID] = p.reservedReclaimedTopologyAwareAssignments[numaID].Clone()
		}
	}

	general.Infof("revised reclaim allocationResult: %s, reclaim topologyAwareAssignments: %v",
		reclaimPool.AllocationResult.String(), reclaimPool.TopologyAwareAssignments)
	return nil
}
