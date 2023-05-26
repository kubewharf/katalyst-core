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

package cpu

import (
	"context"
	"fmt"
	"net"
	"os"
	"path"
	"time"

	"google.golang.org/grpc"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/cpuadvisor"
	qrmstate "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/cpu"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

const (
	cpuServerName string = "cpu-server"
)

// Metric names for cpu server
const (
	metricCPUServerStartCalled              = "cpuserver_start_called"
	metricCPUServerStopCalled               = "cpuserver_stop_called"
	metricCPUServerAddContainerCalled       = "cpuserver_add_container_called"
	metricCPUServerRemovePodCalled          = "cpuserver_remove_pod_called"
	metricCPUServerLWCalled                 = "cpuserver_lw_called"
	metricCPUServerLWGetCheckpointFailed    = "cpuserver_lw_get_checkpoint_failed"
	metricCPUServerLWGetCheckpointSucceeded = "cpuserver_lw_get_checkpoint_succeeded"
	metricCPUServerLWSendResponseFailed     = "cpuserver_lw_send_response_failed"
	metricCPUServerLWSendResponseSucceeded  = "cpuserver_lw_send_response_succeeded"
)

type cpuServer struct {
	name                 string
	period               time.Duration
	cpuAdvisorSocketPath string
	cpuPluginSocketPath  string
	recvCh               chan cpu.InternalCalculationResult
	sendCh               chan struct{}
	lwCalledChan         chan struct{}
	stopCh               chan struct{}
	getCheckpointCalled  bool
	cpuPluginClient      cpuadvisor.CPUPluginClient

	metaCache metacache.MetaCache
	emitter   metrics.MetricEmitter

	server *grpc.Server
	cpuadvisor.UnimplementedCPUAdvisorServer
}

func NewCPUServer(recvCh chan cpu.InternalCalculationResult, sendCh chan struct{}, conf *config.Configuration,
	metaCache metacache.MetaCache, emitter metrics.MetricEmitter) (*cpuServer, error) {
	return &cpuServer{
		name:                 cpuServerName,
		period:               conf.QoSAwarePluginConfiguration.SyncPeriod,
		cpuAdvisorSocketPath: conf.CPUAdvisorSocketAbsPath,
		cpuPluginSocketPath:  conf.CPUPluginSocketAbsPath,
		recvCh:               recvCh,
		sendCh:               sendCh,
		lwCalledChan:         make(chan struct{}),
		stopCh:               make(chan struct{}),
		metaCache:            metaCache,
		emitter:              emitter,
	}, nil
}

func (cs *cpuServer) Name() string {
	return cs.name
}

func (cs *cpuServer) Start() error {
	_ = cs.emitter.StoreInt64(metricCPUServerStartCalled, int64(cs.period.Seconds()), metrics.MetricTypeNameCount)

	if err := cs.serve(); err != nil {
		klog.Errorf("[qosaware-server-cpu] start cpu server failed: %v", err)
		_ = cs.Stop()
		return err
	}
	klog.Infof("[qosaware-server-cpu] started cpu server")

	go func() {
		for {
			select {
			case <-cs.lwCalledChan:
				return
			case <-cs.stopCh:
				return
			}
		}
	}()

	return nil
}

func (cs *cpuServer) Stop() error {
	close(cs.stopCh)
	_ = cs.emitter.StoreInt64(metricCPUServerStopCalled, int64(cs.period.Seconds()), metrics.MetricTypeNameCount)

	if cs.server != nil {
		cs.server.Stop()
		klog.Infof("[qosaware-server-cpu] stopped cpu server")
	}

	if err := os.Remove(cs.cpuAdvisorSocketPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove %v failed: %v", cs.cpuAdvisorSocketPath, err)
	}

	return nil
}

func (cs *cpuServer) AddContainer(ctx context.Context, request *cpuadvisor.AddContainerRequest) (*cpuadvisor.AddContainerResponse, error) {
	_ = cs.emitter.StoreInt64(metricCPUServerAddContainerCalled, int64(cs.period.Seconds()), metrics.MetricTypeNameCount)

	if request == nil {
		klog.Errorf("[qosaware-server-cpu] get add container request nil")
		return nil, fmt.Errorf("add container request nil")
	}
	klog.Infof("[qosaware-server-cpu] get add container request: %v", general.ToString(request))

	err := cs.addContainer(request)
	if err != nil {
		klog.Errorf("[qosaware-server-cpu] add container with error: %v", err)
	}

	return &cpuadvisor.AddContainerResponse{}, err
}

func (cs *cpuServer) RemovePod(ctx context.Context, request *cpuadvisor.RemovePodRequest) (*cpuadvisor.RemovePodResponse, error) {
	_ = cs.emitter.StoreInt64(metricCPUServerRemovePodCalled, int64(cs.period.Seconds()), metrics.MetricTypeNameCount)

	if request == nil {
		return nil, fmt.Errorf("remove pod request is nil")
	}
	klog.Infof("[qosaware-server-cpu] get remove pod request: %v", request.PodUid)

	err := cs.removePod(request.PodUid)
	if err != nil {
		klog.Errorf("[qosaware-server-cpu] remove pod with error: %v", err)
	}

	return &cpuadvisor.RemovePodResponse{}, err
}

func (cs *cpuServer) ListAndWatch(empty *cpuadvisor.Empty, server cpuadvisor.CPUAdvisor_ListAndWatchServer) error {
	_ = cs.emitter.StoreInt64(metricCPUServerLWCalled, int64(cs.period.Seconds()), metrics.MetricTypeNameCount)

	if !cs.getCheckpointCalled {
		if err := cs.startToGetCheckpointFromCPUPlugin(); err != nil {
			klog.Errorf("start to get checkpoint from cpu plugin failed: %v", err)
			return err
		}
		close(cs.lwCalledChan)
		cs.getCheckpointCalled = true
	}

	for {
		select {
		case <-cs.stopCh:
			klog.Infof("[qosaware-server-cpu] lw stopped because cpu server stopped")
			return nil
		case advisorResp, more := <-cs.recvCh:
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
				_ = cs.emitter.StoreInt64(metricCPUServerLWSendResponseFailed, int64(cs.period.Seconds()), metrics.MetricTypeNameCount)
				return err
			}
			klog.Infof("[qosaware-server-cpu] send calculation result: %v", general.ToString(calculationEntriesMap))
			_ = cs.emitter.StoreInt64(metricCPUServerLWSendResponseSucceeded, int64(cs.period.Seconds()), metrics.MetricTypeNameCount)
		}
	}
}

func (cs *cpuServer) getCheckpoint() {
	// Get checkpoint
	resp, err := cs.cpuPluginClient.GetCheckpoint(context.Background(), &cpuadvisor.GetCheckpointRequest{})
	if err != nil {
		klog.Errorf("[qosaware-server-cpu] get checkpoint failed: %v", err)
		_ = cs.emitter.StoreInt64(metricCPUServerLWGetCheckpointFailed, int64(cs.period.Seconds()), metrics.MetricTypeNameCount)
		return
	} else if resp == nil {
		klog.Errorf("[qosaware-server-cpu] get nil checkpoint")
		_ = cs.emitter.StoreInt64(metricCPUServerLWGetCheckpointFailed, int64(cs.period.Seconds()), metrics.MetricTypeNameCount)
		return
	}
	klog.Infof("[qosaware-server-cpu] get checkpoint: %v", general.ToString(resp.Entries))
	_ = cs.emitter.StoreInt64(metricCPUServerLWGetCheckpointSucceeded, int64(cs.period.Seconds()), metrics.MetricTypeNameCount)

	livingPoolNameSet := sets.NewString()

	// Parse checkpoint
	for entryName, entry := range resp.Entries {
		if poolInfo, ok := entry.Entries[cpuadvisor.FakedContainerName]; ok {
			// Update pool info
			poolName := entryName
			livingPoolNameSet.Insert(poolName)
			if err := cs.updatePoolInfo(poolName, poolInfo); err != nil {
				klog.Errorf("[qosaware-server-cpu] update pool info with error: %v", err)
			}
		} else {
			// Update container info
			podUID := entryName
			for containerName, info := range entry.Entries {
				if err := cs.updateContainerInfo(podUID, containerName, info); err != nil {
					klog.Errorf("[qosaware-server-cpu] update container info with error: %v", err)
				}
			}
		}
	}

	// clean up the containers not existed in resp.Entries
	cs.metaCache.RangeAndDeleteContainer(func(containerInfo *types.ContainerInfo) bool {
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
	if err := cs.metaCache.GCPoolEntries(livingPoolNameSet); err != nil {
		klog.Errorf("[qosaware-server-cpu] gc pool entries with error: %v", err)
	}

	// Trigger advisor update
	cs.sendCh <- struct{}{}
}

func (cs *cpuServer) startToGetCheckpointFromCPUPlugin() error {
	if !general.IsPathExists(cs.cpuPluginSocketPath) {
		_ = cs.emitter.StoreInt64(metricCPUServerLWGetCheckpointFailed, int64(cs.period.Seconds()), metrics.MetricTypeNameCount)
		return fmt.Errorf("cpu plugin socket %v doesn't exist", cs.cpuPluginSocketPath)
	}

	conn, err := cs.dial(cs.cpuPluginSocketPath, cs.period)
	if err != nil {
		klog.Errorf("dial cpu plugin socket %v failed: %v", cs.cpuPluginSocketPath, err)
		_ = cs.emitter.StoreInt64(metricCPUServerLWGetCheckpointFailed, int64(cs.period.Seconds()), metrics.MetricTypeNameCount)
		return err
	}

	cs.cpuPluginClient = cpuadvisor.NewCPUPluginClient(conn)
	go wait.Until(cs.getCheckpoint, cs.period, cs.stopCh)

	return nil
}

func (cs *cpuServer) serve() error {
	cpuAdvisorSocketDir := path.Dir(cs.cpuAdvisorSocketPath)

	err := general.EnsureDirectory(cpuAdvisorSocketDir)
	if err != nil {
		return fmt.Errorf("ensure cpuAdvisorSocketDir: %s failed with error: %v",
			cpuAdvisorSocketDir, err)
	}

	klog.Infof("[qosaware-server-cpu] ensure cpuAdvisorSocketDir: %s successfully", cpuAdvisorSocketDir)

	if err := os.Remove(cs.cpuAdvisorSocketPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove %v failed: %v", cs.cpuAdvisorSocketPath, err)
	}

	sock, err := net.Listen("unix", cs.cpuAdvisorSocketPath)
	if err != nil {
		return fmt.Errorf("listen %s failed: %v", cs.cpuAdvisorSocketPath, err)
	}

	klog.Infof("[qosaware-server-cpu] listen at: %s successfully", cs.cpuAdvisorSocketPath)

	grpcServer := grpc.NewServer()
	cpuadvisor.RegisterCPUAdvisorServer(grpcServer, cs)
	cs.server = grpcServer

	go func() {
		lastCrashTime := time.Now()
		restartCount := 0
		for {
			klog.Infof("[qosaware-server-cpu] starting grpc server at %v", cs.cpuAdvisorSocketPath)
			if err := grpcServer.Serve(sock); err == nil {
				break
			}
			klog.Errorf("[qosaware-server-cpu] grpc server at %v crashed: %v", cs.cpuAdvisorSocketPath, err)

			if restartCount > 5 {
				klog.Errorf("[qosaware-server-cpu] grpc server at %v has crashed repeatedly recently, quit", cs.cpuAdvisorSocketPath)
				os.Exit(0)
			}
			timeSinceLastCrash := time.Since(lastCrashTime).Seconds()
			lastCrashTime = time.Now()
			if timeSinceLastCrash > 3600 {
				restartCount = 1
			} else {
				restartCount++
			}
		}
	}()

	if conn, err := cs.dial(cs.cpuAdvisorSocketPath, cs.period); err != nil {
		return fmt.Errorf("dial check at %v failed: %v", cs.cpuAdvisorSocketPath, err)
	} else {
		_ = conn.Close()
	}

	return nil
}

// nolint
func (cs *cpuServer) dial(unixSocketPath string, timeout time.Duration) (*grpc.ClientConn, error) {
	c, err := grpc.Dial(unixSocketPath, grpc.WithInsecure(), grpc.WithBlock(),
		grpc.WithTimeout(timeout),
		grpc.WithDialer(func(addr string, timeout time.Duration) (net.Conn, error) {
			return net.DialTimeout("unix", addr, timeout)
		}),
	)

	if err != nil {
		return nil, err
	}

	return c, nil
}

func (cs *cpuServer) addContainer(request *cpuadvisor.AddContainerRequest) error {
	ci := &types.ContainerInfo{
		PodUID:         request.PodUid,
		PodNamespace:   request.PodNamespace,
		PodName:        request.PodName,
		ContainerName:  request.ContainerName,
		ContainerType:  request.ContainerType,
		ContainerIndex: int(request.ContainerIndex),
		Labels:         request.Labels,
		Annotations:    request.Annotations,
		QoSLevel:       request.QosLevel,
		CPURequest:     float64(request.RequestQuantity),
	}

	if err := cs.metaCache.AddContainer(request.PodUid, request.ContainerName, ci); err != nil {
		// Try to delete container info in both memory and state file if add container returns error
		_ = cs.metaCache.DeleteContainer(request.PodUid, request.ContainerName)
		return err
	}

	return nil
}

func (cs *cpuServer) removePod(podUID string) error {
	return cs.metaCache.RemovePod(podUID)
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
	ci.OwnerPoolName = info.OwnerPoolName
	ci.TopologyAwareAssignments = machine.TransformCPUAssignmentFormat(info.TopologyAwareAssignments)
	ci.OriginalTopologyAwareAssignments = machine.TransformCPUAssignmentFormat(info.OriginalTopologyAwareAssignments)

	// Need to set back because of deep copy
	return cs.metaCache.SetContainerInfo(podUID, containerName, ci)
}

// assemblePoolEntries fills up calculationEntriesMap and blockSet based on cpu.InternalCalculationResult
// - for each [pool, numa] set, there exists a new Block (and corresponding internalBlock)
func (cs *cpuServer) assemblePoolEntries(advisorResp *cpu.InternalCalculationResult, calculationEntriesMap map[string]*cpuadvisor.CalculationEntries, bs blockSet) {
	for poolName, entries := range advisorResp.PoolEntries {
		poolEntry := NewPoolCalculationEntries(poolName)
		for numaID, size := range entries {
			block := NewBlock(uint64(size.Value()), "")
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

				reclaimPoolCalculationResults, ok := GetNumaCalculationResult(calculationEntriesMap, qrmstate.PoolNameReclaim,
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
