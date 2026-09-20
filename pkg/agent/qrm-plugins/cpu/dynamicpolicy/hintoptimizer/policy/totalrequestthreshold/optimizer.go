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

package totalrequestthreshold

import (
	"fmt"

	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/hintoptimizer"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/hintoptimizer/policy"
	hintoptimizerutil "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/hintoptimizer/util"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	cpuutil "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/util"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

const HintOptimizerNameTotalRequestThreshold = "total_request_threshold"

type cpuTotalRequestThresholdHintOptimizer struct {
	conf         *config.Configuration
	metaServer   *metaserver.MetaServer
	state        state.State
	reservedCPUs machine.CPUSet
}

func NewTotalRequestThresholdHintOptimizer(
	options policy.HintOptimizerFactoryOptions,
) (hintoptimizer.HintOptimizer, error) {
	return &cpuTotalRequestThresholdHintOptimizer{
		conf:         options.Conf,
		metaServer:   options.MetaServer,
		state:        options.State,
		reservedCPUs: options.ReservedCPUs,
	}, nil
}

func (o *cpuTotalRequestThresholdHintOptimizer) Run(<-chan struct{}) error {
	return nil
}

func (o *cpuTotalRequestThresholdHintOptimizer) OptimizeHints(
	request hintoptimizer.Request,
	hints *pluginapi.ListOfTopologyHints,
) error {
	if err := hintoptimizerutil.GenericOptimizeHintsCheck(request, hints); err != nil {
		general.Errorf("GenericOptimizeHintsCheck failed with error: %v", err)
		return err
	}

	ratio := 0.0
	if o.conf != nil && o.conf.CPUQRMPluginConfig != nil && o.conf.CPUQRMPluginConfig.TotalRequestThresholdHintOptimizerConfig != nil {
		ratio = o.conf.CPUQRMPluginConfig.TotalRequestThresholdHintOptimizerConfig.CPUTotalRequestThresholdRatio
	}
	if ratio <= 0 {
		return nil
	}
	if ratio > 1 {
		general.Warningf("skip totalRequestThresholdHintOptimizer for ratio > 1, ratio: %.3f", ratio)
		return hintoptimizerutil.ErrHintOptimizerSkip
	}
	if len(hints.Hints) == 0 {
		general.Warningf("totalRequestThresholdHintOptimizer got no available cpu hints for pod: %s/%s, container: %s",
			request.PodNamespace, request.PodName, request.ContainerName)
		return cpuutil.ErrNoAvailableCPUHints
	}

	machineState := o.state.GetMachineState()
	filteredTopologyHints := make([]*pluginapi.TopologyHint, 0, len(hints.Hints))
	scope := ""
	for _, hint := range hints.Hints {
		if hint == nil {
			continue
		}

		hintNUMASet, err := machine.NewCPUSetUint64(hint.Nodes...)
		if err != nil {
			return err
		}

		totalAllocatable := o.getTotalAllocatable(hintNUMASet)
		totalRequestedQuantity := o.getTotalRequest(request.ResourceRequest, request.CPURequest, machineState, hintNUMASet)
		existingRequestedQuantity := totalRequestedQuantity - request.CPURequest
		scopeName := fmt.Sprintf("numa:%s", hintNUMASet.String())
		if totalAllocatable <= 0 {
			general.Warningf("filter out topology hint %v for pod: %s/%s container %s error: got non-positive shared_cores numa_binding cpu total request threshold total allocatable: %.3f, scope: %s, existing requested: %.3f, current request: %.3f, total requested: %.3f",
				hint.Nodes, request.PodNamespace, request.PodName, request.ContainerName, totalAllocatable, scopeName, existingRequestedQuantity, request.CPURequest, totalRequestedQuantity)
			scope += fmt.Sprintf("numa%s ", hintNUMASet.String())
			continue
		}

		allowed := totalAllocatable * ratio
		if !cpuutil.CPUIsSufficient(totalRequestedQuantity, allowed) {
			general.Warningf("filter out topology hint %v for pod: %s/%s container %s error: shared_cores numa_binding cpu total request %.3f exceeds threshold %.3f, total allocatable: %.3f, ratio: %.3f, scope: %s, existing requested: %.3f, current request: %.3f, total requested: %.3f",
				hint.Nodes, request.PodNamespace, request.PodName, request.ContainerName, totalRequestedQuantity, allowed, totalAllocatable, ratio, scopeName, existingRequestedQuantity, request.CPURequest, totalRequestedQuantity)
			scope += fmt.Sprintf("numa%s ", hintNUMASet.String())
			continue
		}
		filteredTopologyHints = append(filteredTopologyHints, hint)
	}

	if len(filteredTopologyHints) == 0 {
		return fmt.Errorf("pod: %s/%s, container: %s shared_cores numa_binding cpu total request exceeds threshold, current request: %.3f, ratio: %.3f, scope: %s: %w",
			request.PodNamespace, request.PodName, request.ContainerName, request.CPURequest, ratio, scope, cpuutil.ErrNoAvailableCPUHints)
	}

	hints.Hints = filteredTopologyHints
	return nil
}

func (o *cpuTotalRequestThresholdHintOptimizer) getTotalAllocatable(numaSet machine.CPUSet) float64 {
	return float64(o.metaServer.CPUDetails.CPUsInNUMANodes(numaSet.ToSliceInt()...).Difference(o.state.GetReservedCPUs()).Size())
}

func (o *cpuTotalRequestThresholdHintOptimizer) getTotalRequest(req *pluginapi.ResourceRequest,
	request float64, machineState state.NUMANodeMap, numaSet machine.CPUSet,
) float64 {
	existingRequestedQuantity := 0.0
	for _, nodeID := range numaSet.ToSliceNoSortInt() {
		if machineState[nodeID] == nil {
			continue
		}

		existingRequestedQuantity += state.GetRequestedQuantityFromPodEntries(machineState[nodeID].PodEntries,
			func(ai *state.AllocationInfo) bool {
				if ai == nil || ai.PodUid == req.PodUid {
					return false
				}
				return ai.CheckSharedOrDedicatedNUMABinding()
			}, func(allocationInfo *state.AllocationInfo) float64 {
				return cpuutil.GetContainerRequestedCores(o.metaServer, allocationInfo)
			})
	}

	return existingRequestedQuantity + request
}

var _ hintoptimizer.HintOptimizer = &cpuTotalRequestThresholdHintOptimizer{}
