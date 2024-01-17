package server

import (
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"google.golang.org/grpc"
	"k8s.io/klog/v2"

	podresourcesapi "k8s.io/kubelet/pkg/apis/podresources/v1"
	kubeletutil "k8s.io/kubernetes/pkg/kubelet/util"

	"github.com/kubewharf/katalyst-core/pkg/agent/orm/server/podresources"
)

// ListenAndServePodResources initializes a gRPC server to serve the PodResources service
func ListenAndServePodResources(socket string, podsProvider podresources.PodsProvider, resourcesProvider podresources.ResourcesProvider, emitter metrics.MetricEmitter) {
	klog.V(5).Infof("ListenAndServePodResources ...")
	server := grpc.NewServer()
	podresourcesapi.RegisterPodResourcesListerServer(server, podresources.NewV1PodResourcesServer(podsProvider, resourcesProvider, emitter))

	l, err := kubeletutil.CreateListener(socket)
	if err != nil {
		klog.Fatalf("failed to create listener for podResources endpoint: %v", err)
	}

	if err := server.Serve(l); err != nil {
		klog.Fatalf("server podResources fail: %v", err)
	}
}
