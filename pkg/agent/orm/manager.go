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
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/opencontainers/selinux/go-selinux"
	"google.golang.org/grpc"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"
	"k8s.io/kubelet/pkg/apis/pluginregistration/v1"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"
	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager"
	maputil "k8s.io/kubernetes/pkg/util/maps"

	"github.com/kubewharf/katalyst-core/pkg/agent/orm/endpoint"
	"github.com/kubewharf/katalyst-core/pkg/agent/orm/executor"
	"github.com/kubewharf/katalyst-core/pkg/agent/orm/metamanager"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	cgroupmgr "github.com/kubewharf/katalyst-core/pkg/util/cgroup/manager"
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

	server *grpc.Server
	wg     sync.WaitGroup

	podAddChan    chan string
	podDeleteChan chan string

	podResources      *podResourcesChk
	checkpointManager checkpointmanager.CheckpointManager

	emitter   metrics.MetricEmitter
	qosConfig *generic.QoSConfiguration

	reconcilePeriod  time.Duration
	resourceNamesMap map[string]string
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

		podAddChan:    make(chan string, config.ORMPodNotifyChanLen),
		podDeleteChan: make(chan string, config.ORMPodNotifyChanLen),
		emitter:       emitter,
		qosConfig:     config.QoSConfiguration,
	}

	m.resourceExecutor = executor.NewExecutor(cgroupmgr.GetManager())

	metaManager := metamanager.NewManager(emitter, m.podResources.pods, metaServer)
	m.metaManager = metaManager

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

	if err = os.MkdirAll(m.socketdir, 0750); err != nil {
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
}

func (m *ManagerImpl) GetHandlerType() string {
	return pluginregistration.ResourcePlugin
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

	for _, container := range append(pod.Spec.InitContainers, pod.Spec.Containers...) {
		err = m.addContainer(pod, &container)
		if err != nil {
			klog.Errorf("[ORM] add container fail, pod: %v, container: %v, err: %v", pod.Name, container.Name, err)
			return err
		}

		_ = m.syncContainer(pod, &container)
	}

	return nil
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

	for k, v := range container.Resources.Requests {
		resource, err := m.getMappedResourceName(string(k), container.Resources.Requests)
		if err != nil {
			klog.Errorf("resource %s getMappedResourceName fail: %v", string(k), err)
			return err
		}

		m.mutex.Lock()
		e, ok := m.endpoints[resource]
		m.mutex.Unlock()
		if !ok {
			klog.V(5).Infof("[ORM] addContainer resource %s not supported", resource)
			continue
		}

		containerType, containerIndex, err := GetContainerTypeAndIndex(pod, container)
		if err != nil {
			return err
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
			// hint is not used in ORM but it can not be nil
			Hint: &pluginapi.TopologyHint{},
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
	pod *v1.Pod, container *v1.Container, resource string) {
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
