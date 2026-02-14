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
	"fmt"
	"sync"

	v1 "k8s.io/api/core/v1"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/baseplugin/reporter"

	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/agent"
	gpuconsts "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/state"
	"github.com/kubewharf/katalyst-core/pkg/config"
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
	reporter reporter.GPUReporter
	mu       sync.RWMutex
	Conf     *config.Configuration

	Emitter    metrics.MetricEmitter
	MetaServer *metaserver.MetaServer
	AgentCtx   *agent.GenericContext

	PodAnnotationKeptKeys []string
	PodLabelKeptKeys      []string

	state state.State
	// Registry of device topology providers
	DeviceTopologyRegistry *machine.DeviceTopologyRegistry

	// Registry of default resource state generators
	DefaultResourceStateGeneratorRegistry *state.DefaultResourceStateGeneratorRegistry

	// Map of specific device name to device type
	deviceNameToTypeMap map[string]string

	initializedCh chan struct{}
}

func NewBasePlugin(
	agentCtx *agent.GenericContext, conf *config.Configuration, wrappedEmitter metrics.MetricEmitter,
) (*BasePlugin, error) {
	deviceTopologyRegistry := machine.NewDeviceTopologyRegistry()

	gpuReporter, err := reporter.NewGPUReporter(wrappedEmitter, agentCtx.MetaServer, conf, deviceTopologyRegistry)
	if err != nil {
		return nil, fmt.Errorf("newGPUReporterPlugin failed with error: %v", err)
	}

	return &BasePlugin{
		Conf:     conf,
		reporter: gpuReporter,

		Emitter:    wrappedEmitter,
		MetaServer: agentCtx.MetaServer,
		AgentCtx:   agentCtx,

		PodAnnotationKeptKeys: conf.PodAnnotationKeptKeys,
		PodLabelKeptKeys:      conf.PodLabelKeptKeys,

		DeviceTopologyRegistry:                deviceTopologyRegistry,
		DefaultResourceStateGeneratorRegistry: state.NewDefaultResourceStateGeneratorRegistry(),

		deviceNameToTypeMap: make(map[string]string),
		initializedCh:       make(chan struct{}),
	}, nil
}

// Run starts the asynchronous tasks of the plugin
func (p *BasePlugin) Run(stopCh <-chan struct{}) {
	go p.reporter.Run(stopCh)
	go p.DeviceTopologyRegistry.Run(stopCh, p.initializedCh)
}

// SetInitialized sends a signal through initializedCh when we have finished initializing all dependencies.
func (p *BasePlugin) SetInitialized() {
	close(p.initializedCh)
}

// GetState may return a nil state because the state is only initialized when InitState is called.
func (p *BasePlugin) GetState() state.State {
	p.mu.RLock()
	defer p.mu.RUnlock()

	return p.state
}

// SetState sets the state only for unit testing purposes.
func (p *BasePlugin) SetState(s state.State) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.state = s
}

// InitState initializes the state of the plugin.
func (p *BasePlugin) InitState() error {
	stateImpl, err := state.NewCheckpointState(p.Conf.StateDirectoryConfiguration, p.Conf.QRMPluginsConfiguration, GPUPluginStateFileName,
		gpuconsts.GPUResourcePluginPolicyNameStatic, p.DefaultResourceStateGeneratorRegistry, p.Conf.SkipGPUStateCorruption, p.Emitter)
	if err != nil {
		return fmt.Errorf("NewCheckpointState failed with error: %v", err)
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	p.state = stateImpl
	return nil
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

// UpdateAllocatableAssociatedDevicesByDeviceType updates the topology provider with topology information of the
// given device type.
func (p *BasePlugin) UpdateAllocatableAssociatedDevicesByDeviceType(
	request *pluginapi.UpdateAllocatableAssociatedDevicesRequest, deviceType string,
) (*pluginapi.UpdateAllocatableAssociatedDevicesResponse, error) {
	deviceTopology := &machine.DeviceTopology{
		Devices: make(map[string]machine.DeviceInfo, len(request.Devices)),
	}

	for _, device := range request.Devices {
		var numaNode []int
		if device.Topology != nil {
			numaNode = make([]int, 0, len(device.Topology.Nodes))

			for _, node := range device.Topology.Nodes {
				if node == nil {
					continue
				}
				numaNode = append(numaNode, int(node.ID))
			}
		}

		deviceTopology.Devices[device.ID] = machine.DeviceInfo{
			Health:         device.Health,
			NumaNodes:      numaNode,
			DeviceAffinity: make(map[machine.AffinityPriority]machine.DeviceIDs),
		}
	}

	err := p.DeviceTopologyRegistry.SetDeviceTopology(deviceType, deviceTopology)
	if err != nil {
		general.Errorf("set device topology failed with error: %v", err)
		return nil, fmt.Errorf("set device topology failed with error: %v", err)
	}

	general.Infof("got device %s topology success: %v", request.DeviceName, deviceTopology)

	return &pluginapi.UpdateAllocatableAssociatedDevicesResponse{}, nil
}

// GenerateResourceStateFromPodEntries returns an AllocationMap of a certain resource based on pod entries
// 1. If podEntries is nil, it will get pod entries from state
// 2. If the generator is not found, it will return an error
func (p *BasePlugin) GenerateResourceStateFromPodEntries(
	resourceName string,
	podEntries state.PodEntries,
) (state.AllocationMap, error) {
	if podEntries == nil {
		podEntries = p.state.GetPodEntries(v1.ResourceName(resourceName))
	}

	generator, ok := p.DefaultResourceStateGeneratorRegistry.GetGenerator(resourceName)
	if !ok {
		return nil, fmt.Errorf("could not find generator for resource %s", resourceName)
	}

	return state.GenerateResourceStateFromPodEntries(podEntries, generator)
}

func (p *BasePlugin) GenerateMachineStateFromPodEntries(
	podResourceEntries state.PodResourceEntries,
) (state.AllocationResourcesMap, error) {
	return state.GenerateMachineStateFromPodEntries(podResourceEntries, p.DefaultResourceStateGeneratorRegistry)
}

// RegisterDeviceNameToType is used to map device name to device type.
// For example, we may have multiple device names for a same device type, e.g. "nvidia.com/gpu" and "nvidia.com/be-gpu",
// so we map them to the same device type, which allows us to allocate them interchangeably.
func (p *BasePlugin) RegisterDeviceNameToType(resourceNames []string, deviceType string) {
	for _, resourceName := range resourceNames {
		p.deviceNameToTypeMap[resourceName] = deviceType
	}
}

func (p *BasePlugin) GetResourceTypeFromDeviceName(deviceName string) (string, error) {
	deviceType, ok := p.deviceNameToTypeMap[deviceName]
	if !ok {
		return "", fmt.Errorf("no device type found for device name %s", deviceName)
	}
	return deviceType, nil
}

// RegisterTopologyAffinityProvider is a hook to set device affinity for a certain device type
func (p *BasePlugin) RegisterTopologyAffinityProvider(
	deviceType string, deviceAffinityProvider machine.DeviceAffinityProvider,
) {
	p.DeviceTopologyRegistry.RegisterTopologyAffinityProvider(deviceType, deviceAffinityProvider)
}
