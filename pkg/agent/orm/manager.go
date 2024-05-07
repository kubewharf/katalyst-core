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

package orm

import (
	"context"
	"fmt"
	"math"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/opencontainers/selinux/go-selinux"
	"k8s.io/klog/v2"

	"google.golang.org/grpc"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/kubelet/pkg/apis/pluginregistration/v1"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"
	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager"
	maputil "k8s.io/kubernetes/pkg/util/maps"

	"github.com/kubewharf/katalyst-core/pkg/agent/orm/deviceprovider/kubelet"
	"github.com/kubewharf/katalyst-core/pkg/agent/orm/endpoint"
	"github.com/kubewharf/katalyst-core/pkg/agent/orm/executor"
	"github.com/kubewharf/katalyst-core/pkg/agent/orm/metamanager"
	"github.com/kubewharf/katalyst-core/pkg/agent/orm/server"
	"github.com/kubewharf/katalyst-core/pkg/agent/orm/server/podresources"
	"github.com/kubewharf/katalyst-core/pkg/agent/orm/topology"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/bitmask"
	cgroupmgr "github.com/kubewharf/katalyst-core/pkg/util/cgroup/manager"
	podresourcesutil "github.com/kubewharf/katalyst-core/pkg/util/kubelet/podresources"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

type ManagerImpl struct {
	ctx context.Context

	socketname string
	socketdir  string

	// resource to QRMPlugins and executors
	mutex            sync.RWMutex
	endpoints        map[string]endpoint.EndpointInfo
	resourceExecutor executor.Executor

	metaManager *metamanager.Manager

	topologyManager topology.Manager

	server *grpc.Server
	wg     sync.WaitGroup

	podAddChan    chan string
	podDeleteChan chan string

	podResources      *podResourcesChk
	checkpointManager checkpointmanager.CheckpointManager

	emitter   metrics.MetricEmitter
	qosConfig *generic.QoSConfiguration

	reconcilePeriod   time.Duration
	resourceNamesMap  map[string]string
	podResourceSocket string

	devicesProvider podresources.DevicesProvider
}

func NewManager(socketPath string, emitter metrics.MetricEmitter, metaServer *metaserver.MetaServer, config *config.Configuration) (*ManagerImpl, error) {
	klog.V(2).Infof("new ORM..., socketPath: %v, resourceNameMap: %v, reconcilePeriod: %v", socketPath, config.ORMResourceNamesMap, config.ORMRconcilePeriod)

	if socketPath == "" || !filepath.IsAbs(socketPath) {
		return nil, fmt.Errorf(errBadSocket+" %s", socketPath)
	}
	dir, file := filepath.Split(socketPath)

	checkpointManager, err := checkpointmanager.NewCheckpointManager(dir)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize checkpoint manager: %v", err)
	}

	m := &ManagerImpl{
		socketdir:  dir,
		socketname: file,

		endpoints:         make(map[string]endpoint.EndpointInfo),
		podResources:      newPodResourcesChk(),
		checkpointManager: checkpointManager,

		resourceNamesMap: config.ORMResourceNamesMap,
		reconcilePeriod:  config.ORMRconcilePeriod,

		podAddChan:        make(chan string, config.ORMPodNotifyChanLen),
		podDeleteChan:     make(chan string, config.ORMPodNotifyChanLen),
		emitter:           emitter,
		qosConfig:         config.QoSConfiguration,
		podResourceSocket: config.ORMPodResourcesSocket,
	}

	m.resourceExecutor = executor.NewExecutor(cgroupmgr.GetManager())

	metaManager := metamanager.NewManager(emitter, m.podResources.pods, metaServer)
	m.metaManager = metaManager

	topologyManager, err := topology.NewManager(metaServer.Topology, config.TopologyPolicyName, config.NumericAlignResources)
	if err != nil {
		klog.Error(err)
		return nil, err
	}
	topologyManager.AddHintProvider(m)
	m.topologyManager = topologyManager

	m.initDeviceProvider(config)

	if err := m.removeContents(m.socketdir); err != nil {
		err = fmt.Errorf("[ORM] Fail to clean up stale contents under %s: %v", m.socketdir, err)
		klog.Error(err)
		return nil, err
	}
	klog.V(5).Infof("removeContents......")

	return m, nil
}

func (m *ManagerImpl) Run(ctx context.Context) {
	klog.V(2).Infof("[ORM] running...")
	m.ctx = ctx

	// read data from checkpoint
	err := m.readCheckpoint()
	if err != nil {
		klog.Fatalf("[ORM] read checkpoint fail: %v", err)
	}

	if err = os.MkdirAll(m.socketdir, 0o750); err != nil {
		klog.Fatalf("[ORM] Mkdir socketdir %v fail: %v", m.socketdir, err)
	}
	if selinux.GetEnabled() {
		if err := selinux.SetFileLabel(m.socketdir, KubeletPluginsDirSELinuxLabel); err != nil {
			klog.Warningf("[ORM] Unprivileged containerized plugins might not work. Could not set selinux context on %s: %v", m.socketdir, err)
		}
	}

	socketPath := filepath.Join(m.socketdir, m.socketname)
	s, err := net.Listen("unix", socketPath)
	if err != nil {
		klog.Fatalf(errListenSocket+" %v", err)
	}

	m.wg.Add(1)
	m.server = grpc.NewServer([]grpc.ServerOption{}...)

	pluginapi.RegisterRegistrationServer(m.server, m)

	klog.V(2).Infof("[ORM] Serving resource plugin registration server on %q", socketPath)
	go func() {
		defer func() {
			m.wg.Done()

			if err := recover(); err != nil {
				klog.Fatalf("[ORM] Start recover from err: %v", err)
			}
			s.Close()
		}()
		m.server.Serve(s)
	}()

	klog.V(5).Infof("[ORM] start serve socketPath %v", socketPath)
	go func() {
		m.process()
	}()

	go wait.Until(m.reconcile, m.reconcilePeriod, m.ctx.Done())

	m.metaManager.RegistPodAddedFunc(m.onPodAdd)
	m.metaManager.RegistPodDeletedFunc(m.onPodDelete)

	m.metaManager.Run(ctx, m.reconcilePeriod)

	go server.ListenAndServePodResources(m.podResourceSocket, m.metaManager, m, m.devicesProvider, m.emitter)
}

func (m *ManagerImpl) GetHandlerType() string {
	return pluginregistration.ResourcePlugin
}

func (m *ManagerImpl) GetTopologyHints(pod *v1.Pod, container *v1.Container) map[string][]topology.TopologyHint {
	if pod == nil || container == nil {
		klog.Errorf("[ORM] GetTopologyHints got nil pod: %v or container: %v", pod, container)
		return nil
	}

	podUID := string(pod.UID)
	contName := container.Name
	containerType, containerIndex, err := GetContainerTypeAndIndex(pod, container)
	if err != nil {
		return nil
	}

	resourceHints := make(map[string][]topology.TopologyHint)
	for resourceObj, requestedObj := range container.Resources.Requests {
		requested := int(requestedObj.Value())
		resource, err := m.getMappedResourceName(string(resourceObj), container.Resources.Requests)
		if err != nil {
			klog.Errorf("resource %s getMappedResourceName fail: %v", string(resourceObj), err)
			return nil
		}

		if requestedObj.IsZero() {
			continue
		}

		allocationInfo := m.podResources.containerResource(podUID, contName, resource)
		if allocationInfo != nil && allocationInfo.ResourceHints != nil && len(allocationInfo.ResourceHints.Hints) > 0 {

			allocated := int(math.Ceil(allocationInfo.AllocatedQuantity))

			if allocationInfo.IsScalarResource && allocated >= requested {
				resourceHints[resource] = ParseListOfTopologyHints(allocationInfo.ResourceHints)
				klog.Warningf("[ORM] resource %s already allocated to (pod %s/%s, container %v) with larger number than request: requested: %d, allocated: %d; not to getTopologyHints",
					resource, pod.GetNamespace(), pod.GetName(), container.Name, requested, allocated)
				continue
			} else {
				klog.Warningf("[ORM] resource %s already allocated to (pod %s/%s, container %v) with smaller number than request: requested: %d, allocated: %d; continue to getTopologyHints",
					resource, pod.GetNamespace(), pod.GetName(), container.Name, requested, int(math.Ceil(allocationInfo.AllocatedQuantity)))
			}
		}

		m.mutex.Lock()
		e, ok := m.endpoints[resource]
		m.mutex.Unlock()
		if !ok || e.Opts == nil || !e.Opts.WithTopologyAlignment {
			klog.V(5).Infof("[ORM] GetTopologyHints resource %s not supported", resource)
			continue
		}

		resourceReq := &pluginapi.ResourceRequest{
			PodUid:         podUID,
			PodNamespace:   pod.GetNamespace(),
			PodName:        pod.GetName(),
			ContainerName:  container.Name,
			ContainerType:  containerType,
			ContainerIndex: containerIndex,
			PodRole:        pod.Labels[pluginapi.PodRoleLabelKey],
			PodType:        pod.Annotations[pluginapi.PodTypeAnnotationKey],
			Labels:         maputil.CopySS(pod.Labels),
			Annotations:    maputil.CopySS(pod.Annotations),
			// use mapped resource name in "ResourceName" to indicates which endpoint to request
			ResourceName: resource,
			// use original requested resource name in "ResourceRequests" in order to make plugin identity real requested resource name
			ResourceRequests: map[string]float64{string(resourceObj): requestedObj.AsApproximateFloat64()},
		}

		resp, err := e.E.GetTopologyHints(context.Background(), resourceReq)
		if err != nil {
			klog.Errorf("[ORM] call GetTopologyHints of %s resource plugin for pod: %s/%s, container: %s failed with error: %v",
				resource, pod.GetNamespace(), pod.GetName(), contName, err)

			resourceHints[resource] = []topology.TopologyHint{}
			continue
		}

		resourceHints[resource] = ParseListOfTopologyHints(resp.ResourceHints[resource])

		klog.Infof("[ORM] GetTopologyHints for resource: %s, pod: %s/%s, container: %s, result: %+v",
			resource, pod.Namespace, pod.Name, contName, resourceHints[resource])
	}

	return resourceHints
}

func (m *ManagerImpl) GetPodTopologyHints(pod *v1.Pod) map[string][]topology.TopologyHint {
	// [TODO]: implement pod scope get topologyHints for provider and resource plugins.
	return nil
}

func (m *ManagerImpl) Allocate(pod *v1.Pod, container *v1.Container) error {
	if pod == nil || container == nil {
		return fmt.Errorf("Allocate got nil pod: %v or container: %v", pod, container)
	}

	err := m.addContainer(pod, container)
	if err != nil {
		return err
	}

	err = m.syncContainer(pod, container)
	return err
}

func (m *ManagerImpl) initDeviceProvider(config *config.Configuration) {
	switch config.ORMDevicesProvider {
	case kubeletDevicesProvider:
		p, err := kubelet.NewProvider(config.ORMKubeletPodResourcesEndpoints, podresourcesutil.GetV1Client)
		if err != nil {
			klog.Fatalf("new kubelet devices provider fail: %v", err)
		}
		m.devicesProvider = p
	case NoneDevicesProvider:
		m.devicesProvider = &podresources.DevicesProviderStub{}
	default:
		klog.Fatalf("Unknown ORMDevicesProvider: %s", config.ORMDevicesProvider)
	}
}

func (m *ManagerImpl) onPodAdd(podUID string) {
	klog.V(5).Infof("[ORM] onPodAdd: %v", podUID)

	timeout, cancel := context.WithTimeout(m.ctx, 1*time.Second)
	defer cancel()

	select {
	case m.podAddChan <- podUID:

	case <-timeout.Done():
		klog.Errorf("[ORM] add pod timeout: %v", podUID)
		_ = m.emitter.StoreInt64(MetricAddPodTimeout, 1, metrics.MetricTypeNameRaw)
	}
}

func (m *ManagerImpl) onPodDelete(podUID string) {
	klog.V(5).Infof("[ORM] onPodDelete: %v", podUID)

	timeout, cancel := context.WithTimeout(m.ctx, 1*time.Second)
	defer cancel()

	select {
	case m.podDeleteChan <- podUID:

	case <-timeout.Done():
		klog.Errorf("[ORM] delete pod timeout: %v", podUID)
		_ = m.emitter.StoreInt64(MetricDeletePodTImeout, 1, metrics.MetricTypeNameRaw)
	}
}

func (m *ManagerImpl) process() {
	klog.Infof("[ORM] start process...")

	for {
		select {
		case podUID := <-m.podAddChan:
			err := m.processAddPod(podUID)
			if err != nil {
				klog.Errorf("[ORM] processAddPod fail, podUID: %v, err: %v", podUID, err)
			}

		case podUID := <-m.podDeleteChan:
			err := m.processDeletePod(podUID)
			if err != nil {
				klog.Errorf("[ORM] processDeletePod fail, podUID: %v, err: %v", podUID, err)
			}

		case <-m.ctx.Done():
			klog.Infof("[ORM] ctx done, exit")
			return
		}
	}
}

func (m *ManagerImpl) processAddPod(podUID string) error {
	pod, err := m.metaManager.MetaServer.GetPod(m.ctx, podUID)
	if err != nil {
		klog.Errorf("[ORM] processAddPod getPod fail, podUID: %v, err: %v", podUID, err)
		return err
	}

	return m.topologyManager.Admit(pod)
}

func (m *ManagerImpl) processDeletePod(podUID string) error {
	allSuccess := true

	m.mutex.Lock()
	for resourceName, endpoint := range m.endpoints {
		_, err := endpoint.E.RemovePod(m.ctx, &pluginapi.RemovePodRequest{
			PodUid: podUID,
		})
		if err != nil {
			allSuccess = false
			klog.Errorf("[ORM] plugin %v remove pod %v fail: %v", resourceName, podUID, err)
		}
	}
	m.mutex.Unlock()

	if allSuccess {
		m.podResources.deletePod(podUID)
		m.topologyManager.RemovePod(podUID)
	}

	return m.writeCheckpoint()
}

func (m *ManagerImpl) addContainer(pod *v1.Pod, container *v1.Container) error {
	klog.V(5).Infof("[ORM] addContainer, pod: %v, container: %v", pod.Name, container.Name)

	systemCores, err := isPodKatalystQoSLevelSystemCores(m.qosConfig, pod)
	if err != nil {
		klog.Errorf("[ORM] check pod %s qos level fail: %v", pod.Name, err)
		return err
	}

	if native.CheckDaemonPod(pod) && !systemCores {
		klog.Infof("[ORM] skip pod: %s/%s, container: %s resource allocation",
			pod.Namespace, pod.Name, container.Name)
		return nil
	}

	containerType, containerIndex, err := GetContainerTypeAndIndex(pod, container)
	if err != nil {
		return err
	}

	for k, v := range container.Resources.Requests {
		needed := int(v.Value())
		resource, err := m.getMappedResourceName(string(k), container.Resources.Requests)
		if err != nil {
			klog.Errorf("resource %s getMappedResourceName fail: %v", string(k), err)
			return err
		}

		allocationInfo := m.podResources.containerResource(string(pod.UID), container.Name, resource)
		if allocationInfo != nil {
			allocated := int(math.Ceil(allocationInfo.AllocatedQuantity))

			if allocationInfo.IsScalarResource && allocated >= needed {
				klog.Infof("[ORM] resource %s already allocated to (pod %s/%s, container %v) with larger number than request: requested: %d, allocated: %d; not to allocate",
					resource, pod.GetNamespace(), pod.GetName(), container.Name, needed, allocated)
				continue
			} else {
				klog.Warningf("[ORM] resource %s already allocated to (pod %s/%s, container %v) with smaller number than request: requested: %d, allocated: %d; continue to allocate",
					resource, pod.GetNamespace(), pod.GetName(), container.Name, needed, allocated)
			}
		}

		m.mutex.Lock()
		e, ok := m.endpoints[resource]
		m.mutex.Unlock()
		if !ok {
			klog.V(5).Infof("[ORM] addContainer resource %s not supported", resource)
			continue
		}

		resourceReq := &pluginapi.ResourceRequest{
			PodUid:         string(pod.UID),
			PodNamespace:   pod.GetNamespace(),
			PodName:        pod.GetName(),
			ContainerName:  container.Name,
			ContainerType:  containerType,
			ContainerIndex: containerIndex,
			// PodRole and PodType should be identified by more general annotations
			PodRole: pod.Labels[pluginapi.PodRoleLabelKey],
			PodType: pod.Annotations[pluginapi.PodTypeAnnotationKey],
			// use mapped resource name in "ResourceName" to indicates which endpoint to request
			ResourceName: resource,
			// use original requested resource name in "ResourceRequests" in order to make plugin identity real requested resource name
			ResourceRequests: map[string]float64{resource: v.AsApproximateFloat64()},
			Labels:           maputil.CopySS(pod.Labels),
			Annotations:      maputil.CopySS(pod.Annotations),
		}

		if e.Opts != nil && e.Opts.WithTopologyAlignment {
			hint := m.topologyManager.GetAffinity(string(pod.UID), container.Name, resource)

			if hint.NUMANodeAffinity == nil {
				klog.Warningf("[ORM] pod: %s/%s; container: %s allocate resource: %s without numa nodes affinity",
					pod.Namespace, pod.Name, container.Name, resource)
			} else {
				klog.Warningf("[ORM] pod: %s/%s; container: %s allocate resource: %s get hint: %v from store",
					pod.Namespace, pod.Name, container.Name, resource, hint)
			}

			resourceReq.Hint = ParseTopologyManagerHint(hint)
		}

		response, err := e.E.Allocate(m.ctx, resourceReq)
		if err != nil {
			err = fmt.Errorf("[ORM] addContainer allocate fail, pod %v, container %v, err: %v", pod.Name, container.Name, err)
			klog.Error(err)
			return err
		}

		if response.AllocationResult == nil {
			klog.Warningf("[ORM] allocate for pod %v container %v resource %v got nil allocation result", pod.Name, container.Name, resource)
			continue
		}

		// update
		m.UpdatePodResources(response.AllocationResult.ResourceAllocation, pod, container, resource)
	}

	// write checkpoint
	return m.writeCheckpoint()
}

func (m *ManagerImpl) syncContainer(pod *v1.Pod, container *v1.Container) error {
	klog.Infof("[ORM] syncContainer, pod: %v, container: %v", pod.Name, container.Name)
	containerAllResources := m.podResources.containerAllResources(string(pod.UID), container.Name)
	if containerAllResources == nil {
		klog.V(5).Infof("got pod %v container %v resources nil", pod.Name, container.Name)
		return nil
	}

	err := m.resourceExecutor.UpdateContainerResources(pod, container, containerAllResources)
	if err != nil {
		klog.Errorf("[ORM] UpdateContainerResources fail, pod: %v, container: %v, err: %v", pod.Name, container.Name, err)
		return err
	}

	return nil
}

func (m *ManagerImpl) reconcile() {
	klog.V(5).Infof("[ORM] reconcile...")
	resourceAllocationResps := make(map[string]*pluginapi.GetResourcesAllocationResponse)
	activePods, err := m.metaManager.MetaServer.GetPodList(m.ctx, native.PodIsActive)
	if err != nil {
		klog.Errorf("[ORM] getPodList fail: %v", err)
		return
	}

	m.mutex.Lock()
	for resourceName, e := range m.endpoints {
		if e.E.IsStopped() {
			klog.Warningf("[ORM] skip getResourceAllocation of resource: %s, because plugin stopped", resourceName)
			continue
		} else if !e.Opts.NeedReconcile {
			klog.V(5).Infof("[ORM] skip getResourceAllocation of resource: %s, because plugin needn't reconciling", resourceName)
			continue
		}
		resp, err := e.E.GetResourceAllocation(m.ctx, &pluginapi.GetResourcesAllocationRequest{})
		if err != nil {
			klog.Errorf("[ORM] plugin %s getResourcesAllocation fail: %v", resourceName, err)
			continue
		}

		resourceAllocationResps[resourceName] = resp
	}
	m.mutex.Unlock()

	for _, pod := range activePods {
		if pod == nil {
			continue
		}
		systemCores, err := isPodKatalystQoSLevelSystemCores(m.qosConfig, pod)
		if err != nil {
			klog.Errorf("[ORM] check pod %s qos level fail: %v", pod.Name, err)
		}

		if native.CheckDaemonPod(pod) && !systemCores {
			continue
		}
		for _, container := range pod.Spec.Containers {

			needsReAllocate := false
			for resourceName, resp := range resourceAllocationResps {
				if resp == nil {
					klog.Warningf("[ORM] resource: %s got nil resourceAllocationResp", resourceName)
					continue
				}

				isRequested, err := m.IsContainerRequestResource(&container, resourceName)
				if err != nil {
					klog.Errorf("[ORM] IsContainerRequestResource fail, container %v,  resourceName %v, err: %v", container.Name, resourceName, err)
					continue
				}

				if isRequested {
					if resp.PodResources[string(pod.UID)] != nil && resp.PodResources[string(pod.UID)].ContainerResources[container.Name] != nil {
						resourceAllocations := resp.PodResources[string(pod.UID)].ContainerResources[container.Name]
						m.UpdatePodResources(resourceAllocations.ResourceAllocation, pod, &container, resourceName)
					} else {
						needsReAllocate = true
						m.podResources.deleteResourceAllocationInfo(string(pod.UID), container.Name, resourceName)
					}
				}
			}
			if needsReAllocate && !isSkippedContainer(pod, &container) {
				klog.Infof("[ORM] needs re-allocate resource plugin resources for pod %s/%s, container %s during reconcileState",
					pod.Namespace, pod.Name, container.Name)
				err = m.addContainer(pod, &container)
				if err != nil {
					klog.Errorf("[ORM] re addContainer fail, pod %v container %v, err: %v", pod.Name, container.Name, err)
					continue
				}
			}

			_ = m.syncContainer(pod, &container)
		}
	}

	err = m.writeCheckpoint()
	if err != nil {
		klog.Errorf("[ORM] writeCheckpoint: %v", err)
	}
}

func (m *ManagerImpl) UpdatePodResources(
	resourceAllocation map[string]*pluginapi.ResourceAllocationInfo,
	pod *v1.Pod, container *v1.Container, resource string,
) {
	for accResourceName, allocationInfo := range resourceAllocation {
		if allocationInfo == nil {
			klog.Warningf("[ORM] allocation request for resources %s - accompanying resource: %s for pod: %s/%s, container: %s got nil allocation information",
				resource, accResourceName, pod.Namespace, pod.Name, container.Name)
			continue
		}

		klog.V(4).Infof("[ORM] allocation information for resources %s - accompanying resource: %s for pod: %s/%s, container: %s is %v",
			resource, accResourceName, pod.Namespace, pod.Name, container.Name, *allocationInfo)

		m.podResources.insert(string(pod.UID), container.Name, accResourceName, allocationInfo)
	}
}

// getMappedResourceName returns mapped resource name of input "resourceName" in m.resourceNamesMap if there is the mapping entry,
// or it will return input "resourceName".
// If both the input "resourceName" and the mapped resource name are requested, it will return error.
func (m *ManagerImpl) getMappedResourceName(resourceName string, requests v1.ResourceList) (string, error) {
	if _, found := m.resourceNamesMap[resourceName]; !found {
		return resourceName, nil
	}

	mappedResourceName := m.resourceNamesMap[resourceName]

	_, foundReq := requests[v1.ResourceName(resourceName)]
	_, foundMappedReq := requests[v1.ResourceName(mappedResourceName)]

	if foundReq && foundMappedReq {
		return mappedResourceName, fmt.Errorf("both %s and mapped %s are requested", resourceName, mappedResourceName)
	}

	klog.V(5).Infof("[ORM] map resource name: %s to %s", resourceName, mappedResourceName)

	return mappedResourceName, nil
}

func (m *ManagerImpl) IsContainerRequestResource(container *v1.Container, resourceName string) (bool, error) {
	if container == nil {
		return false, nil
	}

	for k := range container.Resources.Requests {
		requestedResourceName, err := m.getMappedResourceName(string(k), container.Resources.Requests)
		if err != nil {
			return false, err
		}

		if requestedResourceName == resourceName {
			return true, nil
		}
	}

	return false, nil
}

func GetContainerTypeAndIndex(pod *v1.Pod, container *v1.Container) (containerType pluginapi.ContainerType, containerIndex uint64, err error) {
	if pod == nil || container == nil {
		err = fmt.Errorf("got nil pod: %v or container: %v", pod, container)
		return
	}

	foundContainer := false

	for i, initContainer := range pod.Spec.InitContainers {
		if container.Name == initContainer.Name {
			foundContainer = true
			containerType = pluginapi.ContainerType_INIT
			containerIndex = uint64(i)
			break
		}
	}

	if !foundContainer {
		mainContainerName := pod.Annotations[MainContainerNameAnnotationKey]

		if mainContainerName == "" && len(pod.Spec.Containers) > 0 {
			mainContainerName = pod.Spec.Containers[0].Name
		}

		for i, appContainer := range pod.Spec.Containers {
			if container.Name == appContainer.Name {
				foundContainer = true

				if container.Name == mainContainerName {
					containerType = pluginapi.ContainerType_MAIN
				} else {
					containerType = pluginapi.ContainerType_SIDECAR
				}

				containerIndex = uint64(i)
				break
			}
		}
	}

	if !foundContainer {
		err = fmt.Errorf("GetContainerTypeAndIndex doesn't find container: %s in pod: %s/%s", container.Name, pod.Namespace, pod.Name)
	}

	return
}

func isSkippedContainer(pod *v1.Pod, container *v1.Container) bool {
	containerType, _, err := GetContainerTypeAndIndex(pod, container)
	if err != nil {
		klog.Errorf("GetContainerTypeAndIndex failed with error: %v", err)
		return false
	}

	return containerType == pluginapi.ContainerType_INIT
}

func isPodKatalystQoSLevelSystemCores(qosConfig *generic.QoSConfiguration, pod *v1.Pod) (bool, error) {
	qosLevel, err := qosConfig.GetQoSLevelForPod(pod)
	if err != nil {
		return false, err
	}

	return qosLevel == pluginapi.KatalystQoSLevelSystemCores, nil
}

func ParseListOfTopologyHints(hintsList *pluginapi.ListOfTopologyHints) []topology.TopologyHint {
	if hintsList == nil {
		return nil
	}

	resultHints := make([]topology.TopologyHint, 0, len(hintsList.Hints))

	for _, hint := range hintsList.Hints {
		if hint != nil {

			mask := bitmask.NewEmptyBitMask()

			for _, node := range hint.Nodes {
				mask.Add(int(node))
			}

			resultHints = append(resultHints, topology.TopologyHint{
				NUMANodeAffinity: mask,
				Preferred:        hint.Preferred,
			})
		}
	}

	return resultHints
}

func ParseTopologyManagerHint(hint topology.TopologyHint) *pluginapi.TopologyHint {
	var nodes []uint64

	if hint.NUMANodeAffinity != nil {
		bits := hint.NUMANodeAffinity.GetBits()

		for _, node := range bits {
			nodes = append(nodes, uint64(node))
		}
	}

	return &pluginapi.TopologyHint{
		Nodes:     nodes,
		Preferred: hint.Preferred,
	}
}
