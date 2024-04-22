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

package kubelet

import (
	"context"
	"encoding/json"
	"time"

	"google.golang.org/grpc"
	"k8s.io/klog/v2"
	podresourcesapi "k8s.io/kubelet/pkg/apis/podresources/v1"

	podresourcesserver "github.com/kubewharf/katalyst-core/pkg/agent/orm/server/podresources"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/kubelet/podresources"
)

const (
	podResourcesClientTimeout    = 10 * time.Second
	podResourcesClientMaxMsgSize = 1024 * 1024 * 16
)

// Provider provide devices resource by kubelet v1 podResources api
type Provider struct {
	client    podresourcesapi.PodResourcesListerClient
	endpoints []string
	conn      *grpc.ClientConn
}

func NewProvider(endpoints []string, getClientFunc podresources.GetClientFunc) (podresourcesserver.DevicesProvider, error) {
	klog.V(5).Infof("new kubelet devices provider, endpoints: %v", endpoints)

	p := &Provider{
		endpoints: endpoints,
	}

	var err error

	p.client, p.conn, err = getClientFunc(
		general.GetOneExistPath(endpoints),
		podResourcesClientTimeout,
		podResourcesClientMaxMsgSize)
	if err != nil {
		klog.Error(err)
		return nil, err
	}

	return p, nil
}

func (p *Provider) GetDevices() []*podresourcesapi.PodResources {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*1)
	defer cancel()

	response, err := p.client.List(ctx, &podresourcesapi.ListPodResourcesRequest{})
	if err != nil {
		klog.Errorf("list resources from kubelet fail: %v", err)
		return nil
	}
	if response == nil {
		klog.Error("list resources from kubelet, get nil response without err")
		return nil
	}
	if response.GetPodResources() == nil {
		klog.Error("list resources from kubelet, get nil podResources without err")
		return nil
	}

	if klog.V(6).Enabled() {
		str, _ := json.Marshal(response)
		klog.Infof("GetDevices: %v", string(str))
	}

	return response.PodResources
}

func (p *Provider) GetAllocatableDevices() []*podresourcesapi.ContainerDevices {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*1)
	defer cancel()

	response, err := p.client.GetAllocatableResources(ctx, &podresourcesapi.AllocatableResourcesRequest{})
	if err != nil {
		klog.Errorf("GetAllocatableResources from kubelet fail: %v", err)
		return nil
	}
	if response == nil {
		klog.Error("GetAllocatableResources from kubelet, get nil response without err")
		return nil
	}
	if response.GetDevices() == nil {
		klog.Error("GetAllocatableResources from kubelet, get nil response without err")
		return nil
	}

	if klog.V(6).Enabled() {
		str, _ := json.Marshal(response)
		klog.Infof("GetAllocatableDevices: %v", str)
	}
	return response.GetDevices()
}
