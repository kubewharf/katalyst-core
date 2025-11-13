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

package cpuburst

import (
	"context"
	"fmt"

	v1 "k8s.io/api/core/v1"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/util"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	"github.com/kubewharf/katalyst-core/pkg/util/cgroup/manager"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

type Manager interface {
	UpdateCPUBurst(qosConf *generic.QoSConfiguration, dynamicConfig *dynamic.DynamicAgentConfiguration) error
}

type managerImpl struct {
	metaServer *metaserver.MetaServer
}

func NewManager(metaServer *metaserver.MetaServer) Manager {
	return &managerImpl{metaServer: metaServer}
}

// UpdateCPUBurst calculates the value of cpu burst and sets it to the cgroup.
func (m *managerImpl) UpdateCPUBurst(qosConf *generic.QoSConfiguration, dynamicConfig *dynamic.DynamicAgentConfiguration) error {
	if m.metaServer == nil {
		return fmt.Errorf("nil metaServer")
	}

	ctx := context.Background()
	podList, err := m.metaServer.GetPodList(ctx, native.PodIsActive)
	if err != nil {
		return fmt.Errorf("error getting pod list: %v", err)
	}

	var errList []error

	for _, pod := range podList {
		cpuBurstPolicy, err := util.GetPodCPUBurstPolicy(qosConf, pod, dynamicConfig)
		if err != nil {
			errList = append(errList, fmt.Errorf("error getting cpu burst policy for pod %s: %v", pod.Name, err))
			continue
		}

		cpuBurstPercent, err := util.GetPodCPUBurstPercent(qosConf, pod, dynamicConfig)
		if err != nil {
			errList = append(errList, fmt.Errorf("error getting cpu burst percent for pod %s: %v", pod.Name, err))
			continue
		}

		switch cpuBurstPolicy {
		case consts.PodAnnotationCPUEnhancementCPUBurstPolicyNone, consts.PodAnnotationCPUEnhancementCPUBurstPolicyDynamic:
			continue
		case consts.PodAnnotationCPUEnhancementCPUBurstPolicyStatic:
			if err = m.updateCPUBurstForStaticPolicy(cpuBurstPercent, pod); err != nil {
				errList = append(errList, err)
			}
		default:
			errList = append(errList, fmt.Errorf("cpu burst policy %s is not supported", cpuBurstPolicy))
		}
	}

	return utilerrors.NewAggregate(errList)
}

// updateCPUBurstForStaticPolicy updates the value of cpu burst for static policy by taking the
// cpu quota from cgroup and calculating the cpu burst value by taking cpu quota * percent / 100.
func (m *managerImpl) updateCPUBurstForStaticPolicy(percent float64, pod *v1.Pod) error {
	var errList []error
	podUID := string(pod.GetUID())

	for _, container := range pod.Spec.Containers {
		containerName := container.Name
		containerID, err := m.metaServer.GetContainerID(podUID, containerName)
		if err != nil {
			general.Errorf("get container id failed, pod: %s, container: %s(%s), err: %v", podUID, containerName, containerID, err)
			errList = append(errList, err)
			continue
		}

		if exist, err := common.IsContainerCgroupExist(podUID, containerID); err != nil {
			general.Errorf("check if container cgroup exists failed, pod: %s, container: %s(%s), err: %v",
				podUID, containerName, containerID, err)
			errList = append(errList, err)
			continue
		} else if !exist {
			general.Infof("container cgroup does not exist, pod: %s, container: %s(%s)", podUID, containerName, containerID)
			continue
		}

		containerAbsoluteCgroupPath, err := common.GetContainerAbsCgroupPath(common.CgroupSubsysCPU, podUID, containerID)
		if err != nil {
			general.Errorf("get container absolute cgroup path failed, pod: %s, container: %s(%s), err: %v", podUID, containerName, containerID, err)
			errList = append(errList, err)
			continue
		}

		cpuStats, err := manager.GetCPUWithAbsolutePath(containerAbsoluteCgroupPath)
		if err != nil {
			general.Errorf("get container cpu stats failed, pod: %s, container: %s(%s), err: %v", podUID, containerName, containerID, err)
			errList = append(errList, err)
			continue
		}

		cpuBurstValue := util.CalculateCPUBurstFromPercent(percent, cpuStats.CpuQuota)
		if err = manager.ApplyCPUWithAbsolutePath(containerAbsoluteCgroupPath, &common.CPUData{CpuBurst: cpuBurstValue}); err != nil {
			general.Errorf("apply container cpu burst failed, pod: %s, container: %s(%s), err: %v", podUID, containerName, containerID, err)
			errList = append(errList, err)
			continue
		}

		general.Infof("apply container cpu burst successfully, pod: %s, container: %s(%s), cpu burst: %d", podUID, containerName, containerID, cpuBurstValue)
	}

	return utilerrors.NewAggregate(errList)
}
