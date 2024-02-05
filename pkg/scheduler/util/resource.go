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

package util

import (
	"fmt"

	v1 "k8s.io/api/core/v1"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"
	"k8s.io/kubernetes/pkg/scheduler/framework"
	"k8s.io/kubernetes/pkg/scheduler/util"

	"github.com/kubewharf/katalyst-core/pkg/consts"
)

func IsRequestFullCPU(pod *v1.Pod) bool {
	for _, initContainer := range pod.Spec.InitContainers {
		for resName, resInfo := range initContainer.Resources.Requests {
			if resName == v1.ResourceCPU {
				if resInfo.MilliValue()%1000 != 0 {
					return false
				}
			}
		}
	}
	for _, container := range pod.Spec.Containers {
		for resName, resInfo := range container.Resources.Requests {
			if resName == v1.ResourceCPU {
				if resInfo.MilliValue()%1000 != 0 {
					return false
				}
			}
		}
	}
	return true
}

func GetPodEffectiveRequest(pod *v1.Pod) v1.ResourceList {
	resources := make(v1.ResourceList)

	for _, container := range pod.Spec.Containers {
		for name, quantity := range container.Resources.Requests {
			if q, ok := resources[name]; ok {
				quantity.Add(q)
			}
			resources[name] = quantity
		}
	}
	for _, container := range pod.Spec.InitContainers {
		for name, quantity := range container.Resources.Requests {
			if q, ok := resources[name]; ok && quantity.Cmp(q) <= 0 {
				continue
			}
			resources[name] = quantity
		}
	}

	return resources
}

func CalculateEffectiveResource(pod *v1.Pod) (res framework.Resource, non0CPU int64, non0Mem int64) {
	resPtr := &res
	for _, c := range pod.Spec.Containers {
		resPtr.Add(c.Resources.Requests)
		non0CPUReq, non0MemReq := util.GetNonzeroRequests(&c.Resources.Requests)
		non0CPU += non0CPUReq
		non0Mem += non0MemReq
	}

	for _, ic := range pod.Spec.InitContainers {
		resPtr.SetMaxResource(ic.Resources.Requests)
		non0CPUReq, non0MemReq := util.GetNonzeroRequests(&ic.Resources.Requests)
		if non0CPU < non0CPUReq {
			non0CPU = non0CPUReq
		}
		if non0Mem < non0MemReq {
			non0Mem = non0MemReq
		}
	}
	return
}

func GetContainerTypeAndIndex(pod *v1.Pod, container *v1.Container) (containerType pluginapi.ContainerType, containerIndex uint64, err error) {
	if pod == nil || container == nil {
		err = fmt.Errorf("got nil pod: %v or container: %v", pod, container)
		return
	}

	foundContainer := false

	for i, initContainer := range pod.Spec.InitContainers {
		if container.Name == initContainer.Name {
			foundContainer = true
			containerType = pluginapi.ContainerType_INIT
			containerIndex = uint64(i)
			break
		}
	}

	if !foundContainer {
		mainContainerName := pod.Annotations[consts.MainContainerNameAnnotationKey]

		if mainContainerName == "" && len(pod.Spec.Containers) > 0 {
			mainContainerName = pod.Spec.Containers[0].Name
		}

		for i, appContainer := range pod.Spec.Containers {
			if container.Name == appContainer.Name {
				foundContainer = true

				if container.Name == mainContainerName {
					containerType = pluginapi.ContainerType_MAIN
				} else {
					containerType = pluginapi.ContainerType_SIDECAR
				}

				containerIndex = uint64(i)
				break
			}
		}
	}

	if !foundContainer {
		err = fmt.Errorf("GetContainerTypeAndIndex doesn't find container: %s in pod: %s/%s", container.Name, pod.Namespace, pod.Name)
	}

	return
}
