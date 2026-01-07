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

	"k8s.io/apimachinery/pkg/util/wait"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"
	"k8s.io/kubernetes/pkg/kubelet/cri/remote"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/agent"
	appqrm "github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/agent/qrm"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/sriov/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/sriov/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/sriov/util"
	qrmutil "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util"
	"github.com/kubewharf/katalyst-core/pkg/agent/utilcomponent/periodicalhandler"
	"github.com/kubewharf/katalyst-core/pkg/config"
	qrmconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/qrm"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	"github.com/kubewharf/katalyst-core/pkg/util/metric"

	cpudynamicpolicy "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy"
)

type DynamicPolicy struct {
	sync.RWMutex

	name            string
	started         bool
	emitter         metrics.MetricEmitter
	metaServer      *metaserver.MetaServer
	agentCtx        *agent.GenericContext
	state           state.State
	stateReconciler *util.StateReconciler

	qosConfig             *generic.QoSConfiguration
	podAnnotationKeptKeys []string
	podLabelKeptKeys      []string

	qrmconfig.SriovAllocationConfig
	qrmconfig.SriovDynamicPolicyConfig
}

func NewDynamicPolicy(agentCtx *agent.GenericContext, conf *config.Configuration,
	_ interface{}, agentName string,
) (bool, agent.Component, error) {
	wrappedEmitter := agentCtx.EmitterPool.GetDefaultMetricsEmitter().WithTags(agentName, metrics.MetricTag{
		Key: qrmutil.QRMPluginPolicyTagName,
		Val: consts.SriovResourcePluginPolicyNameDynamic,
	})

	stateImpl, err := state.NewCheckpointState(conf.QRMPluginsConfiguration, conf.MachineInfoConfiguration,
		conf.StateDirectoryConfiguration, consts.SriovPluginStateFileName, consts.SriovResourcePluginPolicyNameStatic,
		conf.SkipSriovStateCorruption, wrappedEmitter)
	if err != nil {
		return false, agent.ComponentStub{}, fmt.Errorf("NewCheckpointState failed with error: %v", err)
	}

	runtimeClient, err := remote.NewRemoteRuntimeService(conf.BaseConfiguration.RuntimeEndpoint, 2*time.Minute)
	if err != nil {
		return false, nil, fmt.Errorf("create remote runtime service failed %s", err)
	}

	hostNetworkBonding, err := machine.IsHostNetworkBonding()
	if !hostNetworkBonding {
		general.InfoS("skip running sriov dynamic policy plugin on host without bonding network")
		return false, agent.ComponentStub{}, nil
	}

	policy := &DynamicPolicy{
		name:       fmt.Sprintf("%s_%s", agentName, consts.SriovResourcePluginPolicyNameDynamic),
		emitter:    wrappedEmitter,
		agentCtx:   agentCtx,
		metaServer: agentCtx.MetaServer,
		state:      stateImpl,
		stateReconciler: util.NewStateReconciler(stateImpl, conf.SriovAllocationConfig.PCIAnnotation,
			agentCtx.Client.KubeClient, runtimeClient),
		qosConfig:             conf.QoSConfiguration,
		podAnnotationKeptKeys: conf.PodAnnotationKeptKeys,
		podLabelKeptKeys:      conf.PodLabelKeptKeys,
	}

	if err := cpudynamicpolicy.AccompanyResource.RegisterPlugin(string(apiconsts.ResourceSriovNic), policy); err != nil {
		return false, nil, fmt.Errorf("RegisterPlugin failed with error: %v", err)
	}

	return true, policy, nil
}

// Run runs this plugin
func (p *DynamicPolicy) Run(ctx context.Context) {
	general.Infof("called")

	go wait.Until(func() {
		_ = p.emitter.StoreInt64(qrmutil.MetricNameHeartBeat, 1, metrics.MetricTypeNameRaw)
	}, time.Second*30, ctx.Done())

	if err := periodicalhandler.RegisterPeriodicalHandlerWithHealthz(consts.HealthzReconcileState, general.HealthzCheckStateNotReady,
		appqrm.QRMSriovPluginPeriodicalHandlerGroupName, p.stateReconciler.Reconcile,
		consts.ReconcileStatePeriod, consts.ReconcileStateTolerationTimes); err != nil {
		general.Errorf("start %v failed, err: %v", consts.ReconcileState, err)
	}

	go wait.Until(func() {
		periodicalhandler.ReadyToStartHandlersByGroup(appqrm.QRMSriovPluginPeriodicalHandlerGroupName)
	}, 5*time.Second, ctx.Done())

	<-ctx.Done()
	general.Infof("stopped")

	return
}

// AugmentTopologyHints augments hints of accompany resources
func (p *DynamicPolicy) GetAccompanyResourceTopologyHints(req *pluginapi.ResourceRequest, hints *pluginapi.ListOfTopologyHints) (err error) {
	if req == nil {
		return fmt.Errorf("GetAccompanyResourceTopologyHints got nil req")
	}

	general.InfoS("called", "request", req, "hints", hints)

	p.RLock()
	defer func() {
		p.RUnlock()
		if err != nil {
			general.ErrorS(err, "GetAccompanyResourceTopologyHints failed", "request", req, "hints", hints)
			_ = p.emitter.StoreInt64(qrmutil.MetricNameGetAccompanyResourceTopologyHintsFailed, 1, metrics.MetricTypeNameRaw,
				metrics.MetricTag{Key: "error_message", Val: metric.MetricTagValueFormat(err)})
		}
	}()

	podEntries := p.state.GetPodEntries()
	machineState := p.state.GetMachineState()

	var candidates state.VFState

	// reuse allocation info allocated by same pod and container
	_, exists := podEntries[req.PodUid]
	if exists {
		return nil
	}

	request, _, err := qrmutil.GetPodAggregatedRequestResource(req)
	if err != nil {
		return fmt.Errorf("GetPodAggregatedRequestResource failed with error: %v", err)
	}

	queueCount, failOnExhaustion := getVFQueueCountAndExhaustionStrategy(p.SriovDynamicPolicyConfig, request)
	if queueCount < 0 {
		return nil
	}

	filters := []state.VFFilter{
		state.FilterByPodAllocated(podEntries, false),
		state.FilterByQueueCount(queueCount, math.MaxInt),
	}
	candidates = machineState.Filter(filters...)

	if len(candidates) == 0 {
		if failOnExhaustion {
			return fmt.Errorf("no available VFs")
		}
		return nil
	}

	numaSet := machine.CPUSet{}
	for _, vf := range candidates {
		numaSet.Add(vf.NumaNode)
	}
	socketSet := p.agentCtx.CPUDetails.SocketsInNUMANodes(numaSet.ToSliceInt()...)
	numaNodesInSocketSet := p.agentCtx.CPUDetails.NUMANodesInSockets(socketSet.ToSliceInt()...)

	augmentedHints := make([]*pluginapi.TopologyHint, 0, len(hints.Hints))

	for _, hint := range hints.Hints {
		if hint == nil {
			continue
		}
		hintNumaSet, _ := machine.NewCPUSetUint64(hint.Nodes...)
		augmentedHints = append(augmentedHints, &pluginapi.TopologyHint{
			Nodes:     hint.Nodes,
			Preferred: hint.Preferred && hintNumaSet.IsSubsetOf(numaNodesInSocketSet),
		})
	}

	general.InfoS("finished", "request", req, "originHints", hints, "augmentedHints", augmentedHints)

	if !p.DryRun {
		hints.Hints = augmentedHints
	}

	return nil
}

// AugmentAllocationResult augments allocation result of accompany resources
func (p *DynamicPolicy) AllocateAccompanyResource(req *pluginapi.ResourceRequest, resp *pluginapi.ResourceAllocationResponse) (err error) {
	if req == nil {
		return fmt.Errorf("Allocate got nil req")
	}

	general.InfoS("called", "request", req)

	qosLevel, err := qrmutil.GetKatalystQoSLevelFromResourceReq(p.qosConfig, req, p.podAnnotationKeptKeys, p.podLabelKeptKeys)
	if err != nil {
		err = fmt.Errorf("GetKatalystQoSLevelFromResourceReq for pod: %s/%s, container: %s failed with error: %v",
			req.PodNamespace, req.PodName, req.ContainerName, err)
		general.Errorf("%s", err.Error())
		return err
	}

	p.Lock()
	defer func() {
		p.Unlock()
		if err != nil {
			general.ErrorS(err, "AllocateAccompanyResource failed", "request", req)
			_ = p.emitter.StoreInt64(qrmutil.MetricNameAllocateAccompanyResourceFailed, 1, metrics.MetricTypeNameRaw,
				metrics.MetricTag{Key: "error_message", Val: metric.MetricTagValueFormat(err)})
		}
	}()

	// reuse allocation info allocated by same pod and container
	allocationInfo := p.state.GetAllocationInfo(req.PodUid, req.ContainerName)
	if allocationInfo != nil {
		if p.DryRun {
			return nil
		}
		return addAllocationInfoToResponse(p.SriovAllocationConfig, allocationInfo, resp)
	}

	// get request quantity of main resource: cpu
	request, _, err := qrmutil.GetPodAggregatedRequestResource(req)
	if err != nil {
		return fmt.Errorf("GetPodAggregatedRequestResource failed with error: %v", err)
	}

	queueCount, failOnExhaustion := getVFQueueCountAndExhaustionStrategy(p.SriovDynamicPolicyConfig, request)
	if queueCount < 0 {
		return nil
	}

	hintNUMASet, err := machine.NewCPUSetUint64(req.Hint.Nodes...)
	if err != nil {
		return fmt.Errorf("failed to parse hint numa nodes: %v", err)
	}

	podEntries := p.state.GetPodEntries()
	machineState := p.state.GetMachineState()
	candidates := machineState.Filter(
		state.FilterByPodAllocated(podEntries, false),
		state.FilterByNumaSet(hintNUMASet),
		state.FilterByRDMA(true),
		state.FilterByQueueCount(queueCount, queueCount),
	)
	if len(candidates) == 0 {
		if failOnExhaustion {
			return fmt.Errorf("no available VFs")
		}
		return nil
	}

	candidates.SortByIndex()
	allocationInfo = &state.AllocationInfo{
		AllocationMeta: commonstate.GenerateGenericContainerAllocationMeta(req,
			commonstate.EmptyOwnerPoolName, qosLevel),
		// return the VF with the lowest index
		VFInfo: candidates[0],
	}

	general.InfoS("augment allocation result", "request", req, "allocationInfo", allocationInfo)

	p.state.SetAllocationInfo(req.PodUid, req.ContainerName, allocationInfo, true)

	if p.DryRun {
		return nil
	}

	// try to update sriov vf result annotation, if failed, leave it to state_reconciler to update
	if err := util.UpdateSriovVFResultAnnotation(p.agentCtx.Client.KubeClient, allocationInfo); err != nil {
		general.ErrorS(err, "UpdateSriovVFResultAnnotation failed")
	}

	return nil
}

func (p *DynamicPolicy) ReleaseAccompanyResource(req *pluginapi.RemovePodRequest) (err error) {
	if req == nil {
		return fmt.Errorf("ReleaseAccompanyResource got nil req")
	}

	p.Lock()
	defer func() {
		p.Unlock()
		if err != nil {
			general.ErrorS(err, "ReleaseAccompanyResource failed", "podUid", req.PodUid)
			_ = p.emitter.StoreInt64(qrmutil.MetricNameReleaseAccompanyResourceFailed, 1, metrics.MetricTypeNameRaw,
				metrics.MetricTag{Key: "error_message", Val: metric.MetricTagValueFormat(err)})
		}
	}()

	p.state.Delete(req.PodUid, true)

	return nil
}
