//go:build linux
// +build linux

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

package common

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAbsCgroupPathWithSuffix(t *testing.T) {
	t.Parallel()

	as := require.New(t)
	path := GetAbsCgroupPath("cpuset", "abc")

	if CheckCgroup2UnifiedMode() {
		as.Equal("/sys/fs/cgroup/abc", path)
	} else {
		as.Equal("/sys/fs/cgroup/cpuset/abc", path)
	}
}

func TestGetAbsCgroupPath(t *testing.T) {
	t.Parallel()

	as := require.New(t)
	_, err := GetKubernetesAnyExistAbsCgroupPath("cpuset", "")
	as.NotNil(err)
}

func TestGetPodAbsCgroupPath(t *testing.T) {
	t.Parallel()

	as := require.New(t)
	_, err := GetPodAbsCgroupPath("cpuset", "")
	as.NotNil(err)
}

func TestGetContainerAbsCgroupPath(t *testing.T) {
	t.Parallel()

	as := require.New(t)
	_, err := GetContainerAbsCgroupPath("cpuset", "", "")
	as.NotNil(err)
}

func TestIsContainerCgroupExist(t *testing.T) {
	t.Parallel()

	as := require.New(t)
	_, err := IsContainerCgroupExist("fake-pod-uid", "fake-container-id")
	as.NotNil(err)
}

func TestIsContainerCgroupFileExist(t *testing.T) {
	t.Parallel()

	as := require.New(t)
	// test the case that file doesn't exist
	_, err := IsContainerCgroupFileExist("cpuset", "fake-pod-uid", "fake-container-id", "nonexistentfile")
	as.NotNil(err)
}

func TestGetKataContainerAbsCgroupPath(t *testing.T) {
	t.Parallel()
	// store original handlers and restore it after test
	originalHandlers := absoluteCgroupPathHandlerMap
	defer func() {
		absoluteCgroupPathHandlerMap = originalHandlers
	}()

	tests := []struct {
		name         string
		handlers     map[string]AbsoluteCgroupPathHandler
		expectedPath string
		expectedErr  bool
	}{
		{
			name: "handler returns path",
			handlers: map[string]AbsoluteCgroupPathHandler{
				"test_handler_1": func(subsys, podUID, containerId string) (string, error) {
					return "/test/path", nil
				},
			},
			expectedPath: "/test/path",
			expectedErr:  false,
		},
		{
			name: "handler returns error",
			handlers: map[string]AbsoluteCgroupPathHandler{
				"test_handler_2": func(subsys, podUID, containerId string) (string, error) {
					return "", fmt.Errorf("test error")
				},
			},
			expectedPath: "",
			expectedErr:  true,
		},
		{
			name: "nil handler returns error",
			handlers: map[string]AbsoluteCgroupPathHandler{
				"test_handler_1": nil,
			},
			expectedPath: "",
			expectedErr:  true,
		},
		{
			name: "multiple handlers return first path",
			handlers: map[string]AbsoluteCgroupPathHandler{
				"error_handler": func(subsys, podUID, containerId string) (string, error) {
					return "", fmt.Errorf("test error")
				},
				"success_handler": func(subsys, podUID, containerId string) (string, error) {
					return "/test/path", nil
				},
			},
			expectedPath: "/test/path",
			expectedErr:  false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			absoluteCgroupPathHandlerMap = tt.handlers
			path, err := GetContainerAbsCgroupPath("cpuset", "pod-uid", "container-id")
			if (err != nil) != tt.expectedErr {
				t.Errorf("GetContainerAbsCgroupPath() error = %v, wantErr %v", err, tt.expectedErr)
				return
			}
			if path != tt.expectedPath {
				t.Errorf("getKataCgroupPathSuffix() path = %v, want %v", path, tt.expectedPath)
			}
		})
	}
}

func TestGetKataContainerRelativeCgroupPath(t *testing.T) {
	t.Parallel()
	// store original handlers and restore it after test
	originalHandlers := relativeCgroupPathHandlerMap
	defer func() {
		relativeCgroupPathHandlerMap = originalHandlers
	}()
	tests := []struct {
		name         string
		handlers     map[string]RelativeCgroupPathHandler
		expectedPath string
		expectedErr  bool
	}{
		{
			name: "handler returns path",
			handlers: map[string]RelativeCgroupPathHandler{
				"test_handler_1": func(podUID, containerId string) (string, error) {
					return "/test/path", nil
				},
			},
			expectedPath: "/test/path",
			expectedErr:  false,
		},
		{
			name: "handler returns error",
			handlers: map[string]RelativeCgroupPathHandler{
				"test_handler_2": func(podUID, containerId string) (string, error) {
					return "", fmt.Errorf("test error")
				},
			},
			expectedPath: "",
			expectedErr:  true,
		},
		{
			name: "nil handler returns error",
			handlers: map[string]RelativeCgroupPathHandler{
				"test_handler_1": nil,
			},
			expectedPath: "",
			expectedErr:  true,
		},
		{
			name: "multiple handlers return first path",
			handlers: map[string]RelativeCgroupPathHandler{
				"error_handler": func(podUID, containerId string) (string, error) {
					return "", fmt.Errorf("test error")
				},
				"success_handler": func(podUID, containerId string) (string, error) {
					return "/test/path", nil
				},
			},
			expectedPath: "/test/path",
			expectedErr:  false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			relativeCgroupPathHandlerMap = tt.handlers
			path, err := GetContainerRelativeCgroupPath("pod-uid", "container-id")
			if (err != nil) != tt.expectedErr {
				t.Errorf("GetContainerRelativeCgroupPath() error = %v, wantErr %v", err, tt.expectedErr)
				return
			}
			if path != tt.expectedPath {
				t.Errorf("getKataCgroupPathSuffix() path = %v, want %v", path, tt.expectedPath)
			}
		})
	}
}
