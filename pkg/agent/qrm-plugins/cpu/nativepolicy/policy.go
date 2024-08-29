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

package nativepolicy

import (
	"context"
	"fmt"
	"math"
	"sync"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-api/pkg/plugins/skeleton"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/agent"
	cpuconsts "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	nativepolicyutil "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/nativepolicy/util"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util"
	"github.com/kubewharf/katalyst-core/pkg/config"
	dynamicconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/crd"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	utilkubeconfig "github.com/kubewharf/katalyst-core/pkg/util/kubelet/config"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

const (
	// cpuPluginStateFileName is the name of cpu plugin state file.
	cpuPluginStateFileName = "cpu_plugin_state"
)

const (
	stateCheckPeriod = 30 * time.Second
	maxResidualTime  = 5 * time.Minute
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

// NativePolicy is a policy compatible with Kubernetes native semantics and is used in topology-aware scheduling scenarios.
type NativePolicy struct {
	sync.RWMutex
	name    string
	stopCh  chan struct{}
	started bool

	emitter     metrics.MetricEmitter
	metaServer  *metaserver.MetaServer
	machineInfo *machine.KatalystMachineInfo

	state          state.State
	residualHitMap map[string]int64
	// set of CPUs to reuse across allocations in a pod
	cpusToReuse map[string]machine.CPUSet

	// those are parsed from configurations
	// todo if we want to use dynamic configuration, we'd better not use self-defined conf
	reservedCPUs           machine.CPUSet
	cpuPluginSocketAbsPath string
	extraStateFileAbsPath  string
	dynamicConfig          *dynamicconfig.DynamicAgentConfiguration
	podDebugAnnoKeys       []string

	// enableFullPhysicalCPUsOnly is a flag to enable extra allocation restrictions to avoid
	// different containers to possibly end up on the same core.
	enableFullPhysicalCPUsOnly bool

	// cpuAllocationOption is is the allocation option of cpu (packed/distributed).
	cpuAllocationOption string
}

func NewNativePolicy(agentCtx *agent.GenericContext, conf *config.Configuration,
	_ interface{}, agentName string,
) (bool, agent.Component, error) {
	general.Infof("new native policy")

	stateImpl, stateErr := state.NewCheckpointState(conf.GenericQRMPluginConfiguration.StateFileDirectory, cpuPluginStateFileName,
		cpuconsts.CPUResourcePluginPolicyNameNative, agentCtx.CPUTopology, conf.SkipCPUStateCorruption)
	if stateErr != nil {
		return false, agent.ComponentStub{}, fmt.Errorf("NewCheckpointState failed with error: %v", stateErr)
	}

	readonlyStateLock.Lock()
	readonlyState = stateImpl
	readonlyStateLock.Unlock()

	wrappedEmitter := agentCtx.EmitterPool.GetDefaultMetricsEmitter().WithTags(agentName, metrics.MetricTag{
		Key: util.QRMPluginPolicyTagName,
		Val: cpuconsts.CPUResourcePluginPolicyNameNative,
	})

	policyImplement := &NativePolicy{
		name:                       fmt.Sprintf("%s_%s", agentName, cpuconsts.CPUResourcePluginPolicyNameNative),
		stopCh:                     make(chan struct{}),
		machineInfo:                agentCtx.KatalystMachineInfo,
		emitter:                    wrappedEmitter,
		metaServer:                 agentCtx.MetaServer,
		residualHitMap:             make(map[string]int64),
		cpusToReuse:                make(map[string]machine.CPUSet),
		state:                      stateImpl,
		dynamicConfig:              conf.DynamicAgentConfiguration,
		cpuPluginSocketAbsPath:     conf.CPUPluginSocketAbsPath,
		extraStateFileAbsPath:      conf.ExtraStateFileAbsPath,
		podDebugAnnoKeys:           conf.PodDebugAnnoKeys,
		enableFullPhysicalCPUsOnly: conf.EnableFullPhysicalCPUsOnly,
		cpuAllocationOption:        conf.CPUAllocationOption,
	}

	if err := policyImplement.setReservedCPUs(agentCtx.CPUDetails.CPUs().Clone()); err != nil {
		return false, agent.ComponentStub{}, fmt.Errorf("native policy set reserved CPUs failed with error: %v", err)
	}

	err := agentCtx.MetaServer.ConfigurationManager.AddConfigWatcher(crd.AdminQoSConfigurationGVR)
	if err != nil {
		return false, nil, err
	}

	pluginWrapper, err := skeleton.NewRegistrationPluginWrapper(policyImplement, conf.QRMPluginSocketDirs, nil)
	if err != nil {
		return false, agent.ComponentStub{}, fmt.Errorf("native policy new plugin wrapper failed with error: %v", err)
	}

	return true, &agent.PluginWrapper{GenericPlugin: pluginWrapper}, nil
}

func (p *NativePolicy) Name() string {
	return p.name
}

func (p *NativePolicy) ResourceName() string {
	return string(v1.ResourceCPU)
}

func (p *NativePolicy) Start() (err error) {
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

	return nil
}

func (p *NativePolicy) Stop() error {
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

	return nil
}

// GetResourcePluginOptions returns options to be communicated with Resource Manager
func (p *NativePolicy) GetResourcePluginOptions(context.Context,
	*pluginapi.Empty,
) (*pluginapi.ResourcePluginOptions, error) {
	general.Infof("called")
	return &pluginapi.ResourcePluginOptions{
		PreStartRequired:      false,
		WithTopologyAlignment: true,
		NeedReconcile:         true,
	}, nil
}

// GetTopologyHints returns hints of corresponding resources
func (p *NativePolicy) GetTopologyHints(ctx context.Context,
	req *pluginapi.ResourceRequest,
) (resp *pluginapi.ResourceHintsResponse, err error) {
	if req == nil {
		return nil, fmt.Errorf("GetTopologyHints got nil req")
	}

	// identify if the pod is a debug pod,
	// if so, apply specific strategy to it.
	// since GetKatalystQoSLevelFromResourceReq function will filter annotations,
	// we should do it before GetKatalystQoSLevelFromResourceReq.
	isDebugPod := util.IsDebugPod(req.Annotations, p.podDebugAnnoKeys)

	reqInt, _, err := util.GetQuantityFromResourceReq(req)
	if err != nil {
		return nil, fmt.Errorf("getReqQuantityFromResourceReq failed with error: %v", err)
	}

	isInteger := float64(reqInt) == req.ResourceRequests[string(v1.ResourceCPU)]

	general.InfoS("called",
		"podNamespace", req.PodNamespace,
		"podName", req.PodName,
		"containerName", req.ContainerName,
		"podType", req.PodType,
		"podRole", req.PodRole,
		"containerType", req.ContainerType,
		"qosClass", req.NativeQosClass,
		"numCPUs", reqInt,
		"isDebugPod", isDebugPod,
		"isInteger", isInteger)

	if req.ContainerType == pluginapi.ContainerType_INIT || isDebugPod {
		general.Infof("there is no NUMA preference, return nil hint")
		return util.PackResourceHintsResponse(req, string(v1.ResourceCPU),
			map[string]*pluginapi.ListOfTopologyHints{
				string(v1.ResourceCPU): nil, // indicates that there is no numa preference
			})
	}

	p.RLock()
	defer func() {
		p.RUnlock()
		if err != nil {
			_ = p.emitter.StoreInt64(util.MetricNameGetTopologyHintsFailed, 1, metrics.MetricTypeNameRaw)
		}
	}()

	if req.NativeQosClass != string(v1.PodQOSGuaranteed) || !isInteger {
		return p.sharedPoolHintHandler(ctx, req)
	}
	return p.dedicatedCoresHintHandler(ctx, req)
}

// GetPodTopologyHints returns hints of corresponding resources for pod
func (p *NativePolicy) GetPodTopologyHints(ctx context.Context,
	req *pluginapi.PodResourceRequest,
) (resp *pluginapi.PodResourceHintsResponse, err error) {
	return nil, util.ErrNotImplemented
}

// Allocate is called during pod admit so that the resource
// plugin can allocate corresponding resource for the container
// according to resource request
func (p *NativePolicy) Allocate(ctx context.Context,
	req *pluginapi.ResourceRequest,
) (resp *pluginapi.ResourceAllocationResponse, respErr error) {
	if req == nil {
		return nil, fmt.Errorf("allocate got nil req")
	}

	// identify if the pod is a debug pod,
	// if so, apply specific strategy to it.
	// since GetKatalystQoSLevelFromResourceReq function will filter annotations,
	// we should do it before GetKatalystQoSLevelFromResourceReq.
	isDebugPod := util.IsDebugPod(req.Annotations, p.podDebugAnnoKeys)

	reqInt, _, err := util.GetQuantityFromResourceReq(req)
	if err != nil {
		return nil, fmt.Errorf("getReqQuantityFromResourceReq failed with error: %v", err)
	}

	isInteger := float64(reqInt) == req.ResourceRequests[string(v1.ResourceCPU)]

	general.InfoS("called",
		"podNamespace", req.PodNamespace,
		"podName", req.PodName,
		"containerName", req.ContainerName,
		"podType", req.PodType,
		"podRole", req.PodRole,
		"containerType", req.ContainerType,
		"qosClass", req.NativeQosClass,
		"numCPUs", reqInt,
		"isDebugPod", isDebugPod,
		"isInteger", isInteger)

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
			NativeQosClass: req.NativeQosClass,
		}, nil
	}

	if isDebugPod {
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
						// return ResourceAllocation with empty OciPropertyName, AllocatedQuantity, AllocationResult for containers in debug pod,
						// it won't influence oci spec properties of the container
						IsNodeResource:   false,
						IsScalarResource: true,
					},
				},
			},
			Labels:         general.DeepCopyMap(req.Labels),
			Annotations:    general.DeepCopyMap(req.Annotations),
			NativeQosClass: req.NativeQosClass,
		}, nil
	}

	p.Lock()
	defer func() {
		if respErr != nil {
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

		p.updateCPUsToReuse(req, allocationInfo.AllocationResult)

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
			Labels:         general.DeepCopyMap(req.Labels),
			Annotations:    general.DeepCopyMap(req.Annotations),
			NativeQosClass: req.NativeQosClass,
		}, nil
	}

	if req.NativeQosClass != string(v1.PodQOSGuaranteed) || !isInteger {
		return p.sharedPoolAllocationHandler(ctx, req)
	}
	return p.dedicatedCoresAllocationHandler(ctx, req)
}

// AllocateForPod is called during pod admit so that the resource
// plugin can allocate corresponding resource for the pod
// according to resource request
func (p *NativePolicy) AllocateForPod(ctx context.Context,
	req *pluginapi.PodResourceRequest,
) (resp *pluginapi.PodResourceAllocationResponse, respErr error) {
	return nil, util.ErrNotImplemented
}

// GetResourcesAllocation returns allocation results of corresponding resources
func (p *NativePolicy) GetResourcesAllocation(_ context.Context,
	req *pluginapi.GetResourcesAllocationRequest,
) (*pluginapi.GetResourcesAllocationResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("GetResourcesAllocation got nil req")
	}

	general.Infof("called")
	p.Lock()
	defer p.Unlock()

	defaultCPUSet := p.state.GetMachineState().GetDefaultCPUSet()
	defaultCPUSetTopologyAwareAssignments, err := machine.GetNumaAwareAssignments(p.machineInfo.CPUTopology, defaultCPUSet)
	if err != nil {
		return nil, fmt.Errorf("GetNumaAwareAssignments err: %v", err)
	}

	podResources := make(map[string]*pluginapi.ContainerResources)

	for podUID, containerEntries := range p.state.GetPodEntries() {
		if podResources[podUID] == nil {
			podResources[podUID] = &pluginapi.ContainerResources{}
		}

		for containerName, allocationInfo := range containerEntries {
			if allocationInfo == nil {
				continue
			}
			allocationInfo = allocationInfo.Clone()

			resultCPUSet := machine.NewCPUSet()
			switch allocationInfo.OwnerPoolName {
			case state.PoolNameDedicated:
				resultCPUSet = allocationInfo.AllocationResult
			case state.PoolNameShare:
				resultCPUSet = defaultCPUSet

				if !allocationInfo.AllocationResult.Equals(defaultCPUSet) {
					clonedDefaultCPUSet := defaultCPUSet.Clone()
					clonedDefaultCPUSetTopologyAwareAssignments := machine.DeepcopyCPUAssignment(defaultCPUSetTopologyAwareAssignments)

					allocationInfo.AllocationResult = clonedDefaultCPUSet
					allocationInfo.OriginalAllocationResult = clonedDefaultCPUSet
					allocationInfo.TopologyAwareAssignments = clonedDefaultCPUSetTopologyAwareAssignments
					allocationInfo.OriginalTopologyAwareAssignments = clonedDefaultCPUSetTopologyAwareAssignments

					p.state.SetAllocationInfo(podUID, containerName, allocationInfo)
				}
			default:
				general.Errorf("skip container because the pool name is not supported, pod: %s, container: %s, cpuset: %s",
					podUID, containerName, resultCPUSet.String())
				continue
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
						AllocatedQuantity: float64(resultCPUSet.Size()),
						AllocationResult:  resultCPUSet.String(),
					},
				},
			}
		}
	}

	return &pluginapi.GetResourcesAllocationResponse{
		PodResources: podResources,
	}, nil
}

// GetTopologyAwareResources returns allocation results of corresponding resources as machineInfo aware format
func (p *NativePolicy) GetTopologyAwareResources(_ context.Context,
	req *pluginapi.GetTopologyAwareResourcesRequest,
) (*pluginapi.GetTopologyAwareResourcesResponse, error) {
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
			ContainerName:      allocationInfo.ContainerName,
			AllocatedResources: make(map[string]*pluginapi.TopologyAwareResource),
		},
	}

	if allocationInfo.OwnerPoolName == state.PoolNameDedicated {
		resp.ContainerTopologyAwareResources.AllocatedResources[string(v1.ResourceCPU)] = &pluginapi.TopologyAwareResource{
			IsNodeResource:                    false,
			IsScalarResource:                  true,
			AggregatedQuantity:                float64(allocationInfo.AllocationResult.Size()),
			OriginalAggregatedQuantity:        float64(allocationInfo.OriginalAllocationResult.Size()),
			TopologyAwareQuantityList:         util.GetTopologyAwareQuantityFromAssignments(allocationInfo.TopologyAwareAssignments),
			OriginalTopologyAwareQuantityList: util.GetTopologyAwareQuantityFromAssignments(allocationInfo.OriginalTopologyAwareAssignments),
		}
	}

	return resp, nil
}

// GetTopologyAwareAllocatableResources returns corresponding allocatable resources as machineInfo aware format
func (p *NativePolicy) GetTopologyAwareAllocatableResources(_ context.Context,
	_ *pluginapi.GetTopologyAwareAllocatableResourcesRequest,
) (*pluginapi.GetTopologyAwareAllocatableResourcesResponse, error) {
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

// PreStartContainer is called, if indicated by resource plugin during registration phase,
// before each container start. Resource plugin can run resource specific operations
// such as resetting the resource before making resources available to the container
func (p *NativePolicy) PreStartContainer(context.Context,
	*pluginapi.PreStartContainerRequest,
) (*pluginapi.PreStartContainerResponse, error) {
	return nil, nil
}

func (p *NativePolicy) RemovePod(ctx context.Context,
	req *pluginapi.RemovePodRequest,
) (resp *pluginapi.RemovePodResponse, err error) {
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

	err = p.removePod(req.PodUid)
	if err != nil {
		general.ErrorS(err, "remove pod failed with error", "podUID", req.PodUid)
		return nil, err
	}

	return &pluginapi.RemovePodResponse{}, nil
}

func (p *NativePolicy) removePod(podUID string) error {
	podEntries := p.state.GetPodEntries()
	if len(podEntries[podUID]) == 0 {
		return nil
	}
	delete(podEntries, podUID)

	updatedMachineState, err := nativepolicyutil.GenerateMachineStateFromPodEntries(p.machineInfo.CPUTopology, podEntries)
	if err != nil {
		return fmt.Errorf("GenerateMachineStateFromPodEntries failed with error: %v", err)
	}

	p.state.SetPodEntries(podEntries)
	p.state.SetMachineState(updatedMachineState)
	return nil
}

func (p *NativePolicy) removeContainer(podUID, containerName string) error {
	podEntries := p.state.GetPodEntries()
	if podEntries[podUID][containerName] == nil {
		return nil
	}
	delete(podEntries[podUID], containerName)

	updatedMachineState, err := nativepolicyutil.GenerateMachineStateFromPodEntries(p.machineInfo.CPUTopology, podEntries)
	if err != nil {
		return fmt.Errorf("GenerateMachineStateFromPodEntries failed with error: %v", err)
	}

	p.state.SetPodEntries(podEntries)
	p.state.SetMachineState(updatedMachineState)
	return nil
}

// getContainerRequestedCores parses and returns request cores for the given container
func (p *NativePolicy) getContainerRequestedCores(allocationInfo *state.AllocationInfo) float64 {
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

		cpuQuantity := native.CPUQuantityGetter()(container.Resources.Requests)
		allocationInfo.RequestQuantity = general.MaxFloat64(float64(cpuQuantity.MilliValue())/1000, 0)
		general.Infof("get cpu request quantity: %.3f for pod: %s/%s container: %s from podWatcher",
			allocationInfo.RequestQuantity, allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName)
	}
	return allocationInfo.RequestQuantity
}

// setReservedCPUs calculates and sets the reservedCPUs field
func (p *NativePolicy) setReservedCPUs(allCPUs machine.CPUSet) error {
	klConfig, err := p.metaServer.GetKubeletConfig(context.TODO())
	if err != nil {
		return fmt.Errorf("NewNativePolicy failed because get kubelet config failed with error: %v", err)
	}

	reservedQuantity, _, err := utilkubeconfig.GetReservedQuantity(klConfig, string(v1.ResourceCPU))
	if err != nil {
		return fmt.Errorf("getKubeletReservedQuantity failed because get kubelet reserved quantity failed with error: %v", err)
	} else if reservedQuantity.IsZero() {
		// The native policy requires this to be nonzero. Zero CPU reservation
		// would allow the shared pool to be completely exhausted. At that point
		// either we would violate our guarantee of exclusivity or need to evict
		// any pod that has at least one container that requires zero CPUs.
		// See the comments in policy_static.go for more details.
		return fmt.Errorf("the native policy requires systemreserved.cpu + kubereserved.cpu to be greater than zero")
	}

	// Take the ceiling of the reservation, since fractional CPUs cannot be
	// exclusively allocated.
	reservedCPUsFloat := float64(reservedQuantity.MilliValue()) / 1000
	numReservedCPUs := int(math.Ceil(reservedCPUsFloat))

	var reserved machine.CPUSet
	reservedCPUs, err := machine.Parse(klConfig.ReservedSystemCPUs)
	if err != nil {
		return fmt.Errorf("NewNativePolicy parse cpuset for reserved-cpus failed with error: %v", err)
	}
	if reservedCPUs.Size() > 0 {
		reserved = reservedCPUs
	} else {
		// takeByTopology allocates CPUs associated with low-numbered cores from
		// allCPUs.
		reserved, _ = p.takeByTopology(allCPUs, numReservedCPUs)
	}

	if reserved.Size() != numReservedCPUs {
		return fmt.Errorf("unable to reserve the required amount of CPUs (size of %s did not equal %d)", reserved, numReservedCPUs)
	}

	general.Infof("take reserved CPUs: %s by reservedCPUsNum: %d", reserved.String(), numReservedCPUs)

	p.reservedCPUs = reserved

	return nil
}
