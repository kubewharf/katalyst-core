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

package podresources

import (
	"context"

	v1 "k8s.io/kubelet/pkg/apis/podresources/v1"
	maputil "k8s.io/kubernetes/pkg/util/maps"

	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

type v1PodResourcesServer struct {
	podsProvider      PodsProvider
	resourcesProvider ResourcesProvider
	devicesProvider   DevicesProvider
	emitter           metrics.MetricEmitter
}

func NewV1PodResourcesServer(
	podsProvider PodsProvider,
	resourcesProvider ResourcesProvider,
	devicesProvider DevicesProvider,
	emitter metrics.MetricEmitter,
) v1.PodResourcesListerServer {
	return &v1PodResourcesServer{
		podsProvider:      podsProvider,
		resourcesProvider: resourcesProvider,
		devicesProvider:   devicesProvider,
		emitter:           emitter,
	}
}

// List returns information about the resources assigned to pods on the node
func (p *v1PodResourcesServer) List(ctx context.Context, req *v1.ListPodResourcesRequest) (*v1.ListPodResourcesResponse, error) {
	pods := p.podsProvider.GetPods()
	podResources := make([]*v1.PodResources, len(pods))

	// sync pods resource before list
	p.resourcesProvider.UpdateAllocatedResources()

	devicesResources := p.getDevices()

	for i, pod := range pods {
		pRes := v1.PodResources{
			Name:        pod.Name,
			Namespace:   pod.Namespace,
			Labels:      maputil.CopySS(pod.Labels),
			Annotations: maputil.CopySS(pod.Annotations),
			Containers:  make([]*v1.ContainerResources, len(pod.Spec.Containers)),
		}

		podKey := native.GenerateNamespaceNameKey(pod.Namespace, pod.Name)
		podDevicesResources, ok := devicesResources[podKey]

		for j, container := range pod.Spec.Containers {
			pRes.Containers[j] = &v1.ContainerResources{
				Name:      container.Name,
				Resources: p.resourcesProvider.GetTopologyAwareResources(pod, &container),
			}
			if ok {
				pRes.Containers[j].Devices = podDevicesResources[container.Name]
			}
		}
		podResources[i] = &pRes
	}

	_ = p.emitter.StoreInt64(podResourcesList, 1, metrics.MetricTypeNameCount)
	return &v1.ListPodResourcesResponse{
		PodResources: podResources,
	}, nil
}

func (p *v1PodResourcesServer) GetAllocatableResources(ctx context.Context, req *v1.AllocatableResourcesRequest) (*v1.AllocatableResourcesResponse, error) {
	_ = p.emitter.StoreInt64(podResourceGetAllocatableResources, 1, metrics.MetricTypeNameCount)

	return &v1.AllocatableResourcesResponse{
		Resources: p.resourcesProvider.GetTopologyAwareAllocatableResources(),
		Devices:   p.devicesProvider.GetAllocatableDevices(),
	}, nil
}

func (p *v1PodResourcesServer) getDevices() map[string]map[string][]*v1.ContainerDevices {
	res := make(map[string]map[string][]*v1.ContainerDevices)

	podResources := p.devicesProvider.GetDevices()
	if len(podResources) == 0 {
		return nil
	}
	for _, podResource := range podResources {
		key := native.GenerateNamespaceNameKey(podResource.Namespace, podResource.Name)
		res[key] = make(map[string][]*v1.ContainerDevices)
		for _, container := range podResource.Containers {
			res[key][container.Name] = container.GetDevices()
		}
	}

	return res
}
