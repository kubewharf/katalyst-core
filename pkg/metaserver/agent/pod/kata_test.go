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

package pod

import (
	"errors"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
)

var (
	testKataJsonInfo       = `{"sandboxID": "12345678", "pid": 1234, "runtimeType": "io.containerd.kata.v2"}`
	testInvalidJsonInfo    = `{"pid: 2345"}` // no sandbox id field
	testMissingRuntimeInfo = `{"sandboxID": "12345678", "pid": 2345}`
	testNonKataJsonInfo    = `{"sandboxID": "234567890", "pid": "2345", "runtimeType": "docker"}`
)

type kataRuntimePodFetcherStub struct {
	*runtimePodFetcherStub
	containerIdToError map[string]error
}

func (r *kataRuntimePodFetcherStub) GetContainerInfo(containerId string) (map[string]string, error) {
	if err := r.containerIdToError[containerId]; err != nil {
		return nil, err
	}
	return r.runtimePodFetcherStub.GetContainerInfo(containerId)
}

func TestKataContainerFetcher_getKataCgroupPathSuffix(t *testing.T) {
	t.Parallel()

	type fields struct {
		podUid             string
		containerId        string
		containerIdToInfo  map[string]map[string]string
		containerIdToError map[string]error
	}

	tests := []struct {
		name                 string
		fields               fields
		wantCgroupPathSuffix string
		wantSkip             bool
		wantErr              bool
	}{
		{
			name: "Cannot find container info",
			fields: fields{
				podUid:      "12345678",
				containerId: "invalidContainerId",
				containerIdToInfo: map[string]map[string]string{
					"container1234": {
						"info": testKataJsonInfo,
					},
				},
				containerIdToError: map[string]error{
					"invalidContainerId": status.Error(codes.NotFound, "container not found"),
				},
			},
			wantCgroupPathSuffix: "",
			wantSkip:             true,
			wantErr:              false,
		},
		{
			name: "Can find container info but cannot unmarshal json",
			fields: fields{
				podUid:      "12345678",
				containerId: "container1234",
				containerIdToInfo: map[string]map[string]string{
					"container1234": {
						"invalidField": testKataJsonInfo,
					},
				},
			},
			wantCgroupPathSuffix: "",
			wantSkip:             false,
			wantErr:              true,
		},
		{
			name: "Empty sandbox id",
			fields: fields{
				podUid:      "12345678",
				containerId: "container1234",
				containerIdToInfo: map[string]map[string]string{
					"container1234": {
						"info": testInvalidJsonInfo,
					},
				},
			},
			wantCgroupPathSuffix: "",
			wantSkip:             false,
			wantErr:              true,
		},
		{
			name: "Not kata container",
			fields: fields{
				podUid:      "12345678",
				containerId: "container1234",
				containerIdToInfo: map[string]map[string]string{
					"container1234": {
						"info": testNonKataJsonInfo,
					},
				},
			},
			wantCgroupPathSuffix: "",
			wantSkip:             true,
			wantErr:              false,
		},
		{
			name: "Missing runtime type",
			fields: fields{
				podUid:      "12345678",
				containerId: "container1234",
				containerIdToInfo: map[string]map[string]string{
					"container1234": {
						"info": testMissingRuntimeInfo,
					},
				},
			},
			wantCgroupPathSuffix: "",
			wantSkip:             false,
			wantErr:              true,
		},
		{
			name: "Can get the kata cgroup path suffix",
			fields: fields{
				podUid:      "12345678",
				containerId: "container1234",
				containerIdToInfo: map[string]map[string]string{
					"container1234": {
						"info": testKataJsonInfo,
					},
				},
			},
			wantCgroupPathSuffix: "pod12345678/kata_12345678",
			wantSkip:             false,
			wantErr:              false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			kataContainerFetcher := &KataContainerFetcher{
				runtimePodFetcher: &kataRuntimePodFetcherStub{
					runtimePodFetcherStub: &runtimePodFetcherStub{
						containerIdToInfo: tt.fields.containerIdToInfo,
					},
					containerIdToError: tt.fields.containerIdToError,
				},
			}
			pathSuffix, skip, err := kataContainerFetcher.getKataCgroupPathSuffix(tt.fields.podUid, tt.fields.containerId)
			if (err != nil) != tt.wantErr {
				t.Errorf("getKataCgroupPathSuffix() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if skip != tt.wantSkip {
				t.Errorf("getKataCgroupPathSuffix() skip = %v, want %v", skip, tt.wantSkip)
			}
			if pathSuffix != tt.wantCgroupPathSuffix {
				t.Errorf("getKataCgroupPathSuffix() pathSuffix = %v, want %v", pathSuffix, tt.wantCgroupPathSuffix)
			}
		})
	}
}

func TestKataContainerFetcher_getKataCgroupPathSuffixReturnsNonNotFoundError(t *testing.T) {
	t.Parallel()

	kataContainerFetcher := &KataContainerFetcher{
		runtimePodFetcher: &kataRuntimePodFetcherStub{
			runtimePodFetcherStub: &runtimePodFetcherStub{},
			containerIdToError: map[string]error{
				"container": status.Error(codes.Internal, "runtime unavailable"),
			},
		},
	}

	pathSuffix, skip, err := kataContainerFetcher.getKataCgroupPathSuffix("pod", "container")

	assert.Empty(t, pathSuffix)
	assert.False(t, skip)
	assert.Equal(t, codes.Internal, status.Code(err))
}

func TestKataContainerFetcher_getKataCgroupPathSuffixReturnsErrorForNilRuntimePodFetcher(t *testing.T) {
	t.Parallel()

	kataContainerFetcher := &KataContainerFetcher{}

	pathSuffix, skip, err := kataContainerFetcher.getKataCgroupPathSuffix("pod", "container")

	assert.Empty(t, pathSuffix)
	assert.False(t, skip)
	assert.EqualError(t, err, "runtime pod fetcher is nil")
}

func TestKataContainerFetcher_DefaultMissFallsBackToKataWithNilRuntimePodFetcher(t *testing.T) {
	t.Parallel()

	RegisterKataContainerFetcher(nil)

	cgroupPath, err := common.GetContainerAbsCgroupPath("cpu", "missing-pod", "missing-container")

	assert.Empty(t, cgroupPath)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "runtime pod fetcher is nil")
}

func TestKataContainerFetcher_getKataContainerCgroupPathSkipsNonKataRuntime(t *testing.T) {
	t.Parallel()

	kataContainerFetcher := &KataContainerFetcher{
		runtimePodFetcher: &runtimePodFetcherStub{
			containerIdToInfo: map[string]map[string]string{
				"container1234": {
					"info": testNonKataJsonInfo,
				},
			},
		},
	}

	absPath, skip, err := kataContainerFetcher.getKataContainerAbsoluteCgroupPath("cpu", "12345", "container1234")
	assert.Empty(t, absPath)
	assert.True(t, skip)
	assert.NoError(t, err)

	relPath, skip, err := kataContainerFetcher.getKataContainerRelativeCgroupPath("12345", "container1234")
	assert.Empty(t, relPath)
	assert.True(t, skip)
	assert.NoError(t, err)
}

func TestKataContainerFetcher_getKataContainerAbsoluteCgroupPath(t *testing.T) {
	t.Parallel()

	containerIdToInfo := map[string]map[string]string{
		"container1234": {
			"info": testInvalidJsonInfo,
		},
	}

	kataContainerFetcher := &KataContainerFetcher{
		runtimePodFetcher: &runtimePodFetcherStub{
			containerIdToInfo: containerIdToInfo,
		},
	}

	absPath, skip, err := kataContainerFetcher.getKataContainerAbsoluteCgroupPath("cpu", "12345", "123456")
	assert.Equal(t, absPath, "")
	assert.False(t, skip)
	assert.NotNil(t, err)
}

func TestKataContainerFetcher_getKataContainerRelativeCgroupPath(t *testing.T) {
	t.Parallel()

	containerIdToInfo := map[string]map[string]string{
		"container1234": {
			"info": testInvalidJsonInfo,
		},
	}

	kataContainerFetcher := &KataContainerFetcher{
		runtimePodFetcher: &runtimePodFetcherStub{
			containerIdToInfo: containerIdToInfo,
		},
	}

	absPath, skip, err := kataContainerFetcher.getKataContainerRelativeCgroupPath("12345", "123456")
	assert.Equal(t, absPath, "")
	assert.False(t, skip)
	assert.NotNil(t, err)
}

// TestKataContainerFetcher_CRIErrorSingleHandlerClassification pins only the
// kata handler's single-layer behavior: getKataContainerAbsoluteCgroupPath and
// getKataContainerRelativeCgroupPath must turn a gRPC NotFound (or a non-kata
// runtime) into a skip with no operational error, while a transport-level CRI
// failure (Internal/Unavailable, or an unparseable sandbox) must surface as an
// operational error (fail-closed) instead of being mistaken for "container
// absent".
//
// This test deliberately does NOT drive the multi-handler aggregate
// (GetContainerAbs/RelativeCgroupPath): the aggregate's skip-fall-through and
// fail-closed semantics, including the interaction between the kata-like
// detector, the duma-like detector, and the cgroupfs default handler, are
// covered directly against resolveContainerAbs/RelativeCgroupPath in
// pkg/util/cgroup/common/path_registry_test.go
// (TestResolveContainerCgroupPathHandlerAggregation). The duma detector shares
// that same aggregator and its own NotFound->skip mapping is covered in the
// katalyst-adapter repository (commit 7d83297b); it needs no change here.
func TestKataContainerFetcher_CRIErrorSingleHandlerClassification(t *testing.T) {
	t.Parallel()

	const podUID = "test-pod"

	tests := []struct {
		name        string
		runtimeInfo string // when non-empty, GetContainerInfo returns this instead of an error
		fetchErr    error
		wantSkip    bool
		// wantAbsence means the aggregate outcome must classify as
		// os.ErrNotExist (skip with no operational error). When false the
		// handler must surface an operational error that is NOT os.ErrNotExist.
		wantAbsence bool
	}{
		{
			name:        "cri NotFound skips kata handler and aggregates to absence",
			fetchErr:    status.Error(codes.NotFound, "no such container"),
			wantSkip:    true,
			wantAbsence: true,
		},
		{
			name:        "cri Internal fails closed instead of treating as absent",
			fetchErr:    status.Error(codes.Internal, "runtime backend exploded"),
			wantSkip:    false,
			wantAbsence: false,
		},
		{
			name:        "cri Unavailable fails closed instead of treating as absent",
			fetchErr:    status.Error(codes.Unavailable, "runtime backend down"),
			wantSkip:    false,
			wantAbsence: false,
		},
		{
			name:        "non-kata runtime skips kata handler",
			runtimeInfo: testNonKataJsonInfo,
			wantSkip:    true,
			wantAbsence: true,
		},
		{
			name:        "missing runtime type fails closed instead of guessing",
			runtimeInfo: testMissingRuntimeInfo,
			wantSkip:    false,
			wantAbsence: false,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			info := map[string]string{}
			if tc.runtimeInfo != "" {
				info["info"] = tc.runtimeInfo
			}
			fetcher := &KataContainerFetcher{
				runtimePodFetcher: &kataRuntimePodFetcherStub{
					runtimePodFetcherStub: &runtimePodFetcherStub{
						containerIdToInfo: map[string]map[string]string{"c1": info},
					},
					containerIdToError: map[string]error{"c1": tc.fetchErr},
				},
			}

			abs, skip, err := fetcher.getKataContainerAbsoluteCgroupPath("cpu", podUID, "c1")
			_, relSkip, relErr := fetcher.getKataContainerRelativeCgroupPath(podUID, "c1")

			assert.Equal(t, tc.wantSkip, skip, "absolute skip mismatch")
			assert.Equal(t, tc.wantSkip, relSkip, "relative skip mismatch")
			if tc.wantAbsence {
				assert.Empty(t, abs)
				assert.NoError(t, err, "skip must not leak an operational error: %v", err)
				assert.NoError(t, relErr)
			} else {
				assert.Error(t, err)
				assert.Error(t, relErr)
				assert.False(t, errors.Is(err, os.ErrNotExist),
					"operational CRI error must be fail-closed, not absence: %v", err)
				assert.False(t, errors.Is(relErr, os.ErrNotExist),
					"operational CRI error must be fail-closed, not absence: %v", relErr)
			}
		})
	}
}
