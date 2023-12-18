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
	"path"
	"testing"

	"github.com/stretchr/testify/require"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"
)

var (
	eSocketName = "mock.sock"
)

func TestNewEndpoint(t *testing.T) {
	t.Parallel()

	socket := path.Join("/tmp", "TestNewEndpoint"+eSocketName)

	p, e := eSetup(t, socket, "mock")
	defer eCleanup(t, p, e)
}

func TestAllocate(t *testing.T) {
	t.Parallel()

	socket := path.Join("/tmp", "TestAllocate"+eSocketName)
	p, e := eSetup(t, socket, "mock")
	defer eCleanup(t, p, e)

	req := generateResourceRequest()
	resp := generateResourceResponse()

	p.SetAllocFunc(func(r *pluginapi.ResourceRequest) (*pluginapi.ResourceAllocationResponse, error) {
		return resp, nil
	})

	respOut, err := e.Allocate(context.TODO(), req)
	require.NoError(t, err)
	require.Equal(t, resp, respOut)
}

func TestNewStoppedEndpointImpl(t *testing.T) {
	t.Parallel()

	ei := NewStoppedEndpointImpl("cpu")
	require.NotNil(t, ei)
}

func TestRemovePod(t *testing.T) {
	t.Parallel()

	socket := path.Join("/tmp", "TestRemovePod"+eSocketName)
	p, e := eSetup(t, socket, "mock")
	defer eCleanup(t, p, e)

	req := generateRemovePodRequest()
	resp, err := e.RemovePod(context.TODO(), req)
	require.NoError(t, err)
	require.NotNil(t, resp)
}

func TestGetResourceAllocation(t *testing.T) {
	t.Parallel()

	socket := path.Join("/tmp", "TestGetResourceAllocation"+eSocketName)
	p, e := eSetup(t, socket, "mock")
	defer eCleanup(t, p, e)

	resp := generateGetResourceAllocationInfoResponse()
	p.SetGetAllocFunc(func(r *pluginapi.GetResourcesAllocationRequest) (*pluginapi.GetResourcesAllocationResponse, error) {
		return resp, nil
	})

	actual, err := e.GetResourceAllocation(context.TODO(), &pluginapi.GetResourcesAllocationRequest{})
	require.NoError(t, err)
	require.Equal(t, actual, resp)
}

func TestClient(t *testing.T) {
	t.Parallel()

	socket := path.Join("/tmp", "TestClient"+eSocketName)
	p, e := eSetup(t, socket, "mock")
	defer eCleanup(t, p, e)

	client := e.Client()
	require.NotNil(t, client)
}

func generateResourceRequest() *pluginapi.ResourceRequest {
	return &pluginapi.ResourceRequest{
		PodUid:        "mock_pod",
		PodNamespace:  "mock_pod_ns",
		PodName:       "mock_pod_name",
		ContainerName: "mock_con_name",
		//IsInitContainer: false,
		PodRole:      "mock_role",
		PodType:      "mock_type",
		ResourceName: "mock_res",
		Hint: &pluginapi.TopologyHint{
			Nodes:     []uint64{0, 1},
			Preferred: true,
		},
		ResourceRequests: map[string]float64{
			"mock_res": 2,
		},
	}
}

func generateResourceResponse() *pluginapi.ResourceAllocationResponse {
	return &pluginapi.ResourceAllocationResponse{
		PodUid:        "mock_pod",
		PodNamespace:  "mock_pod_ns",
		PodName:       "mock_pod_name",
		ContainerName: "mock_con_name",
		//IsInitContainer: false,
		PodRole:      "mock_role",
		PodType:      "mock_type",
		ResourceName: "mock_res",
		AllocationResult: &pluginapi.ResourceAllocation{
			ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{
				"mock_res": generateResourceAllocationInfo(),
			},
		},
	}
}

func generateResourceAllocationInfo() *pluginapi.ResourceAllocationInfo {
	return &pluginapi.ResourceAllocationInfo{
		OciPropertyName:   "CpusetCpus",
		IsNodeResource:    true,
		IsScalarResource:  true,
		AllocatedQuantity: 3,
		AllocationResult:  "5-6,10",
		Envs:              map[string]string{"mock_key": "mock_env"},
		Annotations:       map[string]string{"mock_key": "mock_ano"},
		ResourceHints:     &pluginapi.ListOfTopologyHints{},
	}
}

func generateRemovePodRequest() *pluginapi.RemovePodRequest {
	return &pluginapi.RemovePodRequest{
		PodUid: "mock_pod",
	}
}

func generateGetResourceAllocationInfoResponse() *pluginapi.GetResourcesAllocationResponse {
	return &pluginapi.GetResourcesAllocationResponse{
		PodResources: map[string]*pluginapi.ContainerResources{
			"mock_pod": {
				ContainerResources: map[string]*pluginapi.ResourceAllocation{
					"mock_container": {
						ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{
							"mock_res": generateResourceAllocationInfo(),
						},
					},
				},
			},
		},
	}
}

func eSetup(t *testing.T, socket, resourceName string) (*Stub, *EndpointImpl) {
	p := NewResourcePluginStub(socket, resourceName, false)

	err := p.Start()
	require.NoError(t, err)

	e, err := NewEndpointImpl(socket, resourceName)
	require.NoError(t, err)

	return p, e
}

func eCleanup(t *testing.T, p *Stub, e *EndpointImpl) {
	p.Stop()
	e.Stop()
}
