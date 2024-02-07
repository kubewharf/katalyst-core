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

package topology

import (
	v1 "k8s.io/api/core/v1"
	podresv1 "k8s.io/kubelet/pkg/apis/podresources/v1"

	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/util/qos"
)

// GenericPodResourcesFilter is used to filter pod resources by qos conf
func GenericPodResourcesFilter(qosConf *generic.QoSConfiguration) PodResourcesFilter {
	return func(pod *v1.Pod, podResources *podresv1.PodResources) (*podresv1.PodResources, error) {
		if podResources == nil || pod == nil {
			return nil, nil
		}

		podIsNumaBinding := qos.IsPodNumaBinding(qosConf, pod)
		for _, container := range podResources.Containers {
			newResources := make([]*podresv1.TopologyAwareResource, 0, len(container.Resources))
			for _, resource := range container.Resources {
				if resource == nil {
					continue
				}

				// skip cpu and memory if pod is not numa binding
				if !podIsNumaBinding &&
					(resource.ResourceName == string(v1.ResourceCPU) || resource.ResourceName == string(v1.ResourceMemory)) {
					continue
				}

				newResources = append(newResources, resource)
			}

			container.Resources = newResources
		}

		return podResources, nil
	}
}
