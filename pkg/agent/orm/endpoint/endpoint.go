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

package endpoint

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"google.golang.org/grpc/credentials/insecure"

	"google.golang.org/grpc"
	"k8s.io/klog/v2"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"
)

// Endpoint maps to a single registered resource plugin. It is responsible
// for managing gRPC communications with the resource plugin and caching
// resource states reported by the resource plugin.
type Endpoint interface {
	Stop()
	Allocate(c context.Context, resourceRequest *pluginapi.ResourceRequest) (*pluginapi.ResourceAllocationResponse, error)
	GetTopologyHints(c context.Context, resourceRequest *pluginapi.ResourceRequest) (*pluginapi.ResourceHintsResponse, error)
	GetResourceAllocation(c context.Context, request *pluginapi.GetResourcesAllocationRequest) (*pluginapi.GetResourcesAllocationResponse, error)
	RemovePod(c context.Context, removePodRequest *pluginapi.RemovePodRequest) (*pluginapi.RemovePodResponse, error)
	IsStopped() bool
	StopGracePeriodExpired() bool
	GetResourcePluginOptions(ctx context.Context, in *pluginapi.Empty, opts ...grpc.CallOption) (*pluginapi.ResourcePluginOptions, error)
}

type EndpointInfo struct {
	E    Endpoint
	Opts *pluginapi.ResourcePluginOptions
}

type EndpointImpl struct {
	client     pluginapi.ResourcePluginClient
	clientConn *grpc.ClientConn

	socketPath   string
	resourceName string
	stopTime     time.Time

	mutex sync.Mutex
}

// NewEndpointImpl creates a new endpoint for the given resourceName.
// This is to be used during normal resource plugin registration.
func NewEndpointImpl(socketPath, resourceName string) (*EndpointImpl, error) {
	client, c, err := dial(socketPath)
	if err != nil {
		klog.Errorf("[ORM] Can't create new endpoint with path %s err %v", socketPath, err)
		return nil, err
	}

	return &EndpointImpl{
		client:     client,
		clientConn: c,

		socketPath:   socketPath,
		resourceName: resourceName,
	}, nil
}

func (e *EndpointImpl) Client() pluginapi.ResourcePluginClient {
	return e.client
}

// NewStoppedEndpointImpl creates a new endpoint for the given resourceName with stopTime set.
// This is to be used during Kubelet restart, before the actual resource plugin re-registers.
func NewStoppedEndpointImpl(resourceName string) *EndpointImpl {
	return &EndpointImpl{
		resourceName: resourceName,
		stopTime:     time.Now(),
	}
}

func (e *EndpointImpl) IsStopped() bool {
	e.mutex.Lock()
	defer e.mutex.Unlock()
	return !e.stopTime.IsZero()
}

func (e *EndpointImpl) StopGracePeriodExpired() bool {
	e.mutex.Lock()
	defer e.mutex.Unlock()
	return !e.stopTime.IsZero() && time.Since(e.stopTime) > endpointStopGracePeriod
}

// used for testing only
func (e *EndpointImpl) setStopTime(t time.Time) {
	e.mutex.Lock()
	defer e.mutex.Unlock()
	e.stopTime = t
}

// allocate issues Allocate gRPC call to the resource plugin.
func (e *EndpointImpl) Allocate(c context.Context, resourceRequest *pluginapi.ResourceRequest) (*pluginapi.ResourceAllocationResponse, error) {
	if e.IsStopped() {
		return nil, fmt.Errorf(errEndpointStopped, e)
	}
	ctx, cancel := context.WithTimeout(c, pluginapi.KubeletResourcePluginAllocateRPCTimeoutInSecs*time.Second)
	defer cancel()
	return e.client.Allocate(ctx, resourceRequest)
}

func (e *EndpointImpl) Stop() {
	e.mutex.Lock()
	defer e.mutex.Unlock()
	if e.clientConn != nil {
		e.clientConn.Close()
	}
	e.stopTime = time.Now()
}

func (e *EndpointImpl) GetResourceAllocation(c context.Context, request *pluginapi.GetResourcesAllocationRequest) (*pluginapi.GetResourcesAllocationResponse, error) {
	if e.IsStopped() {
		return nil, fmt.Errorf(errEndpointStopped, e)
	}
	ctx, cancel := context.WithTimeout(c, pluginapi.KubeletResourcePluginGetResourcesAllocationRPCTimeoutInSecs*time.Second)
	defer cancel()
	return e.client.GetResourcesAllocation(ctx, request)
}

func (e *EndpointImpl) RemovePod(c context.Context, removePodRequest *pluginapi.RemovePodRequest) (*pluginapi.RemovePodResponse, error) {
	if e.IsStopped() {
		return nil, fmt.Errorf(errEndpointStopped, e)
	}
	ctx, cancel := context.WithTimeout(c, pluginapi.KubeletResourcePluginRemovePodRPCTimeoutInSecs*time.Second)
	defer cancel()
	return e.client.RemovePod(ctx, removePodRequest)
}

func (e *EndpointImpl) GetResourcePluginOptions(ctx context.Context, in *pluginapi.Empty, opts ...grpc.CallOption) (*pluginapi.ResourcePluginOptions, error) {
	return e.client.GetResourcePluginOptions(ctx, in, opts...)
}

func (e *EndpointImpl) GetTopologyHints(c context.Context, resourceRequest *pluginapi.ResourceRequest) (*pluginapi.ResourceHintsResponse, error) {
	if e.IsStopped() {
		return nil, fmt.Errorf(errEndpointStopped, e)
	}
	ctx, cancel := context.WithTimeout(c, pluginapi.KubeletResourcePluginGetTopologyHintsRPCTimeoutInSecs*time.Second)
	defer cancel()

	return e.client.GetTopologyHints(ctx, resourceRequest)
}

// dial establishes the gRPC communication with the registered resource plugin. https://godoc.org/google.golang.org/grpc#Dial
func dial(unixSocketPath string) (pluginapi.ResourcePluginClient, *grpc.ClientConn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	c, err := grpc.DialContext(ctx, unixSocketPath, grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithBlock(),
		grpc.WithContextDialer(func(ctx context.Context, addr string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", addr)
		}),
	)

	if err != nil {
		return nil, nil, fmt.Errorf(errFailedToDialResourcePlugin+" %v", err)
	}

	return pluginapi.NewResourcePluginClient(c), c, nil
}
