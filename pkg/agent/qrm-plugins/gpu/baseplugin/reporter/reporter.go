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

package reporter

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/gogo/protobuf/proto"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager"

	nodev1alpha1 "github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/plugins/registration"
	"github.com/kubewharf/katalyst-api/pkg/plugins/skeleton"
	"github.com/kubewharf/katalyst-api/pkg/protocol/reporterplugin/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/state"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

const (
	gpuReporterPluginName   = "gpu-reporter-plugin"
	propertyNameGPUTopology = "gpu_topology_attribute_key"
)

var zeroQuantity = *resource.NewQuantity(0, resource.DecimalSI)

// GPUReporter reports gpu information to CNR
type GPUReporter interface {
	Run(stopCh <-chan struct{})
	Trigger()
}

type gpuReporterImpl struct {
	skeleton.GenericPlugin
	plugin *gpuReporterPlugin
}

var _ GPUReporter = (*gpuReporterImpl)(nil)

func NewGPUReporter(emitter metrics.MetricEmitter, metaServer *metaserver.MetaServer,
	conf *config.Configuration, topologyRegistry *machine.DeviceTopologyRegistry, stateGetter func() state.State, deviceTypeToNames map[string]sets.String,
) (GPUReporter, error) {
	plugin, reporter, err := newGPUReporterPlugin(emitter, metaServer, conf, topologyRegistry, stateGetter, deviceTypeToNames)
	if err != nil {
		return nil, fmt.Errorf("[gpu-reporter] create reporter failed: %v", err)
	}

	return &gpuReporterImpl{GenericPlugin: plugin, plugin: reporter}, nil
}

func (r *gpuReporterImpl) Trigger() {
	r.plugin.Trigger()
}

func (r *gpuReporterImpl) Run(stopCh <-chan struct{}) {
	if err := r.Start(); err != nil {
		klog.Fatalf("[gpu reporter] start %v failed with error: %v", r.Name(), err)
	}
	klog.Infof("[gpu-reporter] plugin wrapper %v started", r.Name())

	defer func() {
		if err := r.Stop(); err != nil {
			klog.Errorf("[gpu-reporter] stop %v failed with error: %v", r.Name(), err)
		}
	}()

	<-stopCh
}

// gpuReporterPlugin is the plugin that reports gpu device topology information
type gpuReporterPlugin struct {
	sync.RWMutex
	started    bool
	ctx        context.Context
	cancel     context.CancelFunc
	emitter    metrics.MetricEmitter
	metaServer *metaserver.MetaServer

	gpuDeviceNames         []string
	numaSocketZoneNodeMap  map[util.ZoneNode]util.ZoneNode
	deviceTopologyRegistry *machine.DeviceTopologyRegistry
	stateGetter            func() state.State
	deviceTypeToNames      map[string]sets.String

	reportNotifyCh    chan struct{}
	lastReportContent *v1alpha1.GetReportContentResponse
	checkpointManager checkpointmanager.CheckpointManager
}

var (
	_ skeleton.GenericPlugin        = (*gpuReporterPlugin)(nil)
	_ v1alpha1.ReporterPluginServer = (*gpuReporterPlugin)(nil)
)

func newGPUReporterPlugin(emitter metrics.MetricEmitter, metaServer *metaserver.MetaServer,
	conf *config.Configuration, topologyRegistry *machine.DeviceTopologyRegistry, stateGetter func() state.State, deviceTypeToNames map[string]sets.String,
) (skeleton.GenericPlugin, *gpuReporterPlugin, error) {
	checkpointManager, err := checkpointmanager.NewCheckpointManager(conf.KubeletDevicePluginPath)
	if err != nil {
		klog.Warningf("[gpu-reporter] new checkpoint manager failed: %v", err)
	}

	reporter := &gpuReporterPlugin{
		gpuDeviceNames:         conf.GPUDeviceNames,
		numaSocketZoneNodeMap:  util.GenerateNumaSocketZone(metaServer.MachineInfo.Topology),
		emitter:                emitter,
		deviceTopologyRegistry: topologyRegistry,
		stateGetter:            stateGetter,
		deviceTypeToNames:      deviceTypeToNames,
		reportNotifyCh:         make(chan struct{}, 1),
		metaServer:             metaServer,
		checkpointManager:      checkpointManager,
	}
	pluginWrapper, err := skeleton.NewRegistrationPluginWrapper(reporter, []string{conf.PluginRegistrationDir},
		func(key string, value int64) {
			_ = emitter.StoreInt64(key, value, metrics.MetricTypeNameCount, metrics.ConvertMapToTags(map[string]string{
				"pluginName": gpuReporterPluginName,
				"pluginType": registration.ReporterPlugin,
			})...)
		})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to register %s plugin: %w", gpuReporterPluginName, err)
	}

	return pluginWrapper, reporter, nil
}

func (p *gpuReporterPlugin) Name() string {
	return gpuReporterPluginName
}

func (p *gpuReporterPlugin) Start() (err error) {
	p.Lock()
	defer func() {
		if err == nil {
			p.started = true
		}
		p.Unlock()
	}()

	if p.started {
		return
	}

	p.ctx, p.cancel = context.WithCancel(context.Background())
	return
}

func (p *gpuReporterPlugin) Stop() error {
	p.Lock()
	defer func() {
		p.started = false
		p.Unlock()
	}()

	if !p.started {
		return nil
	}

	p.cancel()
	return nil
}

// GetReportContent implements ReporterPluginServer to report the gpu device topology information to CNR.
func (p *gpuReporterPlugin) GetReportContent(ctx context.Context, _ *v1alpha1.Empty) (*v1alpha1.GetReportContentResponse, error) {
	p.RLock()
	if p.lastReportContent != nil {
		resp := p.lastReportContent
		p.RUnlock()
		return resp, nil
	}
	p.RUnlock()

	resp, err := p.buildReportResponse()
	if err != nil {
		return nil, err
	}

	p.Lock()
	if p.lastReportContent != nil {
		resp = p.lastReportContent
	} else {
		p.lastReportContent = resp
	}
	p.Unlock()

	return resp, nil
}

func (p *gpuReporterPlugin) buildReportResponse() (*v1alpha1.GetReportContentResponse, error) {
	// The reporter picks the latest topology from all configured GPU devices to report to CNR.
	topologiesMap, err := p.deviceTopologyRegistry.GetDeviceTopologies(p.gpuDeviceNames)
	if err != nil {
		return nil, err
	}
	latestDeviceTopology := machine.PickLatestDeviceTopology(topologiesMap)

	stateImpl := p.stateGetter()
	if stateImpl == nil {
		return nil, fmt.Errorf("state is nil")
	}

	machineState := stateImpl.GetMachineState()
	if machineState == nil {
		return nil, fmt.Errorf("machine state is nil")
	}

	var reportFields []*v1alpha1.ReportField

	zoneField, err := p.getTopologyZoneReportField(topologiesMap, latestDeviceTopology, machineState)
	if err != nil {
		return nil, err
	}
	reportFields = append(reportFields, zoneField)

	propertyField, err := p.getResourcePropertyReportField(latestDeviceTopology)
	if err != nil {
		return nil, err
	}
	if propertyField != nil {
		reportFields = append(reportFields, propertyField)
	} else {
		// when resourceProperty is nil, we choose not to report NodeResourceProperties instead of returning error
		general.Warningf("no resource property found for device topology, skip reporting %s", util.CNRFieldNameNodeResourceProperties)
	}

	return &v1alpha1.GetReportContentResponse{
		Content: []*v1alpha1.ReportContent{
			{
				GroupVersionKind: &util.CNRGroupVersionKind,
				Field:            reportFields,
			},
		},
	}, nil
}

func (p *gpuReporterPlugin) getTopologyZoneReportField(topologiesMap map[string]*machine.DeviceTopology, latestDeviceTopology *machine.DeviceTopology, machineState state.AllocationResourcesMap) (*v1alpha1.ReportField, error) {
	// generate the zones for numa and socket in machine
	topologyZoneGenerator, err := util.NewNumaSocketTopologyZoneGenerator(p.numaSocketZoneNodeMap)
	if err != nil {
		return nil, fmt.Errorf("failed to create topology zone generator: %w", err)
	}

	// add the GPU zone nodes in and generate their topology zones by merging their resources and attributes
	if err = p.addGPUZoneNodes(latestDeviceTopology, topologyZoneGenerator); err != nil {
		return nil, err
	}

	zoneAttributes := p.getGPUZoneAttributes(latestDeviceTopology)
	if zoneAttributes == nil {
		return nil, fmt.Errorf("no zone attributes found for device topology")
	}

	zoneResources := p.getZoneResources(topologiesMap, machineState)
	if zoneResources == nil {
		return nil, fmt.Errorf("no zone resources found for device topology")
	}

	zoneAllocations, err := p.getZoneAllocations(machineState)
	if err != nil {
		return nil, err
	}

	generatedTopologyZones := topologyZoneGenerator.GenerateTopologyZoneStatus(zoneAllocations, zoneResources,
		zoneAttributes, nil, nil, nil)

	zoneValues, err := json.Marshal(&generatedTopologyZones)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal topology zone values: %w", err)
	}

	return &v1alpha1.ReportField{
		FieldType: v1alpha1.FieldType_Status,
		FieldName: util.CNRFieldNameTopologyZone,
		Value:     zoneValues,
	}, nil
}

func (p *gpuReporterPlugin) getResourcePropertyReportField(latestDeviceTopology *machine.DeviceTopology) (*v1alpha1.ReportField, error) {
	resourceProperty := p.getGPUResourceProperty(latestDeviceTopology)
	if resourceProperty == nil {
		return nil, nil
	}

	propertyValues, err := json.Marshal(&resourceProperty)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal resource property values: %w", err)
	}

	return &v1alpha1.ReportField{
		FieldType: v1alpha1.FieldType_Spec,
		FieldName: util.CNRFieldNameNodeResourceProperties,
		Value:     propertyValues,
	}, nil
}

// getGPUResourceProperty returns the different dimensions to differentiate affinity priority of gpu devices.
func (p *gpuReporterPlugin) getGPUResourceProperty(deviceTopology *machine.DeviceTopology) []*nodev1alpha1.Property {
	if deviceTopology == nil || deviceTopology.PriorityDimensions == nil {
		return nil
	}

	return []*nodev1alpha1.Property{
		{
			PropertyName:   propertyNameGPUTopology,
			PropertyValues: deviceTopology.PriorityDimensions,
		},
	}
}

// getGPUZoneAttributes returns the map of gpu zone nodes to their attributes
func (p *gpuReporterPlugin) getGPUZoneAttributes(deviceTopology *machine.DeviceTopology) map[util.ZoneNode]util.ZoneAttributes {
	if deviceTopology == nil {
		return nil
	}

	zoneAttributes := make(map[util.ZoneNode]util.ZoneAttributes)

	for id, device := range deviceTopology.Devices {
		dimensions := device.GetDimensions()
		zoneNode := util.GenerateDeviceZoneNode(id, string(nodev1alpha1.TopologyTypeGPU))

		attributes := make([]nodev1alpha1.Attribute, 0, len(deviceTopology.PriorityDimensions))
		for _, dimName := range deviceTopology.PriorityDimensions {
			dimValue, ok := dimensions[dimName]
			if !ok {
				general.Warningf("failed to find dimension %s for device %s", dimName, id)
				continue
			}

			attributes = append(attributes, nodev1alpha1.Attribute{
				Name:  dimName,
				Value: dimValue,
			})
		}

		zoneAttributes[zoneNode] = attributes
	}

	return zoneAttributes
}

// getZoneResources returns the map of gpu zone nodes to their resources
// it merges resources from different device names (e.g. nvidia.com/gpu) for the same physical device ID
func (p *gpuReporterPlugin) getZoneResources(topologiesMap map[string]*machine.DeviceTopology, machineState state.AllocationResourcesMap) map[util.ZoneNode]nodev1alpha1.Resources {
	if len(topologiesMap) == 0 {
		return nil
	}

	// 1. first construct temporary map from device ID to resources to merge resources for the same device
	idToResources := make(map[string]nodev1alpha1.Resources)
	for resourceName, allocMap := range machineState {
		for id, allocState := range allocMap {
			var allocatableQuantity resource.Quantity
			var capacityQuantity resource.Quantity

			if deviceNames, ok := p.deviceTypeToNames[string(resourceName)]; ok {
				for deviceName := range deviceNames {
					topology, ok := topologiesMap[deviceName]
					if !ok {
						continue
					}

					healthy, deviceOk := topology.IsDeviceHealthy(id)
					if !deviceOk {
						continue
					}
					// Allocatable: 1 is reported when device is healthy, 0 is reported when device is unhealthy
					if !healthy {
						allocatableQuantity = zeroQuantity
					} else {
						allocatableQuantity = *resource.NewQuantity(int64(allocState.Allocatable), resource.DecimalSI)
					}

					capacityQuantity = *resource.NewQuantity(int64(allocState.Allocatable), resource.DecimalSI)

					resources, ok := idToResources[id]
					if !ok {
						resources = nodev1alpha1.Resources{
							Allocatable: &v1.ResourceList{},
							Capacity:    &v1.ResourceList{},
						}
					}

					(*resources.Allocatable)[v1.ResourceName(deviceName)] = allocatableQuantity
					(*resources.Capacity)[v1.ResourceName(deviceName)] = capacityQuantity
					idToResources[id] = resources
				}
			} else {
				allocatableQuantity = *resource.NewQuantity(int64(allocState.Allocatable), resource.DecimalSI)
				capacityQuantity = *resource.NewQuantity(int64(allocState.Allocatable), resource.DecimalSI)

				resources, ok := idToResources[id]
				if !ok {
					resources = nodev1alpha1.Resources{
						Allocatable: &v1.ResourceList{},
						Capacity:    &v1.ResourceList{},
					}
				}

				(*resources.Allocatable)[resourceName] = allocatableQuantity
				(*resources.Capacity)[resourceName] = capacityQuantity
				idToResources[id] = resources
			}
		}
	}

	// 2. then construct final zoneResources map from ZoneNode to Resources
	zoneResources := make(map[util.ZoneNode]nodev1alpha1.Resources)
	for id, resources := range idToResources {
		zoneNode := util.GenerateDeviceZoneNode(id, string(nodev1alpha1.TopologyTypeGPU))
		zoneResources[zoneNode] = resources
	}

	return zoneResources
}

// getZoneAllocations returns the map of gpu zone nodes to their pod allocations
func (p *gpuReporterPlugin) getZoneAllocations(machineState state.AllocationResourcesMap) (map[util.ZoneNode]util.ZoneAllocations, error) {
	// First construct map of device id to allocations
	idToAllocations := make(map[string]util.ZoneAllocations)

	// Add allocations from machine state
	p.addStateAllocations(idToAllocations, machineState)

	// Add allocations from kubelet device manager checkpoint
	if err := p.addKubeletCheckpointAllocations(idToAllocations); err != nil {
		return nil, err
	}

	// Then construct the final zone allocations from the map of device id to allocations
	zoneAllocations := make(map[util.ZoneNode]util.ZoneAllocations)
	for id, allocations := range idToAllocations {
		zoneNode := util.GenerateDeviceZoneNode(id, string(nodev1alpha1.TopologyTypeGPU))
		zoneAllocations[zoneNode] = allocations
	}

	return zoneAllocations, nil
}

// addStateAllocations merges the allocations stored in the local machine state
// (Katalyst's QRM state) into the target idToAllocations map. This map acts as
// an intermediate state mapping device IDs to their corresponding pod allocations.
func (p *gpuReporterPlugin) addStateAllocations(idToAllocations map[string]util.ZoneAllocations, machineState state.AllocationResourcesMap) {
	for resourceName, allocMap := range machineState {
		for id, allocState := range allocMap {
			if _, ok := idToAllocations[id]; !ok {
				idToAllocations[id] = make(util.ZoneAllocations, 0)
			}

			podEntries := allocState.PodEntries

			for podUID, containerEntries := range podEntries {
				// Get any pod namespace and pod name from allocationMeta
				for _, allocInfo := range containerEntries {
					podNamespace := allocInfo.PodNamespace
					podName := allocInfo.PodName

					// Override the resource name if there is a specified device name
					if allocInfo.DeviceName != "" {
						resourceName = v1.ResourceName(allocInfo.DeviceName)
					}

					allocated := allocInfo.AllocatedAllocation
					gpuResourceList := make(v1.ResourceList)
					gpuResourceList[resourceName] = *resource.NewQuantity(int64(allocated.Quantity), resource.DecimalSI)

					idToAllocations[id] = append(idToAllocations[id], &nodev1alpha1.Allocation{
						Consumer: native.GenerateNamespaceNameUIDKey(podNamespace, podName, podUID),
						Requests: &gpuResourceList,
					})
				}
			}
		}
	}
}

// addKubeletCheckpointAllocations retrieves and merges allocations from the kubelet device manager checkpoint
// into the target idToAllocations map. This prevents reporting inconsistencies where a device is already
// allocated to a pod by kubelet, but the local QRM state has not yet fully synced or recorded the allocation.
func (p *gpuReporterPlugin) addKubeletCheckpointAllocations(idToAllocations map[string]util.ZoneAllocations) error {
	if p.checkpointManager == nil {
		general.Warningf("kubelet checkpoint manager is nil")
		return nil
	}

	checkpointData, err := native.GetKubeletCheckpoint(p.checkpointManager)
	if err != nil {
		general.Warningf("failed to get kubelet checkpoint: %v", err)
		return nil
	}

	podDeviceEntries, _ := checkpointData.GetDataInLatestFormat()

	for _, entry := range podDeviceEntries {
		resourceName := v1.ResourceName(entry.ResourceName)

		// Iterate through all devices per NUMA node
		for numaNode, deviceIDs := range entry.DeviceIDs {
			for _, deviceID := range deviceIDs {
				if _, ok := idToAllocations[deviceID]; !ok {
					idToAllocations[deviceID] = make(util.ZoneAllocations, 0)
				}

				// Check if there's already an allocation for this pod UID
				if hasExistingPodAllocation(idToAllocations[deviceID], entry.PodUID) {
					continue
				}

				// Create resource list - quantity is 1 since each device entry represents one device
				gpuResourceList := make(v1.ResourceList)
				gpuResourceList[resourceName] = *resource.NewQuantity(1, resource.DecimalSI)

				// Find the pod from the metaserver to get its namespace and name
				pod, err := p.metaServer.GetPod(p.ctx, entry.PodUID)
				if err != nil {
					general.Errorf("failed to get pod %s/%s: %v", entry.PodUID, entry.PodUID, err)
					return fmt.Errorf("failed to get pod %s/%s: %w", entry.PodUID, entry.PodUID, err)
				}

				// Generate consumer key using namespace, name and podUID
				consumer := native.GenerateNamespaceNameUIDKey(pod.Namespace, pod.Name, entry.PodUID)

				idToAllocations[deviceID] = append(idToAllocations[deviceID], &nodev1alpha1.Allocation{
					Consumer: consumer,
					Requests: &gpuResourceList,
				})

				general.Infof("added allocation from checkpoint: pod=%s, container=%s, device=%s, numa=%d",
					entry.PodUID, entry.ContainerName, deviceID, numaNode)
			}
		}
	}

	return nil
}

// addGPUZoneNodes adds the gpu zone nodes to the topology zone generator
func (p *gpuReporterPlugin) addGPUZoneNodes(deviceTopology *machine.DeviceTopology, generator *util.TopologyZoneGenerator) error {
	if deviceTopology == nil {
		return nil
	}

	var errList []error

	for id, device := range deviceTopology.Devices {
		deviceNode := util.GenerateDeviceZoneNode(id, string(nodev1alpha1.TopologyTypeGPU))
		for _, numaNode := range device.NumaNodes {
			numaZoneNode := util.GenerateNumaZoneNode(numaNode)
			err := generator.AddNode(&numaZoneNode, deviceNode)
			if err != nil {
				errList = append(errList, err)
			}
		}
	}

	return utilerrors.NewAggregate(errList)
}

// hasExistingPodAllocation verifies whether a specific pod UID already exists in the given allocations.
// This is used to deduplicate allocations when merging states from both Katalyst QRM and the Kubelet checkpoint.
func hasExistingPodAllocation(allocations util.ZoneAllocations, podUID string) bool {
	for _, existingAlloc := range allocations {
		_, _, existingUID, err := native.ParseNamespaceNameUIDKey(existingAlloc.Consumer)
		if err == nil && existingUID == podUID {
			return true
		}
	}
	return false
}

// ListAndWatchReportContent implements ReporterPluginServer to list and watch report content.
func (p *gpuReporterPlugin) ListAndWatchReportContent(_ *v1alpha1.Empty, server v1alpha1.ReporterPlugin_ListAndWatchReportContentServer) error {
	isFirst := true
	var lastSentContent *v1alpha1.GetReportContentResponse

	for {
		resp, err := p.buildReportResponse()
		if err != nil {
			general.Errorf("failed to build report response: %v", err)
			return err
		}

		p.Lock()
		p.lastReportContent = resp
		p.Unlock()

		// Send report only when it's the first time or the content has changed
		if isFirst || !proto.Equal(lastSentContent, resp) {
			if err := server.Send(resp); err != nil {
				general.Errorf("failed to send report content: %v", err)
				return err
			}
			general.Infof("successfully sent report content to reporter manager, content: %v", resp)
			lastSentContent = resp
			isFirst = false
		} else {
			general.Infof("report content unchanged, skip sending")
		}

		select {
		case <-p.ctx.Done():
			general.Infof("reporter plugin context done, stop watching report content")
			return nil
		case <-server.Context().Done():
			general.Infof("reporter server context done, stop watching report content")
			return nil
		case <-p.reportNotifyCh:
			general.Infof("received report notify trigger, start to rebuild and send report")
		}
	}
}

// Trigger invalidates the cached report content and triggers a new report to be built and sent
func (p *gpuReporterPlugin) Trigger() {
	p.Lock()
	p.lastReportContent = nil
	p.Unlock()

	// Use non-blocking channel send to avoid blocking the caller (e.g. state/topology updates)
	select {
	case p.reportNotifyCh <- struct{}{}:
		general.Infof("triggered report content update")
	default:
		// If the channel is full, a trigger is already pending, so we don't need to block or send another one.
	}
}
