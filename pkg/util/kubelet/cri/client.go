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

package cri

import (
	"context"
	"fmt"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	runtimeapi "k8s.io/cri-api/pkg/apis/runtime/v1"
	criutil "k8s.io/cri-client/pkg/util"
	"k8s.io/klog/v2"
)

const (
	criV1ServicePrefix       = "/runtime.v1.RuntimeService/"
	criV1Alpha2ServicePrefix = "/runtime.v1alpha2.RuntimeService/"
	criVersion               = "0.1.0"
	maxMsgSize               = 16 * 1024 * 1024
)

// RuntimeService is the minimal container runtime API used by Katalyst.
type RuntimeService interface {
	ListPodSandbox(context.Context, *runtimeapi.PodSandboxFilter) ([]*runtimeapi.PodSandbox, error)
	ListContainers(context.Context, *runtimeapi.ContainerFilter) ([]*runtimeapi.Container, error)
	ContainerStatus(context.Context, string, bool) (*runtimeapi.ContainerStatusResponse, error)
	ContainerStats(context.Context, string) (*runtimeapi.ContainerStats, error)
	StopContainer(context.Context, string, int64) error
}

// runtimeService dispatches requests to the negotiated CRI service version.
type runtimeService struct {
	conn           grpc.ClientConnInterface
	servicePrefix  string
	requestTimeout time.Duration
}

// NewRuntimeService connects to the endpoint and selects CRI v1 or v1alpha2.
// CRI v1 is always preferred.
func NewRuntimeService(endpoint string, connectionTimeout time.Duration) (RuntimeService, error) {
	addr, dialer, err := criutil.GetAddressAndDialer(endpoint)
	if err != nil {
		return nil, fmt.Errorf("get runtime endpoint dialer: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), connectionTimeout)
	defer cancel()

	conn, err := grpc.DialContext(
		ctx,
		addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithAuthority("localhost"),
		grpc.WithContextDialer(dialer),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(maxMsgSize)),
		grpc.WithBlock(),
	)
	if err != nil {
		return nil, fmt.Errorf("dial runtime endpoint %q: %w", endpoint, err)
	}

	service, err := newRuntimeService(conn, connectionTimeout)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	return service, nil
}

func newRuntimeService(conn grpc.ClientConnInterface, requestTimeout time.Duration) (*runtimeService, error) {
	service := &runtimeService{
		conn:           conn,
		servicePrefix:  criV1ServicePrefix,
		requestTimeout: requestTimeout,
	}

	err := service.invoke(
		context.Background(),
		"Version",
		&runtimeapi.VersionRequest{Version: criVersion},
		&runtimeapi.VersionResponse{},
	)
	if err == nil {
		klog.InfoS("Using CRI runtime API", "version", "v1")
		return service, nil
	}
	// Other errors are unexpected
	if status.Code(err) != codes.Unimplemented {
		return nil, fmt.Errorf("error validating CRI v1 runtime API: %w", err)
	}

	v1Err := err
	// Fallback to CRI v1alpha2
	// These CRI calls retain compatible protobuf fields across v1 and v1alpha2.
	service.servicePrefix = criV1Alpha2ServicePrefix
	err = service.invoke(
		context.Background(),
		"Version",
		&runtimeapi.VersionRequest{Version: criVersion},
		&runtimeapi.VersionResponse{},
	)
	if err != nil {
		return nil, fmt.Errorf("CRI v1 is unsupported (%v), error validating CRI v1alpha2 runtime API: %w", v1Err, err)
	}

	klog.InfoS("Falling back to deprecated CRI runtime API", "version", "v1alpha2")
	return service, nil
}

// invoke applies the request timeout and selected CRI service prefix.
func (r *runtimeService) invoke(ctx context.Context, method string, request, response interface{}) error {
	ctx, cancel := context.WithTimeout(ctx, r.requestTimeout)
	defer cancel()
	return r.conn.Invoke(ctx, r.servicePrefix+method, request, response)
}

func (r *runtimeService) ListPodSandbox(ctx context.Context,
	filter *runtimeapi.PodSandboxFilter,
) ([]*runtimeapi.PodSandbox, error) {
	response := &runtimeapi.ListPodSandboxResponse{}
	err := r.invoke(ctx, "ListPodSandbox", &runtimeapi.ListPodSandboxRequest{Filter: filter}, response)
	if err != nil {
		return nil, err
	}
	return response.Items, nil
}

func (r *runtimeService) ListContainers(ctx context.Context,
	filter *runtimeapi.ContainerFilter,
) ([]*runtimeapi.Container, error) {
	response := &runtimeapi.ListContainersResponse{}
	err := r.invoke(ctx, "ListContainers", &runtimeapi.ListContainersRequest{Filter: filter}, response)
	if err != nil {
		return nil, err
	}
	return response.Containers, nil
}

func (r *runtimeService) ContainerStatus(ctx context.Context,
	containerID string,
	verbose bool,
) (*runtimeapi.ContainerStatusResponse, error) {
	response := &runtimeapi.ContainerStatusResponse{}
	err := r.invoke(ctx, "ContainerStatus", &runtimeapi.ContainerStatusRequest{
		ContainerId: containerID,
		Verbose:     verbose,
	}, response)
	if err != nil {
		return nil, err
	}
	return response, nil
}

func (r *runtimeService) ContainerStats(ctx context.Context,
	containerID string,
) (*runtimeapi.ContainerStats, error) {
	response := &runtimeapi.ContainerStatsResponse{}
	err := r.invoke(ctx, "ContainerStats", &runtimeapi.ContainerStatsRequest{
		ContainerId: containerID,
	}, response)
	if err != nil {
		return nil, err
	}
	return response.Stats, nil
}

func (r *runtimeService) StopContainer(ctx context.Context,
	containerID string,
	timeout int64,
) error {
	return r.invoke(ctx, "StopContainer", &runtimeapi.StopContainerRequest{
		ContainerId: containerID,
		Timeout:     timeout,
	}, &runtimeapi.StopContainerResponse{})
}
