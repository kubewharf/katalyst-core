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

package policy

import (
	"context"
	"fmt"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	apinode "github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/plugins/skeleton"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/agent"
	appqrm "github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/agent/qrm"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/sriov/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/sriov/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/sriov/utils"
	qrmutil "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util"
	"github.com/kubewharf/katalyst-core/pkg/agent/utilcomponent/periodicalhandler"
	"github.com/kubewharf/katalyst-core/pkg/config"
	qrmconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/qrm"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	"github.com/kubewharf/katalyst-core/pkg/util/metric"
)

type StaticPolicy struct {
	sync.RWMutex
	*basePolicy

	name    string
	stopCh  chan struct{}
	started bool
	emitter metrics.MetricEmitter

	policyConfig qrmconfig.SriovStaticPolicyConfig
}

func NewStaticPolicy(agentCtx *agent.GenericContext, conf *config.Configuration,
	_ interface{}, agentName string,
) (bool, agent.Component, error) {

	wrappedEmitter := agentCtx.EmitterPool.GetDefaultMetricsEmitter().WithTags(agentName, metrics.MetricTag{
		Key: qrmutil.QRMPluginPolicyTagName,
		Val: consts.SriovResourcePluginPolicyNameStatic,
	})

	basePolicy, err := newBasePolicy(agentCtx, conf, wrappedEmitter)
	if err != nil {
		return false, agent.ComponentStub{}, fmt.Errorf("failed to create base policy: %w", err)
	}

	hostNetworkBonding, err := machine.IsHostNetworkBonding()
	if err != nil {
		return false, agent.ComponentStub{}, fmt.Errorf("IsHostNetworkBonding failed with error: %v", err)
	}
	general.Infof("detect host network bonding=%t", hostNetworkBonding)

	staticPolicy := &StaticPolicy{
		name:         fmt.Sprintf("%s_%s", agentName, consts.SriovResourcePluginPolicyNameStatic),
		emitter:      wrappedEmitter,
		basePolicy:   basePolicy,
		policyConfig: conf.SriovStaticPolicyConfig,
	}

	pluginWrapper, err := skeleton.NewRegistrationPluginWrapper(staticPolicy, conf.QRMPluginSocketDirs, nil)
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
	return ResourceName
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

	err = periodicalhandler.RegisterPeriodicalHandlerWithHealthz(consts.HealthzReconcileState, general.HealthzCheckStateNotReady,
		appqrm.QRMSriovPluginPeriodicalHandlerGroupName, p.stateReconciler.Reconcile,
		consts.ReconcileStatePeriod, consts.ReconcileStateTolerationTimes)
	if err != nil {
		return fmt.Errorf("register periodical handler %s failed with error: %v", consts.ReconcileState, err)
	}

	go wait.Until(func() {
		periodicalhandler.ReadyToStartHandlersByGroup(appqrm.QRMSriovPluginPeriodicalHandlerGroupName)
	}, 5*time.Second, p.stopCh)

	go wait.Until(func() {
		_ = p.emitter.StoreInt64(qrmutil.MetricNameHeartBeat, 1, metrics.MetricTypeNameRaw)
	}, time.Second*30, p.stopCh)

	go func(stopCh <-chan struct{}) {
		<-stopCh
	}(p.stopCh)

	return nil
}

// GetTopologyHints returns hints of corresponding resources
func (p *StaticPolicy) GetTopologyHints(_ context.Context,
	req *pluginapi.ResourceRequest,
) (resp *pluginapi.ResourceHintsResponse, err error) {
	if err := p.validateRequestQuantity(req); err != nil {
		return nil, fmt.Errorf("ValidateRequestQuantity failed with error: %v", err)
	}

	general.InfoS("called", "request", req)

	p.RLock()
	defer func() {
		p.RUnlock()
		if err != nil {
			general.ErrorS(err, "GetTopologyHints failed", "request", req)
			_ = p.emitter.StoreInt64(qrmutil.MetricNameGetTopologyHintsFailed, 1, metrics.MetricTypeNameRaw,
				metrics.MetricTag{Key: "error_message", Val: metric.MetricTagValueFormat(err)})
		}
	}()

	podEntries := p.state.GetPodEntries()
	machineState := p.state.GetMachineState()

	var candidates state.VFState

	// reuse allocation info allocated by same pod and container
	containerEntries, exists := podEntries[req.PodUid]
	if exists {
		allocationInfo := containerEntries[req.ContainerName]
		if allocationInfo == nil {
			return nil, fmt.Errorf("not support allocate more than 1 vf in single pod")
		}
		candidates = machineState.Filter(state.FilterByPCIAddr(allocationInfo.VFInfo.PCIAddr))
	} else {
		filters := []state.VFFilter{state.FilterByPodAllocated(podEntries, false), state.FilterByRDMA(true)}
		if p.bondingHostNetwork {
			filters = append(filters, state.FilterByQueueCount(p.policyConfig.MinBondingVFQueueCount, p.policyConfig.MaxBondingVFQueueCount))
		}
		candidates = machineState.Filter(filters...)
	}

	if len(candidates) == 0 {
		return nil, fmt.Errorf("no available VFs")
	}

	numaSet := machine.CPUSet{}
	for _, vf := range candidates {
		numaSet.Add(vf.NumaNode)
	}
	socketSet := p.agentCtx.CPUDetails.SocketsInNUMANodes(numaSet.ToSliceInt()...)

	hints := make([]*pluginapi.TopologyHint, 0, socketSet.Size())
	for _, socket := range socketSet.ToSliceInt() {
		hints = append(hints, &pluginapi.TopologyHint{
			Nodes: p.agentCtx.CPUDetails.NUMANodesInSockets(socket).ToSliceUInt64(),
			// as well as there has available vfs, the preferred is true
			Preferred: true,
		})
	}

	resp, err = qrmutil.PackResourceHintsResponse(req, p.ResourceName(),
		map[string]*pluginapi.ListOfTopologyHints{
			p.ResourceName(): {
				Hints: hints,
			},
		},
	)
	if err != nil {
		return nil, fmt.Errorf("failed to pack resource hints response: %v", err)
	}

	general.InfoS("finished", "response", resp)

	if p.dryRun {
		resp.ResourceHints = nil
	}

	return resp, nil
}

// GetPodTopologyHints returns hints of corresponding resources for pod
func (p *StaticPolicy) GetPodTopologyHints(_ context.Context,
	_ *pluginapi.PodResourceRequest,
) (resp *pluginapi.PodResourceHintsResponse, err error) {
	return nil, qrmutil.ErrNotImplemented
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
			_ = p.emitter.StoreInt64(qrmutil.MetricNameRemovePodFailed, 1, metrics.MetricTypeNameRaw,
				metrics.MetricTag{Key: "error_message", Val: metric.MetricTagValueFormat(err)})
		}
	}()

	if !p.dryRun {
		p.state.Delete(req.PodUid, true)
	}

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
	if req == nil {
		return nil, fmt.Errorf("GetTopologyAwareResources got nil req")
	}

	p.RLock()
	defer p.RUnlock()

	allocationInfo := p.state.GetAllocationInfo(req.PodUid, req.ContainerName)
	if allocationInfo == nil {
		return &pluginapi.GetTopologyAwareResourcesResponse{}, nil
	}

	socket := p.agentCtx.CPUDetails.SocketsInNUMANodes(allocationInfo.VFInfo.NumaNode).ToSliceUInt64()[0]

	resp := &pluginapi.GetTopologyAwareResourcesResponse{
		PodUid:       allocationInfo.PodUid,
		PodName:      allocationInfo.PodName,
		PodNamespace: allocationInfo.PodNamespace,
		ContainerTopologyAwareResources: &pluginapi.ContainerTopologyAwareResources{
			ContainerName: allocationInfo.ContainerName,
			AllocatedResources: map[string]*pluginapi.TopologyAwareResource{
				p.ResourceName(): {
					TopologyAwareQuantityList: []*pluginapi.TopologyAwareQuantity{
						{
							ResourceValue: 1,
							Node:          socket,
							Name:          allocationInfo.VFInfo.RepName,
							Type:          string(apinode.TopologyTypeNIC),
							TopologyLevel: pluginapi.TopologyLevel_SOCKET,
						},
					},
				},
			},
		},
	}

	return resp, nil
}

// GetTopologyAwareAllocatableResources returns corresponding allocatable resources as topology aware format
func (p *StaticPolicy) GetTopologyAwareAllocatableResources(_ context.Context,
	_ *pluginapi.GetTopologyAwareAllocatableResourcesRequest,
) (*pluginapi.GetTopologyAwareAllocatableResourcesResponse, error) {
	p.RLock()
	defer p.RUnlock()

	var (
		machineState = p.state.GetMachineState()
		podEntries   = p.state.GetPodEntries()

		aggregatedQuantity float64

		topologyAwareQuantityList = make([]*pluginapi.TopologyAwareQuantity, 0, len(machineState))
	)

	filters := []state.VFFilter{state.FilterByRDMA(true)}
	if p.bondingHostNetwork {
		filters = append(filters, state.FilterByQueueCount(p.policyConfig.MinBondingVFQueueCount, p.policyConfig.MaxBondingVFQueueCount))
	}
	allocatableVFs := machineState.Filter(filters...)

	vfSet := sets.NewString()
	for _, vfInfo := range allocatableVFs {
		aggregatedQuantity += 1
		topologyAwareQuantityList = append(topologyAwareQuantityList, &pluginapi.TopologyAwareQuantity{
			ResourceValue: 1,
			Node:          p.agentCtx.CPUDetails.SocketsInNUMANodes(vfInfo.NumaNode).ToSliceUInt64()[0],
			Name:          vfInfo.RepName,
			Type:          string(apinode.TopologyTypeNIC),
			TopologyLevel: pluginapi.TopologyLevel_SOCKET,
		})
		vfSet.Insert(vfInfo.RepName)
	}

	// the VFs that are allocated by pod could be filtered by (RDMA) filters, so we need to
	// include them from the podEntries
	for _, containerEntries := range podEntries {
		for _, allocationInfo := range containerEntries {
			if vfSet.Has(allocationInfo.VFInfo.RepName) {
				continue
			}
			aggregatedQuantity += 1
			topologyAwareQuantityList = append(topologyAwareQuantityList, &pluginapi.TopologyAwareQuantity{
				ResourceValue: 1,
				Node:          p.agentCtx.CPUDetails.SocketsInNUMANodes(allocationInfo.VFInfo.NumaNode).ToSliceUInt64()[0],
				Name:          allocationInfo.VFInfo.RepName,
				Type:          string(apinode.TopologyTypeNIC),
				TopologyLevel: pluginapi.TopologyLevel_SOCKET,
			})
			vfSet.Insert(allocationInfo.VFInfo.RepName)
		}
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
func (p *StaticPolicy) Allocate(_ context.Context,
	req *pluginapi.ResourceRequest,
) (resp *pluginapi.ResourceAllocationResponse, err error) {
	if err := p.validateRequestQuantity(req); err != nil {
		return nil, fmt.Errorf("ValidateRequestQuantity failed with error: %v", err)
	}

	qosLevel, err := qrmutil.GetKatalystQoSLevelFromResourceReq(p.qosConfig, req, p.podAnnotationKeptKeys, p.podLabelKeptKeys)
	if err != nil {
		err = fmt.Errorf("GetKatalystQoSLevelFromResourceReq for pod: %s/%s, container: %s failed with error: %v",
			req.PodNamespace, req.PodName, req.ContainerName, err)
		general.Errorf("%s", err.Error())
		return nil, err
	}

	general.InfoS("called", "request", req)

	p.Lock()
	defer func() {
		p.Unlock()
		if err != nil {
			general.ErrorS(err, "Allocate failed", "request", req)
			_ = p.emitter.StoreInt64(qrmutil.MetricNameAllocateFailed, 1, metrics.MetricTypeNameRaw,
				metrics.MetricTag{Key: "error_message", Val: metric.MetricTagValueFormat(err)})
		}
	}()

	// reuse allocation info allocated by same pod and container
	allocationInfo := p.state.GetAllocationInfo(req.PodUid, req.ContainerName)
	if allocationInfo != nil {
		return p.packAllocationResponse(req, allocationInfo)
	}

	hintNUMASet, err := machine.NewCPUSetUint64(req.Hint.Nodes...)
	if err != nil {
		return nil, fmt.Errorf("failed to parse hint numa nodes: %v", err)
	}

	podEntries := p.state.GetPodEntries()
	machineState := p.state.GetMachineState()
	filters := []state.VFFilter{
		state.FilterByPodAllocated(podEntries, false),
		state.FilterByNumaSet(hintNUMASet),
		state.FilterByRDMA(true),
	}
	if p.bondingHostNetwork {
		filters = append(filters, state.FilterByQueueCount(p.policyConfig.MinBondingVFQueueCount, p.policyConfig.MaxBondingVFQueueCount))
	}
	candidates := machineState.Filter(filters...)
	if len(candidates) == 0 {
		return nil, fmt.Errorf("no available VFs")
	}

	candidates.SortByIndex()
	allocationInfo = &state.AllocationInfo{
		AllocationMeta: commonstate.GenerateGenericContainerAllocationMeta(req,
			commonstate.EmptyOwnerPoolName, qosLevel),
		// return the VF with the lowest index
		VFInfo: candidates[0],
	}

	p.state.SetAllocationInfo(req.PodUid, req.ContainerName, allocationInfo, true)

	// try to update sriov vf result annotation, if failed, leave it to state_reconciler to update
	if err := utils.UpdateSriovVFResultAnnotation(p.agentCtx.Client.KubeClient, allocationInfo); err != nil {
		general.ErrorS(err, "UpdateSriovVFResultAnnotation failed")
	}

	resp, err = p.packAllocationResponse(req, allocationInfo)
	if err != nil {
		return nil, fmt.Errorf("failed to pack allocation response: %v", err)
	}

	general.InfoS("finished", "response", resp)

	if p.dryRun {
		resp.AllocationResult = nil
	}

	return resp, nil
}

// AllocateForPod is called during pod admit so that the resource
// plugin can allocate corresponding resource for the pod
// according to resource request
func (p *StaticPolicy) AllocateForPod(_ context.Context,
	_ *pluginapi.PodResourceRequest,
) (resp *pluginapi.PodResourceAllocationResponse, err error) {
	return nil, qrmutil.ErrNotImplemented
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
