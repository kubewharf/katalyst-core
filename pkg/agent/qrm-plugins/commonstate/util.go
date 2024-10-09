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

package commonstate

import (
	"encoding/json"
	"io/ioutil"

	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

// CheckNUMABindingSharedCoresAntiAffinity returns true
// if the AllocationMeta isn't compatible for the annotations of a numa binding shared cores candidate
func CheckNUMABindingSharedCoresAntiAffinity(meta *AllocationMeta, annotations map[string]string) bool {
	if meta == nil {
		return false
	} else if len(annotations) == 0 {
		return false
	}

	if meta.CheckDedicatedNUMABinding() {
		return true
	}

	if meta.CheckSharedNUMABinding() {
		// considering isolation, use specified pool instead of actual pool name here
		candidateSpecifiedPoolName := GetSpecifiedPoolName(consts.PodAnnotationQoSLevelSharedCores,
			annotations[consts.PodAnnotationCPUEnhancementCPUSet])
		aiSpecifiedPoolName := meta.GetSpecifiedPoolName()

		// shared_cores with numa binding doesn't support two share type pools with same specified name existing at same NUMA
		if candidateSpecifiedPoolName != aiSpecifiedPoolName {
			return true
		}
	}

	return false
}

// GenerateGenericContainerAllocationMeta generates a generic container's allocation metadata.
// This function populates the AllocationMeta struct using data from the resource request and other parameters.
// Parameters:
// - req: The resource request containing information about the pod, container, and other attributes.
// - ownerPoolName: The name of the pool owning this container.
// - qosLevel: The QoS (Quality of Service) level for the container.
// Returns:
// - A pointer to an AllocationMeta struct filled with relevant data from the request and other inputs.
func GenerateGenericContainerAllocationMeta(req *pluginapi.ResourceRequest, ownerPoolName, qosLevel string) AllocationMeta {
	return AllocationMeta{
		PodUid:         req.PodUid,
		PodNamespace:   req.PodNamespace,
		PodName:        req.PodName,
		ContainerName:  req.ContainerName,
		ContainerType:  req.ContainerType.String(),
		ContainerIndex: req.ContainerIndex,
		OwnerPoolName:  ownerPoolName,
		PodRole:        req.PodRole,
		PodType:        req.PodType,
		Labels:         general.DeepCopyMap(req.Labels),
		Annotations:    general.DeepCopyMap(req.Annotations),
		QoSLevel:       qosLevel,
	}
}

// GenerateGenericPoolAllocationMeta generates a generic allocation metadata for a pool.
// This function creates an AllocationMeta where both PodUid and OwnerPoolName are set to the given pool name.
// Parameters:
// - poolName: The name of the pool for which the metadata is generated.
// Returns:
// - A pointer to an AllocationMeta struct with the pool name set for both PodUid and OwnerPoolName.
func GenerateGenericPoolAllocationMeta(poolName string) AllocationMeta {
	return AllocationMeta{
		PodUid:        poolName, // The unique identifier for the pool (reusing poolName).
		OwnerPoolName: poolName, // The name of the pool itself.
	}
}

func LoadExtraControlKnobConfigs(extraControlKnobConfigAbsPath string) (ExtraControlKnobConfigs, error) {
	configBytes, err := ioutil.ReadFile(extraControlKnobConfigAbsPath)
	if err != nil {
		return nil, err
	}

	extraControlKnobConfigs := make(ExtraControlKnobConfigs)

	err = json.Unmarshal(configBytes, &extraControlKnobConfigs)
	if err != nil {
		return nil, err
	}

	return extraControlKnobConfigs, nil
}
