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
	"sync"
	"time"

	"google.golang.org/grpc"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"
	"k8s.io/utils/clock"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-api/pkg/plugins/skeleton"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/agent"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/advisorsvc"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/memory/dynamicpolicy/memoryadvisor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/memory/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/asyncworker"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
	"github.com/kubewharf/katalyst-core/pkg/util/timemonitor"
)

const (
	MemoryResourcePluginPolicyNameDynamic = "dynamic"

	memoryPluginStateFileName             = "memory_plugin_state"
	memoryPluginAsyncWorkersName          = "qrm_memory_plugin_async_workers"
	memoryPluginAsyncWorkTopicDropCache   = "qrm_memory_plugin_drop_cache"
	memoryPluginAsyncWorkTopicMigratePage = "qrm_memory_plugin_migrate_page"

	dropCacheTimeoutSeconds = 30
)

const (
	memsetCheckPeriod          = 10 * time.Second
	stateCheckPeriod           = 30 * time.Second
	maxResidualTime            = 5 * time.Minute
	setMemoryMigratePeriod     = 5 * time.Second
	applyCgroupPeriod          = 5 * time.Second
	setExtraControlKnobsPeriod = 5 * time.Second
)

var (
	readonlyStateLock sync.RWMutex
	readonlyState     state.ReadonlyState
)

// GetReadonlyState returns state.ReadonlyState to provides a way
// to obtain the running states of the plugin
func GetReadonlyState() (state.ReadonlyState, error) {
	readonlyStateLock.RLock()
	defer readonlyStateLock.RUnlock()

	if readonlyState == nil {
		return nil, fmt.Errorf("readonlyState isn't setted")
	}
	return readonlyState, nil
}

type DynamicPolicy struct {
	sync.RWMutex

	stopCh                  chan struct{}
	started                 bool
	qosConfig               *generic.QoSConfiguration
	extraControlKnobConfigs commonstate.ExtraControlKnobConfigs

	// emitter is used to emit metrics.
	// metaServer is used to collect metadata universal metaServer.
	emitter    metrics.MetricEmitter
	metaServer *metaserver.MetaServer

	advisorClient     advisorsvc.AdvisorServiceClient
	advisorConn       *grpc.ClientConn
	lwRecvTimeMonitor *timemonitor.TimeMonitor

	topology *machine.CPUTopology
	state    state.State

	migrateMemoryLock sync.Mutex
	migratingMemory   map[string]map[string]bool
	residualHitMap    map[string]int64

	allocationHandlers map[string]util.AllocationHandler
	hintHandlers       map[string]util.HintHandler

	extraStateFileAbsPath string
	name                  string

	podDebugAnnoKeys []string

	asyncWorkers *asyncworker.AsyncWorkers

	enableSettingMemoryMigrate bool
	enableMemroyAdvisor        bool
	memoryAdvisorSocketAbsPath string
}

func NewDynamicPolicy(agentCtx *agent.GenericContext, conf *config.Configuration,
	_ interface{}, agentName string) (bool, agent.Component, error) {
	reservedMemory, err := getReservedMemory(agentCtx.MachineInfo, conf.ReservedMemoryGB)
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

	extraControlKnobConfigs := make(commonstate.ExtraControlKnobConfigs)
	if len(conf.ExtraControlKnobConfigFile) > 0 {
		extraControlKnobConfigs, err = commonstate.LoadExtraControlKnobConfigs(conf.ExtraControlKnobConfigFile)
		if err != nil {
			return false, agent.ComponentStub{}, fmt.Errorf("loadExtraControlKnobConfigs failed with error: %v", err)
		}
	} else {
		general.Infof("empty ExtraControlKnobConfigFile, initialize empty extraControlKnobConfigs")
	}

	readonlyStateLock.Lock()
	readonlyState = stateImpl
	readonlyStateLock.Unlock()

	wrappedEmitter := agentCtx.EmitterPool.GetDefaultMetricsEmitter().WithTags(agentName, metrics.MetricTag{
		Key: util.QRMPluginPolicyTagName,
		Val: MemoryResourcePluginPolicyNameDynamic,
	})

	policyImplement := &DynamicPolicy{
		topology:                   agentCtx.CPUTopology,
		qosConfig:                  conf.QoSConfiguration,
		emitter:                    wrappedEmitter,
		metaServer:                 agentCtx.MetaServer,
		state:                      stateImpl,
		stopCh:                     make(chan struct{}),
		migratingMemory:            make(map[string]map[string]bool),
		residualHitMap:             make(map[string]int64),
		extraStateFileAbsPath:      conf.ExtraStateFileAbsPath,
		name:                       fmt.Sprintf("%s_%s", agentName, MemoryResourcePluginPolicyNameDynamic),
		podDebugAnnoKeys:           conf.PodDebugAnnoKeys,
		asyncWorkers:               asyncworker.NewAsyncWorkers(memoryPluginAsyncWorkersName),
		enableSettingMemoryMigrate: conf.EnableSettingMemoryMigrate,
		enableMemroyAdvisor:        conf.EnableMemoryAdvisor,
		memoryAdvisorSocketAbsPath: conf.MemoryAdvisorSocketAbsPath,
		extraControlKnobConfigs:    extraControlKnobConfigs, // [TODO]: support modifying extraControlKnobConfigs by KCC
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

	pluginWrapper, err := skeleton.NewRegistrationPluginWrapper(policyImplement,
		conf.QRMPluginSocketDirs, nil)
	if err != nil {
		return false, agent.ComponentStub{}, fmt.Errorf("dynamic policy new plugin wrapper failed with error: %v", err)
	}

	memoryadvisor.RegisterControlKnobHandler(memoryadvisor.ControlKnobKeyMemoryLimitInBytes,
		memoryadvisor.ControlKnobHandlerWithChecker(policyImplement.handleAdvisorMemoryLimitInBytes))
	memoryadvisor.RegisterControlKnobHandler(memoryadvisor.ControlKnobKeyDropCache,
		memoryadvisor.ControlKnobHandlerWithChecker(policyImplement.handleAdvisorDropCache))
	memoryadvisor.RegisterControlKnobHandler(memoryadvisor.ControlKnobKeyCPUSetMems,
		memoryadvisor.ControlKnobHandlerWithChecker(policyImplement.handleAdvisorCPUSetMems))

	return true, &agent.PluginWrapper{GenericPlugin: pluginWrapper}, nil
}

func (p *DynamicPolicy) Start() (err error) {
	general.Infof("called")

	p.Lock()
	defer func() {
		if !p.started {
			if err == nil {
				p.started = true
			} else {
				close(p.stopCh)
			}
		}
		p.Unlock()
	}()

	if p.started {
		general.Infof("already started")
		return nil
	}
	p.stopCh = make(chan struct{})

	go wait.Until(func() {
		_ = p.emitter.StoreInt64(util.MetricNameHeartBeat, 1, metrics.MetricTypeNameRaw)
	}, time.Second*30, p.stopCh)
	go wait.Until(p.clearResidualState, stateCheckPeriod, p.stopCh)
	go wait.Until(p.checkMemorySet, memsetCheckPeriod, p.stopCh)
	go wait.Until(p.applyExternalCgroupParams, applyCgroupPeriod, p.stopCh)
	go wait.Until(p.setExtraControlKnobByConfigs, setExtraControlKnobsPeriod, p.stopCh)

	if p.enableSettingMemoryMigrate {
		general.Infof("setMemoryMigrate enabled")
		go wait.Until(p.setMemoryMigrate, setMemoryMigratePeriod, p.stopCh)
	}

	if !p.enableMemroyAdvisor {
		general.Infof("start dynamic policy memory plugin without memory advisor")
		return nil
	} else if p.memoryAdvisorSocketAbsPath == "" {
		return fmt.Errorf("invalid memoryAdvisorSocketAbsPath: %s", p.memoryAdvisorSocketAbsPath)
	}

	general.Infof("start dynamic policy memory plugin with memory advisor")
	err = p.initAdvisorClientConn()
	if err != nil {
		general.Errorf("initAdvisorClientConn failed with error: %v", err)
		return
	}

	communicateWithMemoryAdvisorServer := func() {
		// call lw of MemoryAdvisorServer and do allocation
		if err := p.lwMemoryAdvisorServer(p.stopCh); err != nil {
			general.Errorf("lwMemoryAdvisorServer failed with error: %v", err)
		} else {
			general.Infof("lwMemoryAdvisorServer finished")
		}
	}

	go wait.BackoffUntil(communicateWithMemoryAdvisorServer, wait.NewExponentialBackoffManager(800*time.Millisecond,
		30*time.Second, 2*time.Minute, 2.0, 0, &clock.RealClock{}), true, p.stopCh)

	p.lwRecvTimeMonitor = timemonitor.NewTimeMonitor(memoryAdvisorLWRecvTimeMonitorName,
		memoryAdvisorLWRecvTimeMonitorDurationThreshold, memoryAdvisorLWRecvTimeMonitorInterval,
		util.MetricNameLWRecvStuck, p.emitter)
	go p.lwRecvTimeMonitor.Run(p.stopCh)
	return nil
}

func (p *DynamicPolicy) Stop() error {
	p.Lock()
	defer func() {
		p.started = false
		p.Unlock()
		general.Warningf("stopped")
	}()

	if !p.started {
		general.Warningf("already stopped")
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
func (p *DynamicPolicy) GetTopologyHints(ctx context.Context,
	req *pluginapi.ResourceRequest) (*pluginapi.ResourceHintsResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("GetTopologyHints got nil req")
	}

	// identify if the pod is a debug pod,
	// if so, apply specific strategy to it.
	// since GetKatalystQoSLevelFromResourceReq function will filter annotations,
	// we should do it before GetKatalystQoSLevelFromResourceReq.
	isDebugPod := util.IsDebugPod(req.Annotations, p.podDebugAnnoKeys)

	qosLevel, err := util.GetKatalystQoSLevelFromResourceReq(p.qosConfig, req)
	if err != nil {
		err = fmt.Errorf("GetKatalystQoSLevelFromResourceReq for pod: %s/%s, container: %s failed with error: %v",
			req.PodNamespace, req.PodName, req.ContainerName, err)
		general.Errorf("%s", err.Error())
		return nil, err
	}

	reqInt, err := util.GetQuantityFromResourceReq(req)
	if err != nil {
		return nil, fmt.Errorf("getReqQuantityFromResourceReq failed with error: %v", err)
	}

	general.InfoS("GetTopologyHints is called",
		"podNamespace", req.PodNamespace,
		"podName", req.PodName,
		"containerName", req.ContainerName,
		"podType", req.PodType,
		"podRole", req.PodRole,
		"containerType", req.ContainerType,
		"qosLevel", qosLevel,
		"memoryReq(bytes)", reqInt,
		"isDebugPod", isDebugPod)

	if req.ContainerType == pluginapi.ContainerType_INIT || isDebugPod {
		general.Infof("there is no NUMA preference, return nil hint")
		return util.PackResourceHintsResponse(req, string(v1.ResourceMemory),
			map[string]*pluginapi.ListOfTopologyHints{
				string(v1.ResourceMemory): nil,
			})
	}

	p.RLock()
	defer func() {
		p.RUnlock()
		if err != nil {
			_ = p.emitter.StoreInt64(util.MetricNameGetTopologyHintsFailed, 1, metrics.MetricTypeNameRaw)
		}
	}()

	if p.hintHandlers[qosLevel] == nil {
		return nil, fmt.Errorf("katalyst QoS level: %s is not supported yet", qosLevel)
	}
	return p.hintHandlers[qosLevel](ctx, req)
}

func (p *DynamicPolicy) RemovePod(_ context.Context,
	req *pluginapi.RemovePodRequest) (*pluginapi.RemovePodResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("RemovePod got nil req")
	}

	p.Lock()
	defer p.Unlock()

	err := p.removePod(req.PodUid)
	if err != nil {
		general.ErrorS(err, "remove pod failed with error", "podUID", req.PodUid)
		return nil, err
	}

	err = p.adjustAllocationEntries()
	if err != nil {
		general.ErrorS(err, "adjustAllocationEntries failed", "podUID", req.PodUid)
	}

	return &pluginapi.RemovePodResponse{}, nil
}

// GetResourcesAllocation returns allocation results of corresponding resources
func (p *DynamicPolicy) GetResourcesAllocation(_ context.Context,
	req *pluginapi.GetResourcesAllocationRequest) (*pluginapi.GetResourcesAllocationResponse, error) {
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

			var err error
			podResources[podUID].ContainerResources[containerName], err = allocationInfo.GetResourceAllocation()

			if err != nil {
				errMsg := "allocationInfo.GetResourceAllocation failed"
				general.ErrorS(err, errMsg,
					"podNamespace", allocationInfo.PodNamespace,
					"podName", allocationInfo.PodName,
					"containerName", allocationInfo.ContainerName)
				return nil, fmt.Errorf(errMsg)
			}
		}
	}

	return &pluginapi.GetResourcesAllocationResponse{
		PodResources: podResources,
	}, nil
}

// GetTopologyAwareResources returns allocation results of corresponding resources as topology aware format
func (p *DynamicPolicy) GetTopologyAwareResources(_ context.Context,
	req *pluginapi.GetTopologyAwareResourcesRequest) (*pluginapi.GetTopologyAwareResourcesResponse, error) {
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

	if allocationInfo.CheckSideCar() {
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
func (p *DynamicPolicy) GetTopologyAwareAllocatableResources(context.Context,
	*pluginapi.GetTopologyAwareAllocatableResourcesRequest) (*pluginapi.GetTopologyAwareAllocatableResourcesResponse, error) {
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
func (p *DynamicPolicy) GetResourcePluginOptions(context.Context,
	*pluginapi.Empty) (*pluginapi.ResourcePluginOptions, error) {
	return &pluginapi.ResourcePluginOptions{PreStartRequired: false,
		WithTopologyAlignment: true,
		NeedReconcile:         true}, nil
}

// Allocate is called during pod admit so that the resource
// plugin can allocate corresponding resource for the container
// according to resource request
func (p *DynamicPolicy) Allocate(ctx context.Context,
	req *pluginapi.ResourceRequest) (resp *pluginapi.ResourceAllocationResponse, respErr error) {
	if req == nil {
		return nil, fmt.Errorf("Allocate got nil req")
	}

	// identify if the pod is a debug pod,
	// if so, apply specific strategy to it.
	// since GetKatalystQoSLevelFromResourceReq function will filter annotations,
	// we should do it before GetKatalystQoSLevelFromResourceReq.
	isDebugPod := util.IsDebugPod(req.Annotations, p.podDebugAnnoKeys)

	qosLevel, err := util.GetKatalystQoSLevelFromResourceReq(p.qosConfig, req)
	if err != nil {
		err = fmt.Errorf("GetKatalystQoSLevelFromResourceReq for pod: %s/%s, container: %s failed with error: %v",
			req.PodNamespace, req.PodName, req.ContainerName, err)
		general.Errorf("%s", err.Error())
		return nil, err
	}

	reqInt, err := util.GetQuantityFromResourceReq(req)
	if err != nil {
		return nil, fmt.Errorf("getReqQuantityFromResourceReq failed with error: %v", err)
	}

	general.InfoS("called",
		"podNamespace", req.PodNamespace,
		"podName", req.PodName,
		"containerName", req.ContainerName,
		"podType", req.PodType,
		"podRole", req.PodRole,
		"qosLevel", qosLevel,
		"memoryReq(bytes)", reqInt)

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
	} else if isDebugPod {
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
						// return ResourceAllocation with empty OciPropertyName, AllocatedQuantity, AllocationResult for containers in debug pod,
						// it won't influence oci spec properties of the container
						IsNodeResource:   false,
						IsScalarResource: true,
					},
				},
			},
			Labels:      general.DeepCopyMap(req.Labels),
			Annotations: general.DeepCopyMap(req.Annotations),
		}, nil
	}

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
		general.InfoS("already allocated and meet requirement",
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

	if p.allocationHandlers[qosLevel] == nil {
		return nil, fmt.Errorf("katalyst QoS level: %s is not supported yet", qosLevel)
	}
	return p.allocationHandlers[qosLevel](ctx, req)
}

// PreStartContainer is called, if indicated by resource plugin during registration phase,
// before each container start. Resource plugin can run resource specific operations
// such as resetting the resource before making resources available to the container
func (p *DynamicPolicy) PreStartContainer(context.Context, *pluginapi.PreStartContainerRequest) (*pluginapi.PreStartContainerResponse, error) {
	return nil, nil
}

func (p *DynamicPolicy) removePod(podUID string) error {
	podResourceEntries := p.state.GetPodResourceEntries()
	for _, podEntries := range podResourceEntries {
		delete(podEntries, podUID)
	}

	resourcesMachineState, err := state.GenerateMachineStateFromPodEntries(p.state.GetMachineInfo(), podResourceEntries, p.state.GetReservedMemory())
	if err != nil {
		general.Errorf("pod: %s, GenerateMachineStateFromPodEntries failed with error: %v", podUID, err)
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

	resourcesMachineState, err := state.GenerateMachineStateFromPodEntries(p.state.GetMachineInfo(), podResourceEntries, p.state.GetReservedMemory())
	if err != nil {
		general.Errorf("pod: %s, container: %s GenerateMachineStateFromPodEntries failed with error: %v", podUID, containerName, err)
		return fmt.Errorf("calculate machineState by updated pod entries failed with error: %v", err)
	}

	p.state.SetPodResourceEntries(podResourceEntries)
	p.state.SetMachineState(resourcesMachineState)
	return nil
}

// getContainerRequestedMemoryBytes parses and returns requested memory bytes for the given container
func (p *DynamicPolicy) getContainerRequestedMemoryBytes(allocationInfo *state.AllocationInfo) int {
	if allocationInfo == nil {
		general.Errorf("got nil allocationInfo")
		return 0
	}

	if p.metaServer == nil {
		general.Errorf("got nil metaServer")
		return 0
	}

	container, err := p.metaServer.GetContainerSpec(allocationInfo.PodUid, allocationInfo.ContainerName)
	if err != nil || container == nil {
		general.Errorf("get container failed with error: %v", err)
		return 0
	}

	memoryQuantity := native.GetMemoryQuantity(container.Resources.Requests)
	requestBytes := general.Max(int(memoryQuantity.Value()), 0)

	general.Infof("get memory request bytes: %d for pod: %s/%s container: %s from podWatcher",
		requestBytes, allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName)
	return requestBytes
}
