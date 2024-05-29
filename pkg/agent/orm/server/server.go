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

package server

import (
	"google.golang.org/grpc"
	"k8s.io/klog/v2"

	podresourcesapi "k8s.io/kubelet/pkg/apis/podresources/v1"
	kubeletutil "k8s.io/kubernetes/pkg/kubelet/util"

	"github.com/kubewharf/katalyst-core/pkg/agent/orm/server/podresources"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

// ListenAndServePodResources initializes a gRPC server to serve the PodResources service
func ListenAndServePodResources(
	socket string,
	podsProvider podresources.PodsProvider,
	resourcesProvider podresources.ResourcesProvider,
	devicesProvider podresources.DevicesProvider,
	emitter metrics.MetricEmitter,
) {
	klog.V(5).Infof("ListenAndServePodResources ...")
	server := grpc.NewServer()
	podresourcesapi.RegisterPodResourcesListerServer(server, podresources.NewV1PodResourcesServer(podsProvider, resourcesProvider, devicesProvider, emitter))

	l, err := kubeletutil.CreateListener(socket)
	if err != nil {
		klog.Fatalf("failed to create listener for podResources endpoint: %v", err)
	}

	if err := server.Serve(l); err != nil {
		klog.Fatalf("server podResources fail: %v", err)
	}
}
