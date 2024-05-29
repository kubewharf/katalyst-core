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
	"path"
	"path/filepath"
	"sync"

	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/pkg/util/general"
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

// GetContainerAbsCgroupPath returns absolute cgroup path for container level
func GetContainerAbsCgroupPath(subsys, podUID, containerId string) (string, error) {
	return GetKubernetesAnyExistAbsCgroupPath(subsys, path.Join(fmt.Sprintf("%s%s", PodCgroupPathPrefix, podUID), containerId))
}

// GetContainerRelativeCgroupPath returns relative cgroup path for container level
func GetContainerRelativeCgroupPath(podUID, containerId string) (string, error) {
	return GetKubernetesAnyExistRelativeCgroupPath(path.Join(fmt.Sprintf("%s%s", PodCgroupPathPrefix, podUID), containerId))
}

func IsContainerCgroupExist(podUID, containerID string) (bool, error) {
	containerAbsCGPath, err := GetContainerAbsCgroupPath("", podUID, containerID)
	if err != nil {
		return false, fmt.Errorf("GetContainerAbsCgroupPath failed, err: %v", err)
	}

	return general.IsPathExists(containerAbsCGPath), nil
}
