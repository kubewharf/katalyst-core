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
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

const (
	numaBindingReclaimRelativeRootCgroupPathSeparator = "-"
	defaultCgroupPathHandlerName                      = "default"
)

// k8sCgroupPathList is used to record cgroup-path related configurations,
// and it will be set as SystemdRootPath (along with kubernetes levels) as default.
var (
	k8sCgroupPathLock sync.RWMutex
	k8sCgroupPathList = sets.NewString(
		CgroupFsRootPath,
		CgroupFsRootPathBestEffort,
		CgroupFsRootPathBurstable,
	)
)

var k8sCgroupPathSettingOnce = sync.Once{}

var (
	absoluteCgroupPathHandlerLock sync.Mutex
	// Ensure that we always go through the default handler first to get cgroup path
	absoluteCgroupPathHandlerList = []AbsoluteCgroupPathHandler{
		{
			Name:    defaultCgroupPathHandlerName,
			Handler: getContainerDefaultAbsCgroupPath,
		},
	}
	relativeCgroupPathHandlerLock sync.Mutex
	relativeCgroupPathHandlerList = []RelativeCgroupPathHandler{
		{
			Name:    defaultCgroupPathHandlerName,
			Handler: getContainerDefaultRelativeAbsCgroupPath,
		},
	}
)

func RegisterAbsoluteCgroupPathHandler(handler AbsoluteCgroupPathHandler) {
	absoluteCgroupPathHandlerLock.Lock()
	defer absoluteCgroupPathHandlerLock.Unlock()
	absoluteCgroupPathHandlerList = append(absoluteCgroupPathHandlerList, handler)
}

func RegisterRelativeCgroupPathHandler(handler RelativeCgroupPathHandler) {
	relativeCgroupPathHandlerLock.Lock()
	defer relativeCgroupPathHandlerLock.Unlock()
	relativeCgroupPathHandlerList = append(relativeCgroupPathHandlerList, handler)
}

// InitKubernetesCGroupPath can only be called once to init dynamic cgroup path configurations.
// additionalCGroupPath is set because we may have legacy cgroup path settings,
// so it will be used as an adaptive logic.
func InitKubernetesCGroupPath(cgroupType CgroupType, additionalK8SCGroupPath []string) {
	k8sCgroupPathSettingOnce.Do(func() {
		if cgroupType == CgroupTypeSystemd {
			k8sCgroupPathLock.Lock()
			defer k8sCgroupPathLock.Unlock()
			k8sCgroupPathList = sets.NewString(
				SystemdRootPath,
				SystemdRootPathBestEffort,
				SystemdRootPathBurstable,
			)
		}

		k8sCgroupPathList.Insert(additionalK8SCGroupPath...)
	})
}

// GetCgroupRootPath get cgroupfs root path compatible with v1 and v2
func GetCgroupRootPath(subsys string) string {
	if CheckCgroup2UnifiedMode() {
		return CgroupFSMountPoint
	}

	return filepath.Join(CgroupFSMountPoint, subsys)
}

// GetAbsCgroupPath get absolute cgroup path for relative cgroup path
func GetAbsCgroupPath(subsys, suffix string) string {
	return filepath.Join(GetCgroupRootPath(subsys), suffix)
}

// GetKubernetesCgroupRootPathWithSubSys returns all Cgroup paths to run container for
// kubernetes, and the returned values are merged with subsys.
// note: this function is not thread-safe, and it should be called after InitKubernetesCGroupPath.
func GetKubernetesCgroupRootPathWithSubSys(subsys string) []string {
	k8sCgroupPathLock.RLock()
	defer k8sCgroupPathLock.RUnlock()

	var subsysCgroupPathList []string
	for _, p := range k8sCgroupPathList.List() {
		subsysCgroupPathList = append(subsysCgroupPathList,
			GetKubernetesAbsCgroupPath(subsys, p))
	}
	return subsysCgroupPathList
}

// GetKubernetesAbsCgroupPath returns absolute cgroup path for kubernetes with the given
// suffix without considering whether the path exists or not.
func GetKubernetesAbsCgroupPath(subsys, suffix string) string {
	if subsys == "" {
		subsys = DefaultSelectedSubsys
	}

	return GetAbsCgroupPath(subsys, suffix)
}

// GetKubernetesAnyExistAbsCgroupPath returns any absolute cgroup path that exists for kubernetes
func GetKubernetesAnyExistAbsCgroupPath(subsys, suffix string) (string, error) {
	var errs []error

	k8sCgroupPathLock.RLock()
	defer k8sCgroupPathLock.RUnlock()

	for _, cgPath := range k8sCgroupPathList.List() {
		p := GetKubernetesAbsCgroupPath(subsys, path.Join(cgPath, suffix))
		if general.IsPathExists(p) {
			return p, nil
		}
	}

	return "", fmt.Errorf("failed to find absolute path of suffix: %s, error: %v", suffix, utilerrors.NewAggregate(errs))
}

// GetKubernetesAnyExistRelativeCgroupPath returns any relative cgroup path that exists for kubernetes
func GetKubernetesAnyExistRelativeCgroupPath(suffix string) (string, error) {
	var errs []error

	k8sCgroupPathLock.RLock()
	defer k8sCgroupPathLock.RUnlock()

	for _, cgPath := range k8sCgroupPathList.List() {
		relativePath := path.Join(cgPath, suffix)
		for _, defaultSelectedSubsys := range defaultSelectedSubsysList {
			p := GetKubernetesAbsCgroupPath(defaultSelectedSubsys, relativePath)
			if general.IsPathExists(p) {
				return relativePath, nil
			}
		}
	}

	return "", fmt.Errorf("failed to find relative path of suffix: %s, error: %v", suffix, utilerrors.NewAggregate(errs))
}

// GetPodAbsCgroupPath returns absolute cgroup path for pod level
func GetPodAbsCgroupPath(subsys, podUID string) (string, error) {
	return GetKubernetesAnyExistAbsCgroupPath(subsys, fmt.Sprintf("%s%s", PodCgroupPathPrefix, podUID))
}

// GetPodRelativeCgroupPath returns relative cgroup path for pod level
func GetPodRelativeCgroupPath(podUID string) (string, error) {
	return GetKubernetesAnyExistRelativeCgroupPath(fmt.Sprintf("%s%s", PodCgroupPathPrefix, podUID))
}

func getContainerDefaultAbsCgroupPath(subsys, podUID, containerId string) (string, error) {
	return GetKubernetesAnyExistAbsCgroupPath(subsys, path.Join(fmt.Sprintf("%s%s", PodCgroupPathPrefix, podUID), containerId))
}

func getContainerDefaultRelativeAbsCgroupPath(podUID, containerId string) (string, error) {
	return GetKubernetesAnyExistRelativeCgroupPath(path.Join(fmt.Sprintf("%s%s", PodCgroupPathPrefix, podUID), containerId))
}

// GetContainerAbsCgroupPath returns absolute cgroup path for container level
// It uses all the handlers in absoluteCgroupPathHandlerMap and returns the first non-empty path.
func GetContainerAbsCgroupPath(subsys, podUID, containerId string) (string, error) {
	var errors []error
	for _, handler := range absoluteCgroupPathHandlerList {
		if handler.Handler == nil {
			errors = append(errors, fmt.Errorf("absolute cgroup path Handler for %s is nil", handler.Name))
			continue
		}
		cgroupPath, err := handler.Handler(subsys, podUID, containerId)
		if err == nil {
			return cgroupPath, nil
		}
		errors = append(errors, fmt.Errorf("get absolute cgroup path by Handler %s failed, err: %v", handler.Name, err))
	}
	return "", utilerrors.NewAggregate(errors)
}

// GetContainerRelativeCgroupPath returns relative cgroup path for container level
// It uses all the handlers in relativeCgroupPathHandlerMap and returns the first non-empty path.
func GetContainerRelativeCgroupPath(podUID, containerId string) (string, error) {
	var errors []error
	for _, handler := range relativeCgroupPathHandlerList {
		if handler.Handler == nil {
			errors = append(errors, fmt.Errorf("relative cgroup path Handler for %s is nil", handler.Name))
			continue
		}
		cgroupPath, err := handler.Handler(podUID, containerId)
		if err == nil {
			return cgroupPath, nil
		}
		errors = append(errors, fmt.Errorf("get relative cgroup path by Handler %s failed, err: %v", handler.Name, err))
	}
	return "", utilerrors.NewAggregate(errors)
}

func IsContainerCgroupExist(podUID, containerID string) (bool, error) {
	containerAbsCGPath, err := GetContainerAbsCgroupPath("", podUID, containerID)
	if err != nil {
		return false, fmt.Errorf("GetContainerAbsCgroupPath failed, err: %v", err)
	}

	return general.IsPathExists(containerAbsCGPath), nil
}

func IsContainerCgroupFileExist(subsys, podUID, containerId, cgroupFileName string) (bool, error) {
	absCgroupPath, err := GetContainerAbsCgroupPath(subsys, podUID, containerId)
	if err != nil {
		return false, fmt.Errorf("GetContainerAbsCgroupPath failed with error: %v", err)
	}

	absCgroupFilePath := filepath.Join(absCgroupPath, cgroupFileName)
	_, err = os.Stat(absCgroupFilePath)
	if err == nil {
		return true, nil
	} else if os.IsNotExist(err) {
		return false, nil
	} else {
		return false, err
	}
}

// GetNUMABindingReclaimRelativeRootCgroupPaths returns relative cgroup paths for numa-binding reclaim
func GetNUMABindingReclaimRelativeRootCgroupPaths(reclaimRelativeRootCgroupPath string, NUMANode []int) map[int]string {
	paths := make(map[int]string, len(NUMANode))
	for _, numaID := range NUMANode {
		paths[numaID] = reclaimRelativeRootCgroupPath + numaBindingReclaimRelativeRootCgroupPathSeparator + strconv.Itoa(numaID)
	}
	return paths
}

func GetReclaimRelativeRootCgroupPath(reclaimRelativeRootCgroupPath string, NUMANode int) string {
	if NUMANode < 0 {
		return reclaimRelativeRootCgroupPath
	}
	return strings.Join([]string{reclaimRelativeRootCgroupPath, strconv.Itoa(NUMANode)}, numaBindingReclaimRelativeRootCgroupPathSeparator)
}
