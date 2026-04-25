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
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/agent"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/baseplugin/reporter"
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

	// Map of device type to specific device names
	deviceTypeToNames map[string]sets.String

	stateInitializedCh   chan struct{}
	stateInitializedOnce sync.Once
}

func NewBasePlugin(
	agentCtx *agent.GenericContext, conf *config.Configuration, wrappedEmitter metrics.MetricEmitter,
) (*BasePlugin, error) {
	deviceTopologyRegistry := machine.NewDeviceTopologyRegistry()

	basePlugin := &BasePlugin{
		Conf: conf,

		Emitter:    wrappedEmitter,
		MetaServer: agentCtx.MetaServer,
		AgentCtx:   agentCtx,

		PodAnnotationKeptKeys: conf.PodAnnotationKeptKeys,
		PodLabelKeptKeys:      conf.PodLabelKeptKeys,

		DeviceTopologyRegistry:                deviceTopologyRegistry,
		DefaultResourceStateGeneratorRegistry: state.NewDefaultResourceStateGeneratorRegistry(),

		deviceNameToTypeMap: make(map[string]string),
		deviceTypeToNames:   make(map[string]sets.String),

		stateInitializedCh: make(chan struct{}),
	}

	gpuReporter, err := reporter.NewGPUReporter(wrappedEmitter, agentCtx.MetaServer, conf, deviceTopologyRegistry, basePlugin.GetState,
		basePlugin.deviceTypeToNames)
	if err != nil {
		return nil, fmt.Errorf("newGPUReporterPlugin failed with error: %v", err)
	}

	basePlugin.reporter = gpuReporter

	return basePlugin, nil
}

// Run starts the asynchronous tasks of the plugin
func (p *BasePlugin) Run(stopCh <-chan struct{}) {
	go p.DeviceTopologyRegistry.Run(stopCh)
	go func() {
		select {
		case <-p.stateInitializedCh:
			general.Infof("state initialized, starting reporter")
			p.reporter.Run(stopCh)
		case <-stopCh:
			general.Infof("stop channel closed before state initialization, skipping reporter run")
			return
		}
	}()
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

// TriggerReporter safely triggers the reporter to generate and send a new report.
func (p *BasePlugin) TriggerReporter() {
	if p.reporter != nil {
		p.reporter.Trigger()
	}
}

// registerNotifiers registers the necessary callbacks to trigger the reporter
// when device topology or machine state changes.
func (p *BasePlugin) registerNotifiers(state state.State) {
	if p.DeviceTopologyRegistry != nil {
		p.DeviceTopologyRegistry.RegisterTopologyChangeNotifier(func() {
			general.Infof("triggering reporter due to topology change")
			p.TriggerReporter()
		})
	}
	if state != nil {
		state.AddMachineStateSyncNotifier(func() {
			general.Infof("triggering reporter due to machine state change")
			p.TriggerReporter()
		})
	}
}

// InitState initializes the state of the plugin.
func (p *BasePlugin) InitState() error {
	stateImpl, err := state.NewCheckpointState(p.Conf.StateDirectoryConfiguration, p.Conf.QRMPluginsConfiguration, GPUPluginStateFileName,
		gpuconsts.GPUResourcePluginPolicyNameStatic, p.DefaultResourceStateGeneratorRegistry, p.Conf.SkipGPUStateCorruption, p.Emitter)
	if err != nil {
		return fmt.Errorf("NewCheckpointState failed with error: %v", err)
	}

	p.mu.Lock()
	p.state = stateImpl
	p.mu.Unlock()

	p.stateInitializedOnce.Do(func() {
		p.registerNotifiers(stateImpl)
		close(p.stateInitializedCh)
		general.Infof("state initialized channel closed")
	})

	return nil
}

func (p *BasePlugin) PackAllocationResponse(
	req *pluginapi.ResourceRequest, allocationInfoMap map[string]*state.AllocationInfo,
	resourceAllocationAnnotations map[string]string, primaryResourceName string,
	resourceAllocationEnvs map[string]map[string]string,
) (*pluginapi.ResourceAllocationResponse, error) {
	if len(allocationInfoMap) == 0 {
		return nil, fmt.Errorf("packAllocationResponse got empty allocationInfoMap")
	} else if req == nil {
		return nil, fmt.Errorf("packAllocationResponse got nil request")
	}

	resourceAllocation := make(map[string]*pluginapi.ResourceAllocationInfo)
	for resourceName, allocationInfo := range allocationInfoMap {
		resourceAllocation[resourceName] = &pluginapi.ResourceAllocationInfo{
			IsNodeResource:    true,
			IsScalarResource:  true, // to avoid re-allocating
			AllocatedQuantity: allocationInfo.AllocatedAllocation.Quantity,
			Annotations:       resourceAllocationAnnotations,
			Envs:              resourceAllocationEnvs[resourceName],
			ResourceHints: &pluginapi.ListOfTopologyHints{
				Hints: []*pluginapi.TopologyHint{
					req.Hint,
				},
			},
		}
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
		ResourceName:   primaryResourceName,
		AllocationResult: &pluginapi.ResourceAllocation{
			ResourceAllocation: resourceAllocation,
		},
		Labels:      general.DeepCopyMap(req.Labels),
		Annotations: general.DeepCopyMap(req.Annotations),
	}, nil
}

// UpdateAllocatableAssociatedDevices updates the topology provider with topology information of the
// given device request.
func (p *BasePlugin) UpdateAllocatableAssociatedDevices(
	request *pluginapi.UpdateAllocatableAssociatedDevicesRequest,
) (*pluginapi.UpdateAllocatableAssociatedDevicesResponse, error) {
	deviceTopology := &machine.DeviceTopology{
		Devices:    make(map[string]machine.DeviceInfo, len(request.Devices)),
		UpdateTime: time.Now().UnixNano(),
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
			Health:     device.Health,
			NumaNodes:  numaNode,
			Dimensions: make(map[string]string),
		}
	}

	// Store the device topology using the actual resource name from the request
	err := p.DeviceTopologyRegistry.SetDeviceTopology(request.DeviceName, deviceTopology)
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

// RegisterDeviceNames is used to map device name to device type, and map
// For example, we may have multiple device names for a same device type, e.g. "nvidia.com/gpu" and "hw.com/npu",
// so we map them to the same device type, which allows us to allocate them interchangeably.
func (p *BasePlugin) RegisterDeviceNames(deviceNames []string, deviceType string) {
	for _, deviceeName := range deviceNames {
		p.deviceNameToTypeMap[deviceeName] = deviceType
		if _, ok := p.deviceTypeToNames[deviceType]; !ok {
			p.deviceTypeToNames[deviceType] = sets.NewString()
		}
		p.deviceTypeToNames[deviceType].Insert(deviceeName)
	}
}

// ResolveResourceName takes in a resourceName and tries to find a mapping of resource type from deviceNameToTypeMap.
// If no mapping is found, resourceName is returned if fallback is true. If fallback is false, an empty string is returned.
func (p *BasePlugin) ResolveResourceName(resourceName string, fallback bool) string {
	resourceType, ok := p.deviceNameToTypeMap[resourceName]
	if ok {
		return resourceType
	}
	general.Infof("no device type found for resource %s", resourceName)
	if fallback {
		return resourceName
	}
	return ""
}

// RegisterTopologyAffinityProvider is a hook to set device affinity for given device names
func (p *BasePlugin) RegisterTopologyAffinityProvider(
	deviceNames []string, deviceAffinityProvider machine.DeviceAffinityProvider,
) {
	for _, deviceName := range deviceNames {
		p.DeviceTopologyRegistry.RegisterTopologyAffinityProvider(deviceName, deviceAffinityProvider)
	}
}
