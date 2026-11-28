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
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	v1 "k8s.io/api/core/v1"
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
	absoluteCgroupPathHandlerLock sync.RWMutex
	// Ensure that we always go through the default handler first to get cgroup path
	absoluteCgroupPathHandlerList = []AbsoluteCgroupPathHandler{
		{
			Name:    defaultCgroupPathHandlerName,
			Handler: getContainerDefaultAbsCgroupPath,
		},
	}
	relativeCgroupPathHandlerLock   sync.RWMutex
	nextRelativeCgroupPathHandlerID uint64
	relativeCgroupPathHandlerList   = []registeredRelativeCgroupPathHandler{
		{
			handler: RelativeCgroupPathHandler{
				Name:    defaultCgroupPathHandlerName,
				Handler: getContainerDefaultRelativeAbsCgroupPath,
			},
		},
	}
)

type registeredRelativeCgroupPathHandler struct {
	id      uint64
	handler RelativeCgroupPathHandler
}

func RegisterAbsoluteCgroupPathHandler(handler AbsoluteCgroupPathHandler) {
	absoluteCgroupPathHandlerLock.Lock()
	defer absoluteCgroupPathHandlerLock.Unlock()
	absoluteCgroupPathHandlerList = append(absoluteCgroupPathHandlerList, handler)
}

// RegisterRelativeCgroupPathHandler appends a handler for process lifetime.
func RegisterRelativeCgroupPathHandler(handler RelativeCgroupPathHandler) {
	_ = registerRelativeCgroupPathHandler(handler)
}

// RegisterRelativeCgroupPathHandlerWithUnregister appends a handler and returns
// an idempotent function that removes only that registration.
func RegisterRelativeCgroupPathHandlerWithUnregister(handler RelativeCgroupPathHandler) func() {
	return registerRelativeCgroupPathHandler(handler)
}

func registerRelativeCgroupPathHandler(handler RelativeCgroupPathHandler) func() {
	relativeCgroupPathHandlerLock.Lock()
	nextRelativeCgroupPathHandlerID++
	id := nextRelativeCgroupPathHandlerID
	relativeCgroupPathHandlerList = append(relativeCgroupPathHandlerList, registeredRelativeCgroupPathHandler{
		id:      id,
		handler: handler,
	})
	relativeCgroupPathHandlerLock.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			relativeCgroupPathHandlerLock.Lock()
			defer relativeCgroupPathHandlerLock.Unlock()
			for i := range relativeCgroupPathHandlerList {
				if relativeCgroupPathHandlerList[i].id != id {
					continue
				}
				relativeCgroupPathHandlerList = append(
					relativeCgroupPathHandlerList[:i],
					relativeCgroupPathHandlerList[i+1:]...,
				)
				return
			}
		})
	}
}

func snapshotAbsoluteCgroupPathHandlers() []AbsoluteCgroupPathHandler {
	absoluteCgroupPathHandlerLock.RLock()
	defer absoluteCgroupPathHandlerLock.RUnlock()
	handlers := make([]AbsoluteCgroupPathHandler, len(absoluteCgroupPathHandlerList))
	copy(handlers, absoluteCgroupPathHandlerList)
	return handlers
}

func snapshotRelativeCgroupPathHandlers() []RelativeCgroupPathHandler {
	relativeCgroupPathHandlerLock.RLock()
	defer relativeCgroupPathHandlerLock.RUnlock()
	handlers := make([]RelativeCgroupPathHandler, len(relativeCgroupPathHandlerList))
	for i := range relativeCgroupPathHandlerList {
		handlers[i] = relativeCgroupPathHandlerList[i].handler
	}
	return handlers
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

// GetExistingRelativeCgroupPaths returns relative cgroup paths that exist
// under the default selected subsystem.
func GetExistingRelativeCgroupPaths(relativePaths ...string) []string {
	return GetExistingRelativeCgroupPathsForSubsys(DefaultSelectedSubsys, relativePaths...)
}

// GetExistingRelativeCgroupPathsForSubsys returns relative cgroup paths that
// exist under the given subsystem, preserving input order.
func GetExistingRelativeCgroupPathsForSubsys(subsys string, relativePaths ...string) []string {
	return getExistingRelativeCgroupPathsForSubsys(general.IsPathExists, subsys, relativePaths...)
}

func getExistingRelativeCgroupPathsForSubsys(pathExists func(string) bool, subsys string, relativePaths ...string) []string {
	if len(relativePaths) == 0 {
		return nil
	}

	existingPaths := make([]string, 0, len(relativePaths))
	for _, relativePath := range relativePaths {
		if relativePath == "" {
			continue
		}
		if pathExists(GetAbsCgroupPath(subsys, relativePath)) {
			existingPaths = append(existingPaths, relativePath)
		}
	}
	return existingPaths
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

// GetKubernetesAnyExistAbsCgroupPath returns any absolute cgroup path that exists for Kubernetes.
// If every candidate is absent, the returned error wraps os.ErrNotExist. Any
// non-absence probe failure is returned immediately so callers cannot mistake
// an unreadable hierarchy for a safely retired cgroup.
func GetKubernetesAnyExistAbsCgroupPath(subsys, suffix string) (string, error) {
	k8sCgroupPathLock.RLock()
	defer k8sCgroupPathLock.RUnlock()

	return getKubernetesAnyExistAbsCgroupPath(os.Stat, k8sCgroupPathList.List(), subsys, suffix)
}

// GetKubernetesAnyExistRelativeCgroupPath returns any relative cgroup path that exists for Kubernetes.
// It has the same typed-absence and fail-closed probe contract as the absolute
// path variant.
func GetKubernetesAnyExistRelativeCgroupPath(suffix string) (string, error) {
	k8sCgroupPathLock.RLock()
	defer k8sCgroupPathLock.RUnlock()

	return getKubernetesAnyExistRelativeCgroupPath(os.Stat, k8sCgroupPathList.List(), defaultSelectedSubsysList, suffix)
}

type cgroupPathStat func(string) (os.FileInfo, error)

type cgroupPathProbeCandidate struct {
	probePath  string
	resultPath string
}

func getKubernetesAnyExistAbsCgroupPath(
	stat cgroupPathStat,
	kubernetesRoots []string,
	subsys, suffix string,
) (string, error) {
	candidates := make([]cgroupPathProbeCandidate, 0, len(kubernetesRoots))
	for _, cgPath := range kubernetesRoots {
		absolutePath := GetKubernetesAbsCgroupPath(subsys, path.Join(cgPath, suffix))
		candidates = append(candidates, cgroupPathProbeCandidate{
			probePath:  absolutePath,
			resultPath: absolutePath,
		})
	}
	return firstExistingCgroupPath(stat, candidates, "absolute", suffix)
}

func getKubernetesAnyExistRelativeCgroupPath(
	stat cgroupPathStat,
	kubernetesRoots, subsystems []string,
	suffix string,
) (string, error) {
	candidates := make([]cgroupPathProbeCandidate, 0, len(kubernetesRoots)*len(subsystems))
	for _, cgPath := range kubernetesRoots {
		relativePath := path.Join(cgPath, suffix)
		for _, subsys := range subsystems {
			candidates = append(candidates, cgroupPathProbeCandidate{
				probePath:  GetKubernetesAbsCgroupPath(subsys, relativePath),
				resultPath: relativePath,
			})
		}
	}
	return firstExistingCgroupPath(stat, candidates, "relative", suffix)
}

func firstExistingCgroupPath(
	stat cgroupPathStat,
	candidates []cgroupPathProbeCandidate,
	pathKind, suffix string,
) (string, error) {
	for _, candidate := range candidates {
		_, err := stat(candidate.probePath)
		if err == nil {
			return candidate.resultPath, nil
		}
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		return "", fmt.Errorf("failed to stat cgroup path %q: %w", candidate.probePath, err)
	}

	return "", fmt.Errorf("failed to find %s path of suffix %q: %w", pathKind, suffix, os.ErrNotExist)
}

// GetPodRelativeCgroupPathCandidates returns every pod-level relative cgroup
// path implied by the configured Kubernetes roots. It does not inspect the
// filesystem, so callers can resolve a pod scope before kubelet materializes
// the pod cgroup.
func GetPodRelativeCgroupPathCandidates(podUID string) []string {
	k8sCgroupPathLock.RLock()
	defer k8sCgroupPathLock.RUnlock()
	return podRelativeCgroupPathCandidates(k8sCgroupPathList.List(), podUID)
}

// GetPodRelativeCgroupPathCandidatesForQOS returns the filesystem-independent
// candidates for the pod's native Kubernetes QoS class. Unknown QoS classes
// retain all configured roots so the DAG selector can fail closed on ambiguity.
func GetPodRelativeCgroupPathCandidatesForQOS(podUID string, qosClass v1.PodQOSClass) []string {
	k8sCgroupPathLock.RLock()
	defer k8sCgroupPathLock.RUnlock()
	return podRelativeCgroupPathCandidatesForQOS(k8sCgroupPathList.List(), podUID, qosClass)
}

func podRelativeCgroupPathCandidates(kubernetesRoots []string, podUID string) []string {
	candidates := make([]string, 0, len(kubernetesRoots))
	seen := make(map[string]struct{}, len(kubernetesRoots))
	for _, root := range kubernetesRoots {
		candidate := podRelativeCgroupPath(root, podUID)
		if _, ok := seen[candidate]; ok {
			continue
		}
		seen[candidate] = struct{}{}
		candidates = append(candidates, candidate)
	}
	return candidates
}

// podRelativeCgroupPath converts the abstract pod component into the naming
// convention implied by root. Cgroupfs keeps pod<UID> as a directory, while a
// systemd slice must include its parent unit prefix and escape UID dashes.
func podRelativeCgroupPath(root, podUID string) string {
	podName := fmt.Sprintf("%s%s", PodCgroupPathPrefix, podUID)
	rootBase := path.Base(path.Clean(root))
	if !strings.HasSuffix(rootBase, ".slice") {
		return path.Join(root, podName)
	}

	unitPrefix := strings.TrimSuffix(rootBase, ".slice")
	return path.Join(root, fmt.Sprintf("%s-%s.slice", unitPrefix, strings.ReplaceAll(podName, "-", "_")))
}

func podRelativeCgroupPathCandidatesForQOS(
	kubernetesRoots []string,
	podUID string,
	qosClass v1.PodQOSClass,
) []string {
	allowedRoots := map[string]struct{}{}
	switch qosClass {
	case v1.PodQOSGuaranteed:
		allowedRoots[path.Clean(CgroupFsRootPath)] = struct{}{}
		allowedRoots[path.Clean(SystemdRootPath)] = struct{}{}
	case v1.PodQOSBurstable:
		allowedRoots[path.Clean(CgroupFsRootPathBurstable)] = struct{}{}
		allowedRoots[path.Clean(SystemdRootPathBurstable)] = struct{}{}
	case v1.PodQOSBestEffort:
		allowedRoots[path.Clean(CgroupFsRootPathBestEffort)] = struct{}{}
		allowedRoots[path.Clean(SystemdRootPathBestEffort)] = struct{}{}
	default:
		return podRelativeCgroupPathCandidates(kubernetesRoots, podUID)
	}

	filteredRoots := make([]string, 0, len(kubernetesRoots))
	for _, root := range kubernetesRoots {
		if _, ok := allowedRoots[path.Clean(root)]; ok {
			filteredRoots = append(filteredRoots, root)
		}
	}
	return podRelativeCgroupPathCandidates(filteredRoots, podUID)
}

// GetPodAbsCgroupPath returns absolute cgroup path for pod level
func GetPodAbsCgroupPath(subsys, podUID string) (string, error) {
	return GetKubernetesAnyExistAbsCgroupPath(subsys, fmt.Sprintf("%s%s", PodCgroupPathPrefix, podUID))
}

// GetPodRelativeCgroupPath returns relative cgroup path for pod level
func GetPodRelativeCgroupPath(podUID string) (string, error) {
	return GetKubernetesAnyExistRelativeCgroupPath(fmt.Sprintf("%s%s", PodCgroupPathPrefix, podUID))
}

func getContainerDefaultAbsCgroupPath(subsys, podUID, containerId string) (string, bool, error) {
	cgroupPath, err := GetKubernetesAnyExistAbsCgroupPath(subsys, path.Join(fmt.Sprintf("%s%s", PodCgroupPathPrefix, podUID), containerId))
	return cgroupPath, false, err
}

func getContainerDefaultRelativeAbsCgroupPath(podUID, containerId string) (string, bool, error) {
	cgroupPath, err := GetKubernetesAnyExistRelativeCgroupPath(path.Join(fmt.Sprintf("%s%s", PodCgroupPathPrefix, podUID), containerId))
	return cgroupPath, false, err
}

func resolveContainerAbsCgroupPath(handlers []AbsoluteCgroupPathHandler, subsys, podUID, containerId string) (string, error) {
	var operationalErrors []error
	attempted := false
	for _, handler := range handlers {
		if handler.Handler == nil {
			operationalErrors = append(operationalErrors,
				fmt.Errorf("absolute cgroup path Handler for %s is nil", handler.Name))
			continue
		}
		cgroupPath, skip, err := handler.Handler(subsys, podUID, containerId)
		if skip {
			continue
		}
		attempted = true
		if err == nil {
			return cgroupPath, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			operationalErrors = append(operationalErrors,
				fmt.Errorf("get absolute cgroup path by Handler %s failed: %w", handler.Name, err))
		}
	}
	if len(operationalErrors) > 0 {
		return "", utilerrors.NewAggregate(operationalErrors)
	}
	if !attempted {
		return "", fmt.Errorf("all absolute cgroup path handlers skipped for pod %s container %s: %w",
			podUID, containerId, os.ErrNotExist)
	}
	return "", fmt.Errorf("all absolute cgroup path handlers reported absence or skipped for pod %s container %s: %w",
		podUID, containerId, os.ErrNotExist)
}

// GetContainerAbsCgroupPath returns the first container path resolved by the
// configured handlers. It returns typed os.ErrNotExist only when no handler
// reports an operational failure and no handler resolves the container.
// Operational failures dominate absence to keep lifecycle callers fail closed.
func GetContainerAbsCgroupPath(subsys, podUID, containerId string) (string, error) {
	return resolveContainerAbsCgroupPath(snapshotAbsoluteCgroupPathHandlers(), subsys, podUID, containerId)
}

func resolveContainerRelativeCgroupPath(handlers []RelativeCgroupPathHandler, podUID, containerId string) (string, error) {
	var operationalErrors []error
	attempted := false
	for _, handler := range handlers {
		if handler.Handler == nil {
			operationalErrors = append(operationalErrors,
				fmt.Errorf("relative cgroup path Handler for %s is nil", handler.Name))
			continue
		}
		cgroupPath, skip, err := handler.Handler(podUID, containerId)
		if skip {
			continue
		}
		attempted = true
		if err == nil {
			return cgroupPath, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			operationalErrors = append(operationalErrors,
				fmt.Errorf("get relative cgroup path by Handler %s failed: %w", handler.Name, err))
		}
	}
	if len(operationalErrors) > 0 {
		return "", utilerrors.NewAggregate(operationalErrors)
	}
	if !attempted {
		return "", fmt.Errorf("all relative cgroup path handlers skipped for pod %s container %s: %w",
			podUID, containerId, os.ErrNotExist)
	}
	return "", fmt.Errorf("all relative cgroup path handlers reported absence or skipped for pod %s container %s: %w",
		podUID, containerId, os.ErrNotExist)
}

// GetContainerRelativeCgroupPath follows the same handler ordering and
// fail-closed typed-absence contract as GetContainerAbsCgroupPath.
func GetContainerRelativeCgroupPath(podUID, containerId string) (string, error) {
	return resolveContainerRelativeCgroupPath(snapshotRelativeCgroupPathHandlers(), podUID, containerId)
}

func IsContainerCgroupExist(podUID, containerID string) (bool, error) {
	containerAbsCGPath, err := GetContainerAbsCgroupPath("", podUID, containerID)
	if err != nil {
		return false, fmt.Errorf("GetContainerAbsCgroupPath failed: %w", err)
	}

	return general.IsPathExists(containerAbsCGPath), nil
}

func IsContainerCgroupFileExist(subsys, podUID, containerId, cgroupFileName string) (bool, error) {
	absCgroupPath, err := GetContainerAbsCgroupPath(subsys, podUID, containerId)
	if err != nil {
		return false, fmt.Errorf("GetContainerAbsCgroupPath failed: %w", err)
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
