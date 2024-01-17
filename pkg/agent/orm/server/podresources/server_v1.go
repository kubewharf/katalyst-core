package podresources

import (
	"context"

	v1 "k8s.io/kubelet/pkg/apis/podresources/v1"
	maputil "k8s.io/kubernetes/pkg/util/maps"

	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

type v1PodResourcesServer struct {
	podsProvider      PodsProvider
	resourcesProvider ResourcesProvider
	emitter           metrics.MetricEmitter
}

func NewV1PodResourcesServer(podsProvider PodsProvider, resourcesProvider ResourcesProvider, emitter metrics.MetricEmitter) v1.PodResourcesListerServer {
	return &v1PodResourcesServer{
		podsProvider:      podsProvider,
		resourcesProvider: resourcesProvider,
		emitter:           emitter,
	}
}

// List returns information about the resources assigned to pods on the node
func (p *v1PodResourcesServer) List(ctx context.Context, req *v1.ListPodResourcesRequest) (*v1.ListPodResourcesResponse, error) {

	pods := p.podsProvider.GetPods()
	podResources := make([]*v1.PodResources, len(pods))
	p.resourcesProvider.UpdateAllocatedResources()

	for i, pod := range pods {
		pRes := v1.PodResources{
			Name:        pod.Name,
			Namespace:   pod.Namespace,
			Labels:      maputil.CopySS(pod.Labels),
			Annotations: maputil.CopySS(pod.Annotations),
			Containers:  make([]*v1.ContainerResources, len(pod.Spec.Containers)),
		}

		for j, container := range pod.Spec.Containers {
			pRes.Containers[j] = &v1.ContainerResources{
				Name:      container.Name,
				Resources: p.resourcesProvider.GetTopologyAwareResources(pod, &container),
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
	}, nil
}
