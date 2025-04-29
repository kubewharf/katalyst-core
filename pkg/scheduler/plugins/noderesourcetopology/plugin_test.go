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

package noderesourcetopology

import (
	"fmt"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/kubernetes/pkg/scheduler/apis/config"
	"k8s.io/kubernetes/pkg/scheduler/framework"

	config2 "github.com/kubewharf/katalyst-api/pkg/apis/scheduling/config"
	"github.com/kubewharf/katalyst-api/pkg/consts"
)

func MakeTestTm(args *config2.NodeResourceTopologyArgs, h framework.Handle) (framework.Plugin, error) {
	return New(args, h)
}

func MakeTestArgs(scoringType config.ScoringStrategyType, alignedResource []string, resourcePolicy consts.ResourcePluginPolicyName) *config2.NodeResourceTopologyArgs {
	args := &config2.NodeResourceTopologyArgs{
		ScoringStrategy: &config2.ScoringStrategy{
			Type: scoringType,
			Resources: []config.ResourceSpec{
				{
					Name:   v1.ResourceCPU.String(),
					Weight: 50,
				},
				{
					Name:   v1.ResourceMemory.String(),
					Weight: 50,
				},
				{
					Name:   v1.ResourceStorage.String(),
					Weight: 10,
				},
			},
		},
		AlignedResources:     alignedResource,
		ResourcePluginPolicy: resourcePolicy,
	}

	return args
}

func makePodByResourceList(resources *v1.ResourceList, annotation map[string]string) *v1.Pod {
	return &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			UID:         uuid.NewUUID(),
			Annotations: annotation,
		},
		Spec: v1.PodSpec{
			Containers: []v1.Container{
				{
					Resources: v1.ResourceRequirements{
						Requests: *resources,
						Limits:   *resources,
					},
				},
			},
		},
	}
}

func makePodByResourceLists(resources []v1.ResourceList, annotation map[string]string) *v1.Pod {
	pod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: annotation,
		},
		Spec: v1.PodSpec{
			Containers: []v1.Container{},
		},
	}

	for i, r := range resources {
		pod.Spec.Containers = append(pod.Spec.Containers, v1.Container{
			Name: fmt.Sprintf("container%v", i),
			Resources: v1.ResourceRequirements{
				Requests: r,
				Limits:   r,
			},
		})
	}

	return pod
}
