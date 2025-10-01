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

package baseplugin

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/agent"
	gpuconsts "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/qrm"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"
)

const (
	GPUPluginStateFileName = "gpu_plugin_state"
)

// BasePlugin is a shared plugin that provides common functionalities and fields for GPU resource plugins and custom device plugins.
type BasePlugin struct {
	sync.Mutex

	QosConfig           *generic.QoSConfiguration
	QrmConfig           *qrm.QRMPluginsConfiguration
	GpuTopologyProvider machine.GPUTopologyProvider

	Emitter    metrics.MetricEmitter
	MetaServer *metaserver.MetaServer
	AgentCtx   *agent.GenericContext
	State      state.State

	PodAnnotationKeptKeys []string
	PodLabelKeptKeys      []string
	AssociatedDevicesName sets.String
}

func NewBasePlugin(
	agentCtx *agent.GenericContext, conf *config.Configuration, wrappedEmitter metrics.MetricEmitter,
) (*BasePlugin, error) {
	gpuTopologyProvider := machine.NewGPUTopologyProvider(conf.GPUResourceNames)
	stateImpl, err := state.NewCheckpointState(conf.QRMPluginsConfiguration, conf.GenericQRMPluginConfiguration.StateFileDirectory, GPUPluginStateFileName,
		gpuconsts.GPUResourcePluginPolicyNameStatic, gpuTopologyProvider, conf.SkipGPUStateCorruption, wrappedEmitter)

	if err != nil {
		return nil, err
	}

	return &BasePlugin{
		QosConfig:           conf.QoSConfiguration,
		QrmConfig:           conf.QRMPluginsConfiguration,
		GpuTopologyProvider: gpuTopologyProvider,

		Emitter:    wrappedEmitter,
		MetaServer: agentCtx.MetaServer,
		AgentCtx:   agentCtx,
		State:      stateImpl,

		PodAnnotationKeptKeys: conf.PodAnnotationKeptKeys,
		PodLabelKeptKeys:      conf.PodLabelKeptKeys,
		AssociatedDevicesName: sets.NewString(conf.GPUResourceNames...),
	}, nil
}

func (p *BasePlugin) GetGPUCount(req *pluginapi.ResourceRequest) (float64, sets.String, error) {
	gpuCount := float64(0)
	gpuNames := sets.NewString()
	for resourceName := range p.AssociatedDevicesName {
		_, request, err := util.GetQuantityFromResourceRequests(req.ResourceRequests, resourceName, false)
		if err != nil && !errors.IsNotFound(err) {
			return 0, nil, err
		}
		gpuCount += request
		gpuNames.Insert(resourceName)
	}
	return gpuCount, gpuNames, nil
}

func (p *BasePlugin) GetResourcePluginOptions(
	context.Context, *pluginapi.Empty,
) (*pluginapi.ResourcePluginOptions, error) {
	return &pluginapi.ResourcePluginOptions{
		PreStartRequired:      false,
		WithTopologyAlignment: true,
		NeedReconcile:         false,
		AssociatedDevices:     p.AssociatedDevicesName.List(),
	}, nil
}

func (p *BasePlugin) PackAllocationResponse(
	req *pluginapi.ResourceRequest, allocationInfo *state.AllocationInfo,
	resourceAllocationAnnotations map[string]string, resourceName string,
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
				resourceName: {
					IsNodeResource:    true,
					IsScalarResource:  true, // to avoid re-allocating
					AllocatedQuantity: allocationInfo.AllocatedAllocation.GPUMemoryQuantity,
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

func (p *BasePlugin) CalculateAssociatedDevices(
	gpuTopology *machine.GPUTopology, gpuMemoryRequest float64, hintNodes machine.CPUSet,
	request *pluginapi.AssociatedDeviceRequest,
) ([]string, map[string]float64, error) {
	gpuRequest := request.DeviceRequest.GetDeviceRequest()
	gpuMemoryPerGPU := gpuMemoryRequest / float64(gpuRequest)

	machineState := p.State.GetMachineState()

	allocatedDevices := sets.NewString()
	needed := gpuRequest
	allocateDevices := func(devices ...string) bool {
		for _, device := range devices {
			allocatedDevices.Insert(device)
			needed--
			if needed == 0 {
				return true
			}
		}
		return false
	}

	allocatedGPUMemory := func(devices ...string) map[string]float64 {
		memory := make(map[string]float64)
		for _, device := range devices {
			memory[device] = gpuMemoryPerGPU
		}
		return memory
	}

	availableDevices := request.DeviceRequest.GetAvailableDevices()
	reusableDevices := request.DeviceRequest.GetReusableDevices()

	// allocate must include devices first
	for _, device := range reusableDevices {
		if machineState.GPUMemorySatisfiedRequest(device, gpuMemoryPerGPU) {
			general.Warningf("must include gpu %s has enough memory to allocate, gpuMemoryAllocatable: %f, gpuMemoryAllocated: %f, gpuMemoryPerGPU: %f",
				device, machineState.GetGPUMemoryAllocatable(device), machineState.GPUMemoryAllocated(device), gpuMemoryPerGPU)
		}
		allocateDevices(device)
	}

	// if allocated devices is enough, return immediately
	if allocatedDevices.Len() >= int(gpuRequest) {
		return allocatedDevices.UnsortedList(), allocatedGPUMemory(allocatedDevices.UnsortedList()...), nil
	}

	isNUMAAffinityDevice := func(device string) bool {
		info, ok := gpuTopology.GPUs[device]
		if !ok {
			general.Errorf("failed to find hint node for device %s", device)
			return false
		}

		// check if gpu's numa node is the subset of hint nodes
		// todo support multi numa node
		if machine.NewCPUSet(info.GetNUMANode()...).IsSubsetOf(hintNodes) {
			return true
		}
		return false
	}

	sort.SliceStable(availableDevices, func(i, j int) bool {
		return machineState.GPUMemoryAllocated(availableDevices[i]) > machineState.GPUMemoryAllocated(availableDevices[j])
	})

	// second allocate available and numa-affinity gpus
	for _, device := range availableDevices {
		if allocatedDevices.Has(device) {
			continue
		}

		if !isNUMAAffinityDevice(device) {
			continue
		}

		if !machineState.GPUMemorySatisfiedRequest(device, gpuMemoryPerGPU) {
			general.Infof("available numa affinity gpu %s has not enough memory to allocate, gpuMemoryAllocatable: %f, gpuMemoryAllocated: %f, gpuMemoryPerGPU: %f",
				device, machineState.GetGPUMemoryAllocatable(device), machineState.GPUMemoryAllocated(device), gpuMemoryPerGPU)
			continue
		}

		if allocateDevices(device) {
			return allocatedDevices.UnsortedList(), allocatedGPUMemory(allocatedDevices.UnsortedList()...), nil
		}
	}

	return nil, nil, fmt.Errorf("no enough available GPUs found in gpuTopology, availableDevices len: %d, allocatedDevices len: %d", len(availableDevices), len(allocatedDevices))
}
