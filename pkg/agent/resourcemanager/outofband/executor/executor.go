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

package executor

import (
	"fmt"

	v1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
	"k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util"
	"github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	cgroupmgr "github.com/kubewharf/katalyst-core/pkg/util/cgroup/manager"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

type Executor interface {
	UpdateContainerResources(pod *v1.Pod, container *v1.Container, resourceAllocation map[string]*v1alpha1.ResourceAllocationInfo) error
}

type Impl struct {
	cgroupManager cgroupmgr.Manager
}

func NewExecutor(cgroupManager cgroupmgr.Manager) Executor {
	return &Impl{
		cgroupManager: cgroupManager,
	}
}

// UpdateContainerResources update container resources by resourceAllocation
func (ei *Impl) UpdateContainerResources(pod *v1.Pod, container *v1.Container, resourceAllocation map[string]*v1alpha1.ResourceAllocationInfo) error {
	if pod == nil || container == nil {
		klog.Warningf("UpdateContainerResources, pod or container is nil")
		return nil
	}
	if len(resourceAllocation) == 0 {
		return fmt.Errorf("empty resourceAllocation for pod: %v, container: %v", pod.Name, container.Name)
	}

	var (
		CPUSetData = &common.CPUSetData{}
	)

	for _, resourceAllocationInfo := range resourceAllocation {
		switch resourceAllocationInfo.OciPropertyName {
		case util.OCIPropertyNameCPUSetCPUs:
			if resourceAllocationInfo.AllocationResult != "" {
				CPUSetData.CPUs = resourceAllocationInfo.AllocationResult
			}
		case util.OCIPropertyNameCPUSetMems:
			if resourceAllocationInfo.AllocationResult != "" {
				CPUSetData.Mems = resourceAllocationInfo.AllocationResult
			}
		default:

		}
	}

	absCgroupPath, err := ei.containerCgroupPath(pod, container)
	if err != nil {
		klog.Errorf("[ORM] containerCgroupPath fail, pod: %v, container: %v, err: %v", pod.Name, container.Name, err)
		return err
	}

	err = ei.commitCPUSet(absCgroupPath, CPUSetData)
	if err != nil {
		klog.Errorf("[ORM] commitCPUSet fail, pod: %v, container: %v, err: %v", pod.Name, container.Name, err)
		return err
	}

	return nil
}

// applyCPUSet apply CPUSet data by cgroupManager
func (ei *Impl) applyCPUSet(absCgroupPath string, data *common.CPUSetData) error {
	return ei.cgroupManager.ApplyCPUSet(absCgroupPath, data)
}

// commitCPUSet rollback if any data apply failed in data
// consider if such operation is necessary, for runc does not guarantee the atomicity of cgroup subsystem settings either
// https://github.com/opencontainers/runc/blob/main/libcontainer/cgroups/fs/cpuset.go#L27
func (ei *Impl) commitCPUSet(absCgroupPath string, data *common.CPUSetData) error {
	CPUSetStats, err := ei.cgroupManager.GetCPUSet(absCgroupPath)
	if err != nil {
		return err
	}

	err = ei.applyCPUSet(absCgroupPath, data)
	if err != nil {
		// rollback
		rollbackErr := ei.applyCPUSet(absCgroupPath, &common.CPUSetData{
			CPUs: CPUSetStats.CPUs,
			Mems: CPUSetStats.Mems,
		})
		if rollbackErr == nil {
			err = fmt.Errorf("applyCPUSet fail, CPUSet rollback, err: %v", err)
			return err
		} else {
			err = fmt.Errorf("applyCPUSet fail, rollback fail, err: %v, rollbackErr: %v", err, rollbackErr)
			return err
		}
	}

	return nil
}

func (ei *Impl) containerCgroupPath(pod *v1.Pod, container *v1.Container) (string, error) {
	containerID, err := native.GetContainerID(pod, container.Name)
	if err != nil {
		return "", err
	}

	absCgroupPath, err := common.GetContainerAbsCgroupPath(common.CgroupSubsysCPUSet, string(pod.UID), containerID)
	if err != nil {
		return "", err
	}

	return absCgroupPath, nil
}
