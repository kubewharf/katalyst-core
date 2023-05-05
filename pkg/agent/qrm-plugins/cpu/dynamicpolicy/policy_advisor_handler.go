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
	"net"
	"os"
	"path"
	"time"

	"google.golang.org/grpc"
	"k8s.io/klog/v2"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"
	maputil "k8s.io/kubernetes/pkg/util/maps"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/calculator"
	advisorapi "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/cpuadvisor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	"github.com/kubewharf/katalyst-core/pkg/util/process"
)

/* in the below, cpu-plugin works in server-mode, while cpu-advisor works in client-mode */

// serveCPUAdvisor starts a server for cpu-advisor (as a client) to connect with
func (p *DynamicPolicy) serveCPUAdvisor(stopCh <-chan struct{}) {
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
			general.Infof("cpu plugin checkpoint grpc server at socket: %s exits normally", p.cpuPluginSocketAbsPath)
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

// GetCheckpoint works with serveCPUAdvisor to provide ckp for cpu-advisor
func (p *DynamicPolicy) GetCheckpoint(_ context.Context,
	req *advisorapi.GetCheckpointRequest) (*advisorapi.GetCheckpointResponse, error) {
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
				OwnerPoolName: allocationInfo.OwnerPoolName,
			}

			if !state.CheckShared(allocationInfo) && !state.CheckReclaimed(allocationInfo) {
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

			_, err := p.advisorClient.AddContainer(context.Background(), &advisorapi.AddContainerRequest{
				PodUid:          allocationInfo.PodUid,
				PodNamespace:    allocationInfo.PodNamespace,
				PodName:         allocationInfo.PodName,
				ContainerName:   allocationInfo.ContainerName,
				ContainerType:   pluginapi.ContainerType(containerType),
				ContainerIndex:  allocationInfo.ContainerIndex,
				Labels:          maputil.CopySS(allocationInfo.Labels),
				Annotations:     maputil.CopySS(allocationInfo.Annotations),
				QosLevel:        allocationInfo.QoSLevel,
				RequestQuantity: uint64(allocationInfo.RequestQuantity),
			})
			if err != nil {
				return fmt.Errorf("sync pod: %s/%s, container: %s to cpu advisor failed with error: %v",
					allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName, err)
			}
		}
	}

	return nil
}

// lwCPUAdvisorServer works as a client to connect with cpu-advisor.
// it will wait to receive allocations from cpu-advisor, and perform allocate actions
func (p *DynamicPolicy) lwCPUAdvisorServer(stopCh <-chan struct{}) error {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-stopCh
		general.Infof("received stop signal, stop calling ListAndWatch of CPUAdvisorServer")
		cancel()
	}()

	stream, err := p.advisorClient.ListAndWatch(ctx, &advisorapi.Empty{})
	if err != nil {
		return fmt.Errorf("call ListAndWatch of CPUAdvisorServer failed with error: %v", err)
	}

	for {
		resp, err := stream.Recv()
		if err != nil {
			_ = p.emitter.StoreInt64(util.MetricNameLWCPUAdvisorServerFailed, 1, metrics.MetricTypeNameRaw)
			return fmt.Errorf("receive ListAndWatch response of CPUAdvisorServer failed with error: %v", err)
		}

		err = p.allocateByCPUAdvisor(resp)
		if err != nil {
			klog.Errorf("allocate by ListAndWatch response of CPUAdvisorServer failed with error: %v", err)
		}
	}
}

// allocateByCPUAdvisor perform allocate actions based on allocation response from cpu-advisor.
func (p *DynamicPolicy) allocateByCPUAdvisor(resp *advisorapi.ListAndWatchResponse) (err error) {
	if resp == nil {
		return fmt.Errorf("allocateByCPUAdvisor got nil qos aware lw response")
	}

	general.Infof("allocateByCPUAdvisor is called")
	_ = p.emitter.StoreInt64(util.MetricNameAllocateByCPUAdvisorServerCalled, 1, metrics.MetricTypeNameRaw)
	p.Lock()
	defer func() {
		p.Unlock()
		if err != nil {
			_ = p.emitter.StoreInt64(util.MetricNameAllocateByCPUAdvisorServerFailed, 1, metrics.MetricTypeNameRaw)
		}
	}()

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

	return nil
}

// generateBlockCPUSet generates BlockCPUSet from cpu-advisor response
// and the logic contains three main steps
// 1. handle blocks for static pools
// 2. handle blocks for container
// 3. handle blocks for other pools
func (p *DynamicPolicy) generateBlockCPUSet(resp *advisorapi.ListAndWatchResponse) (advisorapi.BlockCPUSet, error) {
	if resp == nil {
		return nil, fmt.Errorf("got nil resp")
	}

	numaBlocks, err := resp.GetBlocks()
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
		allocationInfo := p.state.GetAllocationInfo(poolName, advisorapi.FakedContainerID)
		if allocationInfo == nil {
			continue
		}

		// todo, even validation already guarantees that calculationInfo of static pool
		//  only has one block isn't topology aware, we should not hardcode like this
		blocks, _ := resp.GetBlock(poolName, advisorapi.FakedContainerID, advisorapi.FakedNumaID)
		blockID := blocks[0].BlockId

		blockCPUSet[blockID] = allocationInfo.AllocationResult.Clone()
		availableCPUs = availableCPUs.Difference(blockCPUSet[blockID])
	}

	// walk through all blocks (that belongs to container)
	// for each block, add them into numaBlocks (if not exist) and renew availableCPUs
	for numaID, blocksMap := range numaBlocks {
		if numaID == advisorapi.FakedNumaID {
			continue
		}

		numaAvailableCPUs := availableCPUs.Intersection(topology.CPUDetails.CPUsInNUMANodes(numaID))
		for blockID, block := range blocksMap {
			if block == nil {
				general.Warningf("got nil block")
				continue
			} else if _, found := blockCPUSet[blockID]; found {
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

	// walk through all blocks (that belongs to pool)
	// for each block, add them into numaBlocks (if not exist) and renew availableCPUs
	for blockID, block := range numaBlocks[advisorapi.FakedNumaID] {
		if block == nil {
			general.Warningf("got nil block")
			continue
		} else if _, found := blockCPUSet[blockID]; found {
			general.Warningf("block: %s already allocated", blockID)
			continue
		}

		blockResult, err := general.CovertUInt64ToInt(block.Result)
		if err != nil {
			return nil, fmt.Errorf("parse block: %s result failed with error: %v",
				blockID, err)
		}

		cpuset, err := calculator.TakeByTopology(machineInfo, availableCPUs, blockResult)
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
// 1. construct entries for dedicated containers and none shared and reclaimed pools
// 2. construct entries for reclaimed pools
// 3. construct entries for shared and reclaimed containers
// todo but why we don't need to handle shared pools here
func (p *DynamicPolicy) applyBlocks(blockCPUSet advisorapi.BlockCPUSet, resp *advisorapi.ListAndWatchResponse) error {
	if resp == nil {
		return fmt.Errorf("applyBlocks got nil resp")
	}

	curEntries := p.state.GetPodEntries()
	newEntries := make(state.PodEntries)
	dedicatedCPUSet := machine.NewCPUSet()
	pooledUnionDedicatedCPUSet := machine.NewCPUSet()

	// deal with blocks of dedicated_cores and (none shared and reclaimed) pools
	for entryName, entry := range resp.Entries {
		for subEntryName, calculationInfo := range entry.Entries {
			if calculationInfo == nil {
				general.Warningf("got nil calculationInfo entry: %s, subEntry: %s", entryName, subEntryName)
				continue
			} else if !(subEntryName == advisorapi.FakedContainerID || calculationInfo.OwnerPoolName == state.PoolNameDedicated) {
				continue
			}

			// construct cpuset for this container by union all blocks for it
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

			// if allocation already exits, update them; otherwise, construct new a new one
			allocationInfo := curEntries[entryName][subEntryName].Clone()
			if allocationInfo == nil {
				if qrmGeneratedInfo(subEntryName, calculationInfo) {
					return fmt.Errorf("no-pool entry isn't found in plugin cache, entry: %s, subEntry: %s", entryName, subEntryName)
				} else if entryName != calculationInfo.OwnerPoolName {
					return fmt.Errorf("pool entryName: %s and OwnerPoolName: %s mismatch", entryName, calculationInfo.OwnerPoolName)
				}

				general.Infof("create new pool: %s cpuset result %s", entryName, entryCPUSet.String())
				allocationInfo = &state.AllocationInfo{
					PodUid:                           entryName,
					OwnerPoolName:                    entryName,
					AllocationResult:                 entryCPUSet.Clone(),
					OriginalAllocationResult:         entryCPUSet.Clone(),
					TopologyAwareAssignments:         topologyAwareAssignments,
					OriginalTopologyAwareAssignments: machine.DeepcopyCPUAssignment(topologyAwareAssignments),
				}
			} else {
				general.Infof("entry: %s, subEntryName: %s cpuset allocation result transform from %s to %s",
					entryName, subEntryName, allocationInfo.AllocationResult.String(), entryCPUSet.String())

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

			// ramp-up finishes immediate for dedicated
			if allocationInfo.OwnerPoolName == state.PoolNameDedicated {
				dedicatedCPUSet = dedicatedCPUSet.Union(allocationInfo.AllocationResult)
				general.Infof("try to apply dedicated_cores: %s/%s %s: %s",
					allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName, allocationInfo.AllocationResult.String())

				if allocationInfo.RampUp {
					allocationInfo.RampUp = false
					general.Infof("pod: %s/%s, container: %s ramp up finished", allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName)
				}
			} else {
				_ = p.emitter.StoreInt64(util.MetricNamePoolSize, int64(allocationInfo.AllocationResult.Size()),
					metrics.MetricTypeNameRaw, metrics.MetricTag{Key: "poolName", Val: allocationInfo.OwnerPoolName})
				general.Infof("try to apply pool %s: %s", allocationInfo.OwnerPoolName, allocationInfo.AllocationResult.String())
			}
		}
	}

	rampUpCPUs := p.machineInfo.CPUDetails.CPUs().Difference(p.reservedCPUs).Difference(dedicatedCPUSet)
	rampUpCPUsTopologyAwareAssignments, err := machine.GetNumaAwareAssignments(p.machineInfo.CPUTopology, rampUpCPUs)
	if err != nil {
		return fmt.Errorf("unable to calculate topologyAwareAssignments for rampUpCPUs, result cpuset: %s, error: %v",
			rampUpCPUs.String(), err)
	}

	// construct entries for reclaimed
	if newEntries.CheckPoolEmpty(state.PoolNameReclaim) {
		reclaimPoolCPUSet := p.machineInfo.CPUDetails.CPUs().Difference(p.reservedCPUs).Difference(pooledUnionDedicatedCPUSet)
		if reclaimPoolCPUSet.IsEmpty() {
			// for state.PoolNameReclaim, we must make them exist when the node isn't in hybrid mode even if cause overlap
			allAvailableCPUs := p.machineInfo.CPUDetails.CPUs().Difference(p.reservedCPUs)

			var tErr error
			reclaimPoolCPUSet, _, tErr = calculator.TakeByNUMABalance(p.machineInfo, allAvailableCPUs, reservedReclaimedCPUsSize)
			if tErr != nil {
				return fmt.Errorf("fallback takeByNUMABalance faild in applyBlocks for reclaimPoolCPUSet with error: %v", tErr)
			}
			general.Infof("fallback takeByNUMABalance for reclaimPoolCPUSet: %s", reclaimPoolCPUSet.String())
		}

		general.Infof("set reclaimPoolCPUSet: %s", reclaimPoolCPUSet.String())
		topologyAwareAssignments, err := machine.GetNumaAwareAssignments(p.machineInfo.CPUTopology, reclaimPoolCPUSet)
		if err != nil {
			return fmt.Errorf("unable to calculate topologyAwareAssignments for pool: %s, "+
				"result cpuset: %s, error: %v", state.PoolNameReclaim, reclaimPoolCPUSet.String(), err)
		}

		if newEntries[state.PoolNameReclaim] == nil {
			newEntries[state.PoolNameReclaim] = make(state.ContainerEntries)
		}
		newEntries[state.PoolNameReclaim][advisorapi.FakedContainerID] = &state.AllocationInfo{
			PodUid:                           state.PoolNameReclaim,
			OwnerPoolName:                    state.PoolNameReclaim,
			AllocationResult:                 reclaimPoolCPUSet.Clone(),
			OriginalAllocationResult:         reclaimPoolCPUSet.Clone(),
			TopologyAwareAssignments:         topologyAwareAssignments,
			OriginalTopologyAwareAssignments: machine.DeepcopyCPUAssignment(topologyAwareAssignments),
		}
	} else {
		general.Infof("detected reclaimPoolCPUSet: %s", newEntries[state.PoolNameReclaim][advisorapi.FakedContainerID].AllocationResult.String())
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

			// adapt to old checkpoint without RequestQuantity property
			if newEntries[podUID][containerName] != nil {
				newEntries[podUID][containerName].RequestQuantity = state.GetContainerRequestedCores(allocationInfo)
				continue
			}

			if newEntries[podUID] == nil {
				newEntries[podUID] = make(state.ContainerEntries)
			}
			newEntries[podUID][containerName] = allocationInfo.Clone()

			switch allocationInfo.QoSLevel {
			case consts.PodAnnotationQoSLevelDedicatedCores:
				errMsg := fmt.Sprintf("dedicated_cores blocks aren't applied, pod: %s/%s, container: %s",
					allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName)
				klog.Errorf(errMsg)
				return fmt.Errorf(errMsg)
			case consts.PodAnnotationQoSLevelSharedCores, consts.PodAnnotationQoSLevelReclaimedCores:
				ownerPoolName := allocationInfo.GetPoolName()
				if calculationInfo, ok := resp.GetCalculationInfo(podUID, containerName); ok {
					general.Infof("cpu advisor put pod: %s/%s, container: %s from %s to %s",
						allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName, ownerPoolName, calculationInfo.OwnerPoolName)

					ownerPoolName = calculationInfo.OwnerPoolName
				} else {
					general.Warningf("cpu advisor doesn't return entry for pod: %s/%s, container: %s, qosLevel: %s",
						allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName, allocationInfo.QoSLevel)
				}

				if allocationInfo.RampUp {
					general.Infof("pod: %s/%s container: %s is in ramp up, set its allocation result from %s to rampUpCPUs :%s",
						allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName, allocationInfo.AllocationResult.String(), rampUpCPUs.String())

					if rampUpCPUs.IsEmpty() {
						general.Warningf("rampUpCPUs is empty. pod: %s/%s container: %s reuses its allocation result: %s",
							allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName, allocationInfo.AllocationResult.String())
						continue containerLoop
					}

					newEntries[podUID][containerName].OwnerPoolName = advisorapi.EmptyOwnerPoolName
					newEntries[podUID][containerName].AllocationResult = rampUpCPUs.Clone()
					newEntries[podUID][containerName].OriginalAllocationResult = rampUpCPUs.Clone()
					newEntries[podUID][containerName].TopologyAwareAssignments = machine.DeepcopyCPUAssignment(rampUpCPUsTopologyAwareAssignments)
					newEntries[podUID][containerName].OriginalTopologyAwareAssignments = machine.DeepcopyCPUAssignment(rampUpCPUsTopologyAwareAssignments)
				} else if newEntries[ownerPoolName][advisorapi.FakedContainerID] == nil {
					errMsg := fmt.Sprintf("cpu advisor doesn't return entry for pool: %s and it's referred by pod: %s/%s, container: %s, qosLevel: %s",
						ownerPoolName, allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName, allocationInfo.QoSLevel)

					general.Errorf(errMsg)
					return fmt.Errorf(errMsg)
				} else {
					poolEntry := newEntries[ownerPoolName][advisorapi.FakedContainerID]

					general.Infof("put pod: %s/%s container: %s to pool: %s, set its allocation result from %s to %s",
						allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName, ownerPoolName, allocationInfo.AllocationResult.String(), poolEntry.AllocationResult.String())

					newEntries[podUID][containerName].OwnerPoolName = ownerPoolName
					newEntries[podUID][containerName].AllocationResult = poolEntry.AllocationResult.Clone()
					newEntries[podUID][containerName].OriginalAllocationResult = poolEntry.OriginalAllocationResult.Clone()
					newEntries[podUID][containerName].TopologyAwareAssignments = machine.DeepcopyCPUAssignment(poolEntry.TopologyAwareAssignments)
					newEntries[podUID][containerName].OriginalTopologyAwareAssignments = machine.DeepcopyCPUAssignment(poolEntry.TopologyAwareAssignments)
				}
			default:
				return fmt.Errorf("invalid qosLevel: %s for pod: %s/%s container: %s",
					allocationInfo.QoSLevel, allocationInfo.PodNamespace,
					allocationInfo.PodName, allocationInfo.ContainerName)
			}
		}
	}

	// use pod entries generated above to generate machine state info, and store in local state
	newMachineState, err := state.GenerateMachineStateFromPodEntries(p.machineInfo.CPUTopology, newEntries)
	if err != nil {
		return fmt.Errorf("calculate machineState by newPodEntries failed with error: %v", err)
	}
	p.state.SetPodEntries(newEntries)
	p.state.SetMachineState(newMachineState)

	return nil
}

// qrmGeneratedInfo returns true if the allocation info in state should be
// initialized by qrm rather than advisor
func qrmGeneratedInfo(subEntry string, info *advisorapi.CalculationInfo) bool {
	return info.OwnerPoolName == state.PoolNameDedicated || subEntry != advisorapi.FakedContainerID
}
