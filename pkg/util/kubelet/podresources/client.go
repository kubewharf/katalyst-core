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
	"errors"
	"fmt"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"k8s.io/klog/v2"
	v1 "k8s.io/kubelet/pkg/apis/podresources/v1"
	"k8s.io/kubernetes/pkg/kubelet/util"

	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

const (
	podResourcesClientTimeout              = 10 * time.Second
	podResourcesClientMaxMsgSize           = 1024 * 1024 * 16
	checkPodResourcesSocketInterval        = 100 * time.Millisecond
	checkPodResourcesSocketTimeoutDuration = 10 * time.Second
)

var errNotInitialized = errors.New("pod resources client not initialized")

type podResourcesClient struct {
	sync.RWMutex

	isInitialized bool
	client        v1.PodResourcesListerClient
}

// EndpointAutoDiscoveryPodResourcesClient returns a podResourcesClient and a channel that can be used to close the connection.
func EndpointAutoDiscoveryPodResourcesClient(endpoints []string, getClientFunc GetClientFunc) (*podResourcesClient,
	chan struct{},
) {
	p := &podResourcesClient{}
	closeChan := make(chan struct{})
	var (
		err          error
		conn         *grpc.ClientConn
		existingPath string
	)

	go func() {
		// Attempt to get the first existing path
		existingPath, err = general.GetOneExistPathUntilExist(endpoints, checkPodResourcesSocketInterval,
			checkPodResourcesSocketTimeoutDuration)
		if err != nil {
			klog.Errorf("failed to find an existing pod resources socket: %v", err)
			return
		}

		p.Lock()
		// Attempt to create a client and a connection
		p.client, conn, err = getClientFunc(existingPath, podResourcesClientTimeout, podResourcesClientMaxMsgSize)
		if err != nil {
			klog.Errorf("failed to create podResources client, connect error: %v", err)
			return
		}

		p.isInitialized = true
		p.Unlock()

		// Wait for close signal to close the connection
		<-closeChan
		if err = conn.Close(); err != nil {
			klog.Errorf("pod resource connection close failed: %v", err)
			return
		}
	}()

	return p, closeChan
}

// List wraps the List method of v1.PodResourcesListerClient
func (p *podResourcesClient) List(ctx context.Context, in *v1.ListPodResourcesRequest,
	opts ...grpc.CallOption,
) (*v1.ListPodResourcesResponse, error) {
	p.RLock()
	defer p.RUnlock()

	if !p.isInitialized {
		return nil, errNotInitialized
	}

	return p.client.List(ctx, in, opts...)
}

// GetAllocatableResources wraps the GetAllocatableResources method of v1.PodResourcesListerClient
func (p *podResourcesClient) GetAllocatableResources(ctx context.Context, in *v1.AllocatableResourcesRequest,
	opts ...grpc.CallOption,
) (*v1.AllocatableResourcesResponse, error) {
	p.RLock()
	defer p.RUnlock()

	if !p.isInitialized {
		return nil, errNotInitialized
	}

	return p.client.GetAllocatableResources(ctx, in, opts...)
}

// GetClientFunc is a function to get a client for the PodResourcesLister grpc service.
type GetClientFunc func(socket string, connectionTimeout time.Duration, maxMsgSize int) (v1.PodResourcesListerClient, *grpc.ClientConn, error)

// GetV1Client returns a client for the PodResourcesLister grpc service, and it is referring from upstream k8s's GetV1Client,
// which is also an implement of GetClientFunc.
func GetV1Client(socket string, connectionTimeout time.Duration, maxMsgSize int) (v1.PodResourcesListerClient, *grpc.ClientConn, error) {
	addr, dialer, err := util.GetAddressAndDialer(socket)
	if err != nil {
		return nil, nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), connectionTimeout)
	defer cancel()

	conn, err := grpc.DialContext(ctx, addr, grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(dialer), grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(maxMsgSize)))
	if err != nil {
		return nil, nil, fmt.Errorf("error dialing socket %s: %v", socket, err)
	}
	return v1.NewPodResourcesListerClient(conn), conn, nil
}
