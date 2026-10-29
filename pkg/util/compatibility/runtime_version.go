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

package compatibility

import (
	"context"
	"fmt"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	runtimeapi "k8s.io/cri-api/pkg/apis/runtime/v1"
	"k8s.io/klog/v2"
)

const (
	RuntimeVersionV1       = "v1"
	RuntimeVersionV1Alpha2 = "v1alpha2"

	RuntimeV1ServicePrefix       = "/runtime.v1.RuntimeService/"
	RuntimeV1Alpha2ServicePrefix = "/runtime.v1alpha2.RuntimeService/"

	runtimeVersionCheckpointFileName = "katalyst_cri_runtime_api_version"
	criVersion                       = "0.1.0"
)

type runtimeVersionCheckpoint struct {
	VersionCheckpoint
}

func (*runtimeVersionCheckpoint) FileName() string {
	return runtimeVersionCheckpointFileName
}

type runtimeVersionResolver struct {
	conn           grpc.ClientConnInterface
	requestTimeout time.Duration
	versionStore   VersionStore
}

func NewRuntimeVersionResolver(
	conn grpc.ClientConnInterface,
	requestTimeout time.Duration,
	versionStore VersionStore,
) VersionResolver {
	return &runtimeVersionResolver{
		conn:           conn,
		requestTimeout: requestTimeout,
		versionStore:   versionStore,
	}
}

func (r *runtimeVersionResolver) Get(ctx context.Context) (string, error) {
	if r.versionStore != nil {
		checkpoint := &runtimeVersionCheckpoint{}
		found, err := r.versionStore.Get(checkpoint)
		if err != nil {
			klog.ErrorS(err, "Read CRI runtime API compatibility checkpoint")
		} else if found {
			if isSupportedRuntimeVersion(checkpoint.Version()) {
				return checkpoint.Version(), nil
			}
			klog.ErrorS(nil, "Ignore unsupported cached CRI runtime API version", "version", checkpoint.Version())
		}
	}

	return r.probe(ctx)
}

func (r *runtimeVersionResolver) Refresh(ctx context.Context) (string, error) {
	if r.versionStore != nil {
		if err := r.versionStore.Delete(&runtimeVersionCheckpoint{}); err != nil {
			klog.ErrorS(err, "Delete stale CRI runtime API compatibility checkpoint")
		}
	}
	return r.probe(ctx)
}

func (r *runtimeVersionResolver) probe(ctx context.Context) (string, error) {
	err := r.invokeVersion(ctx, RuntimeV1ServicePrefix)
	if err == nil {
		klog.InfoS("Using CRI runtime API", "version", RuntimeVersionV1)
		r.persist(RuntimeVersionV1)
		return RuntimeVersionV1, nil
	}
	if status.Code(err) != codes.Unimplemented {
		return "", fmt.Errorf("error validating CRI v1 runtime API: %w", err)
	}

	v1Err := err
	err = r.invokeVersion(ctx, RuntimeV1Alpha2ServicePrefix)
	if err != nil {
		return "", fmt.Errorf(
			"CRI v1 is unsupported (%v), error validating CRI v1alpha2 runtime API: %w",
			v1Err, err)
	}

	klog.InfoS("Falling back to deprecated CRI runtime API", "version", RuntimeVersionV1Alpha2)
	r.persist(RuntimeVersionV1Alpha2)
	return RuntimeVersionV1Alpha2, nil
}

func (r *runtimeVersionResolver) invokeVersion(ctx context.Context, servicePrefix string) error {
	ctx, cancel := context.WithTimeout(ctx, r.requestTimeout)
	defer cancel()
	return r.conn.Invoke(
		ctx,
		servicePrefix+"Version",
		&runtimeapi.VersionRequest{Version: criVersion},
		&runtimeapi.VersionResponse{},
	)
}

func (r *runtimeVersionResolver) persist(version string) {
	if r.versionStore == nil {
		return
	}
	checkpoint := &runtimeVersionCheckpoint{VersionCheckpoint: NewVersionCheckpoint(version)}
	if err := r.versionStore.Put(checkpoint); err != nil {
		klog.ErrorS(err, "Persist CRI runtime API compatibility checkpoint")
	}
}

func isSupportedRuntimeVersion(version string) bool {
	return version == RuntimeVersionV1 || version == RuntimeVersionV1Alpha2
}

func RuntimeServicePrefix(version string) (string, error) {
	switch version {
	case RuntimeVersionV1:
		return RuntimeV1ServicePrefix, nil
	case RuntimeVersionV1Alpha2:
		return RuntimeV1Alpha2ServicePrefix, nil
	default:
		return "", fmt.Errorf("unsupported CRI runtime API version %q", version)
	}
}
