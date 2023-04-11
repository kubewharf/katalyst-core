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
	"net"
	"os"
	"sync"
	"time"

	"google.golang.org/grpc"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"
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
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/global/adminqos"
	"github.com/kubewharf/katalyst-core/pkg/config/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	cgroupcm "github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	cgroupcmutils "github.com/kubewharf/katalyst-core/pkg/util/cgroup/manager"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
	"github.com/kubewharf/katalyst-core/pkg/util/process"
)

const (
	cpuPluginStateFileName             = "cpu_plugin_state"
	CPUResourcePluginPolicyNameDynamic = "dynamic"
)

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

// DynamicPolicy is the policy that's used by default;
// it will consider the dynamic running information to calculate
// and adjust resource requirements and configurations
type DynamicPolicy struct {
	name                    string
	stopCh                  chan struct{}
	started                 bool
	qosConfig               *generic.QoSConfiguration
	reclaimedResourceConfig *adminqos.ReclaimedResourceConfiguration
	// emitter is used to emit metrics.
	emitter metrics.MetricEmitter
	// metaGetter is used to collect metadata universal metaServer.
	metaServer     *metaserver.MetaServer
	machineInfo    *machine.KatalystMachineInfo
	state          state.State
	residualHitMap map[string]int64
	advisorClient  advisorapi.CPUAdvisorClient
	advisorConn    *grpc.ClientConn
	advisorapi.UnimplementedCPUPluginServer
	allocationHandlers map[string]util.AllocationHandler
	hintHandlers       map[string]util.HintHandler

	cpuEvictionPlugin       *agent.PluginWrapper
	cpuEvictionPluginCancel context.CancelFunc

	sync.RWMutex

	// those are parsed from configurations
	enableCPUSysAdvisor           bool
	reservedCPUs                  machine.CPUSet
	cpuAdvisorSocketAbsPath       string
	cpuPluginSocketAbsPath        string
	extraStateFileAbsPath         string
	enableCPUPressureEviction     bool
	enableCPUIdle                 bool
	enableSyncingCPUIdle          bool
	reclaimRelativeRootCgroupPath string
}

func NewDynamicPolicy(agentCtx *agent.GenericContext, conf *config.Configuration, _ interface{}, agentName string) (bool, agent.Component, error) {
	allCPUs := agentCtx.CPUDetails.CPUs().Clone()
	reservedCPUsNum := conf.ReservedCPUCores

	reservedCPUs, _, reserveErr := calculator.TakeHTByNUMABalance(agentCtx.KatalystMachineInfo, allCPUs, reservedCPUsNum)
	if reserveErr != nil {
		return false, agent.ComponentStub{}, fmt.Errorf("takeByNUMABalance for reservedCPUsNum: %d failed with error: %v",
			conf.ReservedCPUCores, reserveErr)
	}
	klog.Infof("[CPUDynamicPolicy.NewDynamicPolicy] take reservedCPUs: %s by reservedCPUsNum: %d", reservedCPUs.String(), reservedCPUsNum)

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
		machineInfo:                   agentCtx.KatalystMachineInfo,
		qosConfig:                     conf.QoSConfiguration,
		reclaimedResourceConfig:       conf.ReclaimedResourceConfiguration,
		emitter:                       wrappedEmitter,
		metaServer:                    agentCtx.MetaServer,
		state:                         stateImpl,
		cpuAdvisorSocketAbsPath:       conf.CPUAdvisorSocketAbsPath,
		cpuPluginSocketAbsPath:        conf.CPUPluginSocketAbsPath,
		stopCh:                        make(chan struct{}),
		enableCPUSysAdvisor:           conf.CPUQRMPluginConfig.EnableSysAdvisor,
		reservedCPUs:                  reservedCPUs,
		residualHitMap:                make(map[string]int64),
		extraStateFileAbsPath:         conf.ExtraStateFileAbsPath,
		name:                          fmt.Sprintf("%s_%s", agentName, CPUResourcePluginPolicyNameDynamic),
		enableCPUPressureEviction:     conf.EnableCPUPressureEviction,
		cpuEvictionPlugin:             cpuEvictionPlugin,
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

	pluginWrapper, err := skeleton.NewRegistrationPluginWrapper(
		policyImplement,
		conf.QRMPluginSocketDirs, nil)

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
	klog.Infof("cpu-resource-plugin dynamic-policy start called")

	p.Lock()
	defer func() {
		if err == nil {
			p.started = true
		}
		p.Unlock()
	}()

	if p.started {
		klog.Infof("[CPUDynamicPolicy.Start] DynamicPolicy is already started")
		return nil
	}
	p.stopCh = make(chan struct{})

	go wait.Until(func() {
		_ = p.emitter.StoreInt64(util.MetricNameHeartBeat, 1, metrics.MetricTypeNameRaw)
	}, time.Second*30, p.stopCh)
	go wait.Until(p.clearResidualState, stateCheckPeriod, p.stopCh)
	go wait.Until(p.checkCPUSet, cpusetCheckPeriod, p.stopCh)

	if p.enableSyncingCPUIdle {
		if !cgroupcm.IsCPUIdleSupported() {
			return fmt.Errorf("enable cpu idle in unsupported environment")
		} else if p.reclaimRelativeRootCgroupPath == "" {
			return fmt.Errorf("enable syncing cpu idle but not set reclaiemd relative root cgroup path in configuration")
		}

		go wait.Until(p.syncCPUIdle, syncCPUIdlePeriod, p.stopCh)
	}

	if p.enableCPUPressureEviction {
		var ctx context.Context
		ctx, p.cpuEvictionPluginCancel = context.WithCancel(context.Background())
		go p.cpuEvictionPlugin.Run(ctx)
	}

	if !p.enableCPUSysAdvisor {
		klog.Infof("[CPUDynamicPolicy.Start] start dynamic policy cpu plugin without sys-advisor")
		return nil
	}
	klog.Infof("[CPUDynamicPolicy.Start] start dynamic policy cpu plugin with sys-advisor")

	err = p.initialize()
	if err != nil {
		klog.Errorf("[CPUDynamicPolicy.Start] initialize failed with error: %v", err)
		return
	}

	go wait.BackoffUntil(func() { p.serveCPUPluginCheckpoint(p.stopCh) }, wait.NewExponentialBackoffManager(
		800*time.Millisecond, 30*time.Second, 2*time.Minute, 2.0, 0, &clock.RealClock{}), true, p.stopCh)

	communicateWithCPUAdvisorServer := func() {
		klog.Infof("[CPUDynamicPolicy.Start] waiting cpu plugin checkpoint server serving confirmation")

		if conn, err := process.Dial(p.cpuPluginSocketAbsPath, 5*time.Second); err != nil {
			klog.Errorf("[CPUDynamicPolicy.Start] dial check at socket: %s failed with err: %v", p.cpuPluginSocketAbsPath, err)
			return
		} else {
			_ = conn.Close()
		}

		klog.Infof("[CPUDynamicPolicy.Start] cpu plugin checkpoint server serving confirmed")

		err = p.syncExistingContainersToCPUAdvisor()

		if err != nil {
			klog.Errorf("[CPUDynamicPolicy.Start] sync existing containers to cpu advisor failed with error: %v", err)
			return
		}

		klog.Infof("[CPUDynamicPolicy.Start] sync existing containers to cpu advisor successfully")

		// call lw of CPUAdvisorServer and do allocation
		err := p.lwCPUAdvisorServer(p.stopCh)

		if err != nil {
			klog.Errorf("[CPUDynamicPolicy.Start] lwCPUAdvisorServer failed with error: %v", err)
		} else {
			klog.Infof("[CPUDynamicPolicy.Start] lwCPUAdvisorServer finished")
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

		klog.Infof("[CPUDynamicPolicy.Stop] DynamicPolicy stopped")
	}()

	if !p.started {
		klog.Warningf("[CPUDynamicPolicy.Stop] DynamicPolicy already stopped")
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

	klog.Infof("[CPUDynamicPolicy] GetResourcesAllocation is called")
	p.Lock()
	defer p.Unlock()

	podEntries := p.state.GetPodEntries()
	machineState := p.state.GetMachineState()

	// pooledCPUs is the total available cpu cores minus those that are reserved
	pooledCPUs := machineState.GetAvailableCPUSetExcludeDedicatedCoresPods(p.reservedCPUs)
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
				if allocationInfo.QoSLevel == consts.PodAnnotationQoSLevelSharedCores {
					klog.Errorf("[CPUDynamicPolicy.GetResourcesAllocation] pod: %s/%s, container: %s init timestamp parsed failed with error: %v, re-ramp-up it",
						allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName, tsErr)

					clonedPooledCPUs := pooledCPUs.Clone()
					clonedPooledCPUsTopologyAwareAssignments := util.DeepCopyTopologyAwareAssignments(pooledCPUsTopologyAwareAssignments)

					allocationInfo.AllocationResult = clonedPooledCPUs
					allocationInfo.OriginalAllocationResult = clonedPooledCPUs
					allocationInfo.TopologyAwareAssignments = clonedPooledCPUsTopologyAwareAssignments
					allocationInfo.OriginalTopologyAwareAssignments = clonedPooledCPUsTopologyAwareAssignments
					allocationInfo.OwnerPoolName = "" // fill OwnerPoolName with empty string when ramping up
					allocationInfo.RampUp = true
				}

				allocationInfo.InitTimestamp = time.Now().Format(util.QRMTimeFormat)
				p.state.SetAllocationInfo(podUID, containerName, allocationInfo)
			} else if allocationInfo.RampUp && time.Now().After(initTs.Add(transitionPeriod)) {
				klog.Infof("[CPUDynamicPolicy.GetResourcesAllocation] pod: %s/%s, container: %s ramp up finished",
					allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName)
				allocationInfo.RampUp = false
				p.state.SetAllocationInfo(podUID, containerName, allocationInfo)

				if allocationInfo.QoSLevel == consts.PodAnnotationQoSLevelSharedCores {
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
		err = p.putContainersAndAdjustAllocationEntries(allocationInfosJustFinishRampUp)
		if err != nil {
			// not influencing return response to kubelet when putContainersAndAdjustAllocationEntries failed
			klog.Errorf("[CPUDynamicPolicy.GetResourcesAllocation] putContainersAndAdjustAllocationEntries failed with error: %v", err)
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

	klog.Infof("[CPUDynamicPolicy] GetTopologyAwareResources is called")
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

	if allocationInfo.ContainerType == pluginapi.ContainerType_SIDECAR.String() {
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
	klog.Infof("[CPUDynamicPolicy] GetTopologyAwareAllocatableResources is called")

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
		klog.Errorf("[CPUDynamicPolicy.GetTopologyHints] %s", err.Error())
		return nil, err
	}

	reqInt, err := getReqQuantityFromResourceReq(req)
	if err != nil {
		return nil, fmt.Errorf("getReqQuantityFromResourceReq failed with error: %v", err)
	}

	klog.InfoS("[CPUDynamicPolicy] GetTopologyHints is called",
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
		return util.PackResourceHintsResponse(req, string(v1.ResourceCPU), map[string]*pluginapi.ListOfTopologyHints{
			string(v1.ResourceCPU): nil, // indicates that there is no numa preference
		})
	}

	if p.hintHandlers[qosLevel] == nil {
		return nil, fmt.Errorf("katalyst QoS level: %s is not supported yet", qosLevel)
	}

	return p.hintHandlers[qosLevel](ctx, req)
}

// GetResourcePluginOptions returns options to be communicated with Resource Manager
func (p *DynamicPolicy) GetResourcePluginOptions(context.Context, *pluginapi.Empty) (*pluginapi.ResourcePluginOptions, error) {
	klog.Infof("[CPUDynamicPolicy] GetResourcePluginOptions is called")

	return &pluginapi.ResourcePluginOptions{PreStartRequired: false,
		WithTopologyAlignment: true,
		NeedReconcile:         true,
	}, nil
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
		klog.Errorf("[CPUDynamicPolicy.Allocate] %s", err.Error())
		return nil, err
	}

	reqInt, err := getReqQuantityFromResourceReq(req)
	if err != nil {
		return nil, fmt.Errorf("getReqQuantityFromResourceReq failed with error: %v", err)
	}

	klog.InfoS("[CPUDynamicPolicy] Allocate is called",
		"podNamespace", req.PodNamespace,
		"podName", req.PodName,
		"containerName", req.ContainerName,
		"podType", req.PodType,
		"podRole", req.PodRole,
		"qosLevel", qosLevel,
		"numCPUs", reqInt)

	p.Lock()
	defer func() {
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
		klog.InfoS("[cpu_plugin][CPUDynamicPolicy] already allocated and meet requirement",
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

// PreStartContainer is called, if indicated by resource plugin during registeration phase,
// before each container start. Resource plugin can run resource specific operations
// such as resetting the resource before making resources available to the container
func (p *DynamicPolicy) PreStartContainer(context.Context, *pluginapi.PreStartContainerRequest) (*pluginapi.PreStartContainerResponse, error) {
	return nil, nil
}

func (p *DynamicPolicy) GetCheckpoint(_ context.Context, req *advisorapi.GetCheckpointRequest) (*advisorapi.GetCheckpointResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("GetCheckpoint got nil req")
	}

	klog.Infof("[CPUDynamicPolicy] GetCheckpoint is called")

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

			if allocationInfo.QoSLevel != consts.PodAnnotationQoSLevelSharedCores && allocationInfo.QoSLevel != consts.PodAnnotationQoSLevelReclaimedCores {
				chkEntries[uid].Entries[entryName].TopologyAwareAssignments = util.ParseTopologyAwareAssignments(allocationInfo.TopologyAwareAssignments)
				chkEntries[uid].Entries[entryName].OriginalTopologyAwareAssignments = util.ParseTopologyAwareAssignments(allocationInfo.OriginalTopologyAwareAssignments)
			}
		}
	}

	return &advisorapi.GetCheckpointResponse{
		Entries: chkEntries,
	}, nil
}

func (p *DynamicPolicy) RemovePod(ctx context.Context, req *pluginapi.RemovePodRequest) (resp *pluginapi.RemovePodResponse, err error) {
	if req == nil {
		return nil, fmt.Errorf("RemovePod got nil req")
	}
	klog.InfoS("[CPUDynamicPolicy] RemovePod is called",
		"podUID", req.PodUid)

	p.Lock()
	defer func() {
		p.Unlock()

		if err != nil {
			_ = p.emitter.StoreInt64(util.MetricNameRemovePodFailed, 1, metrics.MetricTypeNameRaw)
		}
	}()

	if p.enableCPUSysAdvisor {
		_, err = p.advisorClient.RemovePod(ctx, &advisorapi.RemovePodRequest{
			PodUid: req.PodUid,
		})

		if err != nil {
			return nil, fmt.Errorf("remove pod in QoS aware server failed with error: %v", err)
		}
	}

	err = p.removePod(req.PodUid)
	if err != nil {
		klog.ErrorS(err, "[CPUDynamicPolicy.RemovePod] remove pod failed with error", "podUID", req.PodUid)
		return nil, err
	}

	err = p.adjustAllocationEntries()
	if err != nil {
		klog.ErrorS(err, "[CPUDynamicPolicy.RemovePod] adjustAllocationEntries failed", "podUID", req.PodUid)
	}

	return &pluginapi.RemovePodResponse{}, nil
}

// cleanPools is used to clean pools-related data in local state
func (p *DynamicPolicy) cleanPools() error {
	requestPools := make(map[string]bool)

	// walk through pod entries to get
	podEntries := p.state.GetPodEntries()
	for _, entries := range podEntries {
		if entries.IsPoolEntry() {
			continue
		}

		for _, allocationInfo := range entries {
			requestPoolName := allocationInfo.Annotations[consts.PodAnnotationCPUEnhancementCPUSet]
			ownerPool := allocationInfo.OwnerPoolName
			if requestPoolName != "" {
				requestPools[requestPoolName] = true
			} else if ownerPool != "" {
				requestPools[ownerPool] = true
			} else if allocationInfo.QoSLevel == consts.PodAnnotationQoSLevelReclaimedCores {
				requestPools[state.PoolNameReclaim] = true
			} else if allocationInfo.QoSLevel == consts.PodAnnotationQoSLevelSharedCores {
				requestPools[state.PoolNameShare] = true
			}
		}
	}

	poolsToDelete := sets.NewString()
	for poolName, entries := range podEntries {
		if entries.IsPoolEntry() {
			if !requestPools[poolName] && !state.ResidentPools.Has(poolName) {
				poolsToDelete.Insert(poolName)
			}
		}
	}

	if poolsToDelete.Len() > 0 {
		klog.Infof("[CPUDynamicPolicy.cleanPools] pools to delete: %v", poolsToDelete.UnsortedList())

		for _, poolName := range poolsToDelete.UnsortedList() {
			delete(podEntries, poolName)
		}

		machineState, err := state.GenerateCPUMachineStateByPodEntries(p.machineInfo.CPUTopology, podEntries)
		if err != nil {
			return fmt.Errorf("calculate machineState by podEntries failed with error: %v", err)
		}

		p.state.SetPodEntries(podEntries)
		p.state.SetMachineState(machineState)
	} else {
		klog.Infof("[CPUDynamicPolicy.cleanPools] there is no pool to delete")
	}

	return nil
}

// clearResidualState is used to clean residual pods in local state
func (p *DynamicPolicy) clearResidualState() {
	klog.Infof("[CPUDynamicPolicy] exec clearResidualState")
	residualSet := make(map[string]bool)

	if p.metaServer == nil {
		klog.Errorf("[CPUDynamicPolicy.clearResidualState] nil metaServer")
		return
	}

	ctx := context.Background()
	podList, err := p.metaServer.GetPodList(ctx, nil)
	if err != nil {
		klog.Errorf("[CPUDynamicPolicy.clearResidualState] get pod list failed: %v", err)
		return
	}

	podSet := sets.NewString()
	for _, pod := range podList {
		podSet.Insert(fmt.Sprintf("%v", pod.UID))
	}

	p.Lock()
	defer p.Unlock()

	podEntries := p.state.GetPodEntries()
	for podUID, containerEntries := range podEntries {
		if containerEntries.IsPoolEntry() {
			continue
		}

		if !podSet.Has(podUID) {
			residualSet[podUID] = true
			p.residualHitMap[podUID] += 1
			klog.Infof("[CPUDynamicPolicy.clearResidualState] found pod: %s with state but doesn't "+
				"show up in pod watcher, hit count: %d", podUID, p.residualHitMap[podUID])
		}
	}

	podsToDelete := sets.NewString()
	for podUID, hitCount := range p.residualHitMap {
		if !residualSet[podUID] {
			klog.Infof("[CPUDynamicPolicy.clearResidualState] already found pod: %s in pod watcher "+
				"or its state is cleared, delete it from residualHitMap", podUID)
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

			var rErr error
			if p.enableCPUSysAdvisor {
				_, rErr = p.advisorClient.RemovePod(ctx, &advisorapi.RemovePodRequest{
					PodUid: podUID,
				})
			}

			if rErr != nil {
				klog.Errorf("[CPUDynamicPolicy.clearResidualState] remove residual pod: %s in sys advisor failed with error: %v, "+
					" remain it in state", podUID, rErr)
				continue
			}

			klog.Infof("[CPUDynamicPolicy.clearResidualState] clear residual pod: %s in state", podUID)
			delete(podEntries, podUID)
		}

		updatedMachineState, err := state.GenerateCPUMachineStateByPodEntries(p.machineInfo.CPUTopology, podEntries)
		if err != nil {
			klog.Errorf("[CPUDynamicPolicy.clearResidualState] GenerateCPUMachineStateByPodEntries failed with error: %v", err)
			return
		}

		p.state.SetPodEntries(podEntries)
		p.state.SetMachineState(updatedMachineState)

		err = p.adjustAllocationEntries()
		if err != nil {
			klog.ErrorS(err, "[CPUDynamicPolicy.clearResidualState] adjustAllocationEntries failed")
		}
	}
}

func (p *DynamicPolicy) removePod(podUID string) error {
	podEntries := p.state.GetPodEntries()
	if len(podEntries[podUID]) == 0 {
		return nil
	}
	delete(podEntries, podUID)

	updatedMachineState, err := state.GenerateCPUMachineStateByPodEntries(p.machineInfo.CPUTopology, podEntries)
	if err != nil {
		return fmt.Errorf("GenerateCPUMachineStateByPodEntries failed with error: %v", err)
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

	updatedMachineState, err := state.GenerateCPUMachineStateByPodEntries(p.machineInfo.CPUTopology, podEntries)
	if err != nil {
		return fmt.Errorf("GenerateCPUMachineStateByPodEntries failed with error: %v", err)
	}

	p.state.SetPodEntries(podEntries)
	p.state.SetMachineState(updatedMachineState)

	return nil
}

// initReclaimPool is used to initialize cpuset pools for Reclaimed Cores when plugin starts.
// if this info already exists in state-file, just use it, otherwise calculate right away
func (p *DynamicPolicy) initReclaimPool() error {
	reclaimedAllocationInfo := p.state.GetAllocationInfo(state.PoolNameReclaim, "")
	if reclaimedAllocationInfo == nil {
		machineState := p.state.GetMachineState()
		podEntries := p.state.GetPodEntries()

		availableCPUs := machineState.GetAvailableCPUSetExcludeDedicatedCoresPodsAndPools(p.reservedCPUs, podEntries, state.ResidentPools)

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
		allAvailableCPUs := p.machineInfo.CPUDetails.CPUs().Difference(p.reservedCPUs)
		if reclaimedCPUSet.IsEmpty() {
			reclaimedCPUSet, _, err = calculator.TakeByNUMABalance(p.machineInfo, allAvailableCPUs, reservedReclaimedCPUsSize)
			if err != nil {
				return fmt.Errorf("fallback takeByNUMABalance faild in initReclaimPool for %s with error: %v",
					state.PoolNameReclaim, err)
			}
		}

		for poolName, cset := range map[string]machine.CPUSet{state.PoolNameReclaim: reclaimedCPUSet} {
			klog.Infof("[CPUDynamicPolicy.initReclaimPool] initReclaimPool %s: %s",
				poolName, cset.String())

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
				OriginalTopologyAwareAssignments: util.DeepCopyTopologyAwareAssignments(topologyAwareAssignments),
			}

			p.state.SetAllocationInfo(poolName, "", curPoolAllocationInfo)
		}
	} else {
		klog.Infof("[CPUDynamicPolicy.initReclaimPool] exist initial %s: %s",
			state.PoolNameReclaim, reclaimedAllocationInfo.AllocationResult.String())
	}

	return nil
}

// takeCPUsForPools tries to allocate cpuset for each given pool,
// and it will consider the total available cpuset during calculation.
// the returned value includes cpuset pool map and remaining available cpuset.
func (p *DynamicPolicy) takeCPUsForPools(poolsQuantityMap map[string]int,
	availableCPUs machine.CPUSet) (map[string]machine.CPUSet, machine.CPUSet, error) {
	poolsCPUSet := make(map[string]machine.CPUSet)
	clonedAvailableCPUs := availableCPUs.Clone()

	// to avoid random map iteration sequence to generate pools randomly
	sortedPoolNames := machine.GetSortedQuantityMapKeys(poolsQuantityMap)
	for _, poolName := range sortedPoolNames {
		req := poolsQuantityMap[poolName]
		klog.Infof("[CPUDynamicPolicy.takeCPUsForPools] allocated for pool: %s with req: %d", poolName, req)

		var err error
		var cset machine.CPUSet
		cset, availableCPUs, err = calculator.TakeByNUMABalance(p.machineInfo, availableCPUs, req)
		if err != nil {
			return nil, clonedAvailableCPUs, fmt.Errorf("take cpu for pool: %s of req: %d failed with error: %v",
				poolName, req, err)
		}

		poolsCPUSet[poolName] = cset
	}

	return poolsCPUSet, availableCPUs, nil
}

// takeCPUsForContainers tries to allocate cpuset for the given pod/container combinations,
// and it will consider the total available cpuset during calculation.
// the returned value includes cpuset map for pod/container combinations and remaining available cpuset.
func (p *DynamicPolicy) takeCPUsForContainers(containersQuantityMap map[string]map[string]int,
	availableCPUs machine.CPUSet) (map[string]map[string]machine.CPUSet, machine.CPUSet, error) {
	containersCPUSet := make(map[string]map[string]machine.CPUSet)
	clonedAvailableCPUs := availableCPUs.Clone()

	for podUID, containerQuantities := range containersQuantityMap {
		if len(containerQuantities) > 0 {
			containersCPUSet[podUID] = make(map[string]machine.CPUSet)
		}

		for containerName, quantity := range containerQuantities {
			klog.Infof("[CPUDynamicPolicy.takeCPUsForContainers] allocated for pod: %s container: %s with req: %d", podUID, containerName, quantity)

			var err error
			var cset machine.CPUSet

			cset, availableCPUs, err = calculator.TakeByNUMABalance(p.machineInfo, availableCPUs, quantity)
			if err != nil {
				return nil, clonedAvailableCPUs, fmt.Errorf("take cpu for pod: %s container: %s of req: %d failed with error: %v",
					podUID, containerName, quantity, err)
			}

			containersCPUSet[podUID][containerName] = cset
		}
	}

	return containersCPUSet, availableCPUs, nil
}

// generatePoolsAndIsolation is used to generate cpuset pools and isolated cpuset.
// it will always try to allocate isolated cpuset for individual pod/containers (and
// divide the total cores evenly if not possible to allocate), and then use the left
// cores to allocate among different pools
func (p *DynamicPolicy) generatePoolsAndIsolation(poolsQuantityMap map[string]int,
	isolatedQuantityMap map[string]map[string]int, availableCPUs machine.CPUSet) (poolsCPUSet map[string]machine.CPUSet,
	isolatedCPUSet map[string]map[string]machine.CPUSet, err error) {

	// clear entry with zero quantity
	for poolName, quantity := range poolsQuantityMap {
		if quantity == 0 {
			klog.Warningf("[CPUDynamicPolicy.generatePoolsAndIsolation] pool: %s with 0 quantity, skip generateit", poolName)
			delete(poolsQuantityMap, poolName)
		}
	}

	for podUID, containerEntries := range isolatedQuantityMap {
		for containerName, quantity := range containerEntries {
			if quantity == 0 {
				klog.Warningf("[CPUDynamicPolicy.generatePoolsAndIsolation] isolated pod: %s, container: %swith 0 quantity, skip generate it", podUID, containerName)
				delete(containerEntries, containerName)
			}
		}
		if len(containerEntries) == 0 {
			klog.Warningf("[CPUDynamicPolicy.generatePoolsAndIsolation] isolated pod: %s all container entries skipped", podUID)
			delete(isolatedQuantityMap, podUID)
		}
	}

	poolsCPUSet = make(map[string]machine.CPUSet)
	isolatedCPUSet = make(map[string]map[string]machine.CPUSet)

	isolatedTotalQuantity := getContainersTotalQuantity(isolatedQuantityMap)
	poolsTotalQuantity := getPoolsTotalQuantity(poolsQuantityMap)
	availableSize := availableCPUs.Size()

	klog.Infof("[CPUDynamicPolicy.generatePoolsAndIsolation] isolatedTotalQuantity: %d, poolsTotalQuantity: %d, availableSize: %d",
		isolatedTotalQuantity, poolsTotalQuantity, availableSize)

	var tErr error
	if poolsTotalQuantity+isolatedTotalQuantity <= availableSize {
		klog.Infof("[CPUDynamicPolicy.generatePoolsAndIsolation] all pools and isolated containers could be allocated")

		isolatedCPUSet, availableCPUs, tErr = p.takeCPUsForContainers(isolatedQuantityMap, availableCPUs)
		if tErr != nil {
			err = fmt.Errorf("allocate isolated cpus for dedicated_cores failed with error: %v", tErr)
			return
		}

		poolsCPUSet, availableCPUs, tErr = p.takeCPUsForPools(poolsQuantityMap, availableCPUs)
		if tErr != nil {
			err = fmt.Errorf("allocate cpus for pools failed with error: %v", tErr)
			return
		}
	} else if poolsTotalQuantity <= availableSize {
		klog.Infof("[CPUDynamicPolicy.generatePoolsAndIsolation] all pools could be allocated, all isolated containers would be put to pools")

		poolsCPUSet, availableCPUs, tErr = p.takeCPUsForPools(poolsQuantityMap, availableCPUs)
		if tErr != nil {
			err = fmt.Errorf("allocate cpus for pools failed with error: %v", tErr)
			return
		}
	} else if poolsTotalQuantity > 0 {
		klog.Infof("[CPUDynamicPolicy.generatePoolsAndIsolation] can't allocate for all pools")

		proportionalPoolsQuantityMap := make(map[string]int)

		totalProportionalPoolsQuantity := 0
		for poolName, poolQuantity := range poolsQuantityMap {
			proportionalSize := general.Max(getProportionalSize(poolQuantity, poolsTotalQuantity, availableSize), 1)
			proportionalPoolsQuantityMap[poolName] = proportionalSize
			totalProportionalPoolsQuantity += proportionalSize
		}

		// corner case: after divide, the total count goes to be bigger than available total
		for totalProportionalPoolsQuantity > availableSize {
			curTotalProportionalPoolsQuantity := totalProportionalPoolsQuantity

			for poolName, quantity := range proportionalPoolsQuantityMap {
				if quantity > 1 && totalProportionalPoolsQuantity > 0 {
					quantity--
					totalProportionalPoolsQuantity--
					proportionalPoolsQuantityMap[poolName] = quantity
				}
			}

			// availableSize can't satisfy every pool has at least one cpu
			if curTotalProportionalPoolsQuantity == totalProportionalPoolsQuantity {
				break
			}
		}

		klog.Infof("[CPUDynamicPolicy.generatePoolsAndIsolation] poolsQuantityMap: %v, proportionalPoolsQuantityMap: %v",
			poolsQuantityMap, proportionalPoolsQuantityMap)

		// availableSize can't satisfy every pool has at least one cpu,
		// we make all pools equals to availableCPUs in this case.
		if totalProportionalPoolsQuantity > availableSize {
			for poolName := range poolsQuantityMap {
				poolsCPUSet[poolName] = availableCPUs.Clone()
			}
		} else {
			poolsCPUSet, availableCPUs, tErr = p.takeCPUsForPools(proportionalPoolsQuantityMap, availableCPUs)
			if tErr != nil {
				err = fmt.Errorf("allocate cpus for pools failed with error: %v", tErr)
				return
			}
		}
	}

	if poolsCPUSet[state.PoolNameReserve].IsEmpty() {
		poolsCPUSet[state.PoolNameReserve] = p.reservedCPUs.Clone()
		klog.Infof("[CPUDynamicPolicy.generatePoolsAndIsolation] set pool %s:%s", state.PoolNameReserve, poolsCPUSet[state.PoolNameReserve].String())
	} else {
		err = fmt.Errorf("static pool %s result: %s is generated dynamically", state.PoolNameReserve, poolsCPUSet[state.PoolNameReserve].String())
		return
	}

	poolsCPUSet[state.PoolNameReclaim] = poolsCPUSet[state.PoolNameReclaim].Union(availableCPUs)
	if poolsCPUSet[state.PoolNameReclaim].IsEmpty() {
		// for state.PoolNameReclaim, we must make them exist when the node isn't in hybrid mode even if cause overlap
		allAvailableCPUs := p.machineInfo.CPUDetails.CPUs().Difference(p.reservedCPUs)
		reclaimedCPUSet, _, tErr := calculator.TakeByNUMABalance(p.machineInfo, allAvailableCPUs, reservedReclaimedCPUsSize)
		if tErr != nil {
			err = fmt.Errorf("fallback takeByNUMABalance faild in generatePoolsAndIsolation for reclaimedCPUSet with error: %v", tErr)
			return
		}

		klog.Infof("[CPUDynamicPolicy.generatePoolsAndIsolation] fallback takeByNUMABalance in generatePoolsAndIsolation for reclaimedCPUSet: %s", reclaimedCPUSet.String())
		poolsCPUSet[state.PoolNameReclaim] = reclaimedCPUSet
	}

	enableReclaim := p.reclaimedResourceConfig.EnableReclaim()
	if !enableReclaim && poolsCPUSet[state.PoolNameReclaim].Size() > reservedReclaimedCPUsSize {
		poolsCPUSet[state.PoolNameReclaim] = p.ReclaimDisabled(poolsCPUSet, poolsCPUSet[state.PoolNameReclaim].Clone())
		klog.Infof("[CPUDynamicPolicy.generatePoolsAndIsolation] ReclaimDisabled finished, current %s pool: %s",
			state.PoolNameReclaim, poolsCPUSet[state.PoolNameReclaim].String())
	}

	return
}

// ReclaimDisabled tries to allocate reclaimed cores to none-reclaimed pools,
// if we disable reclaim on current node. this could be used a down-grade strategy to
// disable reclaimed workloads in emergency
func (p *DynamicPolicy) ReclaimDisabled(poolsCPUSet map[string]machine.CPUSet, reclaimedCPUs machine.CPUSet) machine.CPUSet {
	totalSize := 0
	for poolName, poolCPUs := range poolsCPUSet {
		if state.ResidentPools.Has(poolName) {
			continue
		}

		totalSize += poolCPUs.Size()
	}

	availableSize := reclaimedCPUs.Size() - reservedReclaimedCPUsSize
	if availableSize <= 0 || totalSize == 0 {
		return reclaimedCPUs
	}

	for poolName, poolCPUs := range poolsCPUSet {
		if state.ResidentPools.Has(poolName) {
			continue
		}

		proportionalSize := general.Max(getProportionalSize(poolCPUs.Size(), totalSize, availableSize), 1)

		var err error
		var cset machine.CPUSet

		cset, reclaimedCPUs, err = calculator.TakeByNUMABalance(p.machineInfo, reclaimedCPUs, proportionalSize)
		if err != nil {
			klog.Errorf("[ReclaimDisabled] take %d cpus from reclaimedCPUs: %s, size: %d failed with error: %v",
				proportionalSize, reclaimedCPUs.String(), reclaimedCPUs.Size(), err)
			return reclaimedCPUs
		}

		poolsCPUSet[poolName] = poolCPUs.Union(cset)
		klog.Infof("[ReclaimDisabled] take %s to %s; prev: %s, current: %s", cset.String(), poolName, poolCPUs.String(), poolsCPUSet[poolName].String())

		if reclaimedCPUs.Size() <= reservedReclaimedCPUsSize {
			break
		}
	}

	return reclaimedCPUs
}

// putContainersAndAdjustAllocationEntries puts containers to belonging pools,
// and adjusts size of pools accordingly,
// finally calculates and generates the latest checkpoint.
func (p *DynamicPolicy) putContainersAndAdjustAllocationEntries(allocationInfos []*state.AllocationInfo) error {
	if len(allocationInfos) == 0 {
		return nil
	}

	entries := p.state.GetPodEntries()
	machineState := p.state.GetMachineState()

	// since put container will cause re-generate pools,
	// if sys advisor is enabled, we believe the pools' ratio that sys advisor indicates,
	// else we do sum(containers req) for each pool to get pools ratio
	var poolsQuantityMap map[string]int
	if p.enableCPUSysAdvisor {
		poolsQuantityMap = machine.GetQuantityMap(entries.GetPoolsCPUset(state.ResidentPools))
	} else {
		poolsQuantityMap = state.GetPoolsQuantityMapFromPodEntries(entries, allocationInfos)
	}

	for _, allocationInfo := range allocationInfos {
		if allocationInfo == nil {
			return fmt.Errorf("found nil allocationInfo in input parameter")
		} else if allocationInfo.QoSLevel != consts.PodAnnotationQoSLevelSharedCores {
			return fmt.Errorf("put container with invalid qos level: %s into pool", allocationInfo.QoSLevel)
		}

		poolName := state.GetSpecifiedPoolName(allocationInfo)
		if poolName == "" {
			return fmt.Errorf("allocationInfo points to empty poolName")
		}

		reqInt := state.GetContainerRequestedCores(allocationInfo)
		poolsQuantityMap[poolName] += reqInt
	}

	isolatedQuantityMap := state.GetIsolatedQuantityMapFromPodEntries(entries, allocationInfos)
	err := p.adjustPoolsAndIsolatedEntries(poolsQuantityMap, isolatedQuantityMap, entries, machineState)
	if err != nil {
		return fmt.Errorf("adjustPoolsAndIsolatedEntries failed with error: %v", err)
	}

	return nil
}

// adjustAllocationEntries calculates and generates the latest checkpoint,
// and it will be called when pool entries should be adjusted according to current entries and machine state.
func (p *DynamicPolicy) adjustAllocationEntries() error {
	entries := p.state.GetPodEntries()
	machineState := p.state.GetMachineState()

	// since adjustAllocationEntries will cause re-generate pools,
	// if sys advisor is enabled, we believe the pools' ratio that sys advisor indicates,
	// else we do sum(containers req) for each pool to get pools ratio
	var poolsQuantityMap map[string]int
	if p.enableCPUSysAdvisor {
		poolsQuantityMap = machine.GetQuantityMap(entries.GetPoolsCPUset(state.ResidentPools))
	} else {
		poolsQuantityMap = state.GetPoolsQuantityMapFromPodEntries(entries, nil)
	}
	isolatedQuantityMap := state.GetIsolatedQuantityMapFromPodEntries(entries, nil)

	err := p.adjustPoolsAndIsolatedEntries(poolsQuantityMap, isolatedQuantityMap, entries, machineState)
	if err != nil {
		return fmt.Errorf("adjustPoolsAndIsolatedEntries failed with error: %v", err)
	}

	return nil
}

// reclaimOverlapNUMABinding uses reclaim pool cpuset result in empty NUMA
// union intersection of current reclaim pool and non-ramp-up dedicated_cores numa_binding containers
func (p *DynamicPolicy) reclaimOverlapNUMABinding(poolsCPUSet map[string]machine.CPUSet, entries state.PodEntries) error {
	// reclaimOverlapNUMABinding only works with cpu advisor and reclaim enabled
	if !(p.enableCPUSysAdvisor && p.reclaimedResourceConfig.EnableReclaim()) {
		return nil
	}

	if entries[state.PoolNameReclaim][""] == nil || entries[state.PoolNameReclaim][""].AllocationResult.IsEmpty() {
		return fmt.Errorf("reclaim pool misses in current entries")
	}

	curReclaimCPUSet := entries[state.PoolNameReclaim][""].AllocationResult.Clone()

	klog.Infof("[CPUDynamicPolicy.reclaimOverlapNUMABinding] curReclaimCPUSet: %s", curReclaimCPUSet.String())

	nonOverlapReclaimCPUSet := poolsCPUSet[state.PoolNameReclaim].Clone()

	for _, containerEntries := range entries {
		if containerEntries.IsPoolEntry() {
			continue
		}

		for _, allocationInfo := range containerEntries {

			if !(allocationInfo != nil &&
				allocationInfo.QoSLevel == consts.PodAnnotationQoSLevelDedicatedCores &&
				allocationInfo.Annotations[consts.PodAnnotationMemoryEnhancementNumaBinding] == consts.PodAnnotationMemoryEnhancementNumaBindingEnable &&
				allocationInfo.ContainerType == pluginapi.ContainerType_MAIN.String()) {
				continue
			} else if allocationInfo.RampUp {
				klog.Infof("[CPUDynamicPolicy.reclaimOverlapNUMABinding] dedicated numa_binding pod: %s/%s container: %s is in ramp up, not to overlap reclaim pool with it",
					allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName)
				continue
			}

			poolsCPUSet[state.PoolNameReclaim] = poolsCPUSet[state.PoolNameReclaim].Union(curReclaimCPUSet.Intersection(allocationInfo.AllocationResult))
		}
	}

	if poolsCPUSet[state.PoolNameReclaim].IsEmpty() {
		return fmt.Errorf("reclaim pool is empty after overlapping with dedicated_cores numa_binding containers")
	}

	klog.Infof("[CPUDynamicPolicy.reclaimOverlapNUMABinding] nonOverlapReclaimCPUSet: %s, finalReclaimCPUSet: %s",
		nonOverlapReclaimCPUSet.String(), poolsCPUSet[state.PoolNameReclaim].String())

	return nil
}

// adjustPoolsAndIsolatedEntries calculates pools and isolated cpusets according to expectant quantities,
// and then apply them to local state. this function is used as the fundamental
// helper function.
func (p *DynamicPolicy) adjustPoolsAndIsolatedEntries(poolsQuantityMap map[string]int,
	isolatedQuantityMap map[string]map[string]int, entries state.PodEntries, machineState state.NUMANodeMap) error {
	availableCPUs := machineState.GetAvailableCPUSetExcludeNUMABindingPods(p.reservedCPUs)
	poolsCPUSet, isolatedCPUSet, err := p.generatePoolsAndIsolation(poolsQuantityMap, isolatedQuantityMap, availableCPUs)

	if err != nil {
		return fmt.Errorf("generatePoolsAndIsolation failed with error: %v", err)
	}

	err = p.reclaimOverlapNUMABinding(poolsCPUSet, entries)

	if err != nil {
		return fmt.Errorf("reclaimOverlapNUMABinding failed with error: %v", err)
	}

	err = p.applyPoolsAndIsolatedInfo(poolsCPUSet, isolatedCPUSet, entries, machineState)

	if err != nil {
		return fmt.Errorf("applyPoolsAndIsolatedInfo failed with error: %v", err)
	}

	err = p.cleanPools()

	if err != nil {
		return fmt.Errorf("cleanPools failed with error: %v", err)
	}

	return nil
}

// applyPoolsAndIsolatedInfo generates the latest checkpoint by pools and isolated cpusets calculation results.
func (p *DynamicPolicy) applyPoolsAndIsolatedInfo(poolsCPUSet map[string]machine.CPUSet,
	isolatedCPUSet map[string]map[string]machine.CPUSet, curEntries state.PodEntries, machineState state.NUMANodeMap) error {
	newPodEntries := make(state.PodEntries)
	unionDedicatedIsolatedCPUSet := machine.NewCPUSet()

	// walk through all isolated CPUSet map to store those pods/containers in pod entries
	for podUID, containerEntries := range isolatedCPUSet {
		for containerName, isolatedCPUs := range containerEntries {
			allocationInfo := curEntries[podUID][containerName]
			if allocationInfo == nil {
				klog.Errorf("[CPUDynamicPolicy.applyPoolsAndIsolatedInfo] isolated pod: %s, container: %s without entry in current checkpoint", podUID, containerName)
				continue
			} else if allocationInfo.QoSLevel != consts.PodAnnotationQoSLevelDedicatedCores ||
				allocationInfo.Annotations[consts.PodAnnotationMemoryEnhancementNumaBinding] ==
					consts.PodAnnotationMemoryEnhancementNumaBindingEnable {
				klog.Errorf("[CPUDynamicPolicy.applyPoolsAndIsolatedInfo] isolated pod: %s, container: %s isn't dedicated_cores without NUMA binding", podUID, containerName)
				continue
			}

			topologyAwareAssignments, err := machine.GetNumaAwareAssignments(p.machineInfo.CPUTopology, isolatedCPUs)
			if err != nil {
				klog.ErrorS(err, "[CPUDynamicPolicy.applyPoolsAndIsolatedInfo] Unable to calculate topologyAwareAssignments",
					"podNamespace", allocationInfo.PodNamespace,
					"podName", allocationInfo.PodName,
					"containerName", allocationInfo.ContainerName,
					"result cpuset", isolatedCPUs.String())
				continue
			}

			klog.InfoS("[CPUDynamicPolicy.applyPoolsAndIsolatedInfo] isolate info",
				"podNamespace", allocationInfo.PodNamespace,
				"podName", allocationInfo.PodName,
				"containerName", allocationInfo.ContainerName,
				"result cpuset", isolatedCPUs.String(),
				"result cpuset size", isolatedCPUs.Size(),
				"qosLevel", allocationInfo.QoSLevel)

			if newPodEntries[podUID] == nil {
				newPodEntries[podUID] = make(state.ContainerEntries)
			}

			newPodEntries[podUID][containerName] = allocationInfo.Clone()
			newPodEntries[podUID][containerName].OwnerPoolName = state.PoolNameDedicated
			newPodEntries[podUID][containerName].AllocationResult = isolatedCPUs.Clone()
			newPodEntries[podUID][containerName].OriginalAllocationResult = isolatedCPUs.Clone()
			newPodEntries[podUID][containerName].TopologyAwareAssignments = topologyAwareAssignments
			newPodEntries[podUID][containerName].OriginalTopologyAwareAssignments = util.DeepCopyTopologyAwareAssignments(topologyAwareAssignments)

			unionDedicatedIsolatedCPUSet = unionDedicatedIsolatedCPUSet.Union(isolatedCPUs)
		}
	}
	_ = p.emitter.StoreInt64(util.MetricNameIsolatedPodNum, int64(len(newPodEntries)), metrics.MetricTypeNameRaw)

	if poolsCPUSet[state.PoolNameReclaim].IsEmpty() {
		return fmt.Errorf("entry: %s is empty", state.PoolNameShare)
	}

	// walk through all pools CPUSet map to store those pools in pod entries
	for poolName, cset := range poolsCPUSet {
		klog.Infof("[CPUDynamicPolicy.applyPoolsAndIsolatedInfo] try to apply pool %s: %s", poolName, cset.String())

		topologyAwareAssignments, err := machine.GetNumaAwareAssignments(p.machineInfo.CPUTopology, cset)
		if err != nil {
			return fmt.Errorf("unable to calculate topologyAwareAssignments for pool: %s, result cpuset: %s, error: %v",
				poolName, cset.String(), err)
		}

		if newPodEntries[poolName] == nil {
			newPodEntries[poolName] = make(state.ContainerEntries)
		}

		allocationInfo := curEntries[poolName][""]
		if allocationInfo != nil {
			klog.Infof("[CPUDynamicPolicy.applyPoolsAndIsolatedInfo] pool: %s allocation result transform from %s to %s",
				poolName, allocationInfo.AllocationResult.String(), cset.String())
		}

		newPodEntries[poolName][""] = &state.AllocationInfo{
			PodUid:                           poolName,
			OwnerPoolName:                    poolName,
			AllocationResult:                 cset.Clone(),
			OriginalAllocationResult:         cset.Clone(),
			TopologyAwareAssignments:         topologyAwareAssignments,
			OriginalTopologyAwareAssignments: util.DeepCopyTopologyAwareAssignments(topologyAwareAssignments),
		}

		_ = p.emitter.StoreInt64(util.MetricNamePoolSize, int64(cset.Size()), metrics.MetricTypeNameRaw,
			metrics.MetricTag{Key: "poolName", Val: poolName})
	}

	// rampUpCPUs includes common reclaimed pool
	rampUpCPUs := machineState.GetAvailableCPUSetExcludeNUMABindingPods(p.reservedCPUs).Difference(unionDedicatedIsolatedCPUSet)
	rampUpCPUsTopologyAwareAssignments, err := machine.GetNumaAwareAssignments(p.machineInfo.CPUTopology, rampUpCPUs)
	if err != nil {
		return fmt.Errorf("unable to calculate topologyAwareAssignments for rampUpCPUs, result cpuset: %s, error: %v",
			rampUpCPUs.String(), err)
	}

	// walk through current pod entries to handle container-related entries (besides pooled entries)
	for podUID, containerEntries := range curEntries {
		if containerEntries.IsPoolEntry() {
			continue
		}

	containerLoop:
		for containerName, allocationInfo := range containerEntries {
			if allocationInfo == nil {
				klog.Errorf("[CPUDynamicPolicy.applyPoolsAndIsolatedInfo] pod: %s, container: %s has nil allocationInfo",
					podUID, containerName)
				continue
			}

			reqInt := state.GetContainerRequestedCores(allocationInfo)
			if newPodEntries[podUID][containerName] != nil {
				// adapt to old checkpoint without RequestQuantity property
				newPodEntries[podUID][containerName].RequestQuantity = reqInt
				klog.Infof("[CPUDynamicPolicy.applyPoolsAndIsolatedInfo] pod: %s/%s, container: %s, qosLevel: %s is isolated, ignore original allocationInfo",
					allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName, allocationInfo.QoSLevel)
				continue
			}

			if newPodEntries[podUID] == nil {
				newPodEntries[podUID] = make(state.ContainerEntries)
			}

			newPodEntries[podUID][containerName] = allocationInfo.Clone()
			switch allocationInfo.QoSLevel {
			case consts.PodAnnotationQoSLevelDedicatedCores:
				// todo: currently for numa_binding containers, we just clone checkpoint already exist
				//  if qos aware will adjust cpuset for them, we will make adaption here
				if allocationInfo.Annotations[consts.PodAnnotationMemoryEnhancementNumaBinding] == consts.PodAnnotationMemoryEnhancementNumaBindingEnable {
					continue containerLoop
				}

				// dedicated_cores with numa_binding is not isolated, we will try to isolate it in next adjustment.
				klog.Warningf("[[CPUDynamicPolicy.applyPoolsAndIsolatedInfo] pod: %s/%s, container: %s is a dedicated_cores with numa_binding but not isolated, we put it into fallback pool: %s temporary",
					allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName, rampUpCPUs.String())

				newPodEntries[podUID][containerName].OwnerPoolName = state.PoolNameFallback
				newPodEntries[podUID][containerName].AllocationResult = rampUpCPUs.Clone()
				newPodEntries[podUID][containerName].OriginalAllocationResult = rampUpCPUs.Clone()
				newPodEntries[podUID][containerName].TopologyAwareAssignments = util.DeepCopyTopologyAwareAssignments(rampUpCPUsTopologyAwareAssignments)
				newPodEntries[podUID][containerName].OriginalTopologyAwareAssignments = util.DeepCopyTopologyAwareAssignments(rampUpCPUsTopologyAwareAssignments)

			case consts.PodAnnotationQoSLevelSharedCores, consts.PodAnnotationQoSLevelReclaimedCores:
				// may be indicated by qos aware server
				ownerPoolName := state.GetRealOwnerPoolName(allocationInfo)

				if ownerPoolName == "" {
					ownerPoolName = state.GetSpecifiedPoolName(allocationInfo)
				}

				if allocationInfo.RampUp {
					klog.Infof("[CPUDynamicPolicy.applyPoolsAndIsolatedInfo] pod: %s/%s container: %s is in ramp up, set its allocation result from %s to rampUpCPUs :%s",
						allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName, allocationInfo.AllocationResult.String(), rampUpCPUs.String())

					newPodEntries[podUID][containerName].OwnerPoolName = ""
					newPodEntries[podUID][containerName].AllocationResult = rampUpCPUs.Clone()
					newPodEntries[podUID][containerName].OriginalAllocationResult = rampUpCPUs.Clone()
					newPodEntries[podUID][containerName].TopologyAwareAssignments = util.DeepCopyTopologyAwareAssignments(rampUpCPUsTopologyAwareAssignments)
					newPodEntries[podUID][containerName].OriginalTopologyAwareAssignments = util.DeepCopyTopologyAwareAssignments(rampUpCPUsTopologyAwareAssignments)
				} else if newPodEntries[ownerPoolName][""] == nil {
					klog.Warningf("[CPUDynamicPolicy.applyPoolsAndIsolatedInfo] pod: %s/%s container: %s get owner pool: %s allocationInfo failed. reuse its allocation result: %s",
						allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName, ownerPoolName, allocationInfo.AllocationResult.String())
				} else {
					poolEntry := newPodEntries[ownerPoolName][""]

					klog.Infof("[CPUDynamicPolicy.applyPoolsAndIsolatedInfo] put pod: %s/%s container: %s to pool: %s, set its allocation result from %s to %s",
						allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName,
						ownerPoolName, allocationInfo.AllocationResult.String(), poolEntry.AllocationResult.String())

					newPodEntries[podUID][containerName].OwnerPoolName = ownerPoolName
					newPodEntries[podUID][containerName].AllocationResult = poolEntry.AllocationResult.Clone()
					newPodEntries[podUID][containerName].OriginalAllocationResult = poolEntry.OriginalAllocationResult.Clone()
					newPodEntries[podUID][containerName].TopologyAwareAssignments = util.DeepCopyTopologyAwareAssignments(poolEntry.TopologyAwareAssignments)
					newPodEntries[podUID][containerName].OriginalTopologyAwareAssignments = util.DeepCopyTopologyAwareAssignments(poolEntry.TopologyAwareAssignments)
				}
			default:
				return fmt.Errorf("invalid qosLevel: %s for pod: %s/%s container: %s",
					allocationInfo.QoSLevel, allocationInfo.PodNamespace,
					allocationInfo.PodName, allocationInfo.ContainerName)
			}
		}
	}

	// use pod entries generated above to generate machine state info, and store in local state
	machineState, err = state.GenerateCPUMachineStateByPodEntries(p.machineInfo.CPUTopology, newPodEntries)
	if err != nil {
		return fmt.Errorf("calculate machineState by newPodEntries failed with error: %v", err)
	}

	p.state.SetPodEntries(newPodEntries)
	p.state.SetMachineState(machineState)

	return nil
}

func (p *DynamicPolicy) syncExistingContainersToCPUAdvisor() error {
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

func (p *DynamicPolicy) initialize() (err error) {
	cpuAdvisorConn, err := process.Dial(p.cpuAdvisorSocketAbsPath, 5*time.Second)
	if err != nil {
		err = fmt.Errorf("get cpu advisor connection with socket: %s failed with error: %v", p.cpuAdvisorSocketAbsPath, err)
		return
	}

	p.advisorClient = advisorapi.NewCPUAdvisorClient(cpuAdvisorConn)
	p.advisorConn = cpuAdvisorConn
	return nil
}

func (p *DynamicPolicy) lwCPUAdvisorServer(stopCh <-chan struct{}) error {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-stopCh
		klog.Info("[CPUDynamicPolicy.lwCPUAdvisorServer] received stop signal, stop calling ListAndWatch of CPUAdvisorServer")
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

		err = p.allocateByCPUAdvisorServerListAndWatchResp(resp)
		if err != nil {
			klog.Errorf("allocate by ListAndWatch response of CPUAdvisorServer failed with error: %v", err)
		}
	}
}

func validateBlocksByTopology(numaToBlocks map[int]map[string]*advisorapi.Block, topology *machine.CPUTopology) error {
	if topology == nil {
		return fmt.Errorf("validateBlocksByTopology got nil topology")
	}

	totalQuantity := 0

	numas := topology.CPUDetails.NUMANodes()

	for numaId, blocksMap := range numaToBlocks {
		if numaId != -1 && !numas.Contains(numaId) {
			return fmt.Errorf("NUMA: %d referred by blocks isn't in topology", numaId)
		}

		numaQuantity := 0

		for blockId, block := range blocksMap {
			if block == nil {
				klog.Warningf("[validateBlocksByTopology] got nil block: %d", blockId)
				continue
			}

			quantityInt, err := general.CovertUInt64ToInt(block.Result)

			if err != nil {
				return fmt.Errorf("CovertUInt64ToInt failed with error: %v, blockId: %s, numaId: %d",
					err, blockId, numaId)
			}

			numaQuantity += quantityInt
		}

		if numaId != -1 {
			numaCapacity := topology.CPUDetails.CPUsInNUMANodes(numaId).Size()

			if numaQuantity > numaCapacity {
				return fmt.Errorf("numaQuantity: %d exceeds NUMA capacity: %d in NUMA: %d",
					numaQuantity, numaCapacity, numaId)
			}
		}

		totalQuantity += numaQuantity
	}

	if totalQuantity > topology.NumCPUs {
		return fmt.Errorf("numaQuantity: %d exceeds total capacity: %d",
			totalQuantity, topology.NumCPUs)
	}

	return nil
}

func allocateByBlocks(resp *advisorapi.ListAndWatchResponse,
	entries state.PodEntries,
	numaToBlocks map[int]map[string]*advisorapi.Block,
	topology *machine.CPUTopology,
	machineInfo *machine.KatalystMachineInfo) (map[string]machine.CPUSet, error) {

	if machineInfo == nil {
		return nil, fmt.Errorf("got nil machineInfo")
	} else if topology == nil {
		return nil, fmt.Errorf("got nil topology")
	} else if resp == nil {
		return nil, fmt.Errorf("got nil resp")
	}

	availableCPUs := topology.CPUDetails.CPUs()
	blockToCPUSet := make(map[string]machine.CPUSet)
	for _, poolName := range state.StaticPools.List() {
		allocationInfo := entries[poolName][""]

		if allocationInfo == nil {
			continue
		}

		// validation already guarantees that
		// calculationInfo of static pool only has one block isn't topology aware
		blockId := resp.Entries[poolName].Entries[""].CalculationResultsByNumas[-1].Blocks[0].BlockId
		blockToCPUSet[blockId] = allocationInfo.AllocationResult.Clone()
		availableCPUs = availableCPUs.Difference(blockToCPUSet[blockId])
	}

	for numaId, blocksMap := range numaToBlocks {
		if numaId == -1 {
			continue
		}

		numaAvailableCPUs := availableCPUs.Intersection(topology.CPUDetails.CPUsInNUMANodes(numaId))

		for blockId, block := range blocksMap {
			if block == nil {
				klog.Warningf("[allocateByBlocks] got nil block")
				continue
			} else if _, found := blockToCPUSet[blockId]; found {
				klog.Warningf("[allocateByBlocks] block: %d already allocated", blockId)
				continue
			}

			blockResult, err := general.CovertUInt64ToInt(block.Result)

			if err != nil {
				return nil, fmt.Errorf("parse block: %s result failed with error: %v",
					blockId, err)
			}

			cset, err := calculator.TakeByTopology(machineInfo, numaAvailableCPUs, blockResult)

			if err != nil {
				return nil, fmt.Errorf("allocate cpuset for "+
					"NUMA Aware block: %s in NUMA: %d failed with error: %v, numaAvailableCPUs: %d(%s), blockResult: %d",
					blockId, numaId, err,
					numaAvailableCPUs.Size(), numaAvailableCPUs.String(), blockResult)
			}

			blockToCPUSet[blockId] = cset
			numaAvailableCPUs = numaAvailableCPUs.Difference(cset)
			availableCPUs = availableCPUs.Difference(cset)
		}
	}

	for blockId, block := range numaToBlocks[-1] {
		if block == nil {
			klog.Warningf("[allocateByBlocks] got nil block")
			continue
		} else if _, found := blockToCPUSet[blockId]; found {
			klog.Warningf("[allocateByBlocks] block: %s already allocated", blockId)
			continue
		}

		blockResult, err := general.CovertUInt64ToInt(block.Result)

		if err != nil {
			return nil, fmt.Errorf("parse block: %s result failed with error: %v",
				blockId, err)
		}

		cset, err := calculator.TakeByTopology(machineInfo, availableCPUs, blockResult)

		if err != nil {
			return nil, fmt.Errorf("allocate cpuset for "+
				"non NUMA Aware block: %s failed with error: %v, availableCPUs: %d(%s), blockResult: %d",
				blockId, err,
				availableCPUs.Size(), availableCPUs.String(), blockResult)
		}

		blockToCPUSet[blockId] = cset
		availableCPUs = availableCPUs.Difference(cset)
	}

	return blockToCPUSet, nil
}

func (p *DynamicPolicy) allocateByCPUAdvisorServerListAndWatchResp(resp *advisorapi.ListAndWatchResponse) (err error) {
	if resp == nil {
		return fmt.Errorf("allocateByCPUAdvisorServerListAndWatchResp got nil qos aware lw response")
	}

	klog.Infof("[CPUDynamicPolicy] allocateByCPUAdvisorServerListAndWatchResp is called")
	_ = p.emitter.StoreInt64(util.MetricNameAllocateByCPUAdvisorServerCalled, 1, metrics.MetricTypeNameRaw)
	p.Lock()
	defer func() {
		p.Unlock()

		if err != nil {
			_ = p.emitter.StoreInt64(util.MetricNameAllocateByCPUAdvisorServerFailed, 1, metrics.MetricTypeNameRaw)
		}
	}()

	entries := p.state.GetPodEntries()

	vErr := state.ValidateCPUAdvisorResp(entries, resp)

	if vErr != nil {
		return fmt.Errorf("ValidateCPUAdvisorResp failed with error: %v", vErr)
	}

	numaToBlocks, gErr := resp.GetBlocks()

	if gErr != nil {
		return fmt.Errorf("GetBlocks failed with error: %v", gErr)
	}

	vErr = validateBlocksByTopology(numaToBlocks, p.machineInfo.CPUTopology)

	if vErr != nil {
		return fmt.Errorf("validateBlocksByTopology failed with error: %v", vErr)
	}

	blockToCPUSet, allocateErr := allocateByBlocks(resp,
		entries,
		numaToBlocks,
		p.machineInfo.CPUTopology,
		p.machineInfo)

	if allocateErr != nil {
		return fmt.Errorf("allocateByBlocks failed with error: %v", allocateErr)
	}

	applyErr := p.applyBlocks(blockToCPUSet, entries, resp)

	if applyErr != nil {
		return fmt.Errorf("applyBlocks failed with error: %v", applyErr)
	}

	return nil
}

func (p *DynamicPolicy) serveCPUPluginCheckpoint(stopCh <-chan struct{}) {
	if err := os.Remove(p.cpuPluginSocketAbsPath); err != nil && !os.IsNotExist(err) {
		klog.Errorf("[CPUDynamicPolicy.ServeCPUPluginCheckpoint] failed to remove %s: %v", p.cpuPluginSocketAbsPath, err)
		return
	}

	sock, err := net.Listen("unix", p.cpuPluginSocketAbsPath)
	if err != nil {
		klog.Errorf("[CPUDynamicPolicy.ServeCPUPluginCheckpoint] listen at socket: %s faield with err: %v", p.cpuPluginSocketAbsPath, err)
		return
	}

	grpcServer := grpc.NewServer()
	advisorapi.RegisterCPUPluginServer(grpcServer, p)

	exitCh := make(chan struct{})
	go func() {
		klog.Infof("[CPUDynamicPolicy.ServeCPUPluginCheckpoint] starting cpu plugin checkpoint grpc server at socket: %s", p.cpuPluginSocketAbsPath)
		err := grpcServer.Serve(sock)

		if err != nil {
			klog.Errorf("[CPUDynamicPolicy.ServeCPUPluginCheckpoint] cpu plugin checkpoint grpc server crashed with error: %v at socket: %s", err, p.cpuPluginSocketAbsPath)
		} else {
			klog.Infof("[CPUDynamicPolicy.ServeCPUPluginCheckpoint] cpu plugin checkpoint grpc server at socket: %s exits normally", p.cpuPluginSocketAbsPath)
		}

		exitCh <- struct{}{}
	}()

	if conn, err := process.Dial(p.cpuPluginSocketAbsPath, 5*time.Second); err != nil {
		grpcServer.Stop()
		klog.Errorf("[CPUDynamicPolicy.ServeCPUPluginCheckpoint] dial check at socket: %s faield with err: %v", p.cpuPluginSocketAbsPath, err)
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

// getContainerRequestedCores parses and returns request cores for the given container
func (p *DynamicPolicy) getContainerRequestedCores(allocationInfo *state.AllocationInfo) int {
	if allocationInfo == nil {
		klog.Errorf("[getContainerRequestedCores] got nil allocationInfo")
		return 0
	}

	if allocationInfo.RequestQuantity == 0 {

		if p.metaServer == nil {
			klog.Errorf("[getContainerRequestedCores] nil metaServer")
			return 0
		}

		container, err := p.metaServer.GetContainerSpec(allocationInfo.PodUid, allocationInfo.ContainerName)
		if err != nil || container == nil {
			klog.Errorf("[getContainerRequestedCores] get container failed with error: %v", err)
			return 0
		}

		cpuQuantity := native.GetCPUQuantity(container.Resources.Requests)
		allocationInfo.RequestQuantity = general.Max(int(cpuQuantity.Value()), 0)
		klog.Infof("[getContainerRequestedCores] get cpu request quantity: %d for pod: %s/%s container: %s from podWatcher",
			allocationInfo.RequestQuantity, allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName)
	}

	return allocationInfo.RequestQuantity
}

// getPoolsTotalQuantity is used to accumulate total cores for all cpuset pools
func getPoolsTotalQuantity(poolsQuantityMap map[string]int) int {
	poolsTotalQuantity := 0

	for _, quantity := range poolsQuantityMap {
		poolsTotalQuantity += quantity
	}

	return poolsTotalQuantity
}

// getContainersTotalQuantity is used to accumulate total cores for all containers
func getContainersTotalQuantity(containersQuantityMap map[string]map[string]int) int {
	containersTotalQuantity := 0

	for _, containerQuantities := range containersQuantityMap {
		for _, quantity := range containerQuantities {
			containersTotalQuantity += quantity
		}
	}

	return containersTotalQuantity
}

func getProportionalSize(oldPoolSize, oldTotalSize, newTotalSize int) int {
	return int(float64(newTotalSize) * (float64(oldPoolSize) / float64(oldTotalSize)))
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
		case string(v1.ResourceCPU):
			return general.Max(int(math.Ceil(req.ResourceRequests[key])), 0), nil
		case string(consts.ReclaimedResourceMilliCPU):
			return general.Max(int(math.Ceil(req.ResourceRequests[key]/1000.0)), 0), nil
		default:
			return 0, fmt.Errorf("invalid request resource name: %s", key)
		}
	}

	return 0, fmt.Errorf("unexpected end")
}

// initReservePool is used to initialize reserve cpuset pool for system cores workload when plugin starts.
func (p *DynamicPolicy) initReservePool() error {

	reserveAllocationInfo := p.state.GetAllocationInfo(state.PoolNameReserve, "")

	if reserveAllocationInfo != nil && !reserveAllocationInfo.AllocationResult.IsEmpty() {
		klog.Infof("[CPUDynamicPolicy.initReservePool] pool: %s allocation result transform from %s to %s",
			state.PoolNameReserve, reserveAllocationInfo.AllocationResult.String(), p.reservedCPUs)
	}

	klog.Infof("[CPUDynamicPolicy.initReservePool] initReservePool %s: %s",
		state.PoolNameReserve, p.reservedCPUs)

	topologyAwareAssignments, err := machine.GetNumaAwareAssignments(p.machineInfo.CPUTopology, p.reservedCPUs)
	if err != nil {
		return fmt.Errorf("unable to calculate topologyAwareAssignments for pool: %s, "+
			"result cpuset: %s, error: %v", state.PoolNameReserve, p.reservedCPUs.String(), err)
	}

	curReserveAllocationInfo := &state.AllocationInfo{
		PodUid:                           state.PoolNameReserve,
		OwnerPoolName:                    state.PoolNameReserve,
		AllocationResult:                 p.reservedCPUs.Clone(),
		OriginalAllocationResult:         p.reservedCPUs.Clone(),
		TopologyAwareAssignments:         topologyAwareAssignments,
		OriginalTopologyAwareAssignments: util.DeepCopyTopologyAwareAssignments(topologyAwareAssignments),
	}

	p.state.SetAllocationInfo(state.PoolNameReserve, "", curReserveAllocationInfo)

	return nil
}

func GetReadonlyState() (state.ReadonlyState, error) {
	readonlyStateLock.RLock()
	defer readonlyStateLock.RUnlock()
	if readonlyState == nil {
		return nil, fmt.Errorf("readonlyState isn't setted")
	}

	return readonlyState, nil
}

func (p *DynamicPolicy) syncCPUIdle() {
	klog.Infof("[CPUDynamicPolicy] exec syncCPUIdle")

	err := cgroupcmutils.ApplyCPUWithRelativePath(p.reclaimRelativeRootCgroupPath, &cgroupcm.CPUData{CpuIdlePtr: &p.enableCPUIdle})

	if err != nil {
		klog.Errorf("[CPUDynamicPolicy.syncCPUIdle] ApplyCPUWithRelativePath in %s with enableCPUIdle: %s in failed with error: %v",
			p.reclaimRelativeRootCgroupPath, p.enableCPUIdle, err)
	}
}

func (p *DynamicPolicy) checkCPUSet() {
	klog.Infof("[CPUDynamicPolicy] exec checkCPUSet")

	podEntries := p.state.GetPodEntries()
	actualCPUSets := make(map[string]map[string]machine.CPUSet)

	for podUID, containerEntries := range podEntries {
		if containerEntries.IsPoolEntry() {
			continue
		}

		for containerName, allocationInfo := range containerEntries {
			if allocationInfo == nil ||
				allocationInfo.ContainerType != pluginapi.ContainerType_MAIN.String() {
				continue
			} else if allocationInfo.QoSLevel == consts.PodAnnotationQoSLevelSharedCores &&
				allocationInfo.RequestQuantity == 0 {
				klog.Warningf("[CPUDynamicPolicy.checkCPUSet] skip cpuset checking for pod: %s/%s container: %s with zero cpu request",
					allocationInfo.PodNamespace, allocationInfo.PodName, containerName)
				continue
			}

			tags := metrics.ConvertMapToTags(map[string]string{
				"podNamespace":  allocationInfo.PodNamespace,
				"podName":       allocationInfo.PodName,
				"containerName": allocationInfo.ContainerName,
			})

			containerId, err := p.metaServer.GetContainerID(podUID, containerName)
			if err != nil {
				klog.Errorf("[CPUDynamicPolicy.checkCPUSet] get container id of pod: %s container: %s failed with error: %v",
					podUID, containerName, err)
				continue
			}

			cpusetStats, err := cgroupcmutils.GetCPUSetForContainer(podUID, containerId)

			if err != nil {
				klog.Errorf("[CPUDynamicPolicy.checkCPUSet] GetCPUSet of pod: %s container: name(%s), id(%s) failed with error: %v",
					podUID, containerName, containerId, err)

				_ = p.emitter.StoreInt64(util.MetricNameRealStateInvalid, 1, metrics.MetricTypeNameRaw, tags...)

				continue
			}

			if actualCPUSets[podUID] == nil {
				actualCPUSets[podUID] = make(map[string]machine.CPUSet)
			}

			actualCPUSets[podUID][containerName] = machine.MustParse(cpusetStats.CPUs)

			klog.Infof("[CPUDynamicPolicy.checkCPUSet] pod: %s/%s, container: %s, state CPUSet: %s, actual CPUSet: %s",
				allocationInfo.PodNamespace, allocationInfo.PodName,
				allocationInfo.ContainerName, allocationInfo.AllocationResult.String(),
				actualCPUSets[podUID][containerName].String())

			// only do comparison for dedicated_cores with numa_biding to avoid effect of adjustment for shared_cores
			if !(allocationInfo.QoSLevel == consts.PodAnnotationQoSLevelDedicatedCores &&
				allocationInfo.Annotations[consts.PodAnnotationMemoryEnhancementNumaBinding] ==
					consts.PodAnnotationMemoryEnhancementNumaBindingEnable) {
				continue
			}

			if !actualCPUSets[podUID][containerName].Equals(allocationInfo.OriginalAllocationResult) {
				klog.Errorf("[CPUDynamicPolicy.checkCPUSet] pod: %s/%s, container: %s, cpuset invalid",
					allocationInfo.PodNamespace, allocationInfo.PodName,
					allocationInfo.ContainerName)

				_ = p.emitter.StoreInt64(util.MetricNameCPUSetInvalid, 1, metrics.MetricTypeNameRaw, tags...)
			}
		}
	}

	unionDedicatedCPUSet := machine.NewCPUSet()
	unionSharedCPUSet := machine.NewCPUSet()
	var cpuSetOverlap bool

	for podUID, containerEntries := range actualCPUSets {
		for containerName, cset := range containerEntries {

			allocationInfo := podEntries[podUID][containerName]

			if allocationInfo == nil {
				continue
			}

			switch allocationInfo.QoSLevel {
			case consts.PodAnnotationQoSLevelDedicatedCores:
				if !cpuSetOverlap && cset.Intersection(unionDedicatedCPUSet).Size() != 0 {
					cpuSetOverlap = true
					klog.Errorf("[CPUDynamicPolicy.checkCPUSet] pod: %s/%s, container: %s cpuset: %s overlaps with others",
						allocationInfo.PodNamespace,
						allocationInfo.PodName,
						allocationInfo.ContainerName,
						cset.String())
				}

				unionDedicatedCPUSet = unionDedicatedCPUSet.Union(cset)
			case consts.PodAnnotationQoSLevelSharedCores:
				unionSharedCPUSet = unionSharedCPUSet.Union(cset)
			}
		}
	}

	regionOverlap := unionSharedCPUSet.Intersection(unionDedicatedCPUSet).Size() != 0

	if regionOverlap {
		klog.Errorf("[CPUDynamicPolicy.checkCPUSet] shared_cores union cpuset: %s overlaps with dedicated_cores union cpuset: %s",
			unionSharedCPUSet.String(),
			unionDedicatedCPUSet.String())
	}

	if !cpuSetOverlap {
		cpuSetOverlap = regionOverlap
	}

	if cpuSetOverlap {
		klog.Errorf("[CPUDynamicPolicy.checkCPUSet] found cpuset overlap. actualCPUSets: %+v", actualCPUSets)
		_ = p.emitter.StoreInt64(util.MetricNameCPUSetOverlap, 1, metrics.MetricTypeNameRaw)
	}

	klog.Infof("[CPUDynamicPolicy.checkCPUSet] finish checkCPUSet")
}

func (p *DynamicPolicy) applyBlocks(blockToCPUSet map[string]machine.CPUSet,
	curEntries state.PodEntries, resp *advisorapi.ListAndWatchResponse) error {

	if resp == nil {
		return fmt.Errorf("applyBlocks got nil resp")
	}

	newEntries := make(state.PodEntries)

	dedicatedCPUSet := machine.NewCPUSet()
	pooledUnionDedicatedCPUSet := machine.NewCPUSet()

	// deal with blocks of dedicated_cores and pools
	for entryName, entry := range resp.Entries {
		for subEntryName, calculationInfo := range entry.Entries {

			if calculationInfo == nil {
				klog.Warningf("[applyBlocks] got nil calculationInfo entry: %s, subEntry: %s",
					entryName, subEntryName)
				continue
			}

			// skip shared_cores and reclaimed_cores temporarily
			if !(subEntryName == "" || calculationInfo.OwnerPoolName == state.PoolNameDedicated) {
				continue
			}

			entryCPUSet := machine.NewCPUSet()

			for _, calculationResult := range calculationInfo.CalculationResultsByNumas {
				if calculationResult == nil {
					klog.Warningf("[applyBlocks] got nil NUMA result entry: %s, subEntry: %s",
						entryName, subEntryName)
					continue
				}

				for _, block := range calculationResult.Blocks {
					if block == nil {
						klog.Warningf("[applyBlocks] got nil block result entry: %s, subEntry: %s",
							entryName, subEntryName)
						continue
					}

					blockId := block.BlockId

					if cset, found := blockToCPUSet[blockId]; !found {
						return fmt.Errorf("block %s not found, entry: %s, subEntry: %s",
							blockId, entryName, subEntryName)
					} else {
						entryCPUSet = entryCPUSet.Union(cset)
					}
				}
			}

			topologyAwareAssignments, err := machine.GetNumaAwareAssignments(p.machineInfo.CPUTopology, entryCPUSet)

			if err != nil {
				return fmt.Errorf("unable to calculate topologyAwareAssignments for entry: %s, subEntry: %s, entry cpuset: %s, error: %v",
					entryName, subEntryName, entryCPUSet.String(), err)
			}

			allocationInfo := curEntries[entryName][subEntryName].Clone()

			if allocationInfo == nil {
				// only pool can be created by cpu advisor
				if calculationInfo.OwnerPoolName == state.PoolNameDedicated || subEntryName != "" {
					return fmt.Errorf("no-pool entry isn't found in plugin cache, entry: %s, subEntry: %s",
						entryName, subEntryName)
				} else if entryName != calculationInfo.OwnerPoolName {
					return fmt.Errorf("pool entryName: %s and OwnerPoolName: %s mismatch", entryName, calculationInfo.OwnerPoolName)
				}

				klog.Infof("[CPUDynamicPolicy.applyBlocks] create new pool: %s cpuset result %s",
					entryName, entryCPUSet.String())

				allocationInfo = &state.AllocationInfo{
					PodUid:                           entryName,
					OwnerPoolName:                    entryName,
					AllocationResult:                 entryCPUSet.Clone(),
					OriginalAllocationResult:         entryCPUSet.Clone(),
					TopologyAwareAssignments:         topologyAwareAssignments,
					OriginalTopologyAwareAssignments: util.DeepCopyTopologyAwareAssignments(topologyAwareAssignments),
				}
			} else {
				klog.Infof("[CPUDynamicPolicy.applyBlocks] entry: %s, subEntryName: %s cpuset allocation result transform from %s to %s",
					entryName, subEntryName, allocationInfo.AllocationResult.String(), entryCPUSet.String())

				allocationInfo.OwnerPoolName = calculationInfo.OwnerPoolName
				allocationInfo.AllocationResult = entryCPUSet.Clone()
				allocationInfo.OriginalAllocationResult = entryCPUSet.Clone()
				allocationInfo.TopologyAwareAssignments = topologyAwareAssignments
				allocationInfo.OriginalTopologyAwareAssignments = util.DeepCopyTopologyAwareAssignments(topologyAwareAssignments)
			}

			if newEntries[entryName] == nil {
				newEntries[entryName] = make(state.ContainerEntries)
			}

			newEntries[entryName][subEntryName] = allocationInfo
			pooledUnionDedicatedCPUSet = pooledUnionDedicatedCPUSet.Union(allocationInfo.AllocationResult)

			if allocationInfo.OwnerPoolName == state.PoolNameDedicated {
				dedicatedCPUSet = dedicatedCPUSet.Union(allocationInfo.AllocationResult)

				klog.Infof("[CPUDynamicPolicy.applyBlocks] try to apply dedicated_cores: %s/%s %s: %s",
					allocationInfo.PodNamespace, allocationInfo.PodName,
					allocationInfo.ContainerName, allocationInfo.AllocationResult.String())

				if allocationInfo.RampUp {
					allocationInfo.RampUp = false
					klog.Infof("[CPUDynamicPolicy.applyBlocks] pod: %s/%s, container: %s ramp up finished",
						allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName)
				}
			} else {
				_ = p.emitter.StoreInt64(util.MetricNamePoolSize, int64(allocationInfo.AllocationResult.Size()), metrics.MetricTypeNameRaw,
					metrics.MetricTag{Key: "poolName", Val: allocationInfo.OwnerPoolName})

				klog.Infof("[CPUDynamicPolicy.applyBlocks] try to apply pool %s: %s",
					allocationInfo.OwnerPoolName, allocationInfo.AllocationResult.String())
			}
		}
	}

	rampUpCPUs := p.machineInfo.CPUDetails.CPUs().Difference(p.reservedCPUs).Difference(dedicatedCPUSet)
	rampUpCPUsTopologyAwareAssignments, err := machine.GetNumaAwareAssignments(p.machineInfo.CPUTopology, rampUpCPUs)
	if err != nil {
		return fmt.Errorf("unable to calculate topologyAwareAssignments for rampUpCPUs, result cpuset: %s, error: %v",
			rampUpCPUs.String(), err)
	}

	if newEntries[state.PoolNameReclaim][""] == nil || newEntries[state.PoolNameReclaim][""].AllocationResult.IsEmpty() {
		reclaimPoolCPUSet := p.machineInfo.CPUDetails.CPUs().Difference(p.reservedCPUs).Difference(pooledUnionDedicatedCPUSet)

		if reclaimPoolCPUSet.IsEmpty() {
			// for state.PoolNameReclaim, we must make them exist when the node isn't in hybrid mode even if cause overlap
			allAvailableCPUs := p.machineInfo.CPUDetails.CPUs().Difference(p.reservedCPUs)
			var tErr error
			reclaimPoolCPUSet, _, tErr = calculator.TakeByNUMABalance(p.machineInfo, allAvailableCPUs, reservedReclaimedCPUsSize)
			if tErr != nil {
				return fmt.Errorf("fallback takeByNUMABalance faild in applyBlocks for reclaimPoolCPUSet with error: %v", tErr)
			}

			klog.Infof("[CPUDynamicPolicy.applyBlocks] fallback takeByNUMABalance for reclaimPoolCPUSet: %s", reclaimPoolCPUSet.String())
		}

		klog.Infof("[CPUDynamicPolicy.applyBlocks] set reclaimPoolCPUSet: %s", reclaimPoolCPUSet.String())

		topologyAwareAssignments, err := machine.GetNumaAwareAssignments(p.machineInfo.CPUTopology, reclaimPoolCPUSet)
		if err != nil {
			return fmt.Errorf("unable to calculate topologyAwareAssignments for pool: %s, "+
				"result cpuset: %s, error: %v", state.PoolNameReclaim, reclaimPoolCPUSet.String(), err)
		}

		if newEntries[state.PoolNameReclaim] == nil {
			newEntries[state.PoolNameReclaim] = make(state.ContainerEntries)
		}

		newEntries[state.PoolNameReclaim][""] = &state.AllocationInfo{
			PodUid:                           state.PoolNameReclaim,
			OwnerPoolName:                    state.PoolNameReclaim,
			AllocationResult:                 reclaimPoolCPUSet.Clone(),
			OriginalAllocationResult:         reclaimPoolCPUSet.Clone(),
			TopologyAwareAssignments:         topologyAwareAssignments,
			OriginalTopologyAwareAssignments: util.DeepCopyTopologyAwareAssignments(topologyAwareAssignments),
		}
	} else {
		klog.Infof("[CPUDynamicPolicy.applyBlocks] detected reclaimPoolCPUSet: %s", newEntries[state.PoolNameReclaim][""].AllocationResult.String())
	}

	// deal with blocks of reclaimed_cores and share_cores
	for podUID, containerEntries := range curEntries {
		if containerEntries.IsPoolEntry() {
			continue
		}

	containerLoop:
		for containerName, allocationInfo := range containerEntries {
			if allocationInfo == nil {
				klog.Errorf("[CPUDynamicPolicy.applyBlocks] pod: %s, container: %s has nil allocationInfo",
					podUID, containerName)
				continue
			}

			reqInt := state.GetContainerRequestedCores(allocationInfo)
			if newEntries[podUID][containerName] != nil {
				// adapt to old checkpoint without RequestQuantity property
				newEntries[podUID][containerName].RequestQuantity = reqInt
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
				// may be indicated by qos aware server
				ownerPoolName := state.GetRealOwnerPoolName(allocationInfo)

				if ownerPoolName == "" {
					ownerPoolName = state.GetSpecifiedPoolName(allocationInfo)
				}

				if resp.Entries[podUID] != nil && resp.Entries[podUID].Entries[containerName] != nil {
					klog.Infof("[CPUDynamicPolicy.applyBlocks] cpu advisor put pod: %s/%s, container: %s from %s to %s",
						allocationInfo.PodNamespace, allocationInfo.PodName,
						allocationInfo.ContainerName, ownerPoolName,
						resp.Entries[podUID].Entries[containerName].OwnerPoolName)
					ownerPoolName = resp.Entries[podUID].Entries[containerName].OwnerPoolName
				} else {
					klog.Warningf("[CPUDynamicPolicy.applyBlocks] cpu advisor doesn't return entry for pod: %s/%s, container: %s, qosLevel: %s",
						allocationInfo.PodNamespace, allocationInfo.PodName,
						allocationInfo.ContainerName, allocationInfo.QoSLevel)
				}

				if allocationInfo.RampUp {
					klog.Infof("[CPUDynamicPolicy.applyBlocks] pod: %s/%s container: %s is in ramp up, set its allocation result from %s to rampUpCPUs :%s",
						allocationInfo.PodNamespace, allocationInfo.PodName,
						allocationInfo.ContainerName, allocationInfo.AllocationResult.String(),
						rampUpCPUs.String())

					if rampUpCPUs.IsEmpty() {
						klog.Warningf("[CPUDynamicPolicy.applyBlocks] rampUpCPUs is empty. pod: %s/%s container: %s reuses its allocation result: %s",
							allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName, allocationInfo.AllocationResult.String())
						continue containerLoop
					}

					newEntries[podUID][containerName].OwnerPoolName = ""
					newEntries[podUID][containerName].AllocationResult = rampUpCPUs.Clone()
					newEntries[podUID][containerName].OriginalAllocationResult = rampUpCPUs.Clone()
					newEntries[podUID][containerName].TopologyAwareAssignments = util.DeepCopyTopologyAwareAssignments(rampUpCPUsTopologyAwareAssignments)
					newEntries[podUID][containerName].OriginalTopologyAwareAssignments = util.DeepCopyTopologyAwareAssignments(rampUpCPUsTopologyAwareAssignments)
				} else if newEntries[ownerPoolName][""] == nil {
					errMsg := fmt.Sprintf("[CPUDynamicPolicy.applyBlocks] cpu advisor doesn't return entry for pool: %s and it's referred by"+
						" pod: %s/%s, container: %s, qosLevel: %s",
						ownerPoolName, allocationInfo.PodNamespace,
						allocationInfo.PodName, allocationInfo.ContainerName,
						allocationInfo.QoSLevel)
					klog.Errorf(errMsg)
					return fmt.Errorf(errMsg)
				} else {
					poolEntry := newEntries[ownerPoolName][""]

					klog.Infof("[CPUDynamicPolicy.applyBlocks] put pod: %s/%s container: %s to pool: %s, set its allocation result from %s to %s",
						allocationInfo.PodNamespace, allocationInfo.PodName,
						allocationInfo.ContainerName, ownerPoolName,
						allocationInfo.AllocationResult.String(), poolEntry.AllocationResult.String())

					newEntries[podUID][containerName].OwnerPoolName = ownerPoolName
					newEntries[podUID][containerName].AllocationResult = poolEntry.AllocationResult.Clone()
					newEntries[podUID][containerName].OriginalAllocationResult = poolEntry.OriginalAllocationResult.Clone()
					newEntries[podUID][containerName].TopologyAwareAssignments = util.DeepCopyTopologyAwareAssignments(poolEntry.TopologyAwareAssignments)
					newEntries[podUID][containerName].OriginalTopologyAwareAssignments = util.DeepCopyTopologyAwareAssignments(poolEntry.TopologyAwareAssignments)
				}
			default:
				return fmt.Errorf("invalid qosLevel: %s for pod: %s/%s container: %s",
					allocationInfo.QoSLevel, allocationInfo.PodNamespace,
					allocationInfo.PodName, allocationInfo.ContainerName)
			}
		}
	}

	// use pod entries generated above to generate machine state info, and store in local state
	newMachineState, err := state.GenerateCPUMachineStateByPodEntries(p.machineInfo.CPUTopology, newEntries)
	if err != nil {
		return fmt.Errorf("calculate machineState by newPodEntries failed with error: %v", err)
	}

	p.state.SetPodEntries(newEntries)
	p.state.SetMachineState(newMachineState)

	return nil
}
