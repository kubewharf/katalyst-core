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
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/gogo/protobuf/proto"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager"

	nodev1alpha1 "github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/plugins/registration"
	"github.com/kubewharf/katalyst-api/pkg/plugins/skeleton"
	"github.com/kubewharf/katalyst-api/pkg/protocol/reporterplugin/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/kubelet"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/state"
	"github.com/kubewharf/katalyst-core/pkg/config"
	pkgconsts "github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	metaserverpod "github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	"github.com/kubewharf/katalyst-core/pkg/util/metric"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

const (
	gpuReporterPluginName                       = "gpu-reporter-plugin"
	defaultReportRetryInterval                  = 5 * time.Second
	metricGetPodListFailed                      = "qrm_gpu_reporter_get_pod_list_failed"
	metricAddKubeletCheckpointAllocationsFailed = "qrm_gpu_reporter_add_kubelet_checkpoint_allocations_failed"
	metricEnsureKubeletDevicePluginPathFailed   = "qrm_gpu_reporter_ensure_kubelet_device_plugin_path_failed"
	metricAddGPUZoneNodesFallbackNUMA           = "qrm_gpu_reporter_add_gpu_zone_nodes_fallback_numa"
	// fallbackNUMANodeID is used when a device has no NUMA nodes reported;
	// the device is attached under this NUMA node so its zone is still emitted.
	fallbackNUMANodeID = 0
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
	kubeletCheckpointManager checkpointmanager.CheckpointManager,
) (GPUReporter, error) {
	plugin, reporter, err := newGPUReporterPlugin(emitter, metaServer, conf, topologyRegistry, stateGetter, deviceTypeToNames,
		kubeletCheckpointManager)
	if err != nil {
		return nil, fmt.Errorf("create reporter failed: %v", err)
	}

	return &gpuReporterImpl{GenericPlugin: plugin, plugin: reporter}, nil
}

func (r *gpuReporterImpl) Trigger() {
	r.plugin.Trigger()
}

func (r *gpuReporterImpl) Run(stopCh <-chan struct{}) {
	if err := r.Start(); err != nil {
		general.Fatalf("start %v failed with error: %v", r.Name(), err)
	}
	general.Infof("plugin wrapper %v started", r.Name())

	defer func() {
		if err := r.Stop(); err != nil {
			general.Errorf("stop %v failed with error: %v", r.Name(), err)
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
	rdmaDeviceNames        []string
	numaSocketZoneNodeMap  map[util.ZoneNode]util.ZoneNode
	deviceTopologyRegistry *machine.DeviceTopologyRegistry
	stateGetter            func() state.State
	deviceTypeToNames      map[string]sets.String

	reportNotifyCh                  chan struct{}
	reportRetryInterval             time.Duration
	lastReportContent               *v1alpha1.GetReportContentResponse
	kubeletCheckpointManager        checkpointmanager.CheckpointManager
	kubeletDevicePluginPath         string
	enableKubeletCheckpointFallback bool
}

var (
	_ skeleton.GenericPlugin        = (*gpuReporterPlugin)(nil)
	_ v1alpha1.ReporterPluginServer = (*gpuReporterPlugin)(nil)
)

func newGPUReporterPlugin(emitter metrics.MetricEmitter, metaServer *metaserver.MetaServer,
	conf *config.Configuration, topologyRegistry *machine.DeviceTopologyRegistry, stateGetter func() state.State, deviceTypeToNames map[string]sets.String,
	kubeletCheckpointManager checkpointmanager.CheckpointManager,
) (skeleton.GenericPlugin, *gpuReporterPlugin, error) {
	reporter := &gpuReporterPlugin{
		gpuDeviceNames:                  conf.GPUDeviceNames,
		rdmaDeviceNames:                 conf.RDMADeviceNames,
		numaSocketZoneNodeMap:           util.GenerateNumaSocketZone(metaServer.MachineInfo.Topology),
		emitter:                         emitter,
		deviceTopologyRegistry:          topologyRegistry,
		stateGetter:                     stateGetter,
		deviceTypeToNames:               deviceTypeToNames,
		reportNotifyCh:                  make(chan struct{}, 1),
		reportRetryInterval:             defaultReportRetryInterval,
		metaServer:                      metaServer,
		kubeletCheckpointManager:        kubeletCheckpointManager,
		kubeletDevicePluginPath:         conf.KubeletDevicePluginPath,
		enableKubeletCheckpointFallback: conf.EnableKubeletCheckpointFallback,
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

	if p.enableKubeletCheckpointFallback && p.kubeletDevicePluginPath != "" {
		err = general.EnsureDirectory(p.kubeletDevicePluginPath)
		if err != nil {
			if p.emitter != nil {
				_ = p.emitter.StoreInt64(metricEnsureKubeletDevicePluginPathFailed, 1, metrics.MetricTypeNameRaw,
					metrics.MetricTag{Key: "error_message", Val: metric.MetricTagValueFormat(err)},
					metrics.MetricTag{Key: "path", Val: metric.MetricTagValueFormat(p.kubeletDevicePluginPath)})
			}
			return fmt.Errorf("ensure kubelet device plugin path %s exists failed: %w", p.kubeletDevicePluginPath, err)
		}

		watcherCh, err := general.RegisterFileEventWatcher(
			p.ctx.Done(),
			general.FileWatcherInfo{
				Path:     []string{p.kubeletDevicePluginPath},
				Filename: native.KubeletDeviceManagerCheckpoint,
				Op:       fsnotify.Create,
			},
		)
		if err != nil {
			return fmt.Errorf("register file watcher failed: %w", err)
		}

		go func() {
			ticker := time.NewTicker(30 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-watcherCh:
					general.Infof("kubelet device plugin checkpoint changed, trigger report")
					p.Trigger()
				case <-ticker.C:
					general.Infof("periodic ticker fired, trigger report")
					p.Trigger()
				case <-p.ctx.Done():
					general.Infof("file watcher stopped for kubelet device plugin checkpoint")
					return
				}
			}
		}()
	} else {
		general.Infof("kubelet device plugin checkpoint fallback is disabled or path is empty, skip watching kubelet device plugin checkpoint")
	}

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
	topologiesMap, ok := p.deviceTopologyRegistry.GetDeviceTopologies(p.gpuDeviceNames)
	if !ok {
		return nil, fmt.Errorf("failed to get any device topology")
	}
	latestDeviceTopology, _ := machine.PickLatestDeviceTopology(topologiesMap)

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

	zoneAllocations, err := p.getZoneAllocations(topologiesMap, machineState)
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
	properties := p.getGPUResourceProperty(latestDeviceTopology)
	if rdmaProperty := p.getRDMAResourceProperty(); rdmaProperty != nil {
		properties = append(properties, rdmaProperty)
	}

	if len(properties) == 0 {
		return nil, nil
	}

	propertyValues, err := json.Marshal(&properties)
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
	if deviceTopology == nil || len(deviceTopology.PriorityDimensions) == 0 {
		return nil
	}

	return []*nodev1alpha1.Property{
		{
			PropertyName:   pkgconsts.PropertyNameGPUTopology,
			PropertyValues: deviceTopology.PriorityDimensions,
		},
	}
}

// getRDMAResourceProperty reports whether any RDMA device has affinity with
// any GPU device, as a single property with value "true" or "false".
func (p *gpuReporterPlugin) getRDMAResourceProperty() *nodev1alpha1.Property {
	if len(p.rdmaDeviceNames) == 0 || len(p.gpuDeviceNames) == 0 {
		return nil
	}

	hasAffinity := p.deviceTopologyRegistry.HasAnyDeviceAffinity(p.gpuDeviceNames, p.rdmaDeviceNames)

	value := "false"
	if hasAffinity {
		value = "true"
	}

	return &nodev1alpha1.Property{
		PropertyName:   pkgconsts.PropertyNameRDMAAffinityWithGPU,
		PropertyValues: []string{value},
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
func (p *gpuReporterPlugin) getZoneAllocations(topologiesMap map[string]*machine.DeviceTopology, machineState state.AllocationResourcesMap) (map[util.ZoneNode]util.ZoneAllocations, error) {
	// First construct map of device id to allocations
	idToAllocations := make(map[string]util.ZoneAllocations)

	// Add allocations from machine state
	p.addStateAllocations(topologiesMap, idToAllocations, machineState)

	// Add allocations from kubelet device manager checkpoint as a fallback.
	if p.enableKubeletCheckpointFallback {
		if err := p.addKubeletCheckpointAllocations(idToAllocations); err != nil {
			if p.emitter != nil {
				_ = p.emitter.StoreInt64(metricAddKubeletCheckpointAllocationsFailed, 1, metrics.MetricTypeNameRaw,
					metrics.MetricTag{Key: "error_message", Val: metric.MetricTagValueFormat(err)})
			}
			return nil, err
		}
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
func (p *gpuReporterPlugin) addStateAllocations(topologiesMap map[string]*machine.DeviceTopology, idToAllocations map[string]util.ZoneAllocations, machineState state.AllocationResourcesMap) {
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
						// Skip reporting if it is not a GPU device
						if _, ok := topologiesMap[allocInfo.DeviceName]; !ok {
							continue
						}

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
	if p.kubeletCheckpointManager == nil {
		return fmt.Errorf("kubelet checkpoint manager is nil")
	}

	kubeletAllocations, err := kubelet.ReadAllocations(p.kubeletCheckpointManager, sets.NewString(p.gpuDeviceNames...))
	if err != nil {
		return err
	}
	if len(kubeletAllocations) == 0 {
		return nil
	}

	activePods, err := p.metaServer.GetPodList(context.WithValue(p.ctx, metaserverpod.BypassCacheKey, metaserverpod.BypassCacheTrue), native.PodIsActive)
	if err != nil {
		general.Warningf("failed to get active pod list: %v", err)
		if p.emitter != nil {
			_ = p.emitter.StoreInt64(metricGetPodListFailed, 1, metrics.MetricTypeNameRaw)
		}
	}
	activePodMap := native.GetPodKeyMap(activePods, func(obj metav1.Object) string {
		return string(obj.GetUID())
	})

	for _, entry := range kubeletAllocations {
		resourceName := v1.ResourceName(entry.ResourceName)

		pod, ok := activePodMap[entry.PodUID]
		if !ok {
			general.Warningf("pod %s is not active or not found, skipping pod", entry.PodUID)
			continue
		}

		// Generate consumer key using namespace, name and podUID
		consumer := native.GenerateNamespaceNameUIDKey(pod.Namespace, pod.Name, entry.PodUID)

		for _, deviceID := range entry.DeviceIDs {
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

			idToAllocations[deviceID] = append(idToAllocations[deviceID], &nodev1alpha1.Allocation{
				Consumer: consumer,
				Requests: &gpuResourceList,
			})

			general.Infof("added allocation from checkpoint: consumer=%s, container=%s, device=%s",
				consumer, entry.ContainerName, deviceID)
		}
	}

	return nil
}

// addGPUZoneNodes adds the gpu zone nodes to the topology zone generator.
func (p *gpuReporterPlugin) addGPUZoneNodes(deviceTopology *machine.DeviceTopology, generator *util.TopologyZoneGenerator) error {
	if deviceTopology == nil {
		return nil
	}

	var errList []error

	for id, device := range deviceTopology.Devices {
		deviceNode := util.GenerateDeviceZoneNode(id, string(nodev1alpha1.TopologyTypeGPU))

		numaNodes := device.NumaNodes
		// If a device has no NUMA nodes, it is attached under NUMA fallbackNUMANodeID
		// so its zone is still emitted; a warning log and a counter metric are recorded.
		if len(numaNodes) == 0 {
			general.Warningf("device %s has no NUMA nodes; defaulting to NUMA %d", id, fallbackNUMANodeID)
			if p.emitter != nil {
				_ = p.emitter.StoreInt64(metricAddGPUZoneNodesFallbackNUMA, 1, metrics.MetricTypeNameCount,
					metrics.MetricTag{Key: "device_id", Val: id})
			}
			numaNodes = []int{fallbackNUMANodeID}
		}

		for _, numaNode := range numaNodes {
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
	var retryCh <-chan time.Time

	for {
		resp, err := p.buildReportResponse()
		if err != nil {
			general.Errorf("failed to build report response: %v, will retry in %s", err, p.reportRetryInterval)
			// Start a retry timer to ensure the listwatch stream doesn't break due to intermittent failures (e.g. state/topology parsing error)
			retryCh = time.After(p.reportRetryInterval)
		} else {
			// Clear retry timer if build succeeds
			retryCh = nil
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
		case <-retryCh:
			general.Infof("retry timer fired, start to rebuild and send report")
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
