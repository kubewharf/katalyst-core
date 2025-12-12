/*
copyright 2022 The Katalyst Authors.

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

	"k8s.io/apimachinery/pkg/util/wait"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	apinode "github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/plugins/skeleton"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/agent"
	appqrm "github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/agent/qrm"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	sriovconsts "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/sriov/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/sriov/state"
	sriovutil "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/sriov/util"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util"
	"github.com/kubewharf/katalyst-core/pkg/agent/utilcomponent/periodicalhandler"
	"github.com/kubewharf/katalyst-core/pkg/config"
	qrmconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/qrm"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	"github.com/kubewharf/katalyst-core/pkg/util/metric"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
)

type StaticPolicy struct {
	sync.RWMutex

	name       string
	stopCh     chan struct{}
	started    bool
	emitter    metrics.MetricEmitter
	metaServer *metaserver.MetaServer
	agentCtx   *agent.GenericContext
	state      state.State

	qosConfig             *generic.QoSConfiguration
	podAnnotationKeptKeys []string
	podLabelKeptKeys      []string

	qrmconfig.SriovAllocationConfig
	qrmconfig.SriovStaticPolicyConfig
}

func NewStaticPolicy(agentCtx *agent.GenericContext, conf *config.Configuration,
	_ interface{}, agentName string,
) (bool, agent.Component, error) {

	wrappedEmitter := agentCtx.EmitterPool.GetDefaultMetricsEmitter().WithTags(agentName, metrics.MetricTag{
		Key: util.QRMPluginPolicyTagName,
		Val: sriovconsts.SriovResourcePluginPolicyNameStatic,
	})

	stateImpl, err := state.NewCheckpointState(conf.QRMPluginsConfiguration, conf.StateDirectoryConfiguration,
		sriovconsts.SriovPluginStateFileName, sriovconsts.SriovResourcePluginPolicyNameStatic,
		agentCtx.MachineInfo, conf.SkipSriovStateCorruption, wrappedEmitter)
	if err != nil {
		return false, agent.ComponentStub{}, fmt.Errorf("NewCheckpointState failed with error: %v", err)
	}

	policy := &StaticPolicy{
		name:                    fmt.Sprintf("%s_%s", agentName, sriovconsts.SriovResourcePluginPolicyNameStatic),
		emitter:                 wrappedEmitter,
		agentCtx:                agentCtx,
		metaServer:              agentCtx.MetaServer,
		state:                   stateImpl,
		qosConfig:               conf.QoSConfiguration,
		podAnnotationKeptKeys:   conf.PodAnnotationKeptKeys,
		podLabelKeptKeys:        conf.PodLabelKeptKeys,
		SriovAllocationConfig:   conf.SriovAllocationConfig,
		SriovStaticPolicyConfig: conf.SriovStaticPolicyConfig,
	}

	pluginWrapper, err := skeleton.NewRegistrationPluginWrapper(policy, conf.QRMPluginSocketDirs, nil)
	if err != nil {
		return false, agent.ComponentStub{}, fmt.Errorf("static policy new plugin wrapper failed with error: %v", err)
	}

	return true, &agent.PluginWrapper{GenericPlugin: pluginWrapper}, nil
}

// Name returns the name of the policy.
func (p *StaticPolicy) Name() string {
	return p.name
}

// ResourceName returns resource names managed by this plugin
func (p *StaticPolicy) ResourceName() string {
	return string(apiconsts.ResourceSriovNic)
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

	go wait.Until(func() {
		periodicalhandler.ReadyToStartHandlersByGroup(appqrm.QRMNetworkPluginPeriodicalHandlerGroupName)
	}, 5*time.Second, p.stopCh)

	go func(stopCh <-chan struct{}) {
		<-stopCh
	}(p.stopCh)

	return nil
}

// GetTopologyHints returns hints of corresponding resources
func (p *StaticPolicy) GetTopologyHints(_ context.Context,
	req *pluginapi.ResourceRequest,
) (resp *pluginapi.ResourceHintsResponse, err error) {
	if req == nil {
		return nil, fmt.Errorf("GetTopologyHints got nil req")
	}

	if err := sriovutil.ValidateRequestQuantity(req); err != nil {
		return nil, fmt.Errorf("ValidateRequestQuantity failed with error: %v", err)
	}

	general.InfoS("called",
		"podNamespace", req.PodNamespace,
		"podName", req.PodName,
		"containerName", req.ContainerName,
		"resourceRequests", req.ResourceRequests,
		"reqAnnotations", req.Annotations)

	p.RLock()
	defer func() {
		p.RUnlock()
		if err != nil {
			general.ErrorS(err, "GetTopologyHints failed",
				"podNamespace", req.PodNamespace,
				"podName", req.PodName,
				"containerName", req.ContainerName,
				"resourceRequests", req.ResourceRequests,
				"reqAnnotations", req.Annotations)
			_ = p.emitter.StoreInt64(util.MetricNameGetTopologyHintsFailed, 1, metrics.MetricTypeNameRaw,
				metrics.MetricTag{Key: "error_message", Val: metric.MetricTagValueFormat(err)})
		}
	}()

	podEntries := p.state.GetPodEntries()
	machineState := p.state.GetMachineState()

	var candidates state.VFState

	// reuse allocation info allocated by same pod and container
	containerEntries, exists := podEntries[req.PodNamespace]
	if exists {
		allocationInfo := containerEntries[req.ContainerName]
		if allocationInfo == nil {
			return nil, fmt.Errorf("not support allocate more than 1 vf in single pod")
		}
		candidates = machineState.Filter(state.FilterByName(allocationInfo.VFInfo.Name))
	} else {
		candidates = machineState.Filter(
			podEntries.FilterByAllocated(false),
			// todo: only filter in bonding
			state.FilterByQueueCount(p.MaxBondingVFQueueCount, p.MaxBondingVFQueueCount),
			state.FilterByIbDevice(true),
		)
	}

	if len(candidates) == 0 {
		return nil, fmt.Errorf("no available VFs")
	}

	numaSet := machine.CPUSet{}
	for _, vf := range candidates {
		numaSet.Add(vf.NumaNode)
	}
	socketSet := p.agentCtx.CPUDetails.SocketsInNUMANodes(numaSet.ToSliceInt()...)
	numaNodesInSocketSet := p.agentCtx.CPUDetails.NUMANodesInSockets(socketSet.ToSliceInt()...).ToSliceUInt64()

	topologyHints := map[string]*pluginapi.ListOfTopologyHints{
		p.ResourceName(): {
			Hints: []*pluginapi.TopologyHint{
				{
					Nodes: numaNodesInSocketSet,
					// as well as there has available vfs, the preferred is true
					Preferred: true,
				},
			},
		},
	}

	return util.PackResourceHintsResponse(req, p.ResourceName(), topologyHints)
}

// GetPodTopologyHints returns hints of corresponding resources for pod
func (p *StaticPolicy) GetPodTopologyHints(_ context.Context,
	req *pluginapi.PodResourceRequest,
) (resp *pluginapi.PodResourceHintsResponse, err error) {
	return nil, util.ErrNotImplemented
}

func (p *StaticPolicy) RemovePod(_ context.Context,
	req *pluginapi.RemovePodRequest,
) (resp *pluginapi.RemovePodResponse, err error) {
	if req == nil {
		return nil, fmt.Errorf("RemovePod got nil req")
	}

	p.Lock()
	defer func() {
		p.Unlock()
		if err != nil {
			general.ErrorS(err, "RemovePod failed", "podUid", req.PodUid)
			_ = p.emitter.StoreInt64(util.MetricNameRemovePodFailed, 1, metrics.MetricTypeNameRaw,
				metrics.MetricTag{Key: "error_message", Val: metric.MetricTagValueFormat(err)})
		}
	}()

	podEntries := p.state.GetPodEntries()
	delete(podEntries, req.PodUid)
	p.state.SetPodEntries(podEntries, true)

	return &pluginapi.RemovePodResponse{}, nil

}

// GetResourcesAllocation returns allocation results of corresponding resources
func (p *StaticPolicy) GetResourcesAllocation(_ context.Context,
	_ *pluginapi.GetResourcesAllocationRequest,
) (*pluginapi.GetResourcesAllocationResponse, error) {
	return &pluginapi.GetResourcesAllocationResponse{}, nil
}

// GetTopologyAwareResources returns allocation results of corresponding resources as topology aware format
func (p *StaticPolicy) GetTopologyAwareResources(_ context.Context,
	req *pluginapi.GetTopologyAwareResourcesRequest,
) (*pluginapi.GetTopologyAwareResourcesResponse, error) {
	// todo: implement me
	panic("not implemented")
}

// GetTopologyAwareAllocatableResources returns corresponding allocatable resources as topology aware format
func (p *StaticPolicy) GetTopologyAwareAllocatableResources(_ context.Context,
	_ *pluginapi.GetTopologyAwareAllocatableResourcesRequest,
) (*pluginapi.GetTopologyAwareAllocatableResourcesResponse, error) {
	p.RLock()
	defer p.RUnlock()

	var (
		machineState = p.state.GetMachineState()

		aggregatedQuantity float64

		topologyAwareQuantityList = make([]*pluginapi.TopologyAwareQuantity, 0, len(machineState))
	)

	// todo: filter according to whether bonding
	availableVFs := machineState.Filter(state.FilterByIbDevice(true))

	for _, vfInfo := range availableVFs {
		aggregatedQuantity += 1
		topologyAwareQuantityList = append(topologyAwareQuantityList, &pluginapi.TopologyAwareQuantity{
			ResourceValue: 1,
			Node:          p.agentCtx.CPUDetails.SocketsInNUMANodes(vfInfo.NumaNode).ToSliceUInt64()[0],
			Name:          vfInfo.Name,
			Type:          string(apinode.TopologyTypeNIC),
			TopologyLevel: pluginapi.TopologyLevel_SOCKET,
		})
	}

	return &pluginapi.GetTopologyAwareAllocatableResourcesResponse{
		AllocatableResources: map[string]*pluginapi.AllocatableTopologyAwareResource{
			p.ResourceName(): {
				IsNodeResource:                       true,
				IsScalarResource:                     true,
				AggregatedAllocatableQuantity:        aggregatedQuantity,
				TopologyAwareAllocatableQuantityList: topologyAwareQuantityList,
				AggregatedCapacityQuantity:           aggregatedQuantity,
				TopologyAwareCapacityQuantityList:    topologyAwareQuantityList,
			},
		},
	}, nil

}

// GetResourcePluginOptions returns options to be communicated with Resource Manager
func (p *StaticPolicy) GetResourcePluginOptions(context.Context,
	*pluginapi.Empty,
) (*pluginapi.ResourcePluginOptions, error) {
	return &pluginapi.ResourcePluginOptions{
		PreStartRequired:      false,
		WithTopologyAlignment: true,
		NeedReconcile:         false,
	}, nil
}

// Allocate is called during pod admit so that the resource
// plugin can allocate corresponding resource for the container
// according to resource request
func (p *StaticPolicy) Allocate(ctx context.Context,
	req *pluginapi.ResourceRequest,
) (resp *pluginapi.ResourceAllocationResponse, err error) {
	if req == nil {
		return nil, fmt.Errorf("Allocate got nil req")
	}

	if err := sriovutil.ValidateRequestQuantity(req); err != nil {
		return nil, fmt.Errorf("ValidateRequestQuantity failed with error: %v", err)
	}

	qosLevel, err := util.GetKatalystQoSLevelFromResourceReq(p.qosConfig, req, p.podAnnotationKeptKeys, p.podLabelKeptKeys)
	if err != nil {
		err = fmt.Errorf("GetKatalystQoSLevelFromResourceReq for pod: %s/%s, container: %s failed with error: %v",
			req.PodNamespace, req.PodName, req.ContainerName, err)
		general.Errorf("%s", err.Error())
		return nil, err
	}

	general.InfoS("called",
		"podNamespace", req.PodNamespace,
		"podName", req.PodName,
		"containerName", req.ContainerName,
		"resourceRequests", req.ResourceRequests,
		"reqAnnotations", req.Annotations)

	p.Lock()
	defer func() {
		p.Unlock()
		if err != nil {
			general.ErrorS(err, "Allocate failed",
				"podNamespace", req.PodNamespace,
				"podName", req.PodName,
				"containerName", req.ContainerName,
				"resourceRequests", req.ResourceRequests,
				"reqAnnotations", req.Annotations)
			_ = p.emitter.StoreInt64(util.MetricNameAllocateFailed, 1, metrics.MetricTypeNameRaw,
				metrics.MetricTag{Key: "error_message", Val: metric.MetricTagValueFormat(err)})
		}
	}()

	// reuse allocation info allocated by same pod and container
	podEntries := p.state.GetPodEntries()
	containerEntries, exists := podEntries[req.PodNamespace]
	if exists {
		allocationInfo := containerEntries[req.ContainerName]
		if allocationInfo == nil {
			return nil, fmt.Errorf("not support allocate more than 1 vf in single pod")
		}
		return sriovutil.PackAllocationResponse(p.SriovAllocationConfig, req, allocationInfo)
	}

	hintNUMASet, err := machine.NewCPUSetUint64(req.Hint.Nodes...)
	if err != nil {
		return nil, fmt.Errorf("failed to parse hint numa nodes: %v", err)
	}

	machineState := p.state.GetMachineState()
	candidates := machineState.Filter(
		podEntries.FilterByAllocated(false),
		state.FilterByNumaID(hintNUMASet),
		state.FilterByIbDevice(true),
	)
	if len(candidates) == 0 {
		return nil, fmt.Errorf("no available VFs")
	}
	candidates.SortByIndex()

	allocationInfo := &state.AllocationInfo{
		AllocationMeta: commonstate.GenerateGenericContainerAllocationMeta(req,
			commonstate.EmptyOwnerPoolName, qosLevel),
		// return the VF with the lowest index
		VFInfo: candidates[0],
	}

	podEntries[req.PodUid] = map[string]*state.AllocationInfo{
		req.ContainerName: allocationInfo,
	}
	p.state.SetPodEntries(podEntries, true)

	return sriovutil.PackAllocationResponse(p.SriovAllocationConfig, req, allocationInfo)
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
