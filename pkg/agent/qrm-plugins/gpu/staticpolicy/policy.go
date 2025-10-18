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

package staticpolicy

import (
	"context"
	"fmt"
	"sync"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-api/pkg/plugins/skeleton"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/agent"
	appqrm "github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/agent/qrm"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/baseplugin"
	gpuconsts "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/customdeviceplugin"
	devicepluginregistry "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/customdeviceplugin/registry"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/resourceplugin"
	resourcepluginregistry "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/resourceplugin/registry"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util"
	"github.com/kubewharf/katalyst-core/pkg/agent/utilcomponent/periodicalhandler"
	"github.com/kubewharf/katalyst-core/pkg/config"
	dynamicconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

// StaticPolicy is the static gpu policy
type StaticPolicy struct {
	sync.RWMutex
	pluginapi.UnimplementedResourcePluginServer
	*baseplugin.BasePlugin

	name    string
	stopCh  chan struct{}
	started bool

	emitter               metrics.MetricEmitter
	associatedDeviceNames sets.String
	resourcePluginsNames  sets.String

	residualHitMap map[string]int64

	resourcePlugins     map[string]resourceplugin.ResourcePlugin
	customDevicePlugins map[string]customdeviceplugin.CustomDevicePlugin
}

// NewStaticPolicy returns a static gpu policy
func NewStaticPolicy(
	agentCtx *agent.GenericContext, conf *config.Configuration,
	_ interface{}, agentName string,
) (bool, agent.Component, error) {
	wrappedEmitter := agentCtx.EmitterPool.GetDefaultMetricsEmitter().WithTags(agentName, metrics.MetricTag{
		Key: util.QRMPluginPolicyTagName,
		Val: gpuconsts.GPUResourcePluginPolicyNameStatic,
	})

	basePlugin, err := baseplugin.NewBasePlugin(agentCtx, conf, wrappedEmitter)
	if err != nil {
		return false, agent.ComponentStub{}, fmt.Errorf("failed to create base plugin: %w", err)
	}

	policyImplement := &StaticPolicy{
		emitter:               wrappedEmitter,
		stopCh:                make(chan struct{}),
		name:                  fmt.Sprintf("%s_%s", agentName, gpuconsts.GPUResourcePluginPolicyNameStatic),
		residualHitMap:        make(map[string]int64),
		associatedDeviceNames: sets.NewString(conf.GPUDeviceNames...),
		resourcePluginsNames:  sets.NewString(conf.ResourcePluginsNames...),
		BasePlugin:            basePlugin,
		resourcePlugins:       make(map[string]resourceplugin.ResourcePlugin),
		customDevicePlugins:   make(map[string]customdeviceplugin.CustomDevicePlugin),
	}

	if err = policyImplement.registerResourcePlugins(); err != nil {
		return false, agent.ComponentStub{}, fmt.Errorf("failed to register resource plugins: %w", err)
	}
	if err = policyImplement.registerCustomDevicePlugins(); err != nil {
		return false, agent.ComponentStub{}, fmt.Errorf("failed to register custom device plugins: %w", err)
	}

	pluginWrapper, err := skeleton.NewRegistrationPluginWrapper(policyImplement, conf.QRMPluginSocketDirs,
		func(key string, value int64) {
			_ = wrappedEmitter.StoreInt64(key, value, metrics.MetricTypeNameRaw)
		})
	if err != nil {
		return false, agent.ComponentStub{}, fmt.Errorf("static policy new plugin wrapper failed with error: %v", err)
	}

	return true, &agent.PluginWrapper{GenericPlugin: pluginWrapper}, nil
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

	err = periodicalhandler.RegisterPeriodicalHandlerWithHealthz(gpuconsts.ClearResidualState, general.HealthzCheckStateNotReady,
		appqrm.QRMNetworkPluginPeriodicalHandlerGroupName, p.clearResidualState, gpuconsts.StateCheckPeriod, gpuconsts.StateCheckTolerationTimes)
	if err != nil {
		general.Errorf("start %v failed, err: %v", gpuconsts.ClearResidualState, err)
	}

	go wait.Until(func() {
		periodicalhandler.ReadyToStartHandlersByGroup(appqrm.QRMGPUPluginPeriodicalHandlerGroupName)
	}, 5*time.Second, p.stopCh)

	return nil
}

// Stop stops this plugin
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

// Name returns the name of this plugin
func (p *StaticPolicy) Name() string {
	return p.name
}

// ResourceName returns resource names managed by this plugin
func (p *StaticPolicy) ResourceName() string {
	return string(consts.ResourceGPUMemory)
}

// GetTopologyHints returns hints of corresponding resources
func (p *StaticPolicy) GetTopologyHints(
	_ context.Context,
	req *pluginapi.ResourceRequest,
) (resp *pluginapi.ResourceHintsResponse, err error) {
	general.InfofV(4, "called")
	if req == nil {
		return nil, fmt.Errorf("GetTopologyHints got nil req")
	}

	p.RLock()
	defer p.RUnlock()

	resourcePlugin := p.getResourcePlugin(req.ResourceName)
	if resourcePlugin == nil {
		return nil, fmt.Errorf("failed to find resource plugin by name %s", req.ResourceName)
	}
	return resourcePlugin.GetTopologyHints(req)
}

// GetPodTopologyHints returns hints of corresponding resources
func (p *StaticPolicy) GetPodTopologyHints(
	_ context.Context,
	req *pluginapi.PodResourceRequest,
) (resp *pluginapi.PodResourceHintsResponse, err error) {
	return nil, util.ErrNotImplemented
}

func (p *StaticPolicy) RemovePod(
	_ context.Context,
	req *pluginapi.RemovePodRequest,
) (*pluginapi.RemovePodResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("RemovePod got nil req")
	}

	p.Lock()
	defer p.Unlock()

	// For every resource plugin and custom resource plugin, remove pod from their state
	if err := p.removePod(req.PodUid); err != nil {
		general.ErrorS(err, "remove pod failed with error", "podUID", req.PodUid)
		return nil, err
	}

	return &pluginapi.RemovePodResponse{}, nil
}

// GetResourcesAllocation returns allocation results of corresponding resources
func (p *StaticPolicy) GetResourcesAllocation(
	_ context.Context,
	_ *pluginapi.GetResourcesAllocationRequest,
) (*pluginapi.GetResourcesAllocationResponse, error) {
	general.InfofV(4, "called")
	return &pluginapi.GetResourcesAllocationResponse{}, nil
}

// GetTopologyAwareResources returns allocation results of corresponding resources as topology aware format
func (p *StaticPolicy) GetTopologyAwareResources(
	_ context.Context,
	req *pluginapi.GetTopologyAwareResourcesRequest,
) (*pluginapi.GetTopologyAwareResourcesResponse, error) {
	general.InfofV(4, "called")
	if req == nil {
		return nil, fmt.Errorf("GetTopologyAwareResources got nil req")
	}

	p.RLock()
	defer p.RUnlock()

	// Get topology aware resources for all resource plugins
	allocatedResourcesList := make([]*pluginapi.GetTopologyAwareResourcesResponse, 0)
	for _, resourcePlugin := range p.resourcePlugins {
		allocatedResource, err := resourcePlugin.GetTopologyAwareResources(req.PodUid, req.ContainerName)
		if err != nil {
			return nil, fmt.Errorf("failed to get topology aware resources for plugin %s: %w", resourcePlugin.ResourceName(), err)
		}

		if allocatedResource == nil {
			continue
		}
		allocatedResourcesList = append(allocatedResourcesList, allocatedResource)
	}

	// Merge the respective response into one response
	resp, err := p.mergeTopologyAwareResourcesResponse(req.PodUid, req.ContainerName, allocatedResourcesList)
	if err != nil {
		return nil, fmt.Errorf("failed to merge topology aware resources: %w", err)
	}

	return resp, nil
}

// mergeTopologyAwareResourcesResponse takes the separate topology aware resources response from the different sub-plugins and
// merge them into one response.
func (p *StaticPolicy) mergeTopologyAwareResourcesResponse(
	podUID, containerName string, respList []*pluginapi.GetTopologyAwareResourcesResponse,
) (*pluginapi.GetTopologyAwareResourcesResponse, error) {
	result := &pluginapi.GetTopologyAwareResourcesResponse{
		PodUid: podUID,
		ContainerTopologyAwareResources: &pluginapi.ContainerTopologyAwareResources{
			ContainerName: containerName,
		},
	}

	allocatedResources := make(map[string]*pluginapi.TopologyAwareResource)
	for _, resp := range respList {
		if resp == nil {
			continue
		}

		if result.PodName != "" && result.PodName != resp.PodName {
			general.Errorf("pod name %s not match, expect %s", resp.PodName, result.PodName)
			return nil, fmt.Errorf("pod name %s not match, expect %s", resp.PodName, result.PodName)
		}

		if result.PodNamespace != "" && result.PodNamespace != resp.PodNamespace {
			general.Errorf("pod namespace %s not match, expect %s", resp.PodNamespace, result.PodNamespace)
			return nil, fmt.Errorf("pod namespace %s not match, expect %s", resp.PodNamespace, result.PodNamespace)
		}

		if result.PodName == "" {
			result.PodName = resp.PodName
		}
		if result.PodNamespace == "" {
			result.PodNamespace = resp.PodNamespace
		}

		for resourceName, resource := range resp.ContainerTopologyAwareResources.AllocatedResources {
			allocatedResources[resourceName] = resource
		}
	}

	result.ContainerTopologyAwareResources.AllocatedResources = allocatedResources
	return result, nil
}

// GetTopologyAwareAllocatableResources returns corresponding allocatable resources as topology aware format
func (p *StaticPolicy) GetTopologyAwareAllocatableResources(
	_ context.Context,
	req *pluginapi.GetTopologyAwareAllocatableResourcesRequest,
) (*pluginapi.GetTopologyAwareAllocatableResourcesResponse, error) {
	general.InfofV(4, "called")
	if req == nil {
		return nil, fmt.Errorf("GetTopologyAwareAllocatableResources got nil req")
	}

	p.RLock()
	defer p.RUnlock()

	// Get topology aware allocatable resources for all resource plugins
	allocatableResources := make(map[string]*pluginapi.AllocatableTopologyAwareResource)
	for _, resourcePlugin := range p.resourcePlugins {
		allocatableResource, err := resourcePlugin.GetTopologyAwareAllocatableResources()
		if err != nil {
			return nil, fmt.Errorf("failed to get topology aware allocatable resources for plugin %s: %w", resourcePlugin.ResourceName(), err)
		}
		allocatableResources[allocatableResource.ResourceName] = allocatableResource.AllocatableTopologyAwareResource
	}

	return &pluginapi.GetTopologyAwareAllocatableResourcesResponse{
		AllocatableResources: allocatableResources,
	}, nil
}

// GetResourcePluginOptions returns options to be communicated with Resource Manager
func (p *StaticPolicy) GetResourcePluginOptions(
	context.Context,
	*pluginapi.Empty,
) (*pluginapi.ResourcePluginOptions, error) {
	return &pluginapi.ResourcePluginOptions{
		PreStartRequired:      false,
		WithTopologyAlignment: true,
		NeedReconcile:         false,
		AssociatedDevices:     p.associatedDeviceNames.List(),
	}, nil
}

// Allocate is called during pod admit so that the resource
// plugin can allocate corresponding resource for the container
// according to resource request
// TODO: Clean state if fail
func (p *StaticPolicy) Allocate(
	_ context.Context,
	req *pluginapi.ResourceRequest,
) (resp *pluginapi.ResourceAllocationResponse, err error) {
	if req == nil {
		return nil, fmt.Errorf("GetTopologyHints got nil req")
	}

	p.Lock()
	defer func() {
		if err != nil {
			_ = p.removeContainer(req.PodUid, req.ContainerName, v1.ResourceName(req.ResourceName))
			p.Unlock()
		}
	}()

	resourcePlugin := p.getResourcePlugin(req.ResourceName)
	if resourcePlugin == nil {
		return nil, fmt.Errorf("failed to get resource plugin by name %s", req.ResourceName)
	}

	resp, err = resourcePlugin.Allocate(req, nil)
	return resp, err
}

// AllocateForPod is called during pod admit so that the resource
// plugin can allocate corresponding resource for the pod
// according to resource request
func (p *StaticPolicy) AllocateForPod(
	_ context.Context,
	req *pluginapi.PodResourceRequest,
) (resp *pluginapi.PodResourceAllocationResponse, err error) {
	return nil, util.ErrNotImplemented
}

// PreStartContainer is called, if indicated by resource plugin during registration phase,
// before each container start. Resource plugin can run resource specific operations
// such as resetting the resource before making resources available to the container
func (p *StaticPolicy) PreStartContainer(
	context.Context,
	*pluginapi.PreStartContainerRequest,
) (*pluginapi.PreStartContainerResponse, error) {
	return &pluginapi.PreStartContainerResponse{}, nil
}

func (p *StaticPolicy) removePod(podUID string) error {
	return p.removeWithUpdate(podUID, func(podResourceEntries state.PodResourceEntries) bool {
		found := false
		for _, podEntries := range podResourceEntries {
			if podEntries[podUID] != nil {
				found = true
			}
			delete(podEntries, podUID)
		}
		return found
	})
}

// removeContainer removes container entry given the specific podUID, container name and resource name.
func (p *StaticPolicy) removeContainer(podUID, containerName string, resourceName v1.ResourceName) error {
	return p.removeWithUpdate(podUID, func(podResourceEntries state.PodResourceEntries) bool {
		found := false
		for resource, podEntries := range podResourceEntries {
			if resourceName == resource {
				continue
			}
			if podEntries[podUID][containerName] != nil {
				found = true
			}
			delete(podEntries[podUID], containerName)
		}
		return found
	})
}

func (p *StaticPolicy) removeWithUpdate(
	podUID string, removeFn func(podResourceEntries state.PodResourceEntries) bool,
) error {
	podResourceEntries := p.State.GetPodResourceEntries()

	found := removeFn(podResourceEntries)
	if !found {
		return nil
	}

	machineState, err := state.GenerateMachineStateFromPodEntries(podResourceEntries, p.DeviceTopologyRegistry)
	if err != nil {
		general.Errorf("pod: %s, GenerateMachineStateFromPodEntries failed with error: %v", podUID, err)
		return fmt.Errorf("calculate machineState by updated pod entries failed with error: %v", err)
	}

	p.State.SetPodResourceEntries(podResourceEntries, false)
	p.State.SetMachineState(machineState, false)

	if err := p.State.StoreState(); err != nil {
		general.Errorf("store state failed with error: %v", err)
		return err
	}

	return nil
}

// clearResidualState is used to clean residual pods in local state
func (p *StaticPolicy) clearResidualState(
	_ *config.Configuration,
	_ interface{},
	_ *dynamicconfig.DynamicAgentConfiguration,
	_ metrics.MetricEmitter,
	_ *metaserver.MetaServer,
) {
	general.Infof("exec")
	var (
		err     error
		podList []*v1.Pod
	)
	residualSet := make(map[string]bool)

	defer func() {
		_ = general.UpdateHealthzStateByError(gpuconsts.ClearResidualState, err)
	}()

	if p.MetaServer == nil {
		general.Errorf("nil metaServer")
		return
	}

	ctx := context.Background()
	podList, err = p.MetaServer.GetPodList(ctx, nil)
	if err != nil {
		general.Errorf("get pod list failed: %v", err)
		return
	}

	podSet := sets.NewString()
	for _, pod := range podList {
		podSet.Insert(fmt.Sprintf("%v", pod.UID))
	}

	p.Lock()
	defer p.Unlock()

	podResourceEntries := p.State.GetPodResourceEntries()
	for _, podEntries := range podResourceEntries {
		for podUID := range podEntries {
			if !podSet.Has(podUID) {
				residualSet[podUID] = true
				p.residualHitMap[podUID] += 1
				general.Infof("found pod: %s with state but doesn't show up in pod watcher, hit count: %d", podUID, p.residualHitMap[podUID])
			}
		}
	}

	podsToDelete := sets.NewString()
	for podUID, hitCount := range p.residualHitMap {
		if !residualSet[podUID] {
			general.Infof("already found pod: %s in pod watcher or its state is cleared, delete it from residualHitMap", podUID)
			delete(p.residualHitMap, podUID)
			continue
		}

		if time.Duration(hitCount)*gpuconsts.StateCheckPeriod >= gpuconsts.MaxResidualTime {
			podsToDelete.Insert(podUID)
		}
	}

	if podsToDelete.Len() > 0 {
		for {
			podUID, found := podsToDelete.PopAny()
			if !found {
				break
			}

			general.Infof("clear residual pod: %s in state", podUID)
			podResourceEntries.RemovePod(podUID)
		}

		machineState, err := state.GenerateMachineStateFromPodEntries(podResourceEntries, p.DeviceTopologyRegistry)
		if err != nil {
			general.Errorf("GenerateMachineStateFromPodEntries failed with error: %v", err)
			return
		}

		p.State.SetPodResourceEntries(podResourceEntries, false)
		p.State.SetMachineState(machineState, false)

		err = p.State.StoreState()
		if err != nil {
			general.Errorf("store state failed: %v", err)
			return
		}
	}
}

func (p *StaticPolicy) UpdateAllocatableAssociatedDevices(request *pluginapi.UpdateAllocatableAssociatedDevicesRequest) (*pluginapi.UpdateAllocatableAssociatedDevicesResponse, error) {
	if request == nil || len(request.Devices) == 0 {
		return nil, fmt.Errorf("request is nil")
	}

	customDevicePlugin := p.getCustomDevicePlugin(request.DeviceName)
	if customDevicePlugin == nil {
		return nil, fmt.Errorf("no custom device plugin found for device %s", request.DeviceName)
	}

	return customDevicePlugin.UpdateAllocatableAssociatedDevices(request)
}

func (*StaticPolicy) GetAssociatedDeviceTopologyHints(
	_ context.Context, _ *pluginapi.AssociatedDeviceRequest,
) (*pluginapi.AssociatedDeviceHintsResponse, error) {
	return &pluginapi.AssociatedDeviceHintsResponse{}, nil
}

// AllocateAssociatedDevice allocates a device in this sequence:
// 1. Find the resource plugin that corresponds to the accompanyResourceName and allocate
// 2. Find the custom device plugin that corresponds to the deviceName and allocate
func (p *StaticPolicy) AllocateAssociatedDevice(
	_ context.Context, req *pluginapi.AssociatedDeviceRequest,
) (resp *pluginapi.AssociatedDeviceAllocationResponse, respErr error) {
	var isAccompanyResourcePlugin bool
	var isAccompanyCustomDevicePlugin bool
	if req == nil || req.ResourceRequest == nil || req.DeviceRequest == nil {
		return nil, fmt.Errorf("req is nil")
	}

	if req.ResourceRequest.Hint == nil || req.ResourceRequest.Hint.Nodes == nil {
		general.Warningf("got nil resource hint")
		return &pluginapi.AssociatedDeviceAllocationResponse{
			AllocationResult: nil,
		}, nil
	}

	p.Lock()
	defer func() {
		// Reset state for accompany resource and target resource if there is an error
		if respErr != nil {
			if isAccompanyResourcePlugin {
				_ = p.removeContainer(req.ResourceRequest.PodUid, req.ResourceRequest.ContainerName, v1.ResourceName(req.AccompanyResourceName))
			}
			if isAccompanyCustomDevicePlugin {
				accompanyDeviceType, _ := p.GetResourceTypeFromDeviceName(req.AccompanyResourceName)
				if accompanyDeviceType != "" {
					_ = p.removeContainer(req.ResourceRequest.PodUid, req.ResourceRequest.ContainerName, v1.ResourceName(accompanyDeviceType))
				}
			}
			deviceType, _ := p.GetResourceTypeFromDeviceName(req.DeviceName)
			if deviceType != "" {
				_ = p.removeContainer(req.ResourceRequest.PodUid, req.ResourceRequest.ContainerName, v1.ResourceName(deviceType))
			}
		}
		p.Unlock()
	}()

	// Find the target device that we want to allocate for
	var targetDeviceReq *pluginapi.DeviceRequest
	for _, deviceRequest := range req.DeviceRequest {
		if deviceRequest.DeviceName == req.DeviceName {
			targetDeviceReq = deviceRequest
		}
	}

	if targetDeviceReq == nil {
		return nil, fmt.Errorf("no target device plugin found for target device %s", req.DeviceName)
	}

	// Allocate accompany resource
	// Check if accompany resource maps to a resource plugin; if it does, allocate it first
	accompanyResourcePlugin := p.getResourcePlugin(req.AccompanyResourceName)
	if accompanyResourcePlugin != nil {
		_, err := accompanyResourcePlugin.Allocate(req.ResourceRequest, targetDeviceReq)
		if err != nil {
			return nil, fmt.Errorf("allocate accompany resource %s failed with error: %v", req.AccompanyResourceName, err)
		}
		isAccompanyResourcePlugin = true
	} else {
		// Accompany resource maps to a custom device plugin; allocate for it
		accompanyCustomDevicePlugin := p.getCustomDevicePlugin(req.AccompanyResourceName)
		if accompanyCustomDevicePlugin != nil {
			// Get device request for accompany device
			var accompanyDeviceReq *pluginapi.DeviceRequest
			for _, deviceRequest := range req.DeviceRequest {
				if deviceRequest.DeviceName == req.AccompanyResourceName {
					accompanyDeviceReq = deviceRequest
				}
			}

			if accompanyDeviceReq == nil {
				return nil, fmt.Errorf("nil accompany device request")
			}

			_, err := accompanyCustomDevicePlugin.AllocateAssociatedDevice(req.ResourceRequest, accompanyDeviceReq, "")
			if err != nil {
				return nil, fmt.Errorf("AllocateAssociatedDevice accompany resource %s failed with error: %v", req.AccompanyResourceName, err)
			}
			isAccompanyCustomDevicePlugin = true
		}
	}

	// Allocate target custom device
	targetCustomDevicePlugin := p.getCustomDevicePlugin(req.DeviceName)
	if targetCustomDevicePlugin == nil {
		return nil, fmt.Errorf("no custom device plugin found for target device %s", req.DeviceName)
	}

	resp, respErr = targetCustomDevicePlugin.AllocateAssociatedDevice(req.ResourceRequest, targetDeviceReq, req.AccompanyResourceName)
	return resp, respErr
}

func (p *StaticPolicy) registerResourcePlugins() error {
	for name := range p.resourcePluginsNames {
		initFunc := resourcepluginregistry.ResourcePluginsMap[name]
		resourcePlugin := initFunc(p.BasePlugin)
		p.resourcePlugins[resourcePlugin.ResourceName()] = resourcePlugin
	}
	return nil
}

func (p *StaticPolicy) registerCustomDevicePlugins() error {
	for name := range p.associatedDeviceNames {
		initFunc := devicepluginregistry.CustomDevicePluginsMap[name]
		customDevicePlugin := initFunc(p.BasePlugin)
		deviceNames := customDevicePlugin.DeviceNames()
		for _, deviceName := range deviceNames {
			p.customDevicePlugins[deviceName] = customDevicePlugin
		}
	}
	return nil
}

func (p *StaticPolicy) getResourcePlugin(resourceName string) resourceplugin.ResourcePlugin {
	resourcePlugin := p.resourcePlugins[resourceName]
	return resourcePlugin
}

func (p *StaticPolicy) getCustomDevicePlugin(deviceName string) customdeviceplugin.CustomDevicePlugin {
	customDevicePlugin := p.customDevicePlugins[deviceName]
	return customDevicePlugin
}
