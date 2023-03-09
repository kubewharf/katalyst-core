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
	"time"

	info "github.com/google/cadvisor/info/v1"
	"github.com/pkg/errors"
	"google.golang.org/grpc"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"

	nodev1alpha1 "github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/agent/resourcemanager/fetcher/util/kubelet/podresources"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
	podresv1 "k8s.io/kubelet/pkg/apis/podresources/v1"
)

const (
	podResourcesClientTimeout    = 10 * time.Second
	podResourcesClientMaxMsgSize = 1024 * 1024 * 16
)

// NumaInfoGetter is to get numa info
type NumaInfoGetter func() ([]info.Node, error)

var (
	oneQuantity = *resource.NewQuantity(1, resource.DecimalSI)
)

type podResourcesServerTopologyAdapterImpl struct {
	client    podresv1.PodResourcesListerClient
	conn      *grpc.ClientConn
	endpoints []string

	// metaServer is used to fetch pod list to calculate numa allocation
	metaServer *metaserver.MetaServer

	// numaToSocketMap map numa id => socket id
	numaToSocketMap map[int]int

	// skipNumaAllocatableDeviceNames name of devices which will be skipped in getting numa allocatable
	skipNumaAllocatableDeviceNames sets.String

	// getClientFunc is func to get pod resources lister client
	getClientFunc podresources.GetClientFunc

	// podNumaBindFilter is to filter out pods which need numa binding
	podNumaBindFilter func(*v1.Pod) bool
}

// NewPodResourcesServerTopologyAdapter creates a topology adapter which uses pod resources server
func NewPodResourcesServerTopologyAdapter(metaServer *metaserver.MetaServer, endpoints []string,
	skipNumaAllocatableDeviceNames sets.String, numaInfoGetter NumaInfoGetter,
	podNumaBindingFilter func(*v1.Pod) bool, getClientFunc podresources.GetClientFunc) (Adapter, error) {
	numaInfo, err := numaInfoGetter()
	if err != nil {
		return nil, fmt.Errorf("failed to get numa info: %s", err)
	}

	numaToSocketMap := GetNumaToSocketMap(numaInfo)
	return &podResourcesServerTopologyAdapterImpl{
		endpoints:                      endpoints,
		metaServer:                     metaServer,
		numaToSocketMap:                numaToSocketMap,
		skipNumaAllocatableDeviceNames: skipNumaAllocatableDeviceNames,
		getClientFunc:                  getClientFunc,
		podNumaBindFilter:              podNumaBindingFilter,
	}, nil
}

func (p *podResourcesServerTopologyAdapterImpl) GetNumaTopologyStatus(parentCtx context.Context) (*nodev1alpha1.TopologyStatus, error) {
	// always force getting pod list instead of cache
	ctx := context.WithValue(parentCtx, pod.BypassCacheKey, pod.BypassCacheTrue)

	podList, err := p.metaServer.GetPodList(ctx, nil)
	if err != nil {
		return nil, errors.Wrap(err, "get pod list from metaServer failed")
	}

	listPodResourcesResponse, err := p.client.List(ctx, &podresv1.ListPodResourcesRequest{})
	if err != nil {
		return nil, errors.Wrap(err, "list pod from pod resource server failed")
	}

	podResourcesList := filterAllocatedPodResourcesList(listPodResourcesResponse.GetPodResources())

	allocatableResourcesResponse, err := p.client.GetAllocatableResources(ctx, &podresv1.AllocatableResourcesRequest{})
	if err != nil {
		return nil, errors.Wrap(err, "get allocatable resources from pod resource server failed")
	}

	if klog.V(5).Enabled() {
		podResourcesListStr, _ := json.Marshal(podResourcesList)
		allocatableResourcesResponseStr, _ := json.Marshal(allocatableResourcesResponse)
		klog.Infof("pod Resource list: %s\n allocatable resources: %s", string(podResourcesListStr), string(allocatableResourcesResponseStr))
	}

	// get numa allocations by pod resources
	numaAllocationsMap, err := getNumaAllocationsByPodResources(podList, podResourcesList, p.podNumaBindFilter)
	if err != nil {
		return nil, errors.Wrap(err, "get numa allocations failed")
	}

	// get numa allocatable by allocatable resources
	numaCapacity, numaAllocatable, err := getNumaStatusByAllocatableResources(allocatableResourcesResponse, p.skipNumaAllocatableDeviceNames)
	if err != nil {
		return nil, errors.Wrap(err, "get numa status failed")
	}

	socketStatusMap := GenerateSocketStatus(numaCapacity, numaAllocatable, p.numaToSocketMap)

	return GenerateNumaTopologyStatus(socketStatusMap, numaAllocationsMap), nil
}

func (p *podResourcesServerTopologyAdapterImpl) Run(ctx context.Context, _ chan struct{}) error {
	var err error
	p.client, p.conn, err = p.getClientFunc(
		general.GetOneExistPath(p.endpoints), podResourcesClientTimeout, podResourcesClientMaxMsgSize)
	if err != nil {
		return fmt.Errorf("get podResources client failed, connect err: %s", err)
	}

	// because pod resource api is not support list/watch, so we only wait context done to close client connection here
	go func() {
		defer func(conn *grpc.ClientConn) {
			err := conn.Close()
			if err != nil {
				klog.Errorf("pod resource connection close failed")
			}
		}(p.conn)

		<-ctx.Done()
	}()

	return nil
}

func getNumaStatusByAllocatableResources(allocatableResources *podresv1.AllocatableResourcesResponse,
	skipDeviceNames sets.String) (map[int]*v1.ResourceList, map[int]*v1.ResourceList, error) {
	var (
		errList []error
		err     error
	)

	if allocatableResources == nil {
		return nil, nil, fmt.Errorf("allocatable resources is nil")
	}

	numaCapacity := make(map[int]*v1.ResourceList)
	numaAllocatable := make(map[int]*v1.ResourceList)

	numaAllocatable = addContainerTopoAwareDevices(numaAllocatable, allocatableResources.Devices, skipDeviceNames)

	// todo: the capacity and allocatable are equally now because the response includes all
	// 		devices which don't consider them whether is healthy
	numaCapacity = addContainerTopoAwareDevices(numaCapacity, allocatableResources.Devices, skipDeviceNames)

	// calculate resources capacity and allocatable
	for _, resources := range allocatableResources.Resources {
		if resources == nil {
			continue
		}

		resourceName := v1.ResourceName(resources.ResourceName)
		numaCapacity, err = addTopoAwareQuantity(numaCapacity, resourceName, resources.TopologyAwareCapacityQuantityList)
		if err != nil {
			errList = append(errList, err)
			continue
		}

		numaAllocatable, err = addTopoAwareQuantity(numaAllocatable, resourceName, resources.TopologyAwareAllocatableQuantityList)
		if err != nil {
			errList = append(errList, err)
			continue
		}
	}

	if len(errList) > 0 {
		return nil, nil, utilerrors.NewAggregate(errList)
	}

	return numaCapacity, numaAllocatable, nil
}

// getNumaAllocationsByPodResources get a map of numa id to numa allocations, which
// includes pod allocations.
func getNumaAllocationsByPodResources(podList []*v1.Pod, podResourcesList []*podresv1.PodResources,
	podNumaBindingFilter func(*v1.Pod) bool) (map[int]*nodev1alpha1.NumaStatus, error) {
	var errList []error

	podMap := native.GetPodNamespaceNameKeyMap(podList)
	numaStatusMap := make(map[int]*nodev1alpha1.NumaStatus)
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

		var podNeedNumaBinding = true
		// if the pod does not need to bind numa and has no numa device bound, we do not need to report to cnr
		if podNumaBindingFilter != nil {
			podNeedNumaBinding = podNumaBindingFilter(pod)
		}

		podAllocated, err := aggregateContainerAllocated(podResources.Containers, podNeedNumaBinding)
		if err != nil {
			errList = append(errList, fmt.Errorf("pod %s aggregate container allocated failed, %s", podKey, err))
			continue
		}

		for numaID, resourceList := range podAllocated {
			_, ok := numaStatusMap[numaID]
			if !ok {
				numaStatusMap[numaID] = &nodev1alpha1.NumaStatus{
					NumaID: numaID,
				}
			}

			numaStatusMap[numaID].Allocations = append(numaStatusMap[numaID].Allocations, &nodev1alpha1.Allocation{
				Consumer: native.GenerateUniqObjectUIDKey(pod),
				Requests: resourceList,
			})
		}
	}

	if len(errList) > 0 {
		return nil, utilerrors.NewAggregate(errList)
	}

	return numaStatusMap, nil
}

func aggregateContainerAllocated(containers []*podresv1.ContainerResources, podNeedNumaBinding bool) (map[int]*v1.ResourceList, error) {
	var errList []error

	podAllocated := make(map[int]*v1.ResourceList)
	for _, containerResources := range containers {
		if containerResources == nil {
			continue
		}

		var err error
		containerAllocated := make(map[int]*v1.ResourceList)
		containerAllocated = addContainerTopoAwareDevices(containerAllocated, containerResources.Devices, nil)

		// if it doesn't need numa binding, we only report its numa binding device and don't report its resources
		if podNeedNumaBinding {
			containerAllocated, err = addContainerTopoAwareResources(containerAllocated, containerResources.Resources)
			if err != nil {
				errList = append(errList, fmt.Errorf("get container %s resources allocated failed: %s",
					containerResources.Name, err))
				continue
			}
		}

		for numaID, resourceList := range containerAllocated {
			if resourceList == nil {
				continue
			}

			for resourceName, quantity := range *resourceList {
				podAllocated = addNumaResourceList(podAllocated, numaID, resourceName, quantity)
			}
		}
	}

	if len(errList) > 0 {
		return nil, utilerrors.NewAggregate(errList)
	}
	return podAllocated, nil
}

func addContainerTopoAwareDevices(numaResources map[int]*v1.ResourceList,
	containerDevices []*podresv1.ContainerDevices, skipDeviceNames sets.String) map[int]*v1.ResourceList {
	if numaResources == nil {
		numaResources = make(map[int]*v1.ResourceList)
	}

	for _, device := range containerDevices {
		if device == nil || device.Topology == nil {
			continue
		}

		if skipDeviceNames != nil && skipDeviceNames.Has(device.ResourceName) {
			continue
		}

		resourceName := v1.ResourceName(device.ResourceName)
		for _, node := range device.Topology.Nodes {
			if node == nil {
				continue
			}

			numaID := int(node.ID)
			numaResources = addNumaResourceList(numaResources, numaID, resourceName, oneQuantity)
		}
	}

	return numaResources
}

func addContainerTopoAwareResources(numaResourcesList map[int]*v1.ResourceList,
	topoAwareResources []*podresv1.TopologyAwareResource) (map[int]*v1.ResourceList, error) {
	var (
		errList []error
		err     error
	)

	if numaResourcesList == nil {
		numaResourcesList = make(map[int]*v1.ResourceList)
	}

	for _, resources := range topoAwareResources {
		if resources == nil {
			continue
		}

		resourceName := v1.ResourceName(resources.ResourceName)
		numaResourcesList, err = addTopoAwareQuantity(numaResourcesList, resourceName, resources.OriginalTopologyAwareQuantityList)
		if err != nil {
			errList = append(errList, err)
			continue
		}
	}

	if len(errList) > 0 {
		return nil, utilerrors.NewAggregate(errList)
	}

	return numaResourcesList, nil
}

func addTopoAwareQuantity(numaResourcesList map[int]*v1.ResourceList, resourceName v1.ResourceName,
	topoAwareQuantity []*podresv1.TopologyAwareQuantity) (map[int]*v1.ResourceList, error) {
	var (
		errList []error
	)

	if numaResourcesList == nil {
		numaResourcesList = make(map[int]*v1.ResourceList)
	}

	for _, quantityList := range topoAwareQuantity {
		if quantityList == nil {
			continue
		}

		numaID := int(quantityList.Node)
		resourceValue, err := resource.ParseQuantity(fmt.Sprintf("%.2f", quantityList.ResourceValue))
		if err != nil {
			errList = append(errList, fmt.Errorf("parse resource: %s for numaID %d failed: %s", resourceName, numaID, err))
			continue
		}

		numaResourcesList = addNumaResourceList(numaResourcesList, numaID, resourceName, resourceValue)
	}

	if len(errList) > 0 {
		return nil, utilerrors.NewAggregate(errList)
	}

	return numaResourcesList, nil
}

// addNumaResourceList add numa resource to numaResourceList
func addNumaResourceList(numaResourceList map[int]*v1.ResourceList, numaID int,
	resourceName v1.ResourceName, value resource.Quantity) map[int]*v1.ResourceList {
	if numaResourceList == nil {
		numaResourceList = make(map[int]*v1.ResourceList)
	}

	resourceListPtr, ok := numaResourceList[numaID]
	if !ok || resourceListPtr == nil {
		resourceListPtr = &v1.ResourceList{}
		numaResourceList[numaID] = resourceListPtr
	}
	resourceList := *resourceListPtr

	quantity, resourceOk := resourceList[resourceName]
	if !resourceOk {
		quantity = resource.Quantity{}
		resourceList[resourceName] = quantity
	}

	quantity.Add(value)
	resourceList[resourceName] = quantity

	return numaResourceList
}

// filterAllocatedPodResourcesList is to filter pods that have allocated devices or resources
func filterAllocatedPodResourcesList(podResourcesList []*podresv1.PodResources) []*podresv1.PodResources {
	allocatedPodResourcesList := make([]*podresv1.PodResources, 0, len(podResourcesList))
	isAllocatedPod := func(pod *podresv1.PodResources) bool {
		if pod == nil {
			return false
		}

		// filter allocated pod by whether it has at least one container with
		// devices or resources
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
