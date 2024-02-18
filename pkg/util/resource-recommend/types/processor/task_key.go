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

package processor

import (
	"fmt"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/kubewharf/katalyst-api/pkg/apis/recommendation/v1alpha1"
	datasourcetypes "github.com/kubewharf/katalyst-core/pkg/util/resource-recommend/types/datasource"
)

type TaskConfigStr string

type TaskID string

type ProcessKey struct {
	ResourceRecommendNamespacedName types.NamespacedName
	*datasourcetypes.Metric
}

type ProcessConfig struct {
	ProcessKey
	Config TaskConfigStr
}

func NewProcessConfig(NamespacedName types.NamespacedName, targetRef v1alpha1.CrossVersionObjectReference, containerName string, controlledResource v1.ResourceName, taskConfig TaskConfigStr) *ProcessConfig {
	return &ProcessConfig{
		ProcessKey: GetProcessKey(NamespacedName, targetRef, containerName, controlledResource),
		Config:     taskConfig,
	}
}

func GetProcessKey(NamespacedName types.NamespacedName, targetRef v1alpha1.CrossVersionObjectReference, containerName string, controlledResource v1.ResourceName) ProcessKey {
	return ProcessKey{
		ResourceRecommendNamespacedName: NamespacedName,
		Metric: &datasourcetypes.Metric{
			Namespace:     NamespacedName.Namespace,
			Kind:          targetRef.Kind,
			APIVersion:    targetRef.APIVersion,
			WorkloadName:  targetRef.Name,
			ContainerName: containerName,
			Resource:      controlledResource,
		},
	}
}

func (pc *ProcessConfig) Validate() error {
	if pc.Metric == nil {
		return fmt.Errorf("metric is empty")
	}
	if pc.ContainerName == "" {
		return fmt.Errorf("containerName is empty")
	}

	if pc.Kind == "" {
		return fmt.Errorf("kind is empty")
	}

	if pc.WorkloadName == "" {
		return fmt.Errorf("workloadName is empty")
	}
	if !(pc.Resource == v1.ResourceCPU || pc.Resource == v1.ResourceMemory) {
		return fmt.Errorf("controlledResource only can be [%s, %s]", v1.ResourceCPU, v1.ResourceMemory)
	}
	return nil
}

func (pc *ProcessConfig) GenerateTaskID() TaskID {
	return TaskID(pc.ResourceRecommendNamespacedName.String() + "-" +
		pc.Kind + "-" +
		pc.APIVersion + "-" +
		pc.WorkloadName + "-" +
		pc.ContainerName + "-" +
		string(pc.Resource) + "-" +
		string(pc.Config))
}
