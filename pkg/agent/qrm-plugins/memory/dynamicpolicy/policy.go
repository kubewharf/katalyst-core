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
	"math"
	"sync"
	"time"

	info "github.com/google/cadvisor/info/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-api/pkg/plugins/skeleton"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/agent"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/memory/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	cgroupcmutils "github.com/kubewharf/katalyst-core/pkg/util/cgroup/manager"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

const (
	memoryPluginStateFileName             = "memory_plugin_state"
	MemoryResourcePluginPolicyNameDynamic = "dynamic"
)

const (
	memsetCheckPeriod = 10 * time.Second
	stateCheckPeriod  = 30 * time.Second
	maxResidualTime   = 5 * time.Minute
)

var (
	readonlyStateLock sync.RWMutex
	readonlyState     state.ReadonlyState
)

type DynamicPolicy struct {
	sync.RWMutex

	stopCh    chan struct{}
	started   bool
	qosConfig *generic.QoSConfiguration

	// emitter is used to emit metrics.
	emitter metrics.MetricEmitter
	// metaGetter is used to collect metadata universal metaServer.
	metaServer *metaserver.MetaServer

	topology *machine.CPUTopology
	state    state.State

	migrateMemoryLock sync.Mutex
	migratingMemory   map[string]map[string]bool
	residualHitMap    map[string]int64

	allocationHandlers map[string]util.AllocationHandler
	hintHandlers       map[string]util.HintHandler

	extraStateFileAbsPath string
	name                  string
}

func NewDynamicPolicy(agentCtx *agent.GenericContext, conf *config.Configuration, _ interface{}, agentName string) (bool, agent.Component, error) {
	reservedMemory, err := getReservedMemory(conf.ReservedMemoryGB, agentCtx.MachineInfo)
	if err != nil {
		return false, agent.ComponentStub{}, fmt.Errorf("getReservedMemoryFromOptions failed with error: %v", err)
	}

	resourcesReservedMemory := map[v1.ResourceName]map[int]uint64{
		v1.ResourceMemory: reservedMemory,
	}
	stateImpl, err := state.NewCheckpointState(conf.GenericQRMPluginConfiguration.StateFileDirectory, memoryPluginStateFileName,
		MemoryResourcePluginPolicyNameDynamic, agentCtx.CPUTopology, agentCtx.MachineInfo, resourcesReservedMemory, conf.SkipMemoryStateCorruption)
	if err != nil {
		return false, agent.ComponentStub{}, fmt.Errorf("NewCheckpointState failed with error: %v", err)
	}

	readonlyStateLock.Lock()
	readonlyState = stateImpl
	readonlyStateLock.Unlock()

	wrappedEmitter := agentCtx.EmitterPool.GetDefaultMetricsEmitter().WithTags(agentName, metrics.MetricTag{
		Key: util.QRMPluginPolicyTagName,
		Val: MemoryResourcePluginPolicyNameDynamic,
	})

	policyImplement := &DynamicPolicy{
		topology:              agentCtx.CPUTopology,
		qosConfig:             conf.QoSConfiguration,
		emitter:               wrappedEmitter,
		metaServer:            agentCtx.MetaServer,
		state:                 stateImpl,
		stopCh:                make(chan struct{}),
		migratingMemory:       make(map[string]map[string]bool),
		residualHitMap:        make(map[string]int64),
		extraStateFileAbsPath: conf.ExtraStateFileAbsPath,
		name:                  fmt.Sprintf("%s_%s", agentName, MemoryResourcePluginPolicyNameDynamic),
	}

	policyImplement.allocationHandlers = map[string]util.AllocationHandler{
		apiconsts.PodAnnotationQoSLevelSharedCores:    policyImplement.sharedCoresAllocationHandler,
		apiconsts.PodAnnotationQoSLevelDedicatedCores: policyImplement.dedicatedCoresAllocationHandler,
		apiconsts.PodAnnotationQoSLevelReclaimedCores: policyImplement.reclaimedCoresAllocationHandler,
	}

	policyImplement.hintHandlers = map[string]util.HintHandler{
		apiconsts.PodAnnotationQoSLevelSharedCores:    policyImplement.sharedCoresHintHandler,
		apiconsts.PodAnnotationQoSLevelDedicatedCores: policyImplement.dedicatedCoresHintHandler,
		apiconsts.PodAnnotationQoSLevelReclaimedCores: policyImplement.reclaimedCoresHintHandler,
	}

	pluginWrapper, err := skeleton.NewRegistrationPluginWrapper(
		policyImplement,
		conf.QRMPluginSocketDirs, nil)
	if err != nil {
		return false, agent.ComponentStub{}, fmt.Errorf("dynamic policy new plugin wrapper failed with error: %v", err)
	}

	return true, &agent.PluginWrapper{GenericPlugin: pluginWrapper}, nil
}

func (p *DynamicPolicy) Start() (err error) {
	klog.Infof("MemoryDynamicPolicy start called")

	p.Lock()

	defer func() {
		if err == nil {
			p.started = true
		}

		p.Unlock()
	}()

	if p.started {
		klog.Infof("[MemoryDynamicPolicy.Start] DynamicPolicy is already started")
		return nil
	}

	p.stopCh = make(chan struct{})

	go wait.Until(func() {
		_ = p.emitter.StoreInt64(util.MetricNameHeartBeat, 1, metrics.MetricTypeNameRaw)
	}, time.Second*30, p.stopCh)

	go wait.Until(p.clearResidualState, stateCheckPeriod, p.stopCh)
	go wait.Until(p.checkMemorySet, memsetCheckPeriod, p.stopCh)
	go wait.Until(p.setMemoryMigrate, 5*time.Second, p.stopCh)

	return nil
}

func (p *DynamicPolicy) Stop() error {
	p.Lock()
	defer func() {
		p.started = false
		p.Unlock()

		klog.Infof("[MemoryDynamicPolicy.Stop] DynamicPolicy stopped")
	}()

	if !p.started {
		klog.Warningf("[MemoryDynamicPolicy.Stop] DynamicPolicy already stopped")
		return nil
	}
	close(p.stopCh)
	return nil
}

func (p *DynamicPolicy) Name() string {
	return p.name
}

func (p *DynamicPolicy) ResourceName() string {
	return string(v1.ResourceMemory)
}

// GetTopologyHints returns hints of corresponding resources
func (p *DynamicPolicy) GetTopologyHints(ctx context.Context, req *pluginapi.ResourceRequest) (*pluginapi.ResourceHintsResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("GetTopologyHints got nil req")
	}

	qosLevel, err := util.GetKatalystQoSLevelFromResourceReq(p.qosConfig, req)
	if err != nil {
		err = fmt.Errorf("GetKatalystQoSLevelFromResourceReq for pod: %s/%s, container: %s failed with error: %v",
			req.PodNamespace, req.PodName, req.ContainerName, err)
		klog.Errorf("[MemoryDynamicPolicy.GetTopologyHints] %s", err.Error())
		return nil, err
	}

	reqInt, err := getReqQuantityFromResourceReq(req)
	if err != nil {
		return nil, fmt.Errorf("getReqQuantityFromResourceReq failed with error: %v", err)
	}

	klog.InfoS("[MemoryDynamicPolicy] GetTopologyHints is called",
		"podNamespace", req.PodNamespace,
		"podName", req.PodName,
		"containerName", req.ContainerName,
		"podType", req.PodType,
		"podRole", req.PodRole,
		"qosLevel", qosLevel,
		"memoryReq(bytes)", reqInt)

	p.RLock()
	defer func() {
		p.RUnlock()

		if err != nil {
			_ = p.emitter.StoreInt64(util.MetricNameGetTopologyHintsFailed, 1, metrics.MetricTypeNameRaw)
		}
	}()

	if req.ContainerType == pluginapi.ContainerType_INIT {
		return util.PackResourceHintsResponse(req, string(v1.ResourceMemory), map[string]*pluginapi.ListOfTopologyHints{
			string(v1.ResourceMemory): nil,
		})
	}

	if p.hintHandlers[qosLevel] == nil {
		return nil, fmt.Errorf("katalyst QoS level: %s is not supported yet", qosLevel)
	}

	return p.hintHandlers[qosLevel](ctx, req)
}

func (p *DynamicPolicy) RemovePod(ctx context.Context, req *pluginapi.RemovePodRequest) (*pluginapi.RemovePodResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("RemovePod got nil req")
	}

	p.Lock()
	defer p.Unlock()

	err := p.removePod(req.PodUid)
	if err != nil {
		klog.ErrorS(err, "[MemoryDynamicPolicy.RemovePod] remove pod failed with error", "podUID", req.PodUid)
		return nil, err
	}

	err = p.adjustAllocationEntries()
	if err != nil {
		klog.ErrorS(err, "[MemoryDynamicPolicy.RemovePod] adjustAllocationEntries failed", "podUID", req.PodUid)
	}

	return &pluginapi.RemovePodResponse{}, nil
}

// GetResourcesAllocation returns allocation results of corresponding resources
func (p *DynamicPolicy) GetResourcesAllocation(ctx context.Context, req *pluginapi.GetResourcesAllocationRequest) (*pluginapi.GetResourcesAllocationResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("GetResourcesAllocation got nil req")
	}

	p.RLock()
	defer p.RUnlock()

	podResources := make(map[string]*pluginapi.ContainerResources)

	podEntries := p.state.GetPodResourceEntries()[v1.ResourceMemory]
	for podUID, containerEntries := range podEntries {
		if podResources[podUID] == nil {
			podResources[podUID] = &pluginapi.ContainerResources{}
		}

		for containerName, allocationInfo := range containerEntries {
			if allocationInfo == nil {
				continue
			}

			if podResources[podUID].ContainerResources == nil {
				podResources[podUID].ContainerResources = make(map[string]*pluginapi.ResourceAllocation)
			}

			podResources[podUID].ContainerResources[containerName] = &pluginapi.ResourceAllocation{
				ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{
					string(v1.ResourceMemory): {
						OciPropertyName:   util.OCIPropertyNameCPUSetMems,
						IsNodeResource:    false,
						IsScalarResource:  true,
						AllocatedQuantity: float64(allocationInfo.AggregatedQuantity),
						AllocationResult:  allocationInfo.NumaAllocationResult.String(),
					},
				},
			}
		}
	}

	return &pluginapi.GetResourcesAllocationResponse{
		PodResources: podResources,
	}, nil
}

// GetTopologyAwareResources returns allocation results of corresponding resources as topology aware format
func (p *DynamicPolicy) GetTopologyAwareResources(ctx context.Context, req *pluginapi.GetTopologyAwareResourcesRequest) (*pluginapi.GetTopologyAwareResourcesResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("GetTopologyAwareResources got nil req")
	}

	p.RLock()
	defer p.RUnlock()

	allocationInfo := p.state.GetAllocationInfo(v1.ResourceMemory, req.PodUid, req.ContainerName)
	if allocationInfo == nil {
		return nil, fmt.Errorf("pod: %s, container: %s is not show up in memory plugin state", req.PodUid, req.ContainerName)
	}

	topologyAwareQuantityList := util.GetTopologyAwareQuantityFromAssignmentsSize(allocationInfo.TopologyAwareAllocations)
	resp := &pluginapi.GetTopologyAwareResourcesResponse{
		PodUid:       allocationInfo.PodUid,
		PodName:      allocationInfo.PodName,
		PodNamespace: allocationInfo.PodNamespace,
		ContainerTopologyAwareResources: &pluginapi.ContainerTopologyAwareResources{
			ContainerName: allocationInfo.ContainerName,
		},
	}

	if allocationInfo.ContainerType == pluginapi.ContainerType_SIDECAR.String() {
		resp.ContainerTopologyAwareResources.AllocatedResources = map[string]*pluginapi.TopologyAwareResource{
			string(v1.ResourceMemory): {
				IsNodeResource:                    false,
				IsScalarResource:                  true,
				AggregatedQuantity:                0,
				OriginalAggregatedQuantity:        0,
				TopologyAwareQuantityList:         nil,
				OriginalTopologyAwareQuantityList: nil,
			},
		}
	} else {
		resp.ContainerTopologyAwareResources.AllocatedResources = map[string]*pluginapi.TopologyAwareResource{
			string(v1.ResourceMemory): {
				IsNodeResource:                    false,
				IsScalarResource:                  true,
				AggregatedQuantity:                float64(allocationInfo.AggregatedQuantity),
				OriginalAggregatedQuantity:        float64(allocationInfo.AggregatedQuantity),
				TopologyAwareQuantityList:         topologyAwareQuantityList,
				OriginalTopologyAwareQuantityList: topologyAwareQuantityList,
			},
		}
	}

	return resp, nil
}

// GetTopologyAwareAllocatableResources returns corresponding allocatable resources as topology aware format
func (p *DynamicPolicy) GetTopologyAwareAllocatableResources(ctx context.Context, req *pluginapi.GetTopologyAwareAllocatableResourcesRequest) (*pluginapi.GetTopologyAwareAllocatableResourcesResponse, error) {
	p.RLock()
	defer p.RUnlock()

	machineState := p.state.GetMachineState()[v1.ResourceMemory]

	numaNodes := p.topology.CPUDetails.NUMANodes().ToSliceInt()
	topologyAwareAllocatableQuantityList := make([]*pluginapi.TopologyAwareQuantity, 0, len(machineState))
	topologyAwareCapacityQuantityList := make([]*pluginapi.TopologyAwareQuantity, 0, len(machineState))

	var aggregatedAllocatableQuantity, aggregatedCapacityQuantity uint64 = 0, 0
	for _, numaNode := range numaNodes {
		numaNodeState := machineState[numaNode]

		if numaNodeState == nil {
			return nil, fmt.Errorf("nil numaNodeState for NUMA: %d", numaNode)
		}

		topologyAwareAllocatableQuantityList = append(topologyAwareAllocatableQuantityList, &pluginapi.TopologyAwareQuantity{
			ResourceValue: float64(numaNodeState.Allocatable),
			Node:          uint64(numaNode),
		})
		topologyAwareCapacityQuantityList = append(topologyAwareCapacityQuantityList, &pluginapi.TopologyAwareQuantity{
			ResourceValue: float64(numaNodeState.TotalMemSize),
			Node:          uint64(numaNode),
		})
		aggregatedAllocatableQuantity += numaNodeState.Allocatable
		aggregatedCapacityQuantity += numaNodeState.TotalMemSize
	}

	return &pluginapi.GetTopologyAwareAllocatableResourcesResponse{
		AllocatableResources: map[string]*pluginapi.AllocatableTopologyAwareResource{
			string(v1.ResourceMemory): {
				IsNodeResource:                       false,
				IsScalarResource:                     true,
				AggregatedAllocatableQuantity:        float64(aggregatedAllocatableQuantity),
				TopologyAwareAllocatableQuantityList: topologyAwareAllocatableQuantityList,
				AggregatedCapacityQuantity:           float64(aggregatedCapacityQuantity),
				TopologyAwareCapacityQuantityList:    topologyAwareCapacityQuantityList,
			},
		},
	}, nil
}

// GetResourcePluginOptions returns options to be communicated with Resource Manager
func (p *DynamicPolicy) GetResourcePluginOptions(context.Context, *pluginapi.Empty) (*pluginapi.ResourcePluginOptions, error) {
	return &pluginapi.ResourcePluginOptions{PreStartRequired: false,
		WithTopologyAlignment: true,
		NeedReconcile:         true}, nil
}

// Allocate is called during pod admit so that the resource
// plugin can allocate corresponding resource for the container
// according to resource request
func (p *DynamicPolicy) Allocate(ctx context.Context, req *pluginapi.ResourceRequest) (resp *pluginapi.ResourceAllocationResponse, respErr error) {
	if req == nil {
		return nil, fmt.Errorf("Allocate got nil req")
	}

	qosLevel, err := util.GetKatalystQoSLevelFromResourceReq(p.qosConfig, req)
	if err != nil {
		err = fmt.Errorf("GetKatalystQoSLevelFromResourceReq for pod: %s/%s, container: %s failed with error: %v",
			req.PodNamespace, req.PodName, req.ContainerName, err)
		klog.Errorf("[MemoryDynamicPolicy.Allocate] %s", err.Error())
		return nil, err
	}

	reqInt, err := getReqQuantityFromResourceReq(req)
	if err != nil {
		return nil, fmt.Errorf("getReqQuantityFromResourceReq failed with error: %v", err)
	}

	klog.InfoS("[MemoryDynamicPolicy.Allocate] Allocate called",
		"podNamespace", req.PodNamespace,
		"podName", req.PodName,
		"containerName", req.ContainerName,
		"podType", req.PodType,
		"podRole", req.PodRole,
		"qosLevel", qosLevel,
		"memoryReq(bytes)", reqInt)

	p.Lock()
	defer func() {
		if respErr != nil {
			_ = p.removeContainer(req.PodUid, req.ContainerName)
			_ = p.emitter.StoreInt64(util.MetricNameAllocateFailed, 1, metrics.MetricTypeNameRaw)
		}

		p.Unlock()
	}()

	allocationInfo := p.state.GetAllocationInfo(v1.ResourceMemory, req.PodUid, req.ContainerName)
	if allocationInfo != nil && allocationInfo.AggregatedQuantity >= uint64(reqInt) {
		klog.InfoS("[MemoryDynamicPolicy] already allocated and meet requirement",
			"podNamespace", req.PodNamespace,
			"podName", req.PodName,
			"containerName", req.ContainerName,
			"memoryReq(bytes)", reqInt,
			"currentResult(bytes)", allocationInfo.AggregatedQuantity)

		return &pluginapi.ResourceAllocationResponse{
			PodUid:         req.PodUid,
			PodNamespace:   req.PodNamespace,
			PodName:        req.PodName,
			ContainerName:  req.ContainerName,
			ContainerType:  req.ContainerType,
			ContainerIndex: req.ContainerIndex,
			PodRole:        req.PodRole,
			PodType:        req.PodType,
			ResourceName:   string(v1.ResourceMemory),
			AllocationResult: &pluginapi.ResourceAllocation{
				ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{
					string(v1.ResourceMemory): {
						OciPropertyName:   util.OCIPropertyNameCPUSetMems,
						IsNodeResource:    false,
						IsScalarResource:  true,
						AllocatedQuantity: float64(allocationInfo.AggregatedQuantity),
						AllocationResult:  allocationInfo.NumaAllocationResult.String(),
					},
				},
			},
			Labels:      general.DeepCopyMap(req.Labels),
			Annotations: general.DeepCopyMap(req.Annotations),
		}, nil
	}

	if req.ContainerType == pluginapi.ContainerType_INIT {
		return &pluginapi.ResourceAllocationResponse{
			PodUid:         req.PodUid,
			PodNamespace:   req.PodNamespace,
			PodName:        req.PodName,
			ContainerName:  req.ContainerName,
			ContainerType:  req.ContainerType,
			ContainerIndex: req.ContainerIndex,
			PodRole:        req.PodRole,
			PodType:        req.PodType,
			ResourceName:   string(v1.ResourceMemory),
			Labels:         general.DeepCopyMap(req.Labels),
			Annotations:    general.DeepCopyMap(req.Annotations),
		}, nil
	}

	if p.allocationHandlers[qosLevel] == nil {
		return nil, fmt.Errorf("katalyst QoS level: %s is not supported yet", qosLevel)
	}

	return p.allocationHandlers[qosLevel](ctx, req)
}

// PreStartContainer is called, if indicated by resource plugin during registeration phase,
// before each container start. Resource plugin can run resource specific operations
// such as resetting the resource before making resources available to the container
func (p *DynamicPolicy) PreStartContainer(context.Context, *pluginapi.PreStartContainerRequest) (*pluginapi.PreStartContainerResponse, error) {
	return nil, nil
}

// setMemoryMigrate is used to calculate and set memory migrate configuration, notice that
// 1. not to set memory migrate for NUMA binding containers
// 2. for a certain given pod/container, only one setting action is on the flight
// 3. the setting action is done asynchronously to avoid hang
func (p *DynamicPolicy) setMemoryMigrate() {
	p.RLock()

	podResourceEntries := p.state.GetPodResourceEntries()
	podEntries := podResourceEntries[v1.ResourceMemory]

	migrateCGData := make(map[string]map[string]*common.CPUSetData)
	for podUID, containerEntries := range podEntries {
		for containerName, allocationInfo := range containerEntries {
			if allocationInfo == nil {
				klog.Errorf("[MemoryDynamicPolicy.setMemoryMigrate] pod: %s, container: %s has nil allocationInfo",
					podUID, containerName)
				continue
			} else if containerName == "" {
				klog.Errorf("[MemoryDynamicPolicy.setMemoryMigrate] pod: %s has empty containerName entry",
					podUID)
				continue
			} else if allocationInfo.QoSLevel == apiconsts.PodAnnotationQoSLevelDedicatedCores &&
				allocationInfo.Annotations[apiconsts.PodAnnotationMemoryEnhancementNumaBinding] == apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable {
				continue
			}

			if migrateCGData[podUID] == nil {
				migrateCGData[podUID] = make(map[string]*common.CPUSetData)
			}

			migrateCGData[podUID][containerName] = &common.CPUSetData{Migrate: "1"}
		}
	}

	p.RUnlock()
	p.migrateMemoryLock.Lock()

	for podUID, containersData := range migrateCGData {
		for containerName, cgData := range containersData {
			if !p.migratingMemory[podUID][containerName] {

				if p.migratingMemory[podUID] == nil {
					p.migratingMemory[podUID] = make(map[string]bool)
				}

				p.migratingMemory[podUID][containerName] = true
				go func(podUID, containerName string, cgData *common.CPUSetData) {
					defer func() {
						p.migrateMemoryLock.Lock()
						delete(p.migratingMemory[podUID], containerName)
						if len(p.migratingMemory[podUID]) == 0 {
							delete(p.migratingMemory, podUID)
						}
						p.migrateMemoryLock.Unlock()
					}()

					containerId, err := p.metaServer.GetContainerID(podUID, containerName)
					if err != nil {
						klog.Errorf("[MemoryDynamicPolicy.setMemoryMigrate] get container id of pod: %s container: %s failed with error: %v",
							podUID, containerName, err)
						return
					}

					klog.Infof("[MemoryDynamicPolicy.setMemoryMigrate] start to set cgroup memory migrate for pod: %s, container: %s(%s) and pin memory", podUID, containerName, containerId)
					err = cgroupcmutils.ApplyCPUSetForContainer(podUID, containerId, cgData)
					klog.Infof("[MemoryDynamicPolicy.setMemoryMigrate] end to set cgroup memory migrate for pod: %s, container: %s(%s) and pin memory", podUID, containerName, containerId)

					if err != nil {
						klog.Errorf("[MemoryDynamicPolicy.setMemoryMigrate] set cgroup memory migrate for pod: %s, container: %s(%s) failed with error: %v", podUID, containerName, containerId, err)
						return
					}

					klog.Infof("[MemoryDynamicPolicy.setMemoryMigrate] set cgroup memory migrate for pod: %s, container: %s(%s) successfully", podUID, containerName, containerId)
				}(podUID, containerName, cgData)
			}
		}
	}
	p.migrateMemoryLock.Unlock()
}

// clearResidualState is used to clean residual pods in local state
func (p *DynamicPolicy) clearResidualState() {
	klog.Infof("[MemoryDynamicPolicy] exec clearResidualState")
	residualSet := make(map[string]bool)

	ctx := context.Background()
	podList, err := p.metaServer.GetPodList(ctx, nil)
	if err != nil {
		klog.Errorf("[MemoryDynamicPolicy.clearResidualState] get pod list failed: %v", err)
		return
	}

	podSet := sets.NewString()
	for _, pod := range podList {
		podSet.Insert(fmt.Sprintf("%v", pod.UID))
	}

	p.Lock()
	defer p.Unlock()

	podResourceEntries := p.state.GetPodResourceEntries()
	for _, podEntries := range podResourceEntries {
		for podUID, containerEntries := range podEntries {
			if len(containerEntries) == 1 && containerEntries[""] != nil {
				continue
			}

			if !podSet.Has(podUID) && !residualSet[podUID] {
				residualSet[podUID] = true
				p.residualHitMap[podUID] += 1

				klog.Infof("[MemoryDynamicPolicy.clearResidualState] found pod: %s with state but doesn't show up in pod watcher, hit count: %d", podUID, p.residualHitMap[podUID])
			}
		}
	}

	podsToDelete := sets.NewString()
	for podUID, hitCount := range p.residualHitMap {
		if !residualSet[podUID] {
			klog.Infof("[MemoryDynamicPolicy.clearResidualState] already found pod: %s in pod watcher or its state is cleared, delete it from residualHitMap", podUID)
			delete(p.residualHitMap, podUID)
			continue
		}

		if time.Duration(hitCount)*stateCheckPeriod >= maxResidualTime {
			podsToDelete.Insert(podUID)
		}
	}

	if podsToDelete.Len() > 0 {
		for {
			podUID, found := podsToDelete.PopAny()
			if !found {
				break
			}

			// todo: if the sysadvisor memory plugin is supported in the future, we need to call
			//  the memory plugin to remove the pod before deleting the pod entry

			klog.Infof("[MemoryDynamicPolicy.clearResidualState] clear residual pod: %s in state", podUID)
			for _, podEntries := range podResourceEntries {
				delete(podEntries, podUID)
			}
		}

		resourcesMachineState, err := state.GenerateResourcesMachineStateFromPodEntries(p.state.GetMachineInfo(), podResourceEntries, p.state.GetReservedMemory())
		if err != nil {
			klog.Errorf("[MemoryDynamicPolicy.clearResidualState] GenerateResourcesMachineStateFromPodEntries failed with error: %v", err)
			return
		}

		p.state.SetPodResourceEntries(podResourceEntries)
		p.state.SetMachineState(resourcesMachineState)

		err = p.adjustAllocationEntries()
		if err != nil {
			klog.ErrorS(err, "[MemoryDynamicPolicy.clearResidualState] adjustAllocationEntries failed")
		}
	}
}

func (p *DynamicPolicy) removePod(podUID string) error {
	podResourceEntries := p.state.GetPodResourceEntries()
	for _, podEntries := range podResourceEntries {
		delete(podEntries, podUID)
	}

	resourcesMachineState, err := state.GenerateResourcesMachineStateFromPodEntries(p.state.GetMachineInfo(), podResourceEntries, p.state.GetReservedMemory())
	if err != nil {
		klog.Errorf("[MemoryDynamicPolicy.removePod] pod: %s, GenerateResourcesMachineStateFromPodEntries failed with error: %v",
			podUID, err)
		return fmt.Errorf("calculate machineState by updated pod entries failed with error: %v", err)
	}

	p.state.SetPodResourceEntries(podResourceEntries)
	p.state.SetMachineState(resourcesMachineState)

	return nil
}

func (p *DynamicPolicy) removeContainer(podUID, containerName string) error {
	podResourceEntries := p.state.GetPodResourceEntries()
	for _, podEntries := range podResourceEntries {
		delete(podEntries[podUID], containerName)
	}

	resourcesMachineState, err := state.GenerateResourcesMachineStateFromPodEntries(p.state.GetMachineInfo(), podResourceEntries, p.state.GetReservedMemory())
	if err != nil {
		klog.Errorf("[MemoryDynamicPolicy.removeContainer] pod: %s, container: %s GenerateResourcesMachineStateFromPodEntries failed with error: %v",
			podUID, containerName, err)
		return fmt.Errorf("calculate machineState by updated pod entries failed with error: %v", err)
	}

	p.state.SetPodResourceEntries(podResourceEntries)
	p.state.SetMachineState(resourcesMachineState)

	return nil
}

// getReservedMemory is used to spread total reserved memories into per-numa level
func getReservedMemory(reservedMemoryGB uint64, machineInfo *info.MachineInfo) (map[int]uint64, error) {
	if machineInfo == nil {
		return nil, fmt.Errorf("getReservedMemory got nil machineInfo")
	}
	numasCount := len(machineInfo.Topology)

	perNumaReservedGB := uint64(math.Ceil(float64(reservedMemoryGB) / float64(numasCount)))
	perNumaReservedQuantity := resource.MustParse(fmt.Sprintf("%dGi", perNumaReservedGB))

	ceilReservedMemoryGB := perNumaReservedGB * uint64(numasCount)

	klog.Infof("[getReservedMemory] reservedMemoryGB: %d, ceilReservedMemoryGB: %d, perNumaReservedGB: %d, numasCount: %d",
		reservedMemoryGB, ceilReservedMemoryGB, perNumaReservedGB, numasCount)

	reservedMemory := make(map[int]uint64)
	for _, node := range machineInfo.Topology {
		reservedMemory[node.Id] = uint64(perNumaReservedQuantity.Value())
	}
	return reservedMemory, nil
}

// getReqQuantityFromResourceReq parses resources quantity into value,
// since pods with reclaimed_cores and un-reclaimed_cores have different
// representations, we may to adapt to both cases.
func getReqQuantityFromResourceReq(req *pluginapi.ResourceRequest) (int, error) {
	if len(req.ResourceRequests) != 1 {
		return 0, fmt.Errorf("invalid req.ResourceRequests length: %d", len(req.ResourceRequests))
	}

	for key := range req.ResourceRequests {
		switch key {
		case string(v1.ResourceMemory), string(apiconsts.ReclaimedResourceMemory):
			return general.Max(int(math.Ceil(req.ResourceRequests[key])), 0), nil
		default:
			return 0, fmt.Errorf("invalid request resource name: %s", key)
		}
	}

	return 0, fmt.Errorf("unexpected end")
}

func GetReadonlyState() (state.ReadonlyState, error) {
	readonlyStateLock.RLock()
	defer readonlyStateLock.RUnlock()
	if readonlyState == nil {
		return nil, fmt.Errorf("readonlyState isn't setted")
	}

	return readonlyState, nil
}

func (p *DynamicPolicy) checkMemorySet() {
	podEntries := p.state.GetPodResourceEntries()[v1.ResourceMemory]
	actualMemorySets := make(map[string]map[string]machine.CPUSet)

	unionNUMABindingStateMemorySet := machine.NewCPUSet()
	for podUID, containerEntries := range podEntries {
		for containerName, allocationInfo := range containerEntries {

			if allocationInfo == nil || allocationInfo.ContainerType != pluginapi.ContainerType_MAIN.String() {
				continue
			} else if allocationInfo.QoSLevel == consts.PodAnnotationQoSLevelSharedCores &&
				p.getContainerRequestedMemoryBytes(allocationInfo) == 0 {
				klog.Warningf("[MemoryDynamicPolicy.checkMemorySet] skip memset checking for pod: %s/%s container: %s with zero cpu request",
					allocationInfo.PodNamespace, allocationInfo.PodName, containerName)
				continue
			} else if allocationInfo.QoSLevel == consts.PodAnnotationQoSLevelDedicatedCores &&
				allocationInfo.Annotations[consts.PodAnnotationMemoryEnhancementNumaBinding] == consts.PodAnnotationMemoryEnhancementNumaBindingEnable {
				unionNUMABindingStateMemorySet = unionNUMABindingStateMemorySet.Union(allocationInfo.NumaAllocationResult)
			}

			tags := metrics.ConvertMapToTags(map[string]string{
				"podNamespace":  allocationInfo.PodNamespace,
				"podName":       allocationInfo.PodName,
				"containerName": allocationInfo.ContainerName,
			})

			containerId, err := p.metaServer.GetContainerID(podUID, containerName)
			if err != nil {
				klog.Errorf("[MemoryDynamicPolicy.checkMemorySet] get container id of pod: %s container: %s failed with error: %v",
					podUID, containerName, err)
				continue
			}

			cpusetStats, err := cgroupcmutils.GetCPUSetForContainer(podUID, containerId)

			if err != nil {
				klog.Errorf("[MemoryDynamicPolicy.checkMemorySet] GetMemorySet of pod: %s container: name(%s), id(%s) failed with error: %v",
					podUID, containerName, containerId, err)

				_ = p.emitter.StoreInt64(util.MetricNameRealStateInvalid, 1, metrics.MetricTypeNameRaw, tags...)

				continue
			}

			if actualMemorySets[podUID] == nil {
				actualMemorySets[podUID] = make(map[string]machine.CPUSet)
			}

			actualMemorySets[podUID][containerName] = machine.MustParse(cpusetStats.Mems)

			klog.Infof("[MemoryDynamicPolicy.checkMemorySet] pod: %s/%s, container: %s, state MemorySet: %s, actual MemorySet: %s",
				allocationInfo.PodNamespace, allocationInfo.PodName,
				allocationInfo.ContainerName, allocationInfo.NumaAllocationResult.String(),
				actualMemorySets[podUID][containerName].String())

			// only do comparison for dedicated_cores with numa_biding to avoid effect of adjustment for shared_cores
			if !(allocationInfo.QoSLevel == consts.PodAnnotationQoSLevelDedicatedCores &&
				allocationInfo.Annotations[consts.PodAnnotationMemoryEnhancementNumaBinding] == consts.PodAnnotationMemoryEnhancementNumaBindingEnable) {
				continue
			}

			if !actualMemorySets[podUID][containerName].Equals(allocationInfo.NumaAllocationResult) {
				klog.Errorf("[MemoryDynamicPolicy.checkMemorySet] pod: %s/%s, container: %s, memset invalid",
					allocationInfo.PodNamespace, allocationInfo.PodName,
					allocationInfo.ContainerName)

				_ = p.emitter.StoreInt64(util.MetricNameMemSetInvalid, 1, metrics.MetricTypeNameRaw, tags...)
			}
		}
	}

	unionNUMABindingActualMemorySet := machine.NewCPUSet()
	unionDedicatedActualMemorySet := machine.NewCPUSet()
	unionSharedActualMemorySet := machine.NewCPUSet()
	var memorySetOverlap bool

	for podUID, containerEntries := range actualMemorySets {
		for containerName, cset := range containerEntries {

			allocationInfo := podEntries[podUID][containerName]

			if allocationInfo == nil {
				continue
			}

			switch allocationInfo.QoSLevel {
			case consts.PodAnnotationQoSLevelDedicatedCores:
				if allocationInfo.Annotations[consts.PodAnnotationMemoryEnhancementNumaBinding] == consts.PodAnnotationMemoryEnhancementNumaBindingEnable {
					if !memorySetOverlap && cset.Intersection(unionNUMABindingActualMemorySet).Size() != 0 {
						memorySetOverlap = true
						klog.Errorf("[MemoryDynamicPolicy.checkMemorySet] pod: %s/%s, container: %s memset: %s overlaps with others",
							allocationInfo.PodNamespace,
							allocationInfo.PodName,
							allocationInfo.ContainerName,
							cset.String())
					}

					unionNUMABindingActualMemorySet = unionNUMABindingActualMemorySet.Union(cset)
				} else {
					unionDedicatedActualMemorySet = unionDedicatedActualMemorySet.Union(cset)
				}
			case consts.PodAnnotationQoSLevelSharedCores:
				unionSharedActualMemorySet = unionSharedActualMemorySet.Union(cset)
			}
		}
	}

	regionOverlap := unionNUMABindingActualMemorySet.Intersection(unionSharedActualMemorySet).Size() != 0 ||
		unionNUMABindingActualMemorySet.Intersection(unionDedicatedActualMemorySet).Size() != 0

	if regionOverlap {
		klog.Errorf("[MemoryDynamicPolicy.checkMemorySet] shared_cores union memset: %s,"+
			" dedicated_cores union memset: %s overlap with numa_binding union memset: %s",
			unionSharedActualMemorySet.String(),
			unionDedicatedActualMemorySet.String(),
			unionNUMABindingActualMemorySet.String())
	}

	if !memorySetOverlap {
		memorySetOverlap = regionOverlap
	}

	if memorySetOverlap {
		klog.Errorf("[MemoryDynamicPolicy.checkMemorySet] found memset overlap. actualMemorySets: %+v", actualMemorySets)
		_ = p.emitter.StoreInt64(util.MetricNameMemSetOverlap, 1, metrics.MetricTypeNameRaw)
	}

	machineState := p.state.GetMachineState()[v1.ResourceMemory]
	notAssignedMemSet := machineState.GetNUMANodesWithoutNUMABindingPods()
	if !unionNUMABindingStateMemorySet.Union(notAssignedMemSet).Equals(p.topology.CPUDetails.NUMANodes()) {
		klog.Infof("[MemoryDynamicPolicy.checkMemorySet] found node memset invalid. unionNUMABindingStateMemorySet: %s, notAssignedMemSet: %s, topology: %s",
			unionNUMABindingStateMemorySet.String(), notAssignedMemSet.String(), p.topology.CPUDetails.NUMANodes().String())

		_ = p.emitter.StoreInt64(util.MetricNameNodeMemsetInvalid, 1, metrics.MetricTypeNameRaw)
	}

	klog.Infof("[MemoryDynamicPolicy.checkMemorySet] finish checkMemorySet")
}

// getContainerRequestedMemoryBytes parses and returns requested memory bytes for the given container
func (p *DynamicPolicy) getContainerRequestedMemoryBytes(allocationInfo *state.AllocationInfo) int {
	if allocationInfo == nil {
		klog.Errorf("[getContainerRequestedMemoryBytes] got nil allocationInfo")
		return 0
	}

	if p.metaServer == nil {
		klog.Errorf("[getContainerRequestedMemoryBytes] nil metaServer")
		return 0
	}

	container, err := p.metaServer.GetContainerSpec(allocationInfo.PodUid, allocationInfo.ContainerName)
	if err != nil || container == nil {
		klog.Errorf("[getContainerRequestedMemoryBytes] get container failed with error: %v", err)
		return 0
	}

	memoryQuantity := native.GetMemoryQuantity(container.Resources.Requests)
	requestBytes := general.Max(int(memoryQuantity.Value()), 0)

	klog.Infof("[getContainerRequestedMemoryBytes] get memory request bytes: %d for pod: %s/%s container: %s from podWatcher",
		requestBytes, allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName)

	return requestBytes
}
