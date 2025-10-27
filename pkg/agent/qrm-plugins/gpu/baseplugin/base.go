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

	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/agent"
	gpuconsts "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/state"
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

	// Map of checkpoints for each sub-plugin
	State state.State
	// Registry of device topology providers
	DeviceTopologyRegistry *machine.DeviceTopologyRegistry

	// Map of specific device name to device type
	deviceNameToTypeMap map[string]string
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

		State:                  stateImpl,
		DeviceTopologyRegistry: deviceTopologyRegistry,
		deviceNameToTypeMap:    make(map[string]string),
	}, nil
}

// SetDeviceAffinity is a hook to set device affinity for a certain device type
func (p *BasePlugin) SetDeviceAffinity(deviceType string, deviceAffinityProvider machine.DeviceAffinityProvider) error {
	return p.DeviceTopologyRegistry.SetDeviceAffinity(deviceType, deviceAffinityProvider)
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
