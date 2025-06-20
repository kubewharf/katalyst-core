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

package staticpolicy

import (
	"context"
	"fmt"
	"sync"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-api/pkg/plugins/skeleton"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/agent"
	appqrm "github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/agent/qrm"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	gpuconsts "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util"
	"github.com/kubewharf/katalyst-core/pkg/agent/utilcomponent/periodicalhandler"
	"github.com/kubewharf/katalyst-core/pkg/config"
	dynamicconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/qrm"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	"github.com/kubewharf/katalyst-core/pkg/util/metric"
)

const (
	GPUPluginStateFileName = "gpu_plugin_state"
)

// StaticPolicy is the static gpu policy
type StaticPolicy struct {
	sync.Mutex

	name                string
	stopCh              chan struct{}
	started             bool
	qosConfig           *generic.QoSConfiguration
	qrmConfig           *qrm.QRMPluginsConfiguration
	gpuTopologyProvider machine.GPUTopologyProvider

	emitter    metrics.MetricEmitter
	metaServer *metaserver.MetaServer
	agentCtx   *agent.GenericContext
	state      state.State

	podAnnotationKeptKeys []string
	podLabelKeptKeys      []string
	resourceNames         sets.String

	residualHitMap map[string]int64
}

// NewStaticPolicy returns a static gpu policy
func NewStaticPolicy(agentCtx *agent.GenericContext, conf *config.Configuration,
	_ interface{}, agentName string,
) (bool, agent.Component, error) {
	wrappedEmitter := agentCtx.EmitterPool.GetDefaultMetricsEmitter().WithTags(agentName, metrics.MetricTag{
		Key: util.QRMPluginPolicyTagName,
		Val: gpuconsts.GPUResourcePluginPolicyNameStatic,
	})

	gpuTopologyProvider := machine.NewKubeletCheckpointGPUTopologyProvider(conf.GPUResourceNames)
	stateImpl, err := state.NewCheckpointState(conf.QRMPluginsConfiguration, conf.GenericQRMPluginConfiguration.StateFileDirectory, GPUPluginStateFileName,
		gpuconsts.GPUResourcePluginPolicyNameStatic, gpuTopologyProvider, conf.SkipGPUStateCorruption, wrappedEmitter)
	if err != nil {
		return false, agent.ComponentStub{}, fmt.Errorf("NewCheckpointState failed with error: %v", err)
	}

	policyImplement := &StaticPolicy{
		emitter:               wrappedEmitter,
		metaServer:            agentCtx.MetaServer,
		agentCtx:              agentCtx,
		stopCh:                make(chan struct{}),
		name:                  fmt.Sprintf("%s_%s", agentName, gpuconsts.GPUResourcePluginPolicyNameStatic),
		qosConfig:             conf.QoSConfiguration,
		qrmConfig:             conf.QRMPluginsConfiguration,
		gpuTopologyProvider:   gpuTopologyProvider,
		state:                 stateImpl,
		podAnnotationKeptKeys: conf.PodAnnotationKeptKeys,
		podLabelKeptKeys:      conf.PodLabelKeptKeys,
		residualHitMap:        make(map[string]int64),
		resourceNames:         sets.NewString(conf.GPUResourceNames...),
	}

	pluginWrapper, err := skeleton.NewRegistrationPluginWrapper(policyImplement, conf.QRMPluginSocketDirs,
		func(key string, value int64) {
			_ = wrappedEmitter.StoreInt64(key, value, metrics.MetricTypeNameRaw)
		})
	if err != nil {
		return false, agent.ComponentStub{}, fmt.Errorf("static policy new plugin wrapper failed with error: %v", err)
	}

	return true, &agent.PluginWrapper{GenericPlugin: pluginWrapper}, nil
}

// Start starts this plugin
func (p *StaticPolicy) Start() (err error) {
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

	err = periodicalhandler.RegisterPeriodicalHandlerWithHealthz(gpuconsts.ClearResidualState, general.HealthzCheckStateNotReady,
		appqrm.QRMNetworkPluginPeriodicalHandlerGroupName, p.clearResidualState, gpuconsts.StateCheckPeriod, gpuconsts.StateCheckTolerationTimes)
	if err != nil {
		general.Errorf("start %v failed, err: %v", gpuconsts.ClearResidualState, err)
	}

	go wait.Until(func() {
		periodicalhandler.ReadyToStartHandlersByGroup(appqrm.QRMGPUPluginPeriodicalHandlerGroupName)
	}, 5*time.Second, p.stopCh)

	return nil
}

// Stop stops this plugin
func (p *StaticPolicy) Stop() error {
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

// Name returns the name of this plugin
func (p *StaticPolicy) Name() string {
	return p.name
}

// ResourceName returns resource names managed by this plugin
func (p *StaticPolicy) ResourceName() string {
	return string(consts.ResourceGPUMemory)
}

// GetTopologyHints returns hints of corresponding resources
func (p *StaticPolicy) GetTopologyHints(_ context.Context,
	req *pluginapi.ResourceRequest,
) (resp *pluginapi.ResourceHintsResponse, err error) {
	general.InfofV(4, "called")
	if req == nil {
		return nil, fmt.Errorf("GetTopologyHints got nil req")
	}

	return util.PackResourceHintsResponse(req, p.ResourceName(), map[string]*pluginapi.ListOfTopologyHints{
		p.ResourceName(): nil, // indicates that there is no numa preference
	})
}

// GetPodTopologyHints returns hints of corresponding resources
func (p *StaticPolicy) GetPodTopologyHints(_ context.Context,
	req *pluginapi.PodResourceRequest,
) (resp *pluginapi.PodResourceHintsResponse, err error) {
	return nil, util.ErrNotImplemented
}

func (p *StaticPolicy) RemovePod(_ context.Context,
	req *pluginapi.RemovePodRequest,
) (*pluginapi.RemovePodResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("RemovePod got nil req")
	}

	p.Lock()
	defer p.Unlock()

	if err := p.removePod(req.PodUid); err != nil {
		general.ErrorS(err, "remove pod failed with error", "podUID", req.PodUid)
		return nil, err
	}

	return &pluginapi.RemovePodResponse{}, nil
}

// GetResourcesAllocation returns allocation results of corresponding resources
func (p *StaticPolicy) GetResourcesAllocation(_ context.Context,
	_ *pluginapi.GetResourcesAllocationRequest,
) (*pluginapi.GetResourcesAllocationResponse, error) {
	general.InfofV(4, "called")
	err := p.updateAllocatedDevice()
	if err != nil {
		general.Errorf("update allocated device failed with error: %v", err)
		return nil, err
	}

	return &pluginapi.GetResourcesAllocationResponse{}, nil
}

// GetTopologyAwareResources returns allocation results of corresponding resources as topology aware format
func (p *StaticPolicy) GetTopologyAwareResources(_ context.Context,
	req *pluginapi.GetTopologyAwareResourcesRequest,
) (*pluginapi.GetTopologyAwareResourcesResponse, error) {
	general.InfofV(4, "called")
	if req == nil {
		return nil, fmt.Errorf("GetTopologyAwareResources got nil req")
	}

	allocationInfo := p.state.GetAllocationInfo(req.PodUid, req.ContainerName)
	if allocationInfo == nil {
		return &pluginapi.GetTopologyAwareResourcesResponse{}, nil
	}

	topologyAwareQuantityList := make([]*pluginapi.TopologyAwareQuantity, 0, len(allocationInfo.TopologyAwareAllocations))
	for deviceID, alloc := range allocationInfo.TopologyAwareAllocations {
		topologyAwareQuantityList = append(topologyAwareQuantityList, &pluginapi.TopologyAwareQuantity{
			ResourceValue: float64(alloc.GPUMemoryQuantity),
			Name:          deviceID,
			Type:          string(v1alpha1.TopologyTypeGPU),
			Annotations: map[string]string{
				consts.ResourceAnnotationKeyResourceIdentifier: "",
			},
		})
	}

	resp := &pluginapi.GetTopologyAwareResourcesResponse{
		PodUid:       allocationInfo.PodUid,
		PodName:      allocationInfo.PodName,
		PodNamespace: allocationInfo.PodNamespace,
		ContainerTopologyAwareResources: &pluginapi.ContainerTopologyAwareResources{
			ContainerName: allocationInfo.ContainerName,
			AllocatedResources: map[string]*pluginapi.TopologyAwareResource{
				p.ResourceName(): {
					IsNodeResource:                    true,
					IsScalarResource:                  true,
					AggregatedQuantity:                float64(allocationInfo.AllocatedAllocation.GPUMemoryQuantity),
					OriginalAggregatedQuantity:        float64(allocationInfo.AllocatedAllocation.GPUMemoryQuantity),
					TopologyAwareQuantityList:         topologyAwareQuantityList,
					OriginalTopologyAwareQuantityList: topologyAwareQuantityList,
				},
			},
		},
	}

	return resp, nil
}

// GetTopologyAwareAllocatableResources returns corresponding allocatable resources as topology aware format
func (p *StaticPolicy) GetTopologyAwareAllocatableResources(_ context.Context,
	req *pluginapi.GetTopologyAwareAllocatableResourcesRequest,
) (*pluginapi.GetTopologyAwareAllocatableResourcesResponse, error) {
	general.InfofV(4, "called")
	if req == nil {
		return nil, fmt.Errorf("GetTopologyAwareAllocatableResources got nil req")
	}

	p.Lock()
	defer p.Unlock()

	machineState := p.state.GetMachineState()

	topologyAwareAllocatableQuantityList := make([]*pluginapi.TopologyAwareQuantity, 0, len(machineState))
	topologyAwareCapacityQuantityList := make([]*pluginapi.TopologyAwareQuantity, 0, len(machineState))
	var aggregatedAllocatableQuantity, aggregatedCapacityQuantity uint64
	for deviceID, gpuState := range machineState {
		aggregatedAllocatableQuantity += gpuState.GetGPUMemoryAllocatable()
		aggregatedCapacityQuantity += gpuState.GetGPUMemoryAllocatable()
		topologyAwareAllocatableQuantityList = append(topologyAwareAllocatableQuantityList, &pluginapi.TopologyAwareQuantity{
			ResourceValue: float64(gpuState.GetGPUMemoryAllocatable()),
			Name:          deviceID,
			Type:          string(v1alpha1.TopologyTypeGPU),
			Annotations: map[string]string{
				consts.ResourceAnnotationKeyResourceIdentifier: "",
			},
		})
		topologyAwareCapacityQuantityList = append(topologyAwareCapacityQuantityList, &pluginapi.TopologyAwareQuantity{
			ResourceValue: float64(gpuState.GetGPUMemoryAllocatable()),
			Name:          deviceID,
			Type:          string(v1alpha1.TopologyTypeGPU),
			Annotations: map[string]string{
				consts.ResourceAnnotationKeyResourceIdentifier: "",
			},
		})
	}

	return &pluginapi.GetTopologyAwareAllocatableResourcesResponse{
		AllocatableResources: map[string]*pluginapi.AllocatableTopologyAwareResource{
			p.ResourceName(): {
				IsNodeResource:                       true,
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
func (p *StaticPolicy) GetResourcePluginOptions(context.Context,
	*pluginapi.Empty,
) (*pluginapi.ResourcePluginOptions, error) {
	return &pluginapi.ResourcePluginOptions{
		PreStartRequired: false,
		// TODO: add topology aware
		WithTopologyAlignment: true,
		NeedReconcile:         true,
	}, nil
}

// Allocate is called during pod admit so that the resource
// plugin can allocate corresponding resource for the container
// according to resource request
func (p *StaticPolicy) Allocate(_ context.Context,
	req *pluginapi.ResourceRequest,
) (resp *pluginapi.ResourceAllocationResponse, err error) {
	if req == nil {
		return nil, fmt.Errorf("GetTopologyHints got nil req")
	}

	existReallocAnno, isReallocation := util.IsReallocation(req.Annotations)

	qosLevel, err := util.GetKatalystQoSLevelFromResourceReq(p.qosConfig, req, p.podAnnotationKeptKeys, p.podLabelKeptKeys)
	if err != nil {
		err = fmt.Errorf("GetKatalystQoSLevelFromResourceReq for pod: %s/%s, container: %s failed with error: %v",
			req.PodNamespace, req.PodName, req.ContainerName, err)
		general.Errorf("%s", err.Error())
		return nil, err
	}

	reqInt, _, err := util.GetQuantityFromResourceReq(req)
	if err != nil {
		return nil, fmt.Errorf("getReqQuantityFromResourceReq failed with error: %v", err)
	}

	general.InfoS("called",
		"podNamespace", req.PodNamespace,
		"podName", req.PodName,
		"containerName", req.ContainerName,
		"qosLevel", qosLevel,
		"reqAnnotations", req.Annotations,
		"gpuMemory", reqInt)

	p.Lock()
	defer func() {
		if err := p.state.StoreState(); err != nil {
			general.ErrorS(err, "store state failed", "podName", req.PodName, "containerName", req.ContainerName)
		}
		p.Unlock()
		if err != nil {
			metricTags := []metrics.MetricTag{
				{Key: "error_message", Val: metric.MetricTagValueFormat(err)},
			}
			if existReallocAnno {
				metricTags = append(metricTags, metrics.MetricTag{Key: "reallocation", Val: isReallocation})
			}
			_ = p.emitter.StoreInt64(util.MetricNameAllocateFailed, 1, metrics.MetricTypeNameRaw, metricTags...)
		}
	}()

	emptyResponse := &pluginapi.ResourceAllocationResponse{
		PodUid:         req.PodUid,
		PodNamespace:   req.PodNamespace,
		PodName:        req.PodName,
		ContainerName:  req.ContainerName,
		ContainerType:  req.ContainerType,
		ContainerIndex: req.ContainerIndex,
		PodRole:        req.PodRole,
		PodType:        req.PodType,
		ResourceName:   p.ResourceName(),
		Labels:         general.DeepCopyMap(req.Labels),
		Annotations:    general.DeepCopyMap(req.Annotations),
	}

	// currently, not to deal with init containers
	if req.ContainerType == pluginapi.ContainerType_INIT {
		return emptyResponse, nil
	} else if req.ContainerType == pluginapi.ContainerType_SIDECAR {
		// not to deal with sidecars, and return a trivial allocationResult to avoid re-allocating
		return p.packAllocationResponse(req, &state.AllocationInfo{}, nil)
	}

	allocationInfo := p.state.GetAllocationInfo(req.PodUid, req.ContainerName)
	if allocationInfo != nil {
		resp, packErr := p.packAllocationResponse(req, allocationInfo, nil)
		if packErr != nil {
			general.Errorf("pod: %s/%s, container: %s packAllocationResponse failed with error: %v",
				req.PodNamespace, req.PodName, req.ContainerName, packErr)
			return nil, fmt.Errorf("packAllocationResponse failed with error: %v", packErr)
		}
		return resp, nil
	}

	newAllocation := &state.AllocationInfo{
		AllocationMeta: commonstate.GenerateGenericContainerAllocationMeta(req, commonstate.EmptyOwnerPoolName, qosLevel),
		AllocatedAllocation: state.GPUAllocation{
			GPUMemoryQuantity: uint64(reqInt),
		},
	}

	p.state.SetAllocationInfo(req.PodUid, req.ContainerName, newAllocation, false)

	machineState, stateErr := state.GenerateMachineStateFromPodEntries(p.qrmConfig, p.state.GetPodEntries(), p.gpuTopologyProvider)
	if stateErr != nil {
		general.ErrorS(stateErr, "GenerateMachineStateFromPodEntries failed",
			"podNamespace", req.PodNamespace,
			"podName", req.PodName,
			"containerName", req.ContainerName,
			"gpuMemory", reqInt)
		return nil, fmt.Errorf("GenerateMachineStateFromPodEntries failed with error: %v", stateErr)
	}

	// update state cache
	p.state.SetMachineState(machineState, true)

	return p.packAllocationResponse(req, newAllocation, nil)
}

// AllocateForPod is called during pod admit so that the resource
// plugin can allocate corresponding resource for the pod
// according to resource request
func (p *StaticPolicy) AllocateForPod(_ context.Context,
	req *pluginapi.PodResourceRequest,
) (resp *pluginapi.PodResourceAllocationResponse, err error) {
	return nil, util.ErrNotImplemented
}

// PreStartContainer is called, if indicated by resource plugin during registration phase,
// before each container start. Resource plugin can run resource specific operations
// such as resetting the resource before making resources available to the container
func (p *StaticPolicy) PreStartContainer(context.Context,
	*pluginapi.PreStartContainerRequest,
) (*pluginapi.PreStartContainerResponse, error) {
	return &pluginapi.PreStartContainerResponse{}, nil
}

func (p *StaticPolicy) removePod(podUID string) error {
	// update state cache
	podEntries := p.state.GetPodEntries()
	delete(podEntries, podUID)

	machineState, err := state.GenerateMachineStateFromPodEntries(p.qrmConfig, podEntries, p.gpuTopologyProvider)
	if err != nil {
		general.Errorf("pod: %s, GenerateMachineStateFromPodEntries failed with error: %v", podUID, err)
		return fmt.Errorf("calculate machineState by updated pod entries failed with error: %v", err)
	}

	p.state.SetPodEntries(podEntries, false)
	p.state.SetMachineState(machineState, false)

	err = p.state.StoreState()
	if err != nil {
		general.Errorf("store state failed with error: %v", err)
		return err
	}
	return nil
}

// clearResidualState is used to clean residual pods in local state
func (p *StaticPolicy) clearResidualState(_ *config.Configuration,
	_ interface{},
	_ *dynamicconfig.DynamicAgentConfiguration,
	_ metrics.MetricEmitter,
	_ *metaserver.MetaServer,
) {
	general.Infof("exec")
	var (
		err     error
		podList []*v1.Pod
	)
	residualSet := make(map[string]bool)

	defer func() {
		_ = general.UpdateHealthzStateByError(gpuconsts.ClearResidualState, err)
	}()

	if p.metaServer == nil {
		general.Errorf("nil metaServer")
		return
	}

	ctx := context.Background()
	podList, err = p.metaServer.GetPodList(ctx, nil)
	if err != nil {
		general.Errorf("get pod list failed: %v", err)
		return
	}

	podSet := sets.NewString()
	for _, pod := range podList {
		podSet.Insert(fmt.Sprintf("%v", pod.UID))
	}

	p.Lock()
	defer p.Unlock()

	podEntries := p.state.GetPodEntries()
	for podUID := range podEntries {
		if !podSet.Has(podUID) {
			residualSet[podUID] = true
			p.residualHitMap[podUID] += 1
			general.Infof("found pod: %s with state but doesn't show up in pod watcher, hit count: %d", podUID, p.residualHitMap[podUID])
		}
	}

	podsToDelete := sets.NewString()
	for podUID, hitCount := range p.residualHitMap {
		if !residualSet[podUID] {
			general.Infof("already found pod: %s in pod watcher or its state is cleared, delete it from residualHitMap", podUID)
			delete(p.residualHitMap, podUID)
			continue
		}

		if time.Duration(hitCount)*gpuconsts.StateCheckPeriod >= gpuconsts.MaxResidualTime {
			podsToDelete.Insert(podUID)
		}
	}

	if podsToDelete.Len() > 0 {
		for {
			podUID, found := podsToDelete.PopAny()
			if !found {
				break
			}

			general.Infof("clear residual pod: %s in state", podUID)
			delete(podEntries, podUID)
		}

		machineState, err := state.GenerateMachineStateFromPodEntries(p.qrmConfig, podEntries, p.gpuTopologyProvider)
		if err != nil {
			general.Errorf("GenerateMachineStateFromPodEntries failed with error: %v", err)
			return
		}

		p.state.SetPodEntries(podEntries, false)
		p.state.SetMachineState(machineState, false)

		err = p.state.StoreState()
		if err != nil {
			general.Errorf("store state failed: %v", err)
			return
		}
	}
}

// packAllocationResponse fills pluginapi.ResourceAllocationResponse with information from AllocationInfo and pluginapi.ResourceRequest
func (p *StaticPolicy) packAllocationResponse(
	req *pluginapi.ResourceRequest,
	allocationInfo *state.AllocationInfo,
	resourceAllocationAnnotations map[string]string,
) (*pluginapi.ResourceAllocationResponse, error) {
	if allocationInfo == nil {
		return nil, fmt.Errorf("packAllocationResponse got nil allocationInfo")
	} else if req == nil {
		return nil, fmt.Errorf("packAllocationResponse got nil request")
	}

	return &pluginapi.ResourceAllocationResponse{
		PodUid:         req.PodUid,
		PodNamespace:   req.PodNamespace,
		PodName:        req.PodName,
		ContainerName:  req.ContainerName,
		ContainerType:  req.ContainerType,
		ContainerIndex: req.ContainerIndex,
		PodRole:        req.PodRole,
		PodType:        req.PodType,
		ResourceName:   req.ResourceName,
		AllocationResult: &pluginapi.ResourceAllocation{
			ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{
				p.ResourceName(): {
					IsNodeResource:    true,
					IsScalarResource:  true, // to avoid re-allocating
					AllocatedQuantity: float64(allocationInfo.AllocatedAllocation.GPUMemoryQuantity),
					Annotations:       resourceAllocationAnnotations,
					ResourceHints: &pluginapi.ListOfTopologyHints{
						Hints: []*pluginapi.TopologyHint{
							req.Hint,
						},
					},
				},
			},
		},
		Labels:      general.DeepCopyMap(req.Labels),
		Annotations: general.DeepCopyMap(req.Annotations),
	}, nil
}
