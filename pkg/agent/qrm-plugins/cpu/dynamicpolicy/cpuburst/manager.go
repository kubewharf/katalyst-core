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
	"sync"

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

var (
	instance *managerImpl
	once     sync.Once
)

// GetManager returns a single global instance of the cpu burst manager
func GetManager(metaServer *metaserver.MetaServer) Manager {
	once.Do(func() {
		instance = newManager(metaServer)
	})
	return instance
}

func newManager(metaServer *metaserver.MetaServer) *managerImpl {
	return &managerImpl{
		metaServer: metaServer,
	}
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

		switch cpuBurstPolicy {
		case consts.PodAnnotationCPUEnhancementCPUBurstPolicyClosed:
			// For closed policy, we just set the cpu burst value to be 0.
			if err = m.updateCPUBurstByPercent(0, pod); err != nil {
				errList = append(errList, fmt.Errorf("error setting cpu burst for policy %s for pod %s: %v",
					consts.PodAnnotationCPUEnhancementCPUBurstPolicyClosed, pod.Name, err))
			}
		case consts.PodAnnotationCPUEnhancementCPUBurstPolicyDefault:
			continue
		case consts.PodAnnotationCPUEnhancementCPUBurstPolicyStatic:
			// For static policy, calculate the cpu burst value that we need to set.
			cpuBurstPercent, err := util.GetPodCPUBurstPercent(qosConf, pod, dynamicConfig)
			if err != nil {
				errList = append(errList, fmt.Errorf("error getting cpu burst percent for pod %s: %v", pod.Name, err))
				continue
			}

			if err = m.updateCPUBurstByPercent(cpuBurstPercent, pod); err != nil {
				errList = append(errList, fmt.Errorf("error setting cpu burst for policy %s for pod %s: %v",
					consts.PodAnnotationCPUEnhancementCPUBurstPolicyStatic, pod.Name, err))
			}
		case consts.PodAnnotationCPUEnhancementCPUBurstPolicyDynamic:
			errList = append(errList, fmt.Errorf("dynamic cpu burst policy is not supported yet"))
		default:
			errList = append(errList, fmt.Errorf("cpu burst policy %s is not supported", cpuBurstPolicy))
		}
	}

	return utilerrors.NewAggregate(errList)
}

// updateCPUBurstByPercent updates the value of cpu burst for static policy by taking the
// cpu quota from cgroup and calculating the cpu burst value by taking cpu quota * percent / 100.
func (m *managerImpl) updateCPUBurstByPercent(percent float64, pod *v1.Pod) error {
	var errList []error
	podUID := string(pod.GetUID())
	podName := pod.Name

	for _, container := range pod.Spec.Containers {
		containerName := container.Name
		containerID, err := m.metaServer.GetContainerID(podUID, containerName)
		if err != nil {
			general.Errorf("get container id failed, pod: %s, podName: %s, container: %s(%s), err: %v", podUID, podName, containerName, containerID, err)
			continue
		}

		if exist, err := common.IsContainerCgroupExist(podUID, containerID); err != nil {
			general.Errorf("check if container cgroup exists failed, pod: %s, podName: %s, container: %s(%s), err: %v",
				podUID, podName, containerName, containerID, err)
			continue
		} else if !exist {
			general.Infof("container cgroup does not exist, pod: %s, podName: %s, container: %s(%s)", podUID, podName, containerName, containerID)
			continue
		}

		containerAbsoluteCgroupPath, err := common.GetContainerAbsCgroupPath(common.CgroupSubsysCPU, podUID, containerID)
		if err != nil {
			general.Errorf("get container absolute cgroup path failed, pod: %s, podName: %s, container: %s(%s), err: %v", podUID, podName, containerName, containerID, err)
			errList = append(errList, err)
			continue
		}

		cpuStats, err := manager.GetCPUWithAbsolutePath(containerAbsoluteCgroupPath)
		if err != nil {
			general.Errorf("get container cpu stats failed, pod: %s, podName: %s, container: %s(%s), err: %v", podUID, podName, containerName, containerID, err)
			errList = append(errList, err)
			continue
		}

		cpuBurstValue := util.CalculateCPUBurstFromPercent(percent, cpuStats.CpuQuota)
		if err = manager.ApplyCPUWithAbsolutePath(containerAbsoluteCgroupPath, &common.CPUData{CpuBurst: &cpuBurstValue}); err != nil {
			general.Errorf("apply container cpu burst failed, pod: %s, podName: %s, container: %s(%s), err: %v", podUID, podName, containerName, containerID, err)
			errList = append(errList, err)
			continue
		}

		general.Infof("apply container cpu burst successfully, pod: %s, podName: %s, container: %s(%s), cpu burst: %d", podUID, podName, containerName, containerID, cpuBurstValue)
	}

	return utilerrors.NewAggregate(errList)
}
