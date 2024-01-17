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
