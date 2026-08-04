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

package cpuidle

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"

	v1 "k8s.io/api/core/v1"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"

	katalystapiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	qrmutil "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	cgroupmgr "github.com/kubewharf/katalyst-core/pkg/util/cgroup/manager"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
	qosutil "github.com/kubewharf/katalyst-core/pkg/util/qos"
)

const defaultCPUCFSPeriodUs = int64(100000)

var (
	errContainerIDNotReady = errors.New("container id is not ready")
	errContainerNotReady   = errors.New("container is not ready")
)

type Manager interface {
	UpdateContainerCPUIdle(conf *config.Configuration) error
}

type managerImpl struct {
	metaServer *metaserver.MetaServer
}

var (
	instance *managerImpl
	once     sync.Once
)

func GetManager(metaServer *metaserver.MetaServer) Manager {
	once.Do(func() {
		instance = newManager(metaServer)
	})
	return instance
}

func newManager(metaServer *metaserver.MetaServer) *managerImpl {
	return &managerImpl{metaServer: metaServer}
}

func (m *managerImpl) UpdateContainerCPUIdle(conf *config.Configuration) error {
	if conf == nil {
		return fmt.Errorf("nil configuration")
	}
	if m.metaServer == nil {
		return fmt.Errorf("nil metaServer")
	}
	if !common.CheckCgroup2UnifiedMode() {
		general.Infof("current cgroup mode is not cgroupv2, skip container cpu idle sync")
		return nil
	}
	if !common.IsCPUIdleSupported() {
		general.Infof("cpu.idle is not supported, skip container cpu idle sync")
		return nil
	}

	podList, err := m.metaServer.GetPodList(context.Background(), native.PodIsActive)
	if err != nil {
		return fmt.Errorf("error getting pod list: %v", err)
	}

	for _, pod := range podList {
		found, idleConfig, err := qosutil.GetPodContainerCPUIdleRateConfigFromAnnotation(pod.Annotations)
		if err != nil {
			general.Warningf("parse container cpu idle annotation for pod %s/%s failed: %v",
				pod.Namespace, pod.Name, err)
			continue
		}
		if !found || len(idleConfig) == 0 {
			continue
		}

		if err = m.applyCPUIdleSettingsForPod(conf, pod, idleConfig); err != nil {
			general.Warningf("apply container cpu idle for pod %s/%s failed: %v",
				pod.Namespace, pod.Name, err)
		}
	}

	return nil
}

func (m *managerImpl) applyCPUIdleSettingsForPod(conf *config.Configuration, pod *v1.Pod, idleConfig qosutil.ContainerCPUIdleRateConfig) error {
	if pod == nil {
		return fmt.Errorf("nil pod")
	}

	podTotalMilli, containersWithoutLimit := getPodTotalCPULimitMilli(pod)
	if len(containersWithoutLimit) > 0 {
		return fmt.Errorf("pod cpu limit is unset for containers: %s", strings.Join(containersWithoutLimit, ","))
	}
	if podTotalMilli <= 0 {
		return fmt.Errorf("pod total cpu limit is invalid: %d", podTotalMilli)
	}

	mainContainerName := qrmutil.GetMainContainer(pod, conf.MainContainerAnnotationKey)
	var errList []error
	for targetContainerName, targetCPUQuotaRate := range idleConfig {
		if targetContainerName == "" {
			errList = append(errList, fmt.Errorf("empty target container name"))
			continue
		}
		if targetCPUQuotaRate <= 0 || targetCPUQuotaRate > 100 {
			errList = append(errList, fmt.Errorf("invalid target cpu quota rate for container %s: %d", targetContainerName, targetCPUQuotaRate))
			continue
		}
		if targetContainerName == mainContainerName {
			general.Infof("skip applying container cpu idle for main container %s in pod %s/%s", targetContainerName, pod.Namespace, pod.Name)
			continue
		}

		if err := m.applyCPUIdleSettingsToContainer(pod, targetContainerName, targetCPUQuotaRate, podTotalMilli); err != nil {
			errList = append(errList, err)
		}
	}

	return utilerrors.NewAggregate(errList)
}

func (m *managerImpl) applyCPUIdleSettingsToContainer(pod *v1.Pod, targetContainerName string, targetCPUQuotaRate, podTotalMilli int64) error {
	if pod == nil {
		return fmt.Errorf("nil pod")
	}

	containerStatus := getContainerStatusFromPodStatus(pod, targetContainerName)
	if containerStatus == nil || !containerStatus.Ready {
		return nil
	}

	cpuStats, err := m.getContainerCurrentCPUStats(pod, targetContainerName)
	if err != nil {
		if errors.Is(err, errContainerNotReady) || errors.Is(err, errContainerIDNotReady) {
			return nil
		}
		return fmt.Errorf("get current cpu stats for %s failed: %w", targetContainerName, err)
	}

	_, ok := getCurrentCPULimitMilli(cpuStats)
	if !ok {
		return nil
	}

	containerID, err := getContainerIDFromPodStatus(pod, targetContainerName)
	if err != nil {
		if errors.Is(err, errContainerIDNotReady) {
			return nil
		}
		return fmt.Errorf("get container id for %s failed: %w", targetContainerName, err)
	}

	relCgroupPath, err := common.GetContainerRelativeCgroupPath(string(pod.UID), containerID)
	if err != nil {
		return fmt.Errorf("get cgroup path for %s failed: %w", targetContainerName, err)
	}

	targetQuotaUs := podTotalMilli * targetCPUQuotaRate * defaultCPUCFSPeriodUs / 100 / 1000
	idleValue := true
	if err = cgroupmgr.ApplyCPUWithRelativePath(relCgroupPath, &common.CPUData{
		CpuQuota:   targetQuotaUs,
		CpuPeriod:  uint64(defaultCPUCFSPeriodUs),
		CpuIdlePtr: &idleValue,
	}); err != nil {
		return fmt.Errorf("apply cpu settings for %s failed: %w", targetContainerName, err)
	}

	general.Infof("applied annotation cpu idle settings for %s/%s container %s: cpu.idle=1, cpu.max=%d %d, quota rate: %d, annotation key: %s",
		pod.Namespace, pod.Name, targetContainerName, targetQuotaUs, defaultCPUCFSPeriodUs, targetCPUQuotaRate, katalystapiconsts.PodAnnotationContainerCPUIdleRateKey)
	return nil
}

func getPodTotalCPULimitMilli(pod *v1.Pod) (int64, []string) {
	if pod == nil || len(pod.Spec.Containers) == 0 {
		return 0, nil
	}

	var totalMilli int64
	var containersWithoutLimit []string
	for i := range pod.Spec.Containers {
		container := &pod.Spec.Containers[i]
		cpuLimit, ok := container.Resources.Limits[v1.ResourceCPU]
		if !ok {
			containersWithoutLimit = append(containersWithoutLimit, container.Name)
			continue
		}

		totalMilli += cpuLimit.MilliValue()
	}

	return totalMilli, containersWithoutLimit
}

func getCurrentCPULimitMilli(cpuStats *common.CPUStats) (int64, bool) {
	if cpuStats == nil || cpuStats.CpuQuota == math.MaxInt64 || cpuStats.CpuPeriod == 0 {
		return 0, false
	}

	return cpuStats.CpuQuota * 1000 / int64(cpuStats.CpuPeriod), true
}

func (m *managerImpl) getContainerCurrentCPUStats(pod *v1.Pod, containerName string) (*common.CPUStats, error) {
	if m.metaServer == nil {
		return nil, fmt.Errorf("nil metaServer")
	}
	if pod == nil {
		return nil, fmt.Errorf("nil pod")
	}

	containerID, err := getContainerIDFromPodStatus(pod, containerName)
	if err != nil {
		return nil, fmt.Errorf("get container id failed: %w", err)
	}

	relCgroupPath, err := common.GetContainerRelativeCgroupPath(string(pod.UID), containerID)
	if err != nil {
		return nil, fmt.Errorf("get cgroup path failed: %w", err)
	}

	cpuStats, err := cgroupmgr.GetCPUWithRelativePath(relCgroupPath)
	if err != nil {
		return nil, fmt.Errorf("get cpu.max failed: %w", err)
	}

	return cpuStats, nil
}

func getContainerStatusFromPodStatus(pod *v1.Pod, containerName string) *v1.ContainerStatus {
	if pod == nil {
		return nil
	}

	for i := range pod.Status.ContainerStatuses {
		if pod.Status.ContainerStatuses[i].Name == containerName {
			return &pod.Status.ContainerStatuses[i]
		}
	}

	return nil
}

func getContainerIDFromPodStatus(pod *v1.Pod, containerName string) (string, error) {
	if pod == nil {
		return "", fmt.Errorf("nil pod")
	}

	status := getContainerStatusFromPodStatus(pod, containerName)
	if status == nil {
		return "", fmt.Errorf("container %s container id not found", containerName)
	}

	if !status.Ready {
		return "", fmt.Errorf("%w for container %s with state waiting=%t running=%t terminated=%t", errContainerNotReady,
			containerName, status.State.Waiting != nil, status.State.Running != nil, status.State.Terminated != nil)
	}

	if status.ContainerID == "" {
		return "", fmt.Errorf("%w for container %s with state waiting=%t running=%t terminated=%t", errContainerIDNotReady,
			containerName, status.State.Waiting != nil, status.State.Running != nil, status.State.Terminated != nil)
	}

	return native.TrimContainerIDPrefix(status.ContainerID), nil
}
