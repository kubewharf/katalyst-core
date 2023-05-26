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
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"
	maputil "k8s.io/kubernetes/pkg/util/maps"
	"k8s.io/utils/clock"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-api/pkg/plugins/skeleton"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/agent"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/calculator"
	advisorapi "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/cpuadvisor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/cpueviction"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/validator"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/global/adminqos"
	"github.com/kubewharf/katalyst-core/pkg/config/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
	"github.com/kubewharf/katalyst-core/pkg/util/process"
)

const CPUResourcePluginPolicyNameDynamic = "dynamic"

const cpuPluginStateFileName = "cpu_plugin_state"

const (
	reservedReclaimedCPUsSize = 4

	cpusetCheckPeriod = 10 * time.Second
	stateCheckPeriod  = 30 * time.Second
	maxResidualTime   = 5 * time.Minute
	syncCPUIdlePeriod = 30 * time.Second
)

var (
	transitionPeriod = 30 * time.Second
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

// DynamicPolicy is the policy that's used by default;
// it will consider the dynamic running information to calculate
// and adjust resource requirements and configurations
type DynamicPolicy struct {
	sync.RWMutex
	name    string
	stopCh  chan struct{}
	started bool

	emitter     metrics.MetricEmitter
	metaServer  *metaserver.MetaServer
	machineInfo *machine.KatalystMachineInfo

	advisorClient    advisorapi.CPUAdvisorClient
	advisorConn      *grpc.ClientConn
	advisorValidator *validator.CPUAdvisorValidator
	advisorapi.UnimplementedCPUPluginServer

	state              state.State
	residualHitMap     map[string]int64
	allocationHandlers map[string]util.AllocationHandler
	hintHandlers       map[string]util.HintHandler

	cpuEvictionPlugin       *agent.PluginWrapper
	cpuEvictionPluginCancel context.CancelFunc

	// those are parsed from configurations
	// todo if we want to use dynamic configuration, we'd better not use self-defined conf
	enableCPUSysAdvisor           bool
	reservedCPUs                  machine.CPUSet
	cpuAdvisorSocketAbsPath       string
	cpuPluginSocketAbsPath        string
	extraStateFileAbsPath         string
	enableCPUPressureEviction     bool
	enableCPUIdle                 bool
	enableSyncingCPUIdle          bool
	reclaimRelativeRootCgroupPath string
	qosConfig                     *generic.QoSConfiguration
	reclaimedResourceConfig       *adminqos.ReclaimedResourceConfiguration
}

func NewDynamicPolicy(agentCtx *agent.GenericContext, conf *config.Configuration,
	_ interface{}, agentName string) (bool, agent.Component, error) {
	allCPUs := agentCtx.CPUDetails.CPUs().Clone()
	reservedCPUsNum := conf.ReservedCPUCores

	reservedCPUs, _, reserveErr := calculator.TakeHTByNUMABalance(agentCtx.KatalystMachineInfo, allCPUs, reservedCPUsNum)
	if reserveErr != nil {
		return false, agent.ComponentStub{}, fmt.Errorf("takeByNUMABalance for reservedCPUsNum: %d failed with error: %v",
			conf.ReservedCPUCores, reserveErr)
	}
	general.Infof("take reservedCPUs: %s by reservedCPUsNum: %d", reservedCPUs.String(), reservedCPUsNum)

	stateImpl, stateErr := state.NewCheckpointState(conf.GenericQRMPluginConfiguration.StateFileDirectory, cpuPluginStateFileName,
		CPUResourcePluginPolicyNameDynamic, agentCtx.CPUTopology, conf.SkipCPUStateCorruption)
	if stateErr != nil {
		return false, agent.ComponentStub{}, fmt.Errorf("NewCheckpointState failed with error: %v", stateErr)
	}

	readonlyStateLock.Lock()
	readonlyState = stateImpl
	readonlyStateLock.Unlock()

	wrappedEmitter := agentCtx.EmitterPool.GetDefaultMetricsEmitter().WithTags(agentName, metrics.MetricTag{
		Key: util.QRMPluginPolicyTagName,
		Val: CPUResourcePluginPolicyNameDynamic,
	})

	cpuEvictionPlugin, err := cpueviction.NewCPUPressureEvictionPlugin(
		agentCtx.EmitterPool.GetDefaultMetricsEmitter(), agentCtx.MetaServer, conf, stateImpl)
	if err != nil {
		return false, agent.ComponentStub{}, err
	}

	// since the reservedCPUs won't influence stateImpl directly.
	// so we don't modify stateImpl with reservedCPUs here.
	// for those pods have already been allocated reservedCPUs,
	// we won't touch them and wait them to be deleted the next update.
	policyImplement := &DynamicPolicy{
		name:   fmt.Sprintf("%s_%s", agentName, CPUResourcePluginPolicyNameDynamic),
		stopCh: make(chan struct{}),

		machineInfo: agentCtx.KatalystMachineInfo,
		emitter:     wrappedEmitter,
		metaServer:  agentCtx.MetaServer,

		state:          stateImpl,
		residualHitMap: make(map[string]int64),

		advisorValidator: validator.NewCPUAdvisorValidator(stateImpl, agentCtx.KatalystMachineInfo),

		cpuEvictionPlugin: cpuEvictionPlugin,

		qosConfig:                     conf.QoSConfiguration,
		reclaimedResourceConfig:       conf.ReclaimedResourceConfiguration,
		cpuAdvisorSocketAbsPath:       conf.CPUAdvisorSocketAbsPath,
		cpuPluginSocketAbsPath:        conf.CPUPluginSocketAbsPath,
		enableCPUSysAdvisor:           conf.CPUQRMPluginConfig.EnableSysAdvisor,
		reservedCPUs:                  reservedCPUs,
		extraStateFileAbsPath:         conf.ExtraStateFileAbsPath,
		enableCPUPressureEviction:     conf.EnableCPUPressureEviction,
		enableSyncingCPUIdle:          conf.CPUQRMPluginConfig.EnableSyncingCPUIdle,
		enableCPUIdle:                 conf.CPUQRMPluginConfig.EnableCPUIdle,
		reclaimRelativeRootCgroupPath: conf.ReclaimRelativeRootCgroupPath,
	}

	// register allocation behaviors for pods with different QoS level
	policyImplement.allocationHandlers = map[string]util.AllocationHandler{
		consts.PodAnnotationQoSLevelSharedCores:    policyImplement.sharedCoresAllocationHandler,
		consts.PodAnnotationQoSLevelDedicatedCores: policyImplement.dedicatedCoresAllocationHandler,
		consts.PodAnnotationQoSLevelReclaimedCores: policyImplement.reclaimedCoresAllocationHandler,
	}

	// register hint providers for pods with different QoS level
	policyImplement.hintHandlers = map[string]util.HintHandler{
		consts.PodAnnotationQoSLevelSharedCores:    policyImplement.sharedCoresHintHandler,
		consts.PodAnnotationQoSLevelDedicatedCores: policyImplement.dedicatedCoresHintHandler,
		consts.PodAnnotationQoSLevelReclaimedCores: policyImplement.reclaimedCoresHintHandler,
	}

	state.GetContainerRequestedCores = policyImplement.getContainerRequestedCores

	if err := policyImplement.cleanPools(); err != nil {
		return false, agent.ComponentStub{}, fmt.Errorf("cleanPools failed with error: %v", err)
	}

	if err := policyImplement.initReservePool(); err != nil {
		return false, agent.ComponentStub{}, fmt.Errorf("dynamic policy initReservePool failed with error: %v", err)
	}

	if err := policyImplement.initReclaimPool(); err != nil {
		return false, agent.ComponentStub{}, fmt.Errorf("dynamic policy initReclaimPool failed with error: %v", err)
	}

	err = agentCtx.MetaServer.ConfigurationManager.AddConfigWatcher(dynamic.AdminQoSConfigurationGVR)
	if err != nil {
		return false, nil, err
	}

	pluginWrapper, err := skeleton.NewRegistrationPluginWrapper(policyImplement, conf.QRMPluginSocketDirs, nil)
	if err != nil {
		return false, agent.ComponentStub{}, fmt.Errorf("dynamic policy new plugin wrapper failed with error: %v", err)
	}

	return true, &agent.PluginWrapper{GenericPlugin: pluginWrapper}, nil
}

func (p *DynamicPolicy) Name() string {
	return p.name
}

func (p *DynamicPolicy) ResourceName() string {
	return string(v1.ResourceCPU)
}

func (p *DynamicPolicy) Start() (err error) {
	general.Infof("called")

	p.Lock()
	defer func() {
		if err == nil {
			p.started = true
		}
		p.Unlock()
	}()

	if p.started {
		general.Infof("is already started")
		return nil
	}
	p.stopCh = make(chan struct{})

	go wait.Until(func() {
		_ = p.emitter.StoreInt64(util.MetricNameHeartBeat, 1, metrics.MetricTypeNameRaw)
	}, time.Second*30, p.stopCh)
	go wait.Until(p.clearResidualState, stateCheckPeriod, p.stopCh)
	go wait.Until(p.checkCPUSet, cpusetCheckPeriod, p.stopCh)

	// start cpu-idle syncing if needed
	if p.enableSyncingCPUIdle {
		if p.reclaimRelativeRootCgroupPath == "" {
			return fmt.Errorf("enable syncing cpu idle but not set reclaiemd relative root cgroup path in configuration")
		}
		go wait.Until(p.syncCPUIdle, syncCPUIdlePeriod, p.stopCh)
	}

	// start cpu-pressure eviction plugin if needed
	if p.enableCPUPressureEviction {
		var ctx context.Context
		ctx, p.cpuEvictionPluginCancel = context.WithCancel(context.Background())
		go p.cpuEvictionPlugin.Run(ctx)
	}

	// pre-check necessary dirs if sys-advisor is enabled
	if !p.enableCPUSysAdvisor {
		general.Infof("start dynamic policy cpu plugin without sys-advisor")
		return nil
	} else if p.cpuAdvisorSocketAbsPath == "" || p.cpuPluginSocketAbsPath == "" {
		return fmt.Errorf("invalid cpuAdvisorSocketAbsPath: %s or cpuPluginSocketAbsPath: %s",
			p.cpuAdvisorSocketAbsPath, p.cpuPluginSocketAbsPath)
	}

	general.Infof("start dynamic policy cpu plugin with sys-advisor")
	err = p.initAdvisorClientConn()
	if err != nil {
		general.Errorf("initAdvisorClientConn failed with error: %v", err)
		return
	}

	go wait.BackoffUntil(func() { p.serveForAdvisor(p.stopCh) }, wait.NewExponentialBackoffManager(
		800*time.Millisecond, 30*time.Second, 2*time.Minute, 2.0, 0, &clock.RealClock{}), true, p.stopCh)

	communicateWithCPUAdvisorServer := func() {
		general.Infof("waiting cpu plugin checkpoint server serving confirmation")
		if conn, err := process.Dial(p.cpuPluginSocketAbsPath, 5*time.Second); err != nil {
			general.Errorf("dial check at socket: %s failed with err: %v", p.cpuPluginSocketAbsPath, err)
			return
		} else {
			_ = conn.Close()
		}
		general.Infof("cpu plugin checkpoint server serving confirmed")

		err = p.pushCPUAdvisor()
		if err != nil {
			general.Errorf("sync existing containers to cpu advisor failed with error: %v", err)
			return
		}
		general.Infof("sync existing containers to cpu advisor successfully")

		// call lw of CPUAdvisorServer and do allocation
		if err := p.lwCPUAdvisorServer(p.stopCh); err != nil {
			general.Errorf("lwCPUAdvisorServer failed with error: %v", err)
		} else {
			general.Infof("lwCPUAdvisorServer finished")
		}
	}

	go wait.BackoffUntil(communicateWithCPUAdvisorServer, wait.NewExponentialBackoffManager(800*time.Millisecond,
		30*time.Second, 2*time.Minute, 2.0, 0, &clock.RealClock{}), true, p.stopCh)
	return nil
}

func (p *DynamicPolicy) Stop() error {
	p.Lock()
	defer func() {
		p.started = false
		p.Unlock()
		general.Infof("stopped")
	}()

	if !p.started {
		general.Warningf("already stopped")
		return nil
	}
	close(p.stopCh)

	if p.advisorConn != nil {
		return p.advisorConn.Close()
	}

	if p.cpuEvictionPluginCancel != nil {
		p.cpuEvictionPluginCancel()
	}
	return nil
}

// GetResourcesAllocation returns allocation results of corresponding resources
func (p *DynamicPolicy) GetResourcesAllocation(_ context.Context,
	req *pluginapi.GetResourcesAllocationRequest) (*pluginapi.GetResourcesAllocationResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("GetResourcesAllocation got nil req")
	}

	general.Infof("called")
	p.Lock()
	defer p.Unlock()

	podEntries := p.state.GetPodEntries()
	machineState := p.state.GetMachineState()

	// pooledCPUs is the total available cpu cores minus those that are reserved
	pooledCPUs := machineState.GetFilteredAvailableCPUSet(p.reservedCPUs,
		state.CheckDedicated, state.CheckDedicatedNUMABinding)
	pooledCPUsTopologyAwareAssignments, err := machine.GetNumaAwareAssignments(p.machineInfo.CPUTopology, pooledCPUs)
	if err != nil {
		return nil, fmt.Errorf("GetNumaAwareAssignments err: %v", err)
	}

	podResources := make(map[string]*pluginapi.ContainerResources)
	var allocationInfosJustFinishRampUp []*state.AllocationInfo
	for podUID, containerEntries := range podEntries {
		// if it's a pool, not returning to QRM
		if containerEntries.IsPoolEntry() {
			continue
		}

		if podResources[podUID] == nil {
			podResources[podUID] = &pluginapi.ContainerResources{}
		}

		for containerName, allocationInfo := range containerEntries {
			if allocationInfo == nil {
				continue
			}
			allocationInfo = allocationInfo.Clone()

			initTs, tsErr := time.Parse(util.QRMTimeFormat, allocationInfo.InitTimestamp)
			if tsErr != nil {
				if state.CheckShared(allocationInfo) {
					general.Errorf("pod: %s/%s, container: %s init timestamp parsed failed with error: %v, re-ramp-up it",
						allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName, tsErr)

					clonedPooledCPUs := pooledCPUs.Clone()
					clonedPooledCPUsTopologyAwareAssignments := machine.DeepcopyCPUAssignment(pooledCPUsTopologyAwareAssignments)

					allocationInfo.AllocationResult = clonedPooledCPUs
					allocationInfo.OriginalAllocationResult = clonedPooledCPUs
					allocationInfo.TopologyAwareAssignments = clonedPooledCPUsTopologyAwareAssignments
					allocationInfo.OriginalTopologyAwareAssignments = clonedPooledCPUsTopologyAwareAssignments
					// fill OwnerPoolName with empty string when ramping up
					allocationInfo.OwnerPoolName = advisorapi.EmptyOwnerPoolName
					allocationInfo.RampUp = true
				}

				allocationInfo.InitTimestamp = time.Now().Format(util.QRMTimeFormat)
				p.state.SetAllocationInfo(podUID, containerName, allocationInfo)
			} else if allocationInfo.RampUp && time.Now().After(initTs.Add(transitionPeriod)) {
				general.Infof("pod: %s/%s, container: %s ramp up finished", allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName)
				allocationInfo.RampUp = false
				p.state.SetAllocationInfo(podUID, containerName, allocationInfo)

				if state.CheckShared(allocationInfo) {
					allocationInfosJustFinishRampUp = append(allocationInfosJustFinishRampUp, allocationInfo)
				}
			}

			if podResources[podUID].ContainerResources == nil {
				podResources[podUID].ContainerResources = make(map[string]*pluginapi.ResourceAllocation)
			}
			podResources[podUID].ContainerResources[containerName] = &pluginapi.ResourceAllocation{
				ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{
					string(v1.ResourceCPU): {
						OciPropertyName:   util.OCIPropertyNameCPUSetCPUs,
						IsNodeResource:    false,
						IsScalarResource:  true,
						AllocatedQuantity: float64(allocationInfo.AllocationResult.Size()),
						AllocationResult:  allocationInfo.AllocationResult.String(),
					},
				},
			}
		}
	}

	if len(allocationInfosJustFinishRampUp) > 0 {
		if err = p.putAllocationsAndAdjustAllocationEntries(allocationInfosJustFinishRampUp); err != nil {
			// not influencing return response to kubelet when putAllocationsAndAdjustAllocationEntries failed
			general.Errorf("putAllocationsAndAdjustAllocationEntries failed with error: %v", err)
		}
	}

	return &pluginapi.GetResourcesAllocationResponse{
		PodResources: podResources,
	}, nil
}

// GetTopologyAwareResources returns allocation results of corresponding resources as machineInfo aware format
func (p *DynamicPolicy) GetTopologyAwareResources(_ context.Context,
	req *pluginapi.GetTopologyAwareResourcesRequest) (*pluginapi.GetTopologyAwareResourcesResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("GetTopologyAwareResources got nil req")
	}

	general.Infof("called")
	p.RLock()
	defer p.RUnlock()

	allocationInfo := p.state.GetAllocationInfo(req.PodUid, req.ContainerName)
	if allocationInfo == nil {
		return nil, fmt.Errorf("pod: %s, container: %s is not show up in cpu plugin state", req.PodUid, req.ContainerName)
	}

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
			string(v1.ResourceCPU): {
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
			string(v1.ResourceCPU): {
				IsNodeResource:                    false,
				IsScalarResource:                  true,
				AggregatedQuantity:                float64(allocationInfo.AllocationResult.Size()),
				OriginalAggregatedQuantity:        float64(allocationInfo.OriginalAllocationResult.Size()),
				TopologyAwareQuantityList:         util.GetTopologyAwareQuantityFromAssignments(allocationInfo.TopologyAwareAssignments),
				OriginalTopologyAwareQuantityList: util.GetTopologyAwareQuantityFromAssignments(allocationInfo.OriginalTopologyAwareAssignments),
			},
		}
	}

	return resp, nil
}

// GetTopologyAwareAllocatableResources returns corresponding allocatable resources as machineInfo aware format
func (p *DynamicPolicy) GetTopologyAwareAllocatableResources(_ context.Context,
	_ *pluginapi.GetTopologyAwareAllocatableResourcesRequest) (*pluginapi.GetTopologyAwareAllocatableResourcesResponse, error) {
	general.Infof("is called")

	numaNodes := p.machineInfo.CPUDetails.NUMANodes().ToSliceInt()
	topologyAwareAllocatableQuantityList := make([]*pluginapi.TopologyAwareQuantity, 0, len(numaNodes))
	topologyAwareCapacityQuantityList := make([]*pluginapi.TopologyAwareQuantity, 0, len(numaNodes))

	for _, numaNode := range numaNodes {
		numaNodeCPUs := p.machineInfo.CPUDetails.CPUsInNUMANodes(numaNode).Clone()
		topologyAwareAllocatableQuantityList = append(topologyAwareAllocatableQuantityList, &pluginapi.TopologyAwareQuantity{
			ResourceValue: float64(numaNodeCPUs.Difference(p.reservedCPUs).Size()),
			Node:          uint64(numaNode),
		})
		topologyAwareCapacityQuantityList = append(topologyAwareCapacityQuantityList, &pluginapi.TopologyAwareQuantity{
			ResourceValue: float64(numaNodeCPUs.Size()),
			Node:          uint64(numaNode),
		})
	}

	return &pluginapi.GetTopologyAwareAllocatableResourcesResponse{
		AllocatableResources: map[string]*pluginapi.AllocatableTopologyAwareResource{
			string(v1.ResourceCPU): {
				IsNodeResource:                       false,
				IsScalarResource:                     true,
				AggregatedAllocatableQuantity:        float64(p.machineInfo.NumCPUs - p.reservedCPUs.Size()),
				TopologyAwareAllocatableQuantityList: topologyAwareAllocatableQuantityList,
				AggregatedCapacityQuantity:           float64(p.machineInfo.NumCPUs),
				TopologyAwareCapacityQuantityList:    topologyAwareCapacityQuantityList,
			},
		},
	}, nil
}

// GetTopologyHints returns hints of corresponding resources
func (p *DynamicPolicy) GetTopologyHints(ctx context.Context,
	req *pluginapi.ResourceRequest) (resp *pluginapi.ResourceHintsResponse, err error) {
	if req == nil {
		return nil, fmt.Errorf("GetTopologyHints got nil req")
	}

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
		"numCPUs", reqInt)

	p.RLock()
	defer func() {
		p.RUnlock()
		if err != nil {
			_ = p.emitter.StoreInt64(util.MetricNameGetTopologyHintsFailed, 1, metrics.MetricTypeNameRaw)
		}
	}()

	if req.ContainerType == pluginapi.ContainerType_INIT {
		return util.PackResourceHintsResponse(req, string(v1.ResourceCPU),
			map[string]*pluginapi.ListOfTopologyHints{
				string(v1.ResourceCPU): nil, // indicates that there is no numa preference
			})
	}

	if p.hintHandlers[qosLevel] == nil {
		return nil, fmt.Errorf("katalyst QoS level: %s is not supported yet", qosLevel)
	}
	return p.hintHandlers[qosLevel](ctx, req)
}

// GetResourcePluginOptions returns options to be communicated with Resource Manager
func (p *DynamicPolicy) GetResourcePluginOptions(context.Context,
	*pluginapi.Empty) (*pluginapi.ResourcePluginOptions, error) {
	general.Infof("called")
	return &pluginapi.ResourcePluginOptions{PreStartRequired: false,
		WithTopologyAlignment: true,
		NeedReconcile:         true,
	}, nil
}

// Allocate is called during pod admit so that the resource
// plugin can allocate corresponding resource for the container
// according to resource request
func (p *DynamicPolicy) Allocate(ctx context.Context,
	req *pluginapi.ResourceRequest) (resp *pluginapi.ResourceAllocationResponse, respErr error) {
	if req == nil {
		return nil, fmt.Errorf("allocate got nil req")
	}

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
		"numCPUs", reqInt)

	p.Lock()
	defer func() {
		// calls sys-advisor to inform the latest container
		if p.enableCPUSysAdvisor && respErr == nil && req.ContainerType != pluginapi.ContainerType_INIT {
			_, err := p.advisorClient.AddContainer(ctx, &advisorapi.AddContainerRequest{
				PodUid:          req.PodUid,
				PodNamespace:    req.PodNamespace,
				PodName:         req.PodName,
				ContainerName:   req.ContainerName,
				ContainerType:   req.ContainerType,
				ContainerIndex:  req.ContainerIndex,
				Labels:          maputil.CopySS(req.Labels),
				Annotations:     maputil.CopySS(req.Annotations),
				QosLevel:        qosLevel,
				RequestQuantity: uint64(reqInt),
			})

			if err != nil {
				resp = nil
				respErr = fmt.Errorf("add container to qos aware server failed with error: %v", err)
				_ = p.removeContainer(req.PodUid, req.ContainerName)
			}
		} else if respErr != nil {
			_ = p.removeContainer(req.PodUid, req.ContainerName)
			_ = p.emitter.StoreInt64(util.MetricNameAllocateFailed, 1, metrics.MetricTypeNameRaw)
		}

		p.Unlock()
		return
	}()

	allocationInfo := p.state.GetAllocationInfo(req.PodUid, req.ContainerName)
	if allocationInfo != nil && allocationInfo.OriginalAllocationResult.Size() >= reqInt {
		general.InfoS("already allocated and meet requirement",
			"podNamespace", req.PodNamespace,
			"podName", req.PodName,
			"containerName", req.ContainerName,
			"numCPUs", reqInt,
			"originalAllocationResult", allocationInfo.OriginalAllocationResult.String(),
			"currentResult", allocationInfo.AllocationResult.String())

		return &pluginapi.ResourceAllocationResponse{
			PodUid:         req.PodUid,
			PodNamespace:   req.PodNamespace,
			PodName:        req.PodName,
			ContainerName:  req.ContainerName,
			ContainerType:  req.ContainerType,
			ContainerIndex: req.ContainerIndex,
			PodRole:        req.PodRole,
			PodType:        req.PodType,
			ResourceName:   string(v1.ResourceCPU),
			AllocationResult: &pluginapi.ResourceAllocation{
				ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{
					string(v1.ResourceCPU): {
						OciPropertyName:   util.OCIPropertyNameCPUSetCPUs,
						IsNodeResource:    false,
						IsScalarResource:  true,
						AllocatedQuantity: float64(allocationInfo.AllocationResult.Size()),
						AllocationResult:  allocationInfo.AllocationResult.String(),
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
			ResourceName:   string(v1.ResourceCPU),
			Labels:         general.DeepCopyMap(req.Labels),
			Annotations:    general.DeepCopyMap(req.Annotations),
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
func (p *DynamicPolicy) PreStartContainer(context.Context,
	*pluginapi.PreStartContainerRequest) (*pluginapi.PreStartContainerResponse, error) {
	return nil, nil
}

func (p *DynamicPolicy) RemovePod(ctx context.Context,
	req *pluginapi.RemovePodRequest) (resp *pluginapi.RemovePodResponse, err error) {
	if req == nil {
		return nil, fmt.Errorf("RemovePod got nil req")
	}
	general.InfoS("is called", "podUID", req.PodUid)

	p.Lock()
	defer func() {
		p.Unlock()
		if err != nil {
			_ = p.emitter.StoreInt64(util.MetricNameRemovePodFailed, 1, metrics.MetricTypeNameRaw)
		}
	}()

	if p.enableCPUSysAdvisor {
		_, err = p.advisorClient.RemovePod(ctx, &advisorapi.RemovePodRequest{PodUid: req.PodUid})
		if err != nil {
			return nil, fmt.Errorf("remove pod in QoS aware server failed with error: %v", err)
		}
	}

	err = p.removePod(req.PodUid)
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

func (p *DynamicPolicy) removePod(podUID string) error {
	podEntries := p.state.GetPodEntries()
	if len(podEntries[podUID]) == 0 {
		return nil
	}
	delete(podEntries, podUID)

	updatedMachineState, err := state.GenerateMachineStateFromPodEntries(p.machineInfo.CPUTopology, podEntries)
	if err != nil {
		return fmt.Errorf("GenerateMachineStateFromPodEntries failed with error: %v", err)
	}

	p.state.SetPodEntries(podEntries)
	p.state.SetMachineState(updatedMachineState)
	return nil
}

func (p *DynamicPolicy) removeContainer(podUID, containerName string) error {
	podEntries := p.state.GetPodEntries()
	if podEntries[podUID][containerName] == nil {
		return nil
	}
	delete(podEntries[podUID], containerName)

	updatedMachineState, err := state.GenerateMachineStateFromPodEntries(p.machineInfo.CPUTopology, podEntries)
	if err != nil {
		return fmt.Errorf("GenerateMachineStateFromPodEntries failed with error: %v", err)
	}

	p.state.SetPodEntries(podEntries)
	p.state.SetMachineState(updatedMachineState)
	return nil
}

// initAdvisorClientConn initializes cpu-advisor related connections
func (p *DynamicPolicy) initAdvisorClientConn() (err error) {
	cpuAdvisorConn, err := process.Dial(p.cpuAdvisorSocketAbsPath, 5*time.Second)
	if err != nil {
		err = fmt.Errorf("get cpu advisor connection with socket: %s failed with error: %v", p.cpuAdvisorSocketAbsPath, err)
		return
	}

	p.advisorClient = advisorapi.NewCPUAdvisorClient(cpuAdvisorConn)
	p.advisorConn = cpuAdvisorConn
	return nil
}

// cleanPools is used to clean pools-related data in local state
func (p *DynamicPolicy) cleanPools() error {
	remainPools := make(map[string]bool)

	// walk through pod entries to put them into specified pool maps
	podEntries := p.state.GetPodEntries()
	for _, entries := range podEntries {
		if entries.IsPoolEntry() {
			continue
		}

		for _, allocationInfo := range entries {
			ownerPool := allocationInfo.GetOwnerPoolName()
			specifiedPool := allocationInfo.GetSpecifiedPoolName()
			if specifiedPool != advisorapi.EmptyOwnerPoolName || ownerPool != advisorapi.EmptyOwnerPoolName {
				remainPools[specifiedPool] = true
			} else if state.CheckReclaimed(allocationInfo) {
				remainPools[state.PoolNameReclaim] = true
			} else if state.CheckShared(allocationInfo) {
				remainPools[state.PoolNameShare] = true
			}
		}
	}

	// if pool exists in entries, but has no corresponding container, we need to delete it
	poolsToDelete := sets.NewString()
	for poolName, entries := range podEntries {
		if entries.IsPoolEntry() {
			if !remainPools[poolName] && !state.ResidentPools.Has(poolName) {
				poolsToDelete.Insert(poolName)
			}
		}
	}

	if poolsToDelete.Len() > 0 {
		general.Infof("pools to delete: %v", poolsToDelete.UnsortedList())
		for _, poolName := range poolsToDelete.UnsortedList() {
			delete(podEntries, poolName)
		}

		machineState, err := state.GenerateMachineStateFromPodEntries(p.machineInfo.CPUTopology, podEntries)
		if err != nil {
			return fmt.Errorf("calculate machineState by podEntries failed with error: %v", err)
		}

		p.state.SetPodEntries(podEntries)
		p.state.SetMachineState(machineState)
	} else {
		general.Infof("there is no pool to delete")
	}

	return nil
}

// initReservePool initializes reserve pool for system cores workload
func (p *DynamicPolicy) initReservePool() error {
	reserveAllocationInfo := p.state.GetAllocationInfo(state.PoolNameReserve, advisorapi.FakedContainerName)
	if reserveAllocationInfo != nil && !reserveAllocationInfo.AllocationResult.IsEmpty() {
		general.Infof("pool: %s allocation result transform from %s to %s",
			state.PoolNameReserve, reserveAllocationInfo.AllocationResult.String(), p.reservedCPUs)
	}

	general.Infof("initReservePool %s: %s", state.PoolNameReserve, p.reservedCPUs)
	topologyAwareAssignments, err := machine.GetNumaAwareAssignments(p.machineInfo.CPUTopology, p.reservedCPUs)
	if err != nil {
		return fmt.Errorf("unable to calculate topologyAwareAssignments for pool: %s, result cpuset: %s, error: %v",
			state.PoolNameReserve, p.reservedCPUs.String(), err)
	}

	curReserveAllocationInfo := &state.AllocationInfo{
		PodUid:                           state.PoolNameReserve,
		OwnerPoolName:                    state.PoolNameReserve,
		AllocationResult:                 p.reservedCPUs.Clone(),
		OriginalAllocationResult:         p.reservedCPUs.Clone(),
		TopologyAwareAssignments:         topologyAwareAssignments,
		OriginalTopologyAwareAssignments: machine.DeepcopyCPUAssignment(topologyAwareAssignments),
	}
	p.state.SetAllocationInfo(state.PoolNameReserve, advisorapi.FakedContainerName, curReserveAllocationInfo)

	return nil
}

// initReclaimPool initializes pools for reclaimed-cores.
// if this info already exists in state-file, just use it, otherwise calculate right away
func (p *DynamicPolicy) initReclaimPool() error {
	reclaimedAllocationInfo := p.state.GetAllocationInfo(state.PoolNameReclaim, advisorapi.FakedContainerName)
	if reclaimedAllocationInfo == nil {
		podEntries := p.state.GetPodEntries()
		noneResidentCPUs := podEntries.GetFilteredPoolsCPUSet(state.ResidentPools)

		machineState := p.state.GetMachineState()
		availableCPUs := machineState.GetFilteredAvailableCPUSet(p.reservedCPUs,
			state.CheckDedicated, state.CheckDedicatedNUMABinding).Difference(noneResidentCPUs)

		var initReclaimedCPUSetSize int
		if availableCPUs.Size() >= reservedReclaimedCPUsSize {
			initReclaimedCPUSetSize = reservedReclaimedCPUsSize
		} else {
			initReclaimedCPUSetSize = availableCPUs.Size()
		}

		reclaimedCPUSet, _, err := calculator.TakeByNUMABalance(p.machineInfo, availableCPUs, initReclaimedCPUSetSize)
		if err != nil {
			return fmt.Errorf("takeByNUMABalance faild in initReclaimPool for %s and %s with error: %v",
				state.PoolNameShare, state.PoolNameReclaim, err)
		}

		// for residual pools, we must make them exist even if cause overlap
		// todo: noneResidentCPUs is the same as reservedCPUs, why should we do this?
		allAvailableCPUs := p.machineInfo.CPUDetails.CPUs().Difference(p.reservedCPUs)
		if reclaimedCPUSet.IsEmpty() {
			reclaimedCPUSet, _, err = calculator.TakeByNUMABalance(p.machineInfo, allAvailableCPUs, reservedReclaimedCPUsSize)
			if err != nil {
				return fmt.Errorf("fallback takeByNUMABalance faild in initReclaimPool for %s with error: %v",
					state.PoolNameReclaim, err)
			}
		}

		for poolName, cset := range map[string]machine.CPUSet{state.PoolNameReclaim: reclaimedCPUSet} {
			general.Infof("initReclaimPool %s: %s", poolName, cset.String())
			topologyAwareAssignments, err := machine.GetNumaAwareAssignments(p.machineInfo.CPUTopology, cset)
			if err != nil {
				return fmt.Errorf("unable to calculate topologyAwareAssignments for pool: %s, "+
					"result cpuset: %s, error: %v", poolName, cset.String(), err)
			}

			curPoolAllocationInfo := &state.AllocationInfo{
				PodUid:                           poolName,
				OwnerPoolName:                    poolName,
				AllocationResult:                 cset.Clone(),
				OriginalAllocationResult:         cset.Clone(),
				TopologyAwareAssignments:         topologyAwareAssignments,
				OriginalTopologyAwareAssignments: machine.DeepcopyCPUAssignment(topologyAwareAssignments),
			}
			p.state.SetAllocationInfo(poolName, advisorapi.FakedContainerName, curPoolAllocationInfo)
		}
	} else {
		general.Infof("exist initial %s: %s", state.PoolNameReclaim, reclaimedAllocationInfo.AllocationResult.String())
	}

	return nil
}

// getContainerRequestedCores parses and returns request cores for the given container
func (p *DynamicPolicy) getContainerRequestedCores(allocationInfo *state.AllocationInfo) int {
	if allocationInfo == nil {
		general.Errorf("got nil allocationInfo")
		return 0
	}

	if allocationInfo.RequestQuantity == 0 {
		if p.metaServer == nil {
			general.Errorf("got nil metaServer")
			return 0
		}

		container, err := p.metaServer.GetContainerSpec(allocationInfo.PodUid, allocationInfo.ContainerName)
		if err != nil || container == nil {
			general.Errorf("get container failed with error: %v", err)
			return 0
		}

		cpuQuantity := native.GetCPUQuantity(container.Resources.Requests)
		allocationInfo.RequestQuantity = general.Max(int(cpuQuantity.Value()), 0)
		general.Infof("get cpu request quantity: %d for pod: %s/%s container: %s from podWatcher",
			allocationInfo.RequestQuantity, allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName)
	}
	return allocationInfo.RequestQuantity
}
