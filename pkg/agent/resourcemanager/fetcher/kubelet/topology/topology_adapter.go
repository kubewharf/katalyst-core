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

package topology

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/jaypipes/ghw/pkg/pci"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	info "github.com/google/cadvisor/info/v1"
	"github.com/pkg/errors"
	"google.golang.org/grpc"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"
	podresv1 "k8s.io/kubelet/pkg/apis/podresources/v1"

	"github.com/jaypipes/ghw"
	nodev1alpha1 "github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-api/pkg/utils"
	"github.com/kubewharf/katalyst-core/pkg/agent/resourcemanager/fetcher/util/kubelet/podresources"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	metaserverpod "github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	"github.com/kubewharf/katalyst-core/pkg/util"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

const (
	podResourcesClientTimeout    = 10 * time.Second
	podResourcesClientMaxMsgSize = 1024 * 1024 * 16
	NetDeviceType                = 0x02
)

// NumaInfoGetter is to get numa info
type NumaInfoGetter func() ([]info.Node, error)

// PodResourcesFilter is to filter pod resources which does need to be reported
type PodResourcesFilter func(*v1.Pod, *podresv1.PodResources) (*podresv1.PodResources, error)

var (
	oneQuantity = *resource.NewQuantity(1, resource.DecimalSI)
)

type topologyAdapterImpl struct {
	mutex     sync.Mutex
	client    podresv1.PodResourcesListerClient
	endpoints []string

	// metaServer is used to fetch pod list to calculate numa allocation
	metaServer *metaserver.MetaServer

	// numaZoneNodeMap map numa zone node => socket zone node
	numaSocketZoneNodeMap map[util.ZoneNode]util.ZoneNode

	// skipDeviceNames name of devices which will be skipped in getting numa allocatable and allocation
	skipDeviceNames sets.String

	// getClientFunc is func to get pod resources lister client
	getClientFunc podresources.GetClientFunc

	// podResourcesFilter is support to filter out pods or resources which no need report to cnr
	podResourcesFilter PodResourcesFilter

	// kubeletResourcePluginPaths is the path of kubelet resource plugin
	kubeletResourcePluginPaths []string

	// pciInfo pci device information
	pciInfo *pci.Info

	enableReportRDMATopology bool
}

// NewPodResourcesServerTopologyAdapter creates a topology adapter which uses pod resources server
func NewPodResourcesServerTopologyAdapter(metaServer *metaserver.MetaServer, endpoints []string,
	kubeletResourcePluginPaths []string, skipDeviceNames sets.String, numaInfoGetter NumaInfoGetter,
	podResourcesFilter PodResourcesFilter, getClientFunc podresources.GetClientFunc, enableReportRDMATopology bool) (Adapter, error) {
	numaInfo, err := numaInfoGetter()
	if err != nil {
		return nil, fmt.Errorf("failed to get numa info: %s", err)
	}

	// make sure all candidate kubelet resource plugin paths exist
	for _, path := range kubeletResourcePluginPaths {
		// ensure resource plugin path exists
		err = general.EnsureDirectory(path)
		if err != nil {
			return nil, errors.Wrapf(err, "ensure resource plugin path %s exists failed", path)
		}
	}
	pciInfo, err := ghw.PCI()
	if err != nil {
		return nil, errors.Wrapf(err, "get pci Info failed")
	}

	numaSocketZoneNodeMap := util.GenerateNumaSocketZone(numaInfo)
	return &topologyAdapterImpl{
		endpoints:                  endpoints,
		kubeletResourcePluginPaths: kubeletResourcePluginPaths,
		metaServer:                 metaServer,
		numaSocketZoneNodeMap:      numaSocketZoneNodeMap,
		skipDeviceNames:            skipDeviceNames,
		getClientFunc:              getClientFunc,
		podResourcesFilter:         podResourcesFilter,
		pciInfo:                    pciInfo,
		enableReportRDMATopology:   enableReportRDMATopology,
	}, nil
}

func (p *topologyAdapterImpl) GetTopologyZones(parentCtx context.Context) ([]*nodev1alpha1.TopologyZone, error) {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	// always force getting pod list instead of cache
	ctx := context.WithValue(parentCtx, metaserverpod.BypassCacheKey, metaserverpod.BypassCacheTrue)

	podList, err := p.metaServer.GetPodList(ctx, nil)
	if err != nil {
		return nil, errors.Wrap(err, "get pod list from metaServer failed")
	}

	listPodResourcesResponse, err := p.client.List(ctx, &podresv1.ListPodResourcesRequest{})
	if err != nil {
		return nil, errors.Wrap(err, "list pod from pod resource server failed")
	}

	allocatableResources, err := p.client.GetAllocatableResources(ctx, &podresv1.AllocatableResourcesRequest{})
	if err != nil {
		return nil, errors.Wrap(err, "get allocatable Resources from pod resource server failed")
	}

	if klog.V(5).Enabled() {
		listPodResourcesResponseStr, _ := json.Marshal(listPodResourcesResponse)
		allocatableResourcesResponseStr, _ := json.Marshal(allocatableResources)
		klog.Infof("list pod Resources: %s\n allocatable Resources: %s", string(listPodResourcesResponseStr),
			string(allocatableResourcesResponseStr))
	}

	// validate pod Resources server response to make sure report topology status is correct
	if err = validatePodResourcesServerResponse(allocatableResources, listPodResourcesResponse); err != nil {
		return nil, errors.Wrap(err, "validate pod Resources server response failed")
	}

	// filter already allocated pods
	podResourcesList := filterAllocatedPodResourcesList(listPodResourcesResponse.GetPodResources())

	// get numa Allocations by pod Resources
	zoneAllocations, err := p.getZoneAllocations(podList, podResourcesList)
	if err != nil {
		return nil, errors.Wrap(err, "get zone allocations failed")
	}

	// get zone resources by allocatable resources
	zoneResources, err := p.getZoneResources(allocatableResources)
	if err != nil {
		return nil, errors.Wrap(err, "get zone resources failed")
	}

	// get zone attributes by allocatable resources
	zoneAttributes, err := p.getZoneAttributes(allocatableResources)
	if err != nil {
		return nil, errors.Wrap(err, "get zone Attributes failed")
	}

	// initialize a topology zone generator by numa socket zone node map
	topologyZoneGenerator, err := util.NewNumaSocketTopologyZoneGenerator(p.numaSocketZoneNodeMap)
	if err != nil {
		return nil, err
	}

	// add other children zone node of numa or socket into topology zone generator by allocatable resources
	err = p.addNumaSocketChildrenZoneNodes(topologyZoneGenerator, allocatableResources)
	if err != nil {
		return nil, errors.Wrap(err, "get zone topology failed")
	}

	if p.enableReportRDMATopology {
		err = p.addNICNumaChildrenZoneNodes(topologyZoneGenerator, allocatableResources)
		if err != nil {
			return nil, errors.Wrap(err, "get zone topology failed")
		}

		nicZoneAllocations, err := p.getNicZoneAllocations(podList, podResourcesList)
		if err != nil {
			return nil, errors.Wrap(err, "get zone allocations failed")
		}
		mergeZoneAllocations(zoneAllocations, nicZoneAllocations)
	}

	return topologyZoneGenerator.GenerateTopologyZoneStatus(zoneAllocations, zoneResources, zoneAttributes), nil
}

// GetTopologyPolicy return newest topology policy status
func (p *topologyAdapterImpl) GetTopologyPolicy(ctx context.Context) (nodev1alpha1.TopologyPolicy, error) {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	klConfig, err := p.metaServer.GetKubeletConfig(ctx)
	if err != nil {
		return "", errors.Wrap(err, "get kubelet config failed")
	}

	return utils.GenerateTopologyPolicy(klConfig.TopologyManagerPolicy, klConfig.TopologyManagerScope), nil
}

func (p *topologyAdapterImpl) Run(ctx context.Context, handler func()) error {
	var (
		err  error
		conn *grpc.ClientConn
	)
	p.mutex.Lock()
	defer p.mutex.Unlock()

	p.client, conn, err = p.getClientFunc(
		general.GetOneExistPath(p.endpoints), podResourcesClientTimeout, podResourcesClientMaxMsgSize)
	if err != nil {
		return fmt.Errorf("get podResources client failed, connect err: %s", err)
	}

	// register file watcher to watch qrm checkpoint file change
	watcher, err := general.RegisterFileEventWatcher(
		ctx.Done(),
		general.FileWatcherInfo{
			Path:     p.kubeletResourcePluginPaths,
			Filename: consts.KubeletQoSResourceManagerCheckpoint,
			Op:       fsnotify.Create,
		},
	)
	if err != nil {
		return fmt.Errorf("register file watcher failed, err: %s", err)
	}

	// start a goroutine to watch qrm checkpoint file change and notify to update topology status,
	// and when qrm checkpoint file changed, it means that the topology status may be changed
	go func() {
		defer func() {
			err = conn.Close()
			if err != nil {
				klog.Errorf("pod resource connection close failed: %v", err)
			}
		}()
		for {
			select {
			case <-ctx.Done():
				klog.Infof("stopping pod resources server topology adapter")
				return
			case _, ok := <-watcher:
				if !ok {
					klog.Warningf("watcher channel closed")
					return
				}
				klog.Infof("qrm state file changed, notify to update topology status")
				if handler != nil {
					handler()
				}
			}
		}
	}()

	return nil
}

// validatePodResourcesServerResponse validate pod resources server response, if the resource is empty,
// maybe the kubelet or qrm plugin is restarting
func validatePodResourcesServerResponse(allocatableResourcesResponse *podresv1.AllocatableResourcesResponse,
	listPodResourcesResponse *podresv1.ListPodResourcesResponse) error {
	if allocatableResourcesResponse == nil {
		return fmt.Errorf("allocatable Resources response is nil")
	}

	if len(allocatableResourcesResponse.Resources) == 0 {
		return fmt.Errorf("allocatable topology aware Resources is empty")
	}

	if listPodResourcesResponse == nil {
		return fmt.Errorf("list pod Resources response is nil")
	}

	return nil
}

// addNumaSocketChildrenZoneNodes add the child nodes of socket or numa zone nodes to the generator, the child nodes are
// generated by generateZoneNode according to TopologyLevel, Type and Name in TopologyAwareAllocatableQuantityList
func (p *topologyAdapterImpl) addNumaSocketChildrenZoneNodes(generator *util.TopologyZoneGenerator,
	allocatableResources *podresv1.AllocatableResourcesResponse) error {
	if allocatableResources == nil {
		return fmt.Errorf("allocatable Resources is nil")
	}

	var errList []error
	for _, resources := range allocatableResources.Resources {
		for _, quantity := range resources.TopologyAwareAllocatableQuantityList {
			if quantity == nil || len(quantity.Type) == 0 {
				continue
			}

			zoneNode, parentZoneNode, err := p.generateZoneNode(*quantity)
			if err != nil {
				errList = append(errList, fmt.Errorf("get zone key from quantity %v failed: %v", quantity, err))
				continue
			}

			err = generator.AddNode(parentZoneNode, zoneNode)
			if err != nil {
				errList = append(errList, err)
				continue
			}
		}
	}

	if len(errList) > 0 {
		return utilerrors.NewAggregate(errList)
	}

	return nil
}

// addNumaSocketChildrenZoneNodes add the child nodes of socket or numa zone nodes to the generator, the child nodes are
// generated by generateZoneNode according to TopologyLevel, Type and Name in TopologyAwareAllocatableQuantityList
func (p *topologyAdapterImpl) addNICNumaChildrenZoneNodes(generator *util.TopologyZoneGenerator,
	allocatableResources *podresv1.AllocatableResourcesResponse) error {
	if allocatableResources == nil {
		return fmt.Errorf("allocatable Resources is nil")
	}
	var errList []error
	nicNumaZoneNodeMap := make(map[util.ZoneNode]util.ZoneNode)
	for _, device := range allocatableResources.Devices {
		if util.IsRDMA(device.ResourceName) {
			for _, deviceId := range device.DeviceIds {
				nicNode := util.GenerateNICZoneNode(deviceId)
				if _, ok := nicNumaZoneNodeMap[nicNode]; !ok {
					numaId := p.getNumaIdByPCIAddress(deviceId)
					numaZoneNode := util.GenerateNumaZoneNode(numaId)
					nicNumaZoneNodeMap[nicNode] = numaZoneNode
				}
			}
		}
	}
	for nicNode, numaNode := range nicNumaZoneNodeMap {
		err := generator.AddNode(&numaNode, nicNode)
		if err != nil {
			errList = append(errList, err)
			continue
		}
	}

	if len(errList) > 0 {
		return utilerrors.NewAggregate(errList)
	}

	return nil
}

// getZoneResources gets a map of zone node to zone Resources. The zone node Resources is combined by allocatable
// device and allocatable resources from pod resources server
func (p *topologyAdapterImpl) getZoneResources(allocatableResources *podresv1.AllocatableResourcesResponse) (map[util.ZoneNode]nodev1alpha1.Resources, error) {
	var (
		errList []error
		err     error
	)

	if allocatableResources == nil {
		return nil, fmt.Errorf("allocatable Resources is nil")
	}

	zoneAllocatable := make(map[util.ZoneNode]*v1.ResourceList)
	zoneCapacity := make(map[util.ZoneNode]*v1.ResourceList)

	zoneAllocatable, err = p.addContainerDevices(zoneAllocatable, allocatableResources.Devices)
	if err != nil {
		return nil, err
	}

	// todo: the capacity and allocatable are equally now because the response includes all
	// 		devices which don't consider them whether is healthy
	zoneCapacity, err = p.addContainerDevices(zoneCapacity, allocatableResources.Devices)
	if err != nil {
		return nil, err
	}

	// calculate Resources capacity and allocatable
	for _, resources := range allocatableResources.Resources {
		if resources == nil {
			continue
		}

		resourceName := v1.ResourceName(resources.ResourceName)
		zoneCapacity, err = p.addTopologyAwareQuantity(zoneCapacity, resourceName, resources.TopologyAwareCapacityQuantityList)
		if err != nil {
			errList = append(errList, err)
			continue
		}

		zoneAllocatable, err = p.addTopologyAwareQuantity(zoneAllocatable, resourceName, resources.TopologyAwareAllocatableQuantityList)
		if err != nil {
			errList = append(errList, err)
			continue
		}
	}

	if len(errList) > 0 {
		return nil, utilerrors.NewAggregate(errList)
	}

	resources := make(map[util.ZoneNode]nodev1alpha1.Resources)
	for zone, capacity := range zoneCapacity {
		allocatable, ok := zoneAllocatable[zone]
		if !ok {
			return nil, fmt.Errorf("zone %v capacity found but allocatable is not found", zone)
		}

		resources[zone] = nodev1alpha1.Resources{
			Capacity:    capacity,
			Allocatable: allocatable,
		}
	}

	return resources, nil
}

// getZoneAllocations gets a map of zone nodes to zone allocations computed from a list of pod resources that aggregates per-container allocations using
// aggregateContainerAllocated. The podResourcesFilter is used to filter out some pods that do not need to be reported to cnr
func (p *topologyAdapterImpl) getZoneAllocations(podList []*v1.Pod, podResourcesList []*podresv1.PodResources) (map[util.ZoneNode]util.ZoneAllocations, error) {
	var (
		err     error
		errList []error
	)

	podMap := native.GetPodNamespaceNameKeyMap(podList)
	zoneAllocationsMap := make(map[util.ZoneNode]util.ZoneAllocations)
	for _, podResources := range podResourcesList {
		if podResources == nil {
			continue
		}

		podKey := native.GenerateNamespaceNameKey(podResources.Namespace, podResources.Name)
		pod, ok := podMap[podKey]
		if !ok {
			errList = append(errList, fmt.Errorf("pod %s not found in metaserver", podKey))
			continue
		}

		// the pod resource filter will filter out unwanted pods
		if p.podResourcesFilter != nil {
			podResources, err = p.podResourcesFilter(pod, podResources)
			if err != nil {
				errList = append(errList, err)
				continue
			}

			// if podResources is nil, it means that the pod is filtered out
			if podResources == nil {
				continue
			}
		}

		// aggregates resources in each zone used by all containers of the pod
		podAllocated, err := p.aggregateContainerAllocated(podResources.Containers)
		if err != nil {
			errList = append(errList, fmt.Errorf("pod %s aggregate container allocated failed, %s", podKey, err))
			continue
		}

		for zoneNode, resourceList := range podAllocated {
			_, ok := zoneAllocationsMap[zoneNode]
			if !ok {
				zoneAllocationsMap[zoneNode] = util.ZoneAllocations{}
			}

			zoneAllocationsMap[zoneNode] = append(zoneAllocationsMap[zoneNode], &nodev1alpha1.Allocation{
				Consumer: native.GenerateUniqObjectUIDKey(pod),
				Requests: resourceList,
			})
		}
	}

	if len(errList) > 0 {
		return nil, utilerrors.NewAggregate(errList)
	}

	return zoneAllocationsMap, nil
}

func (p *topologyAdapterImpl) getNicZoneAllocations(podList []*v1.Pod, podResourcesList []*podresv1.PodResources) (map[util.ZoneNode]util.ZoneAllocations, error) {
	var (
		err     error
		errList []error
	)

	podMap := native.GetPodNamespaceNameKeyMap(podList)
	zoneAllocationsMap := make(map[util.ZoneNode]util.ZoneAllocations)
	for _, podResources := range podResourcesList {
		if podResources == nil {
			continue
		}

		podKey := native.GenerateNamespaceNameKey(podResources.Namespace, podResources.Name)
		pod, ok := podMap[podKey]
		if !ok {
			errList = append(errList, fmt.Errorf("pod %s not found in metaserver", podKey))
			continue
		}

		if native.PodIsTerminated(pod) {
			continue
		}

		// the pod resource filter will filter out unwanted pods
		if p.podResourcesFilter != nil {
			podResources, err = p.podResourcesFilter(pod, podResources)
			if err != nil {
				errList = append(errList, err)
				continue
			}

			// if podResources is nil, it means that the pod is filtered out
			if podResources == nil {
				continue
			}
		}

		for _, c := range podResources.Containers {
			for _, device := range c.Devices {
				if util.IsRDMA(device.ResourceName) {
					for _, deviceId := range device.DeviceIds {
						nicNode := util.GenerateNICZoneNode(deviceId)
						if _, ok := zoneAllocationsMap[nicNode]; !ok {
							zoneAllocationsMap[nicNode] = []*nodev1alpha1.Allocation{
								{Consumer: native.GenerateUniqObjectUIDKey(pod),
									Requests: &v1.ResourceList{
										util.ResourceRDMA: resource.MustParse("1"),
									},
								},
							}
						}
					}
				}
			}
		}
	}

	if len(errList) > 0 {
		return nil, utilerrors.NewAggregate(errList)
	}

	return zoneAllocationsMap, nil
}

// getZoneAttributes gets a map of zone node to zone attributes, which is generated from the annotation of
// topology aware quantity and socket and numa zone are not support attribute here
func (p *topologyAdapterImpl) getZoneAttributes(allocatableResources *podresv1.AllocatableResourcesResponse) (map[util.ZoneNode]util.ZoneAttributes, error) {
	if allocatableResources == nil {
		return nil, fmt.Errorf("allocatable Resources is nil")
	}

	var errList []error
	zoneAttributes := make(map[util.ZoneNode]util.ZoneAttributes)
	for _, resources := range allocatableResources.Resources {
		if resources == nil {
			continue
		}

		for _, quantity := range resources.TopologyAwareAllocatableQuantityList {
			// only quantity with type need report attributes, and others such as Socket and Numa
			// no need report that
			if quantity == nil || len(quantity.Type) == 0 {
				continue
			}

			zoneNode, _, err := p.generateZoneNode(*quantity)
			if err != nil {
				errList = append(errList, fmt.Errorf("get zone node from quantity %v failed: %v", quantity, err))
				continue
			}

			if _, ok := zoneAttributes[zoneNode]; !ok {
				zoneAttributes[zoneNode] = util.ZoneAttributes{}
			}

			var attrs []nodev1alpha1.Attribute
			for annoKey, value := range quantity.Annotations {
				attrs = append(attrs, nodev1alpha1.Attribute{
					Name:  annoKey,
					Value: value,
				})
			}

			zoneAttributes[zoneNode] = util.MergeAttributes(zoneAttributes[zoneNode], attrs)
		}
	}

	if len(errList) > 0 {
		return nil, utilerrors.NewAggregate(errList)
	}

	return zoneAttributes, nil
}

// aggregateContainerAllocated aggregates resources in each zone used by all containers of a pod and returns a map of zone node to
// container allocated resources.
func (p *topologyAdapterImpl) aggregateContainerAllocated(containers []*podresv1.ContainerResources) (map[util.ZoneNode]*v1.ResourceList, error) {
	var errList []error

	podAllocated := make(map[util.ZoneNode]*v1.ResourceList)
	for _, containerResources := range containers {
		if containerResources == nil {
			continue
		}

		var err error
		containerAllocated := make(map[util.ZoneNode]*v1.ResourceList)
		containerAllocated, err = p.addContainerDevices(containerAllocated, containerResources.Devices)
		if err != nil {
			errList = append(errList, fmt.Errorf("get container %s devices allocated failed: %s",
				containerResources.Name, err))
			continue
		}

		containerAllocated, err = p.addContainerResources(containerAllocated, containerResources.Resources)
		if err != nil {
			errList = append(errList, fmt.Errorf("get container %s resources allocated failed: %s",
				containerResources.Name, err))
			continue
		}

		for zoneNode, resourceList := range containerAllocated {
			if resourceList == nil {
				continue
			}

			for resourceName, quantity := range *resourceList {
				podAllocated = addZoneQuantity(podAllocated, zoneNode, resourceName, quantity)
			}
		}
	}

	if len(errList) > 0 {
		return nil, utilerrors.NewAggregate(errList)
	}

	return podAllocated, nil
}

// addContainerDevices add all numa zone device into the zone resources map, and the skipDeviceNames is used
// to filter out some devices that do not need to be reported to cnr. The device name is the resource name and
// the quantity is the number of devices.
func (p *topologyAdapterImpl) addContainerDevices(zoneResources map[util.ZoneNode]*v1.ResourceList,
	containerDevices []*podresv1.ContainerDevices) (map[util.ZoneNode]*v1.ResourceList, error) {
	var errList []error

	if zoneResources == nil {
		zoneResources = make(map[util.ZoneNode]*v1.ResourceList)
	}

	for _, device := range containerDevices {
		if device == nil || device.Topology == nil {
			continue
		}

		if p.skipDeviceNames != nil && p.skipDeviceNames.Has(device.ResourceName) {
			continue
		}

		resourceName := v1.ResourceName(device.ResourceName)
		for _, node := range device.Topology.Nodes {
			if node == nil {
				continue
			}

			zoneNode := util.GenerateNumaZoneNode(int(node.ID))
			zoneResources = addZoneQuantity(zoneResources, zoneNode, resourceName, oneQuantity)
		}
	}

	if len(errList) > 0 {
		return nil, utilerrors.NewAggregate(errList)
	}

	return zoneResources, nil
}

// addContainerResources add all container resources into the zone resources map, get each resource of each zone node
// and add them together to get the total resource of each zone node.
func (p *topologyAdapterImpl) addContainerResources(zoneResources map[util.ZoneNode]*v1.ResourceList,
	topoAwareResources []*podresv1.TopologyAwareResource) (map[util.ZoneNode]*v1.ResourceList, error) {
	var (
		errList []error
		err     error
	)

	if zoneResources == nil {
		zoneResources = make(map[util.ZoneNode]*v1.ResourceList)
	}

	for _, resources := range topoAwareResources {
		if resources == nil {
			continue
		}

		resourceName := v1.ResourceName(resources.ResourceName)
		zoneResources, err = p.addTopologyAwareQuantity(zoneResources, resourceName, resources.OriginalTopologyAwareQuantityList)
		if err != nil {
			errList = append(errList, err)
			continue
		}
	}

	if len(errList) > 0 {
		return nil, utilerrors.NewAggregate(errList)
	}

	return zoneResources, nil
}

// addTopologyAwareQuantity add zone node resource into the map according to TopologyAwareQuantity list. Each TopologyAwareQuantity has a
// list of topology nodes, and each topology node has name, type, topology level, and annotations, and the resource value. The zone node
// is determined by the topology node name, type, topology level,
func (p *topologyAdapterImpl) addTopologyAwareQuantity(zoneResourceList map[util.ZoneNode]*v1.ResourceList, resourceName v1.ResourceName,
	topoAwareQuantityList []*podresv1.TopologyAwareQuantity) (map[util.ZoneNode]*v1.ResourceList, error) {
	var errList []error

	if zoneResourceList == nil {
		zoneResourceList = make(map[util.ZoneNode]*v1.ResourceList)
	}

	for _, quantity := range topoAwareQuantityList {
		if quantity == nil {
			continue
		}

		zoneNode, _, err := p.generateZoneNode(*quantity)
		if err != nil {
			errList = append(errList, fmt.Errorf("get zone node from quantity %v failed: %v", quantity, err))
			continue
		}

		resourceValue, err := resource.ParseQuantity(fmt.Sprintf("%.2f", quantity.ResourceValue))
		if err != nil {
			errList = append(errList, fmt.Errorf("parse resource: %s for zone %s failed: %s", resourceName, zoneNode, err))
			continue
		}

		zoneResourceList = addZoneQuantity(zoneResourceList, zoneNode, resourceName, resourceValue)
	}

	if len(errList) > 0 {
		return nil, utilerrors.NewAggregate(errList)
	}

	return zoneResourceList, nil
}

// addZoneQuantity add a zone and resource quantity into the zone resource map, if the zone node is not in the map,
// then create a new resource list for the zone node, and add the resource quantity into the resource list. If the
// zone node is in the map, then get the resource list from the map, and add the resource quantity into the resource
// list.
func addZoneQuantity(zoneResourceList map[util.ZoneNode]*v1.ResourceList, zoneNode util.ZoneNode,
	resourceName v1.ResourceName, value resource.Quantity) map[util.ZoneNode]*v1.ResourceList {
	if zoneResourceList == nil {
		zoneResourceList = make(map[util.ZoneNode]*v1.ResourceList)
	}

	resourceListPtr, ok := zoneResourceList[zoneNode]
	if !ok || resourceListPtr == nil {
		resourceListPtr = &v1.ResourceList{}
		zoneResourceList[zoneNode] = resourceListPtr
	}
	resourceList := *resourceListPtr

	quantity, resourceOk := resourceList[resourceName]
	if !resourceOk {
		quantity = resource.Quantity{}
		resourceList[resourceName] = quantity
	}

	quantity.Add(value)
	resourceList[resourceName] = quantity

	return zoneResourceList
}

// generateZoneNode get zone node and its parent zone node from quantity according to quantity type and topology level
//   - if Type is empty, it means that the zone is socket or numa according to TopologyLevel
//   - if Type is not empty, it means that the zone is a child of socket or a child of numa determined by TopologyLevel,
//     and the zone name is determined by the quantity name or its resource identifier if existed.
func (p *topologyAdapterImpl) generateZoneNode(quantity podresv1.TopologyAwareQuantity) (util.ZoneNode, *util.ZoneNode, error) {
	nodeID := int(quantity.Node)
	if len(quantity.Type) == 0 {
		switch quantity.TopologyLevel {
		case podresv1.TopologyLevel_NUMA:
			zoneNode := util.GenerateNumaZoneNode(nodeID)
			parentZoneNode, ok := p.numaSocketZoneNodeMap[zoneNode]
			if !ok {
				return util.ZoneNode{}, nil, fmt.Errorf("numa zone node %v parent not found", zoneNode)
			}
			return zoneNode, &parentZoneNode, nil
		case podresv1.TopologyLevel_SOCKET:
			zoneNode := util.GenerateSocketZoneNode(nodeID)
			return zoneNode, nil, nil
		default:
			return util.ZoneNode{}, nil, fmt.Errorf("quantity %v unsupport topology level: %s", quantity, quantity.TopologyLevel)
		}
	} else {
		// if quantity has type, the zone's type is quantity type and name is quantity name by default,
		// and if it has resource identifier annotation use it instead
		zoneName := quantity.Name
		if identifier, ok := quantity.Annotations[apiconsts.ResourceAnnotationKeyResourceIdentifier]; ok && len(identifier) != 0 {
			zoneName = identifier
		}

		zoneNode := util.ZoneNode{
			Meta: util.ZoneMeta{
				Type: nodev1alpha1.TopologyType(quantity.Type),
				Name: zoneName,
			},
		}

		switch quantity.TopologyLevel {
		case podresv1.TopologyLevel_NUMA:
			parentZoneNode := util.GenerateNumaZoneNode(nodeID)
			return zoneNode, &parentZoneNode, nil
		case podresv1.TopologyLevel_SOCKET:
			parentZoneNode := util.GenerateSocketZoneNode(nodeID)
			return zoneNode, &parentZoneNode, nil
		default:
			return zoneNode, nil, fmt.Errorf("quantity %v unsupport topology level: %s", quantity, quantity.TopologyLevel)
		}
	}
}

// filterAllocatedPodResourcesList is to filter pods that have allocated devices or Resources
func filterAllocatedPodResourcesList(podResourcesList []*podresv1.PodResources) []*podresv1.PodResources {
	allocatedPodResourcesList := make([]*podresv1.PodResources, 0, len(podResourcesList))
	isAllocatedPod := func(pod *podresv1.PodResources) bool {
		if pod == nil {
			return false
		}

		// filter allocated pod by whether it has at least one container with
		// devices or Resources
		for _, container := range pod.Containers {
			if container != nil && (len(container.Devices) != 0 ||
				len(container.Resources) != 0) {
				return true
			}
		}

		return false
	}

	for _, pod := range podResourcesList {
		if isAllocatedPod(pod) {
			allocatedPodResourcesList = append(allocatedPodResourcesList, pod)
		}
	}

	return allocatedPodResourcesList
}

func mergeZoneAllocations(zone1, zone2 map[util.ZoneNode]util.ZoneAllocations) {
	for zoneNode, allocations := range zone2 {
		zone1[zoneNode] = allocations
	}
}

func (p *topologyAdapterImpl) getNumaIdByPCIAddress(address string) int {
	device := p.pciInfo.GetDevice(address)
	if device != nil && device.Node != nil {
		return device.Node.ID
	} else {
		klog.Errorf("get numa Id error, pci address %s", address)
	}
	return -1
}
