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
	"fmt"

	"google.golang.org/grpc"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/advisorsvc"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/cpuadvisor"
	qrmstate "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

const (
	cpuServerName string = "cpu-server"
)

type cpuServer struct {
	*baseServer
	getCheckpointCalled bool
	cpuPluginClient     cpuadvisor.CPUPluginClient
}

func NewCPUServer(recvCh chan types.InternalCPUCalculationResult, sendCh chan struct{}, conf *config.Configuration,
	metaCache metacache.MetaCache, emitter metrics.MetricEmitter) (*cpuServer, error) {
	cs := &cpuServer{}
	cs.baseServer = newBaseServer(cpuServerName, conf, recvCh, sendCh, metaCache, emitter, cs)
	cs.advisorSocketPath = conf.CPUAdvisorSocketAbsPath
	cs.pluginSocketPath = conf.CPUPluginSocketAbsPath
	cs.resourceRequestName = "CPURequest"
	return cs, nil
}

func (cs *cpuServer) RegisterAdvisorServer() {
	grpcServer := grpc.NewServer()
	cpuadvisor.RegisterCPUAdvisorServer(grpcServer, cs)
	cs.grpcServer = grpcServer
}

func (cs *cpuServer) ListAndWatch(_ *advisorsvc.Empty, server cpuadvisor.CPUAdvisor_ListAndWatchServer) error {
	_ = cs.emitter.StoreInt64(cs.genMetricsName(metricServerLWCalled), int64(cs.period.Seconds()), metrics.MetricTypeNameCount)

	if !cs.getCheckpointCalled {
		if err := cs.startToGetCheckpointFromCPUPlugin(); err != nil {
			klog.Errorf("start to get checkpoint from cpu plugin failed: %v", err)
			return err
		}
		close(cs.lwCalledChan)
		cs.getCheckpointCalled = true
	}

	recvCh, ok := cs.recvCh.(chan types.InternalCPUCalculationResult)
	if !ok {
		return fmt.Errorf("recvCh convert failed")
	}

	for {
		select {
		case <-cs.stopCh:
			klog.Infof("[qosaware-server-cpu] lw stopped because cpu server stopped")
			return nil
		case advisorResp, more := <-recvCh:
			if !more {
				klog.Infof("[qosaware-server-cpu] recv channel is closed")
				return nil
			}
			klog.Infof("[qosaware-server-cpu] get advisor update: %+v", advisorResp)

			calculationEntriesMap := make(map[string]*cpuadvisor.CalculationEntries)
			blockID2Blocks := NewBlockSet()

			cs.assemblePoolEntries(&advisorResp, calculationEntriesMap, blockID2Blocks)

			// Assemble pod entries
			f := func(podUID string, containerName string, ci *types.ContainerInfo) bool {
				if err := cs.assemblePodEntries(calculationEntriesMap, blockID2Blocks, podUID, ci); err != nil {
					klog.Errorf("[qosaware-server-cpu] assemblePodEntries err: %v", err)
				}
				return true
			}
			cs.metaCache.RangeContainer(f)

			// Send result
			if err := server.Send(&cpuadvisor.ListAndWatchResponse{Entries: calculationEntriesMap}); err != nil {
				klog.Errorf("[qosaware-server-cpu] send response failed: %v", err)
				_ = cs.emitter.StoreInt64(cs.genMetricsName(metricServerLWSendResponseFailed), int64(cs.period.Seconds()), metrics.MetricTypeNameCount)
				return err
			}
			klog.Infof("[qosaware-server-cpu] send calculation result: %v", general.ToString(calculationEntriesMap))
			_ = cs.emitter.StoreInt64(cs.genMetricsName(metricServerLWSendResponseSucceeded), int64(cs.period.Seconds()), metrics.MetricTypeNameCount)
		}
	}
}

func (cs *cpuServer) getCheckpoint() {
	// get checkpoint
	resp, err := cs.cpuPluginClient.GetCheckpoint(context.Background(), &cpuadvisor.GetCheckpointRequest{})
	if err != nil {
		klog.Errorf("[qosaware-server-cpu] get checkpoint failed: %v", err)
		_ = cs.emitter.StoreInt64(cs.genMetricsName(metricServerLWGetCheckpointFailed), int64(cs.period.Seconds()), metrics.MetricTypeNameCount)
		return
	} else if resp == nil {
		klog.Errorf("[qosaware-server-cpu] get nil checkpoint")
		_ = cs.emitter.StoreInt64(cs.genMetricsName(metricServerLWGetCheckpointFailed), int64(cs.period.Seconds()), metrics.MetricTypeNameCount)
		return
	}
	klog.Infof("[qosaware-server-cpu] get checkpoint: %v", general.ToString(resp.Entries))
	_ = cs.emitter.StoreInt64(cs.genMetricsName(metricServerLWGetCheckpointSucceeded), int64(cs.period.Seconds()), metrics.MetricTypeNameCount)

	livingPoolNameSet := sets.NewString()

	// parse pool entries first, which are needed for parsing container entries
	for entryName, entry := range resp.Entries {
		if poolInfo, ok := entry.Entries[cpuadvisor.FakedContainerName]; ok {
			poolName := entryName
			livingPoolNameSet.Insert(poolName)
			if err := cs.updatePoolInfo(poolName, poolInfo); err != nil {
				klog.Errorf("[qosaware-server-cpu] update pool info with error: %v", err)
			}
		}
	}

	// parse container entries after pool entries
	for entryName, entry := range resp.Entries {
		if _, ok := entry.Entries[cpuadvisor.FakedContainerName]; !ok {
			podUID := entryName
			for containerName, info := range entry.Entries {
				if err := cs.updateContainerInfo(podUID, containerName, info); err != nil {
					klog.Errorf("[qosaware-server-cpu] update container info with error: %v", err)
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
	})

	// GC pool entries
	_ = cs.metaCache.GCPoolEntries(livingPoolNameSet)

	// Trigger advisor update
	cs.sendCh <- struct{}{}
}

func (cs *cpuServer) startToGetCheckpointFromCPUPlugin() error {
	if !general.IsPathExists(cs.pluginSocketPath) {
		_ = cs.emitter.StoreInt64(cs.genMetricsName(metricServerLWGetCheckpointFailed), int64(cs.period.Seconds()), metrics.MetricTypeNameCount)
		return fmt.Errorf("cpu plugin socket %v doesn't exist", cs.pluginSocketPath)
	}

	conn, err := cs.dial(cs.pluginSocketPath, cs.period)
	if err != nil {
		klog.Errorf("dial cpu plugin socket %v failed: %v", cs.pluginSocketPath, err)
		_ = cs.emitter.StoreInt64(cs.genMetricsName(metricServerLWGetCheckpointFailed), int64(cs.period.Seconds()), metrics.MetricTypeNameCount)
		return err
	}

	cs.cpuPluginClient = cpuadvisor.NewCPUPluginClient(conn)
	go wait.Until(cs.getCheckpoint, cs.period, cs.stopCh)

	return nil
}

func (cs *cpuServer) updatePoolInfo(poolName string, info *cpuadvisor.AllocationInfo) error {
	pi, ok := cs.metaCache.GetPoolInfo(poolName)
	if !ok {
		pi = &types.PoolInfo{
			PoolName: info.OwnerPoolName,
		}
	}
	pi.TopologyAwareAssignments = machine.TransformCPUAssignmentFormat(info.TopologyAwareAssignments)
	pi.OriginalTopologyAwareAssignments = machine.TransformCPUAssignmentFormat(info.OriginalTopologyAwareAssignments)

	return cs.metaCache.SetPoolInfo(poolName, pi)
}

func (cs *cpuServer) updateContainerInfo(podUID string, containerName string, info *cpuadvisor.AllocationInfo) error {
	ci, ok := cs.metaCache.GetContainerInfo(podUID, containerName)
	if !ok {
		return fmt.Errorf("container %v/%v not exist", podUID, containerName)
	}

	ci.RampUp = info.RampUp
	ci.TopologyAwareAssignments = machine.TransformCPUAssignmentFormat(info.TopologyAwareAssignments)
	ci.OriginalTopologyAwareAssignments = machine.TransformCPUAssignmentFormat(info.OriginalTopologyAwareAssignments)

	// only reset pool-name according to QRM during starting process
	if len(ci.OriginOwnerPoolName) == 0 {
		ci.OriginOwnerPoolName = info.OwnerPoolName
	}
	if len(ci.OwnerPoolName) == 0 {
		ci.OwnerPoolName = info.OwnerPoolName
	}

	// fill in topology aware assignment for containers with owner pool
	if ci.QoSLevel != consts.PodAnnotationQoSLevelDedicatedCores {
		if len(ci.OwnerPoolName) > 0 {
			if poolInfo, ok := cs.metaCache.GetPoolInfo(ci.OwnerPoolName); ok {
				ci.TopologyAwareAssignments = poolInfo.TopologyAwareAssignments.Clone()
			}
		}
	}

	// Need to set back because of deep copy
	return cs.metaCache.SetContainerInfo(podUID, containerName, ci)
}

// assemblePoolEntries fills up calculationEntriesMap and blockSet based on cpu.InternalCPUCalculationResult
// - for each [pool, numa] set, there exists a new Block (and corresponding internalBlock)
func (cs *cpuServer) assemblePoolEntries(advisorResp *types.InternalCPUCalculationResult, calculationEntriesMap map[string]*cpuadvisor.CalculationEntries, bs blockSet) {
	for poolName, entries := range advisorResp.PoolEntries {
		poolEntry := NewPoolCalculationEntries(poolName)
		for numaID, size := range entries {
			block := NewBlock(uint64(size), "")
			numaCalculationResult := &cpuadvisor.NumaCalculationResult{Blocks: []*cpuadvisor.Block{block}}

			innerBlock := NewInnerBlock(block, int64(numaID), poolName, nil, numaCalculationResult)
			innerBlock.join(block.BlockId, bs)

			poolEntry.Entries[cpuadvisor.FakedContainerName].CalculationResultsByNumas[int64(numaID)] = numaCalculationResult
		}
		calculationEntriesMap[poolName] = poolEntry
	}
}

// assemblePoolEntries fills up calculationEntriesMap and blockSet based on types.ContainerInfo
func (cs *cpuServer) assemblePodEntries(calculationEntriesMap map[string]*cpuadvisor.CalculationEntries,
	bs blockSet, podUID string, ci *types.ContainerInfo) error {
	calculationInfo := &cpuadvisor.CalculationInfo{
		OwnerPoolName:             ci.OwnerPoolName,
		CalculationResultsByNumas: nil,
	}

	if ci.Isolated {
		if ci.RegionNames.Len() != 1 {
			return fmt.Errorf("isolated container should be in only one region")
		}
		calculationInfo.OwnerPoolName = ci.RegionNames.List()[0]
	}

	// currently, only pods in "dedicated_nums with numa binding" has topology aware allocations
	if ci.IsNumaBinding() {
		calculationResultsByNumas := make(map[int64]*cpuadvisor.NumaCalculationResult)

		for numaID, cpuset := range ci.TopologyAwareAssignments {
			numaCalculationResult := &cpuadvisor.NumaCalculationResult{Blocks: []*cpuadvisor.Block{}}

			// the same podUID appears twice iff there exists multiple containers in one pod;
			// in this case, reuse the same blocks as the last container.
			// i.e. sidecar container will always follow up with the main container.
			if podEntries, ok := calculationEntriesMap[podUID]; ok {
				for _, entry := range podEntries.Entries {
					if result, ok := entry.CalculationResultsByNumas[int64(numaID)]; ok {
						for _, block := range result.Blocks {
							newBlock := NewBlock(block.Result, block.BlockId)
							newInnerBlock := NewInnerBlock(newBlock, int64(numaID), "", ci, numaCalculationResult)
							numaCalculationResult.Blocks = append(numaCalculationResult.Blocks, newBlock)
							newInnerBlock.join(block.BlockId, bs)
						}
					}
				}
			} else {
				// if this podUID appears firstly, we should generate a new Block

				reclaimPoolCalculationResults, ok := getNumaCalculationResult(calculationEntriesMap, qrmstate.PoolNameReclaim,
					cpuadvisor.FakedContainerName, int64(numaID))
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
