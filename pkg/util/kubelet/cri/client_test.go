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
	"errors"
	"reflect"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	runtimeapi "k8s.io/cri-api/pkg/apis/runtime/v1"
)

type fakeClientConn struct {
	invoke func(method string, request, response interface{}) error
}

func (f *fakeClientConn) Invoke(_ context.Context, method string, request, response interface{},
	_ ...grpc.CallOption,
) error {
	return f.invoke(method, request, response)
}

func (f *fakeClientConn) NewStream(context.Context, *grpc.StreamDesc, string,
	...grpc.CallOption,
) (grpc.ClientStream, error) {
	return nil, errors.New("not implemented")
}

func TestNewRuntimeServicePrefersV1(t *testing.T) {
	t.Parallel()

	var methods []string
	conn := &fakeClientConn{invoke: func(method string, _, _ interface{}) error {
		methods = append(methods, method)
		return nil
	}}

	service, err := newRuntimeService(conn, time.Second)
	if err != nil {
		t.Fatalf("newRuntimeService returned error: %v", err)
	}
	if service.servicePrefix != criV1ServicePrefix {
		t.Fatalf("service prefix got %q, want %q", service.servicePrefix, criV1ServicePrefix)
	}
	wantMethods := []string{criV1ServicePrefix + "Version"}
	if !reflect.DeepEqual(methods, wantMethods) {
		t.Fatalf("methods got %v, want %v", methods, wantMethods)
	}
}

func TestNewRuntimeServiceFallsBackToV1Alpha2(t *testing.T) {
	t.Parallel()

	var methods []string
	conn := &fakeClientConn{invoke: func(method string, _, _ interface{}) error {
		methods = append(methods, method)
		if method == criV1ServicePrefix+"Version" {
			return status.Error(codes.Unimplemented, "unknown service runtime.v1.RuntimeService")
		}
		return nil
	}}

	service, err := newRuntimeService(conn, time.Second)
	if err != nil {
		t.Fatalf("newRuntimeService returned error: %v", err)
	}
	if service.servicePrefix != criV1Alpha2ServicePrefix {
		t.Fatalf("service prefix got %q, want %q", service.servicePrefix, criV1Alpha2ServicePrefix)
	}
	wantMethods := []string{
		criV1ServicePrefix + "Version",
		criV1Alpha2ServicePrefix + "Version",
	}
	if !reflect.DeepEqual(methods, wantMethods) {
		t.Fatalf("methods got %v, want %v", methods, wantMethods)
	}
}

func TestNewRuntimeServiceDoesNotFallbackForNonVersionErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		code codes.Code
	}{
		{
			name: "unavailable",
			err:  status.Error(codes.Unavailable, "runtime unavailable"),
			code: codes.Unavailable,
		},
		{
			name: "permission denied",
			err:  status.Error(codes.PermissionDenied, "permission denied"),
			code: codes.PermissionDenied,
		},
		{
			name: "deadline exceeded",
			err:  status.Error(codes.DeadlineExceeded, "deadline exceeded"),
			code: codes.DeadlineExceeded,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var methods []string
			conn := &fakeClientConn{invoke: func(method string, _, _ interface{}) error {
				methods = append(methods, method)
				return tt.err
			}}

			_, err := newRuntimeService(conn, time.Second)
			if status.Code(err) != tt.code {
				t.Fatalf("error code got %v, want %v: %v", status.Code(err), tt.code, err)
			}
			wantMethods := []string{criV1ServicePrefix + "Version"}
			if !reflect.DeepEqual(methods, wantMethods) {
				t.Fatalf("methods got %v, want %v", methods, wantMethods)
			}
		})
	}
}

func TestNewRuntimeServiceReturnsV1Alpha2ProbeError(t *testing.T) {
	t.Parallel()

	conn := &fakeClientConn{invoke: func(method string, _, _ interface{}) error {
		switch method {
		case criV1ServicePrefix + "Version":
			return status.Error(codes.Unimplemented, "v1 unsupported")
		case criV1Alpha2ServicePrefix + "Version":
			return status.Error(codes.Unavailable, "v1alpha2 unavailable")
		default:
			return nil
		}
	}}

	_, err := newRuntimeService(conn, time.Second)
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("error code got %v, want %v: %v", status.Code(err), codes.Unavailable, err)
	}
}

func TestRuntimeServiceOperationsUseSelectedVersion(t *testing.T) {
	t.Parallel()

	for _, prefix := range []string{criV1ServicePrefix, criV1Alpha2ServicePrefix} {
		prefix := prefix
		t.Run(prefix, func(t *testing.T) {
			t.Parallel()

			var methods []string
			conn := &fakeClientConn{invoke: func(method string, request, response interface{}) error {
				methods = append(methods, method)
				switch method {
				case prefix + "ListPodSandbox":
					if request.(*runtimeapi.ListPodSandboxRequest).Filter.Id != "sandbox-filter" {
						t.Fatalf("unexpected sandbox filter: %+v", request)
					}
					response.(*runtimeapi.ListPodSandboxResponse).Items = []*runtimeapi.PodSandbox{{Id: "sandbox-id"}}
				case prefix + "ListContainers":
					if request.(*runtimeapi.ListContainersRequest).Filter.Id != "container-filter" {
						t.Fatalf("unexpected container filter: %+v", request)
					}
					response.(*runtimeapi.ListContainersResponse).Containers = []*runtimeapi.Container{{Id: "container-id"}}
				case prefix + "ContainerStatus":
					statusRequest := request.(*runtimeapi.ContainerStatusRequest)
					if statusRequest.ContainerId != "container-id" || !statusRequest.Verbose {
						t.Fatalf("unexpected status request: %+v", statusRequest)
					}
					response.(*runtimeapi.ContainerStatusResponse).Info = map[string]string{"info": "value"}
				case prefix + "ContainerStats":
					if request.(*runtimeapi.ContainerStatsRequest).ContainerId != "container-id" {
						t.Fatalf("unexpected stats request: %+v", request)
					}
					response.(*runtimeapi.ContainerStatsResponse).Stats = &runtimeapi.ContainerStats{
						Attributes: &runtimeapi.ContainerAttributes{Id: "container-id"},
					}
				case prefix + "StopContainer":
					stopRequest := request.(*runtimeapi.StopContainerRequest)
					if stopRequest.ContainerId != "container-id" || stopRequest.Timeout != 30 {
						t.Fatalf("unexpected stop request: %+v", stopRequest)
					}
				default:
					t.Fatalf("unexpected method %q", method)
				}
				return nil
			}}
			service := &runtimeService{
				conn:           conn,
				servicePrefix:  prefix,
				requestTimeout: time.Second,
			}

			sandboxes, err := service.ListPodSandbox(context.Background(),
				&runtimeapi.PodSandboxFilter{Id: "sandbox-filter"})
			if err != nil || len(sandboxes) != 1 || sandboxes[0].Id != "sandbox-id" {
				t.Fatalf("ListPodSandbox got %+v, %v", sandboxes, err)
			}

			containers, err := service.ListContainers(context.Background(),
				&runtimeapi.ContainerFilter{Id: "container-filter"})
			if err != nil || len(containers) != 1 || containers[0].Id != "container-id" {
				t.Fatalf("ListContainers got %+v, %v", containers, err)
			}

			containerStatus, err := service.ContainerStatus(context.Background(), "container-id", true)
			if err != nil || containerStatus.Info["info"] != "value" {
				t.Fatalf("ContainerStatus got %+v, %v", containerStatus, err)
			}

			stats, err := service.ContainerStats(context.Background(), "container-id")
			if err != nil || stats.GetAttributes().GetId() != "container-id" {
				t.Fatalf("ContainerStats got %+v, %v", stats, err)
			}

			if err := service.StopContainer(context.Background(), "container-id", 30); err != nil {
				t.Fatalf("StopContainer returned error: %v", err)
			}

			wantMethods := []string{
				prefix + "ListPodSandbox",
				prefix + "ListContainers",
				prefix + "ContainerStatus",
				prefix + "ContainerStats",
				prefix + "StopContainer",
			}
			if !reflect.DeepEqual(methods, wantMethods) {
				t.Fatalf("methods got %v, want %v", methods, wantMethods)
			}
		})
	}
}

func TestRuntimeServicePreservesOperationErrors(t *testing.T) {
	t.Parallel()

	conn := &fakeClientConn{invoke: func(_ string, _, _ interface{}) error {
		return status.Error(codes.Unavailable, "runtime unavailable")
	}}
	service := &runtimeService{
		conn:           conn,
		servicePrefix:  criV1ServicePrefix,
		requestTimeout: time.Second,
	}

	_, err := service.ListContainers(context.Background(), nil)
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("error code got %v, want %v: %v", status.Code(err), codes.Unavailable, err)
	}
}
