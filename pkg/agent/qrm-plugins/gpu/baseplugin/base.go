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

	gpuconsts "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/state"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/agent"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/qrm"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

const (
	GPUPluginStateFileName = "gpu_plugin_state"
)

// BasePlugin is a shared plugin that provides common functionalities and fields for GPU resource plugins and custom device plugins.
type BasePlugin struct {
	mu sync.RWMutex
	*config.Configuration

	QosConfig *generic.QoSConfiguration
	QrmConfig *qrm.QRMPluginsConfiguration

	Emitter    metrics.MetricEmitter
	MetaServer *metaserver.MetaServer
	AgentCtx   *agent.GenericContext

	PodAnnotationKeptKeys []string
	PodLabelKeptKeys      []string
	AssociatedDevicesName sets.String

	// Map of checkpoints for each sub-plugin
	State state.State
	// Registry of device topology providers
	DeviceTopologyRegistry *machine.DeviceTopologyRegistry
}

func NewBasePlugin(
	agentCtx *agent.GenericContext, conf *config.Configuration, wrappedEmitter metrics.MetricEmitter,
) (*BasePlugin, error) {
	deviceTopologyRegistry := machine.NewDeviceTopologyRegistry()

	stateImpl, err := state.NewCheckpointState(conf.QRMPluginsConfiguration, conf.GenericQRMPluginConfiguration.StateFileDirectory, GPUPluginStateFileName,
		gpuconsts.GPUResourcePluginPolicyNameStatic, deviceTopologyRegistry, conf.SkipGPUStateCorruption, wrappedEmitter)

	if err != nil {
		return nil, fmt.Errorf("NewCheckpointState failed with error: %v", err)
	}

	return &BasePlugin{
		QosConfig: conf.QoSConfiguration,
		QrmConfig: conf.QRMPluginsConfiguration,

		Emitter:    wrappedEmitter,
		MetaServer: agentCtx.MetaServer,
		AgentCtx:   agentCtx,

		PodAnnotationKeptKeys: conf.PodAnnotationKeptKeys,
		PodLabelKeptKeys:      conf.PodLabelKeptKeys,
		AssociatedDevicesName: sets.NewString(conf.GPUResourceNames...),

		State:                  stateImpl,
		DeviceTopologyRegistry: deviceTopologyRegistry,
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
					AllocatedQuantity: allocationInfo.AllocatedAllocation.Quantity,
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
	gpuTopology *machine.DeviceTopology, gpuMemoryRequest float64, hintNodes machine.CPUSet,
	request *pluginapi.AssociatedDeviceRequest, resourceName v1.ResourceName,
) ([]string, map[string]float64, error) {
	gpuRequest := request.DeviceRequest.GetDeviceRequest()
	gpuMemoryPerGPU := gpuMemoryRequest / float64(gpuRequest)

	machineState, ok := p.State.GetMachineState()[resourceName]
	if !ok {
		return nil, nil, fmt.Errorf("no machine state for resource %s", resourceName)
	}

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
		if machineState.IsGPURequestSatisfied(device, gpuMemoryPerGPU, float64(p.GPUMemoryAllocatablePerGPU.Value())) {
			general.Warningf("must include gpu %s has enough memory to allocate, gpuMemoryAllocatable: %f, gpuMemoryAllocated: %f, gpuMemoryPerGPU: %f",
				device, float64(p.GPUMemoryAllocatablePerGPU.Value()), machineState.GetQuantityAllocated(device), gpuMemoryPerGPU)
		}
		allocateDevices(device)
	}

	// if allocated devices is enough, return immediately
	if allocatedDevices.Len() >= int(gpuRequest) {
		return allocatedDevices.UnsortedList(), allocatedGPUMemory(allocatedDevices.UnsortedList()...), nil
	}

	isNUMAAffinityDevice := func(device string) bool {
		info, ok := gpuTopology.Devices[device]
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
		return machineState.GetQuantityAllocated(availableDevices[i]) > machineState.GetQuantityAllocated(availableDevices[j])
	})

	// second allocate available and numa-affinity gpus
	for _, device := range availableDevices {
		if allocatedDevices.Has(device) {
			continue
		}

		if !isNUMAAffinityDevice(device) {
			continue
		}

		if !machineState.IsGPURequestSatisfied(device, gpuMemoryPerGPU, float64(p.GPUMemoryAllocatablePerGPU.Value())) {
			general.Infof("available numa affinity gpu %s has not enough memory to allocate, gpuMemoryAllocatable: %f, gpuMemoryAllocated: %f, gpuMemoryPerGPU: %f",
				device, float64(p.GPUMemoryAllocatablePerGPU.Value()), machineState.GetQuantityAllocated(device), gpuMemoryPerGPU)
			continue
		}

		if allocateDevices(device) {
			return allocatedDevices.UnsortedList(), allocatedGPUMemory(allocatedDevices.UnsortedList()...), nil
		}
	}

	return nil, nil, fmt.Errorf("no enough available GPUs found in gpuTopology, number of needed GPUs: %d, availableDevices len: %d, allocatedDevices len: %d", gpuRequest, len(availableDevices), len(allocatedDevices))
}
