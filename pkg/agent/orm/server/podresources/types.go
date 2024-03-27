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

//go:generate mockgen -source=types.go -destination=testing/provider_mock.go -package=testing DevicesProvider,PodsProvider,ResourcesProvider
package podresources

import (
	v1 "k8s.io/api/core/v1"
	podresourcesapi "k8s.io/kubelet/pkg/apis/podresources/v1"
)

const (
	podResourcesList                   = "podResourcesList"
	podResourceGetAllocatableResources = "podResourceGetAllocatableResources"
)

// PodsProvider knows how to provide the pods admitted by the node
type PodsProvider interface {
	GetPods() []*v1.Pod
}

// ResourcesProvider knows how to provide the resources used by the given container
type ResourcesProvider interface {
	// UpdateAllocatedResources frees any Resources that are bound to terminated pods.
	UpdateAllocatedResources()
	// GetTopologyAwareResources returns information about the resources assigned to pods and containers in topology aware format
	GetTopologyAwareResources(pod *v1.Pod, container *v1.Container) []*podresourcesapi.TopologyAwareResource
	// GetTopologyAwareAllocatableResources returns information about all the resources known to the manager in topology aware format
	GetTopologyAwareAllocatableResources() []*podresourcesapi.AllocatableTopologyAwareResource
}

// DevicesProvider knows how to provide the devices used by the given container
type DevicesProvider interface {
	// GetDevices returns information about the devices assigned to pods and containers
	GetDevices() []*podresourcesapi.PodResources
	// GetAllocatableDevices returns information about all the devices known to the manager
	GetAllocatableDevices() []*podresourcesapi.ContainerDevices
}

type DevicesProviderStub struct{}

func (p *DevicesProviderStub) GetDevices() []*podresourcesapi.PodResources {
	return nil
}

func (p *DevicesProviderStub) GetAllocatableDevices() []*podresourcesapi.ContainerDevices {
	return nil
}
