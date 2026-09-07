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

func TestGetPodRelativeCgroupPath(t *testing.T) {
	t.Parallel()

	as := require.New(t)
	_, err := GetPodRelativeCgroupPath("")
	as.NotNil(err)
}

func TestGetContainerAbsCgroupPath(t *testing.T) {
	t.Parallel()

	as := require.New(t)
	_, err := GetContainerAbsCgroupPath("cpuset", "", "")
	as.NotNil(err)
}

func TestGetContainerAbsCgroupPathSkipsHandlers(t *testing.T) {
	t.Parallel()

	as := require.New(t)

	handlers := []AbsoluteCgroupPathHandler{
		{
			Name: "skip",
			Handler: func(subsys, podUID, containerId string) (string, bool, error) {
				return "", true, nil
			},
		},
		{
			Name: "success",
			Handler: func(subsys, podUID, containerId string) (string, bool, error) {
				return "/sys/fs/cgroup/cpu/pod/container", false, nil
			},
		},
	}

	cgroupPath, err := resolveContainerAbsCgroupPath(handlers, "cpu", "pod", "container")
	as.NoError(err)
	as.Equal("/sys/fs/cgroup/cpu/pod/container", cgroupPath)
}

func TestGetContainerAbsCgroupPathReturnsErrorIfAllHandlersSkip(t *testing.T) {
	t.Parallel()

	as := require.New(t)

	handlers := []AbsoluteCgroupPathHandler{
		{
			Name: "skip",
			Handler: func(subsys, podUID, containerId string) (string, bool, error) {
				return "", true, nil
			},
		},
	}

	_, err := resolveContainerAbsCgroupPath(handlers, "cpu", "pod", "container")
	as.Error(err)
	as.Contains(err.Error(), "all absolute cgroup path handlers skipped")
}

func TestGetContainerRelativeCgroupPathSkipsHandlers(t *testing.T) {
	t.Parallel()

	as := require.New(t)

	handlers := []RelativeCgroupPathHandler{
		{
			Name: "skip",
			Handler: func(podUID, containerId string) (string, bool, error) {
				return "", true, nil
			},
		},
		{
			Name: "success",
			Handler: func(podUID, containerId string) (string, bool, error) {
				return "/kubepods/pod/container", false, nil
			},
		},
	}

	cgroupPath, err := resolveContainerRelativeCgroupPath(handlers, "pod", "container")
	as.NoError(err)
	as.Equal("/kubepods/pod/container", cgroupPath)
}

func TestGetContainerRelativeCgroupPathReturnsErrorIfAllHandlersSkip(t *testing.T) {
	t.Parallel()

	as := require.New(t)

	handlers := []RelativeCgroupPathHandler{
		{
			Name: "skip",
			Handler: func(podUID, containerId string) (string, bool, error) {
				return "", true, nil
			},
		},
	}

	_, err := resolveContainerRelativeCgroupPath(handlers, "pod", "container")
	as.Error(err)
	as.Contains(err.Error(), "all relative cgroup path handlers skipped")
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
