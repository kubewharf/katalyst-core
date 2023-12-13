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
	"log"
	"net"
	"os"
	"path"
	"sync"
	"time"

	"google.golang.org/grpc/credentials/insecure"

	"google.golang.org/grpc"
	watcherapi "k8s.io/kubelet/pkg/apis/pluginregistration/v1"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"
)

type Stub struct {
	socket                string
	resourceName          string
	preStartContainerFlag bool

	stop chan interface{}
	wg   sync.WaitGroup

	server *grpc.Server

	// allocFunc1 is used for handling allocation request
	allocFunc1 stubAllocFunc1
	//handling get allocation request
	allocFunc2 stubAllocFunc2

	registrationStatus chan watcherapi.RegistrationStatus // for testing
	endpoint           string                             // for testing
}

// stubAllocFunc1 is the function called when an allocation request is received from Kubelet
type stubAllocFunc1 func(r *pluginapi.ResourceRequest) (*pluginapi.ResourceAllocationResponse, error)

// stubAllocFYnc2 is the function called when a get allocation request is received form Kubelet
type stubAllocFunc2 func(r *pluginapi.GetResourcesAllocationRequest) (*pluginapi.GetResourcesAllocationResponse, error)

func defaultAllocFunc(r *pluginapi.ResourceRequest) (*pluginapi.ResourceAllocationResponse, error) {
	var response pluginapi.ResourceAllocationResponse

	return &response, nil
}
func defaultGetAllocFunc(r *pluginapi.GetResourcesAllocationRequest) (*pluginapi.GetResourcesAllocationResponse, error) {
	var response pluginapi.GetResourcesAllocationResponse
	return &response, nil
}

// NewResourcePluginStub returns an initialized ResourcePlugin Stub.
func NewResourcePluginStub(socket string, name string, preStartContainerFlag bool) *Stub {
	return &Stub{
		socket:                socket,
		resourceName:          name,
		preStartContainerFlag: preStartContainerFlag,

		stop: make(chan interface{}),

		allocFunc1: defaultAllocFunc,
		allocFunc2: defaultGetAllocFunc,
	}
}

// SetAllocFunc sets allocFunc of the resource plugin
func (m *Stub) SetAllocFunc(f stubAllocFunc1) {
	m.allocFunc1 = f
}
func (m *Stub) SetGetAllocFunc(f stubAllocFunc2) {
	m.allocFunc2 = f
}

// Start starts the gRPC server of the resource plugin. Can only
// be called once.
func (m *Stub) Start() error {
	err := m.cleanup()
	if err != nil {
		return err
	}

	sock, err := net.Listen("unix", m.socket)
	if err != nil {
		return err
	}

	m.wg.Add(1)
	m.server = grpc.NewServer([]grpc.ServerOption{}...)
	pluginapi.RegisterResourcePluginServer(m.server, m)
	watcherapi.RegisterRegistrationServer(m.server, m)

	go func() {
		defer func() {
			m.wg.Done()

			if err := recover(); err != nil {
				log.Fatalf("Start recover from err: %v", err)
			}
		}()
		m.server.Serve(sock)
	}()
	_, conn, err := dial(m.socket)
	if err != nil {
		return err
	}
	conn.Close()
	log.Printf("Starting to serve on %v", m.socket)

	return nil
}

// Stop stops the gRPC server. Can be called without a prior Start
// and more than once. Not safe to be called concurrently by different
// goroutines!
func (m *Stub) Stop() error {
	if m.server == nil {
		return nil
	}
	m.server.Stop()
	m.wg.Wait()
	m.server = nil
	close(m.stop) // This prevents re-starting the server.

	return m.cleanup()
}

// GetInfo is the RPC which return pluginInfo
func (m *Stub) GetInfo(ctx context.Context, req *watcherapi.InfoRequest) (*watcherapi.PluginInfo, error) {
	log.Println("GetInfo")
	return &watcherapi.PluginInfo{
		Type:              watcherapi.ResourcePlugin,
		Name:              m.resourceName,
		Endpoint:          m.endpoint,
		SupportedVersions: []string{pluginapi.Version}}, nil
}

// NotifyRegistrationStatus receives the registration notification from watcher
func (m *Stub) NotifyRegistrationStatus(ctx context.Context, status *watcherapi.RegistrationStatus) (*watcherapi.RegistrationStatusResponse, error) {
	if m.registrationStatus != nil {
		m.registrationStatus <- *status
	}
	if !status.PluginRegistered {
		log.Printf("Registration failed: %v", status.Error)
	}
	return &watcherapi.RegistrationStatusResponse{}, nil
}

// Register registers the resource plugin for the given resourceName with Kubelet.
func (m *Stub) Register(kubeletEndpoint, resourceName string, pluginSockDir string) error {
	if pluginSockDir != "" {
		if _, err := os.Stat(pluginSockDir + "DEPRECATION"); err == nil {
			log.Println("Deprecation file found. Skip registration.")
			return nil
		}
	}
	log.Println("Deprecation file not found. Invoke registration")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, err := grpc.DialContext(ctx, kubeletEndpoint, grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithBlock(),
		grpc.WithContextDialer(func(ctx context.Context, addr string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", addr)
		}))
	if err != nil {
		return err
	}
	defer conn.Close()
	client := pluginapi.NewRegistrationClient(conn)
	reqt := &pluginapi.RegisterRequest{
		Version:      pluginapi.Version,
		Endpoint:     path.Base(m.socket),
		ResourceName: resourceName,
		Options: &pluginapi.ResourcePluginOptions{
			PreStartRequired: m.preStartContainerFlag,
		},
	}

	_, err = client.Register(context.Background(), reqt)
	if err != nil {
		return err
	}
	return nil
}

// GetResourcePluginOptions returns ResourcePluginOptions settings for the resource plugin.
func (m *Stub) GetResourcePluginOptions(ctx context.Context, e *pluginapi.Empty) (*pluginapi.ResourcePluginOptions, error) {
	options := &pluginapi.ResourcePluginOptions{
		PreStartRequired: m.preStartContainerFlag,
	}
	return options, nil
}

// PreStartContainer resets the resources received
func (m *Stub) PreStartContainer(ctx context.Context, r *pluginapi.PreStartContainerRequest) (*pluginapi.PreStartContainerResponse, error) {
	log.Printf("PreStartContainer, %+v", r)
	return &pluginapi.PreStartContainerResponse{}, nil
}

// Allocate does a mock allocation
func (m *Stub) Allocate(ctx context.Context, r *pluginapi.ResourceRequest) (*pluginapi.ResourceAllocationResponse, error) {
	log.Printf("Allocate, %+v", r)

	return m.allocFunc1(r)
}

func (m *Stub) cleanup() error {
	if err := os.Remove(m.socket); err != nil && !os.IsNotExist(err) {
		return err
	}

	return nil
}

// GetResourcesAllocation returns allocation results of corresponding resources
func (m *Stub) GetResourcesAllocation(ctx context.Context, r *pluginapi.GetResourcesAllocationRequest) (*pluginapi.GetResourcesAllocationResponse, error) {
	log.Printf("GetResourcesAllocation, %+v", r)
	return m.allocFunc2(r)
}

// GetTopologyAwareResources returns allocation results of corresponding resources as topology aware format
func (m *Stub) GetTopologyAwareResources(ctx context.Context, r *pluginapi.GetTopologyAwareResourcesRequest) (*pluginapi.GetTopologyAwareResourcesResponse, error) {
	log.Printf("GetTopologyAwareResources, %+v", r)
	return &pluginapi.GetTopologyAwareResourcesResponse{}, nil
}

// GetTopologyAwareResources returns corresponding allocatable resources as topology aware format
func (m *Stub) GetTopologyAwareAllocatableResources(ctx context.Context, r *pluginapi.GetTopologyAwareAllocatableResourcesRequest) (*pluginapi.GetTopologyAwareAllocatableResourcesResponse, error) {
	log.Printf("GetTopologyAwareAllocatableResources, %+v", r)
	return &pluginapi.GetTopologyAwareAllocatableResourcesResponse{}, nil
}

// GetTopologyHints returns hints of corresponding resources
func (m *Stub) GetTopologyHints(ctx context.Context, r *pluginapi.ResourceRequest) (*pluginapi.ResourceHintsResponse, error) {
	log.Printf("GetTopologyHints, %+v", r)
	return &pluginapi.ResourceHintsResponse{}, nil
}

// Notify the resource plugin that the pod has beed deleted,
// and the plugin should do some clear-up work.
func (m *Stub) RemovePod(ctx context.Context, r *pluginapi.RemovePodRequest) (*pluginapi.RemovePodResponse, error) {
	log.Printf("RemovePod, %+v", r)
	return &pluginapi.RemovePodResponse{}, nil
}
