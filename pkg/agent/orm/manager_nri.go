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

package orm

import (
	"context"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"

	"github.com/containerd/nri/pkg/api"
	"github.com/containerd/nri/pkg/stub"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/consts"
)

type nriConfig struct {
	Events []string `json:"events"`
}

func (m *ManagerImpl) validateNRIMode(config *config.Configuration) bool {
	var err error
	if config.ORMWorkMode != consts.WorkModeNri {
		return false
	}
	if _, err := os.Stat(config.ORMNRISocketPath); os.IsNotExist(err) {
		klog.Errorf("[ORM] nri socket path %q does not exist", config.ORMNRISocketPath)
		return false
	}
	var opts []stub.Option
	opts = append(opts, stub.WithPluginName(config.ORMNRIPluginName))
	opts = append(opts, stub.WithPluginIdx(config.ORMNRIPluginIndex))
	opts = append(opts, stub.WithSocketPath(config.ORMNRISocketPath))
	m.nriOptions = opts

	if m.nriMask, err = api.ParseEventMask(config.ORMNRIHandleEvents); err != nil {
		klog.Errorf("[ORM] parse nri handle events fail: %v", err)
		return false
	}
	if m.nriStub, err = stub.New(m, append(opts, stub.WithOnClose(m.onClose))...); err != nil {
		klog.Errorf("[ORM] create nri stub fail: %v", err)
		return false
	}
	return true
}

func (m *ManagerImpl) Configure(_ context.Context, config, runtime, version string) (stub.EventMask, error) {
	klog.V(4).Infof("got configuration data: %q from runtime %s %s", config, runtime, version)
	if config == "" {
		return m.nriMask, nil
	}

	err := yaml.Unmarshal([]byte(config), &m.nriConf)
	if err != nil {
		return 0, fmt.Errorf("failed to parse provided configuration: %w", err)
	}

	m.nriMask, err = api.ParseEventMask(m.nriConf.Events...)
	if err != nil {
		return 0, fmt.Errorf("failed to parse events in configuration: %w", err)
	}

	klog.V(6).Infof("handle NRI Configure successfully, config %s, runtime %s, version %s",
		config, runtime, version)
	return m.nriMask, nil
}

func (m *ManagerImpl) Synchronize(_ context.Context, pods []*api.PodSandbox, containers []*api.Container) (
	[]*api.ContainerUpdate, error,
) {
	// todo: update existed containers resources if orm stared after the Pod create events
	return nil, nil
}

func (m *ManagerImpl) RunPodSandbox(_ context.Context, pod *api.PodSandbox) error {
	klog.Infof("[ORM] RunPodSandbox, pod: %s/%s/%s", pod.Namespace, pod.Name, pod.Uid)
	klog.V(6).Infof("[ORM] RunPodSandbox, pod annotations: %v", pod.Annotations)
	err := m.processAddPod(pod.Uid)
	if err != nil {
		klog.Errorf("[ORM] RunPodSandbox processAddPod fail, pod: %s/%s/%s, err: %v",
			pod.Namespace, pod.Name, pod.Uid, err)
	}
	return err
}

func (m *ManagerImpl) CreateContainer(_ context.Context, pod *api.PodSandbox, container *api.Container) (
	*api.ContainerAdjustment, []*api.ContainerUpdate, error,
) {
	klog.Infof("[ORM] CreateContainer, pod: %s/%s/%s, container: %v", pod.Namespace, pod.Name, pod.Uid, container.Name)
	containerAllResources := m.podResources.containerAllResources(pod.Uid, container.Name)
	if containerAllResources == nil {
		klog.V(5).Infof("[ORM] CreateContainer process failed, pod: %s/%s/%s, container: %v, resources nil",
			pod.Namespace, pod.Name, pod.Uid, container.Name)
		return nil, nil, nil
	}

	adjust := &api.ContainerAdjustment{}
	for _, resourceAllocationInfo := range containerAllResources {
		switch resourceAllocationInfo.OciPropertyName {
		case util.OCIPropertyNameCPUSetCPUs:
			if resourceAllocationInfo.AllocationResult != "" {
				adjust.SetLinuxCPUSetCPUs(resourceAllocationInfo.AllocationResult)
			}
		case util.OCIPropertyNameCPUSetMems:
			if resourceAllocationInfo.AllocationResult != "" {
				adjust.SetLinuxCPUSetMems(resourceAllocationInfo.AllocationResult)
			}
		}
	}
	klog.V(5).Infof("[ORM] handle NRI CreateContainer successfully, pod: %s/%s/%s, container: %s, adjust: %v",
		pod.Namespace, pod.Name, pod.Uid, container.Name, adjust)
	return adjust, nil, nil
}

func (m *ManagerImpl) UpdateContainer(_ context.Context, pod *api.PodSandbox, container *api.Container, r *api.LinuxResources,
) ([]*api.ContainerUpdate, error) {
	// todo: hook this method to update container resources
	return nil, nil
	// containerUpdate := m.getNRIContainerUpdate(pod.Uid, container.Id, container.Name)
	// klog.V(5).Infof("[ORM] handle NRI UpdateContainer successfully, pod: %s/%s/%s, container: %s, update: %v",
	//	pod.Namespace, pod.Name, pod.Uid, container.Name, containerUpdate)
	// return []*api.ContainerUpdate{containerUpdate}, nil
}

func (m *ManagerImpl) RemovePodSandbox(_ context.Context, pod *api.PodSandbox) error {
	klog.Infof("[ORM] RemovePodSandbox, pod: %s/%s/%s", pod.Namespace, pod.Name, pod.Uid)
	err := m.processDeletePod(pod.Uid)
	if err != nil {
		klog.Errorf("[ORM] RemovePodSandbox processDeletePod fail, pod: %s/%s/%s, err: %v",
			pod.Namespace, pod.Name, pod.Uid, err)
	}
	return err
}

func (m *ManagerImpl) onClose() {
	m.nriStub.Stop()
	klog.V(6).Infof("NRI server closes")
}

func (m *ManagerImpl) updateContainerByNRI(podUID, containerId, containerName string) {
	klog.V(2).Infof("[ORM] updateContainerByNRI, pod: %v, container: %v", podUID, containerName)
	containerUpdate := m.getNRIContainerUpdate(podUID, containerId, containerName)
	_, err := m.nriStub.UpdateContainers([]*api.ContainerUpdate{containerUpdate})
	if err != nil {
		klog.Errorf("[ORM] updateContainerByNRI fail, pod %v container %v,resource %v, err: %v", podUID, containerName, err)
	}
}

func (m *ManagerImpl) getNRIContainerUpdate(podUID, containerId, containerName string) *api.ContainerUpdate {
	containerUpdate := &api.ContainerUpdate{
		ContainerId: containerId,
		Linux:       &api.LinuxContainerUpdate{Resources: &api.LinuxResources{Cpu: &api.LinuxCPU{Cpus: "", Mems: ""}}},
	}
	containerAllResources := m.podResources.containerAllResources(podUID, containerName)
	for _, resourceAllocationInfo := range containerAllResources {
		switch resourceAllocationInfo.OciPropertyName {
		case util.OCIPropertyNameCPUSetCPUs:
			if resourceAllocationInfo.AllocationResult != "" {
				containerUpdate.Linux.Resources.Cpu.Cpus = resourceAllocationInfo.AllocationResult
			}
		case util.OCIPropertyNameCPUSetMems:
			if resourceAllocationInfo.AllocationResult != "" {
				containerUpdate.Linux.Resources.Cpu.Mems = resourceAllocationInfo.AllocationResult
			}
		default:

		}
	}
	return containerUpdate
}
