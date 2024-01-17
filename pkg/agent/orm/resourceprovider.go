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
	"fmt"

	//nolint
	"github.com/golang/protobuf/proto"
	v1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
	podresourcesapi "k8s.io/kubelet/pkg/apis/podresources/v1"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"
	resourcepluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"
	maputil "k8s.io/kubernetes/pkg/util/maps"

	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

func (m *ManagerImpl) GetTopologyAwareResources(pod *v1.Pod, container *v1.Container) []*podresourcesapi.TopologyAwareResource {
	resp, err := m.getTopologyAwareResources(pod, container)
	if err != nil {
		klog.Error(err)
		_ = m.emitter.StoreInt64(MetricGetTopologyAwareResourcesFail, 1, metrics.MetricTypeNameCount)
		return nil
	}

	if resp == nil || resp.ContainerTopologyAwareResources == nil {
		return nil
	}

	topologyAwareResources := make([]*podresourcesapi.TopologyAwareResource, 0, len(resp.ContainerTopologyAwareResources.AllocatedResources))

	for resourceName, resource := range resp.ContainerTopologyAwareResources.AllocatedResources {
		if resource == nil {
			continue
		}

		topologyAwareResources = append(topologyAwareResources, &podresourcesapi.TopologyAwareResource{
			ResourceName:                      resourceName,
			IsNodeResource:                    resource.IsNodeResource,
			IsScalarResource:                  resource.IsScalarResource,
			AggregatedQuantity:                resource.AggregatedQuantity,
			OriginalAggregatedQuantity:        resource.OriginalAggregatedQuantity,
			TopologyAwareQuantityList:         transformTopologyAwareQuantity(resource.TopologyAwareQuantityList),
			OriginalTopologyAwareQuantityList: transformTopologyAwareQuantity(resource.OriginalTopologyAwareQuantityList),
		})
	}

	return topologyAwareResources
}

func (m *ManagerImpl) GetTopologyAwareAllocatableResources() []*podresourcesapi.AllocatableTopologyAwareResource {
	resp, err := m.getTopologyAwareAllocatableResources()
	if err != nil {
		klog.Error(err)
		_ = m.emitter.StoreInt64(MetricGetTopologyAwareAllocatableResourcesFail, 1, metrics.MetricTypeNameCount)
		return nil
	}

	if resp == nil {
		return nil
	}

	allocatableTopologyAwareResources := make([]*podresourcesapi.AllocatableTopologyAwareResource, 0, len(resp.AllocatableResources))
	for resourceName, resource := range resp.AllocatableResources {
		if resource == nil {
			continue
		}

		allocatableTopologyAwareResources = append(allocatableTopologyAwareResources, &podresourcesapi.AllocatableTopologyAwareResource{
			ResourceName:                         resourceName,
			IsNodeResource:                       resource.IsNodeResource,
			IsScalarResource:                     resource.IsScalarResource,
			AggregatedAllocatableQuantity:        resource.AggregatedAllocatableQuantity,
			TopologyAwareAllocatableQuantityList: transformTopologyAwareQuantity(resource.TopologyAwareAllocatableQuantityList),
			AggregatedCapacityQuantity:           resource.AggregatedCapacityQuantity,
			TopologyAwareCapacityQuantityList:    transformTopologyAwareQuantity(resource.TopologyAwareCapacityQuantityList),
		})
	}

	return allocatableTopologyAwareResources
}

// UpdateAllocatedResources process add pods and delete pods synchronously.
func (m *ManagerImpl) UpdateAllocatedResources() {
	podsToBeAdded, podsToBeRemoved, err := m.metaManager.ReconcilePods()
	if err != nil {
		klog.Errorf("ReconcilePods fail: %v", err)
		_ = m.emitter.StoreInt64(MetricUpdateAllocatedResourcesFail, 1, metrics.MetricTypeNameCount)
		return
	}

	for _, podUID := range podsToBeAdded {
		err = m.processAddPod(podUID)
		if err != nil {
			klog.Errorf("ReconcilePods fail: %v", err)
			_ = m.emitter.StoreInt64(MetricUpdateAllocatedResourcesFail, 1, metrics.MetricTypeNameCount)
		}
	}

	for podUID := range podsToBeRemoved {
		err = m.processDeletePod(podUID)
		if err != nil {
			klog.Errorf("ReconcilePods fail: %v", err)
			_ = m.emitter.StoreInt64(MetricUpdateAllocatedResourcesFail, 1, metrics.MetricTypeNameCount)
		}
	}
	return
}

func (m *ManagerImpl) getTopologyAwareResources(pod *v1.Pod, container *v1.Container) (*pluginapi.GetTopologyAwareResourcesResponse, error) {
	var resp *pluginapi.GetTopologyAwareResourcesResponse

	if pod == nil || container == nil {
		err := fmt.Errorf("GetTopologyAwareResources got nil pod: %v or container: %v", pod, container)
		return nil, err
	}
	systemCores, err := isPodKatalystQoSLevelSystemCores(m.qosConfig, pod)
	if err != nil {
		err = fmt.Errorf("[ORM] check pod %s qos level fail: %v", pod.Name, err)
		return nil, err
	}
	if native.CheckDaemonPod(pod) && !systemCores {
		klog.V(5).Infof("[ORM] skip pod: %s, container: %v", pod.Name, container.Name)
		return nil, nil
	}

	m.mutex.RLock()
	defer m.mutex.RUnlock()
	for resourceName, eI := range m.endpoints {
		if eI.E.IsStopped() {
			klog.Warningf("[ORM] resource %s endpoints %s stopped, pod: %s, container: %s", resourceName, pod.Name, container.Name)
			continue
		}

		curResp, err := eI.E.GetTopologyAwareResources(m.ctx, &pluginapi.GetTopologyAwareResourcesRequest{
			PodUid:        string(pod.UID),
			ContainerName: container.Name,
		})
		if err != nil {
			return nil, fmt.Errorf("[ORM] getTopologyAwareResources for resource: %s failed with error: %v", resourceName, err)
		} else if curResp == nil {
			klog.Warningf("[ORM] getTopologyAwareResources of resource: %s for pod: %s container: %s, got nil response but without error", resourceName, pod.Name, container.Name)
			continue
		}

		if resp == nil {
			resp = curResp

			if resp.ContainerTopologyAwareResources == nil {
				resp.ContainerTopologyAwareResources = &pluginapi.ContainerTopologyAwareResources{
					ContainerName: container.Name,
				}
			}

			if resp.ContainerTopologyAwareResources.AllocatedResources == nil {
				resp.ContainerTopologyAwareResources.AllocatedResources = make(map[string]*pluginapi.TopologyAwareResource)
			}
		} else if curResp.ContainerTopologyAwareResources != nil && curResp.ContainerTopologyAwareResources.AllocatedResources != nil {
			for resourceName, topologyAwareResource := range curResp.ContainerTopologyAwareResources.AllocatedResources {
				if topologyAwareResource != nil {
					resp.ContainerTopologyAwareResources.AllocatedResources[resourceName] = proto.Clone(topologyAwareResource).(*pluginapi.TopologyAwareResource)
				}
			}
		} else {
			klog.Warningf("[ORM] getTopologyAwareResources of resource: %s for pod: %s container: %s, get nil resp or nil topologyAwareResources in resp",
				resourceName, pod.UID, container.Name)
		}
	}

	return resp, nil
}

func (m *ManagerImpl) getTopologyAwareAllocatableResources() (*pluginapi.GetTopologyAwareAllocatableResourcesResponse, error) {
	var resp *pluginapi.GetTopologyAwareAllocatableResourcesResponse

	m.mutex.RLock()
	defer m.mutex.RUnlock()
	for resourceName, eI := range m.endpoints {
		if eI.E.IsStopped() {
			klog.Warningf("[ORM] resource %s endpoints %s stopped", resourceName)
			continue
		}

		curResp, err := eI.E.GetTopologyAwareAllocatableResources(m.ctx, &pluginapi.GetTopologyAwareAllocatableResourcesRequest{})
		if err != nil {
			return nil, fmt.Errorf("[ORM] getTopologyAwareAllocatableResources for resource: %s failed with error: %v", resourceName, err)
		} else if curResp == nil {
			klog.Warningf("[ORM] getTopologyAwareAllocatableResources of resource: %s, got nil response but without error", resourceName)
			continue
		}

		if resp == nil {
			resp = curResp

			if resp.AllocatableResources == nil {
				resp.AllocatableResources = make(map[string]*pluginapi.AllocatableTopologyAwareResource)
			}
		} else if curResp.AllocatableResources != nil {
			for resourceName, topologyAwareResource := range curResp.AllocatableResources {
				if topologyAwareResource != nil {
					resp.AllocatableResources[resourceName] = proto.Clone(topologyAwareResource).(*pluginapi.AllocatableTopologyAwareResource)
				}
			}
		} else {
			klog.Warningf("[ORM] getTopologyAwareAllocatableResources of resource: %s, get nil resp or nil topologyAwareResources in resp", resourceName)
		}
	}

	return resp, nil
}

func transformTopologyAwareQuantity(pluginAPITopologyAwareQuantityList []*resourcepluginapi.TopologyAwareQuantity) []*podresourcesapi.TopologyAwareQuantity {
	if pluginAPITopologyAwareQuantityList == nil {
		return nil
	}

	topologyAwareQuantityList := make([]*podresourcesapi.TopologyAwareQuantity, 0, len(pluginAPITopologyAwareQuantityList))

	for _, topologyAwareQuantity := range pluginAPITopologyAwareQuantityList {
		if topologyAwareQuantity != nil {
			topologyAwareQuantityList = append(topologyAwareQuantityList, &podresourcesapi.TopologyAwareQuantity{
				ResourceValue: topologyAwareQuantity.ResourceValue,
				Node:          topologyAwareQuantity.Node,
				Name:          topologyAwareQuantity.Name,
				Type:          topologyAwareQuantity.Type,
				TopologyLevel: transformTopologyLevel(topologyAwareQuantity.TopologyLevel),
				Annotations:   maputil.CopySS(topologyAwareQuantity.Annotations),
			})
		}
	}

	return topologyAwareQuantityList
}

func transformTopologyLevel(pluginAPITopologyLevel resourcepluginapi.TopologyLevel) podresourcesapi.TopologyLevel {
	switch pluginAPITopologyLevel {
	case resourcepluginapi.TopologyLevel_NUMA:
		return podresourcesapi.TopologyLevel_NUMA
	case resourcepluginapi.TopologyLevel_SOCKET:
		return podresourcesapi.TopologyLevel_SOCKET
	}

	klog.Warningf("[transformTopologyLevel] unrecognized pluginAPITopologyLevel %s:%v, set podResouresAPITopologyLevel to default value: %s:%v",
		pluginAPITopologyLevel.String(), pluginAPITopologyLevel, podresourcesapi.TopologyLevel_NUMA.String(), podresourcesapi.TopologyLevel_NUMA)
	return podresourcesapi.TopologyLevel_NUMA
}
