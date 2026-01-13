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

package policy

import (
	"context"
	"fmt"
	"math"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/util/wait"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

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

	cpudynamicpolicy "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy"
)

type DynamicPolicy struct {
	sync.RWMutex
	*basePolicy

	name    string
	started bool
	emitter metrics.MetricEmitter

	policyConfig qrmconfig.SriovDynamicPolicyConfig
}

func NewDynamicPolicy(agentCtx *agent.GenericContext, conf *config.Configuration,
	_ interface{}, agentName string,
) (bool, agent.Component, error) {
	wrappedEmitter := agentCtx.EmitterPool.GetDefaultMetricsEmitter().WithTags(agentName, metrics.MetricTag{
		Key: qrmutil.QRMPluginPolicyTagName,
		Val: consts.SriovResourcePluginPolicyNameDynamic,
	}, metrics.MetricTag{
		Key: metricTagDryRunTagKey,
		Val: fmt.Sprintf("%v", conf.SriovDryRun),
	})

	basePolicy, err := newBasePolicy(agentCtx, conf, wrappedEmitter)
	if err != nil {
		return false, agent.ComponentStub{}, fmt.Errorf("failed to create base policy: %w", err)
	}

	dynamicPolicy := &DynamicPolicy{
		name:         fmt.Sprintf("%s_%s", agentName, consts.SriovResourcePluginPolicyNameDynamic),
		emitter:      wrappedEmitter,
		basePolicy:   basePolicy,
		policyConfig: conf.SriovDynamicPolicyConfig,
	}

	if err := cpudynamicpolicy.AccompanyResourceRegistry.RegisterPlugin(dynamicPolicy); err != nil {
		return false, nil, fmt.Errorf("RegisterPlugin failed with error: %v", err)
	}

	return true, dynamicPolicy, nil
}

func (d *DynamicPolicy) ResourceName() string {
	return ResourceName
}

// Run runs this plugin
func (p *DynamicPolicy) Run(ctx context.Context) {

	if err := periodicalhandler.RegisterPeriodicalHandlerWithHealthz(consts.HealthzReconcileState, general.HealthzCheckStateNotReady,
		appqrm.QRMSriovPluginPeriodicalHandlerGroupName, p.stateReconciler.Reconcile,
		consts.ReconcileStatePeriod, consts.ReconcileStateTolerationTimes); err != nil {
		general.ErrorS(err, "register periodical handler %s failed", consts.ReconcileState)
	}

	go wait.Until(func() {
		periodicalhandler.ReadyToStartHandlersByGroup(appqrm.QRMSriovPluginPeriodicalHandlerGroupName)
	}, 5*time.Second, ctx.Done())

	go wait.Until(func() {
		_ = p.emitter.StoreInt64(qrmutil.MetricNameHeartBeat, 1, metrics.MetricTypeNameRaw)
	}, time.Second*30, ctx.Done())

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
			// set err to nil if dryRun is true, to avoid affecting pod admission
			if p.dryRun {
				err = nil
			}
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

	queueCount, failOnExhaustion := p.getVFQueueCountAndExhaustionStrategy(request)
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

	numaSet := machine.NewCPUSet()
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

	general.InfoS("get accompany resource topology hints", "request", req, "originHints", hints, "augmentedHints", augmentedHints)

	if !p.dryRun {
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

	p.Lock()
	defer func() {
		p.Unlock()
		if err != nil {
			general.ErrorS(err, "AllocateAccompanyResource failed", "request", req)
			_ = p.emitter.StoreInt64(qrmutil.MetricNameAllocateAccompanyResourceFailed, 1, metrics.MetricTypeNameRaw,
				metrics.MetricTag{Key: "error_message", Val: metric.MetricTagValueFormat(err)})
			// set err to nil if dryRun is true, to avoid affecting pod admission
			if p.dryRun {
				err = nil
			}
		}
	}()

	qosLevel, err := qrmutil.GetKatalystQoSLevelFromResourceReq(p.qosConfig, req, p.podAnnotationKeptKeys, p.podLabelKeptKeys)
	if err != nil {
		err = fmt.Errorf("GetKatalystQoSLevelFromResourceReq for pod: %s/%s, container: %s failed with error: %v",
			req.PodNamespace, req.PodName, req.ContainerName, err)
		general.Errorf("%s", err.Error())
		return err
	}

	// reuse allocation info allocated by same pod and container
	allocationInfo := p.state.GetAllocationInfo(req.PodUid, req.ContainerName)
	if allocationInfo != nil {
		if p.dryRun {
			return nil
		}
		return p.addAllocationInfoToResponse(allocationInfo, resp)
	}

	// get request quantity of main resource: cpu
	request, _, err := qrmutil.GetPodAggregatedRequestResource(req)
	if err != nil {
		return fmt.Errorf("GetPodAggregatedRequestResource failed with error: %v", err)
	}

	queueCount, failOnExhaustion := p.getVFQueueCountAndExhaustionStrategy(request)
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

	general.InfoS("allocate accompany resource result", "request", req, "allocationInfo", allocationInfo)

	if p.dryRun {
		return nil
	}

	if err := p.addAllocationInfoToResponse(allocationInfo, resp); err != nil {
		return fmt.Errorf("addAllocationInfoToResponse failed with error: %v", err)
	}
	p.state.SetAllocationInfo(req.PodUid, req.ContainerName, allocationInfo, true)

	// try to update sriov vf result annotation, if failed, leave it to state_reconciler to update
	if err := utils.UpdateSriovVFResultAnnotation(p.agentCtx.Client.KubeClient, allocationInfo); err != nil {
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

	// delete is safe, no need to check dryRun
	p.state.Delete(req.PodUid, true)

	return nil
}

func (p *DynamicPolicy) addAllocationInfoToResponse(allocationInfo *state.AllocationInfo, resp *pluginapi.ResourceAllocationResponse) error {
	resourceAllocationInfo, err := p.generateResourceAllocationInfo(allocationInfo)
	if err != nil {
		return fmt.Errorf("generateResourceAllocationInfo failed with error: %v", err)
	}
	resp.AllocationResult.ResourceAllocation[ResourceName] = resourceAllocationInfo
	return nil
}

func (p *DynamicPolicy) getVFQueueCountAndExhaustionStrategy(request int) (queueCount int, failOnExhaustion bool) {
	switch {
	case request >= p.policyConfig.LargeSizeVFCPUThreshold:
		return p.policyConfig.LargeSizeVFQueueCount, p.policyConfig.LargeSizeVFFailOnExhaustion
	case request >= p.policyConfig.SmallSizeVFCPUThreshold:
		return p.policyConfig.SmallSizeVFQueueCount, p.policyConfig.SmallSizeVFFailOnExhaustion
	}
	return -1, false
}
