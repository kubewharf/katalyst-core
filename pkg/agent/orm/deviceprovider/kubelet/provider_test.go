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
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"google.golang.org/grpc"
	v1 "k8s.io/kubelet/pkg/apis/podresources/v1"
)

func TestGetDevice(t *testing.T) {
	t.Parallel()

	mockProvider := &Provider{
		client: &mockPodResourcesListerClient{
			ListPodResourcesResponse: &v1.ListPodResourcesResponse{
				PodResources: []*v1.PodResources{},
			},
		},
	}

	resp := mockProvider.GetDevices()
	assert.NotNil(t, resp)

	nilPodResourcesProvider := &Provider{
		client: &mockPodResourcesListerClient{
			ListPodResourcesResponse: &v1.ListPodResourcesResponse{},
		},
	}
	resp = nilPodResourcesProvider.GetDevices()
	assert.Nil(t, resp)

	nilRespProvider := &Provider{
		client: &mockPodResourcesListerClient{
			ListPodResourcesResponse: nil,
		},
	}
	resp = nilRespProvider.GetDevices()
	assert.Nil(t, resp)

	errPodResourceProvider := &Provider{
		client: &errPodResourceListerClient{},
	}
	resp = errPodResourceProvider.GetDevices()
	assert.Nil(t, resp)
}

func TestGetAllocatableDevices(t *testing.T) {
	t.Parallel()

	mockProvider := &Provider{
		client: &mockPodResourcesListerClient{
			AllocatableResourcesResponse: &v1.AllocatableResourcesResponse{
				Devices: []*v1.ContainerDevices{},
			},
		},
	}
	resp := mockProvider.GetAllocatableDevices()
	assert.NotNil(t, resp)

	nilRespProvider := &Provider{
		client: &mockPodResourcesListerClient{
			AllocatableResourcesResponse: nil,
		},
	}
	resp = nilRespProvider.GetAllocatableDevices()
	assert.Nil(t, resp)

	nilDeviceProvider := &Provider{
		client: &mockPodResourcesListerClient{
			AllocatableResourcesResponse: &v1.AllocatableResourcesResponse{},
		},
	}
	resp = nilDeviceProvider.GetAllocatableDevices()
	assert.Nil(t, resp)

	errProvider := &Provider{
		client: &errPodResourceListerClient{},
	}
	resp = errProvider.GetAllocatableDevices()
	assert.Nil(t, resp)
}

func TestNewProvider(t *testing.T) {
	t.Parallel()

	p, err := NewProvider([]string{}, getMockClientFunc)
	assert.NoError(t, err)
	assert.NotNil(t, p)
}

type mockPodResourcesListerClient struct {
	*v1.ListPodResourcesResponse
	*v1.AllocatableResourcesResponse
}

func (f *mockPodResourcesListerClient) List(ctx context.Context, in *v1.ListPodResourcesRequest, opts ...grpc.CallOption) (*v1.ListPodResourcesResponse, error) {
	return f.ListPodResourcesResponse, nil
}

func (f *mockPodResourcesListerClient) GetAllocatableResources(ctx context.Context, in *v1.AllocatableResourcesRequest, opts ...grpc.CallOption) (*v1.AllocatableResourcesResponse, error) {
	return f.AllocatableResourcesResponse, nil
}

type errPodResourceListerClient struct{}

func (e *errPodResourceListerClient) List(ctx context.Context, in *v1.ListPodResourcesRequest, opts ...grpc.CallOption) (*v1.ListPodResourcesResponse, error) {
	return nil, fmt.Errorf("err")
}

func (e *errPodResourceListerClient) GetAllocatableResources(ctx context.Context, in *v1.AllocatableResourcesRequest, opts ...grpc.CallOption) (*v1.AllocatableResourcesResponse, error) {
	return nil, fmt.Errorf("err")
}

func getMockClientFunc(socket string, connectionTimeout time.Duration, maxMsgSize int) (v1.PodResourcesListerClient, *grpc.ClientConn, error) {
	return &mockPodResourcesListerClient{}, nil, nil
}
