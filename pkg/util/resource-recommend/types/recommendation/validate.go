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

package recommendation

import (
	"context"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/restmapper"
	"k8s.io/klog/v2"
	k8sclient "sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kubewharf/katalyst-api/pkg/apis/recommendation/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	resourceutils "github.com/kubewharf/katalyst-core/pkg/util/resource-recommend/resource"
	errortypes "github.com/kubewharf/katalyst-core/pkg/util/resource-recommend/types/error"
)

func ValidateAndExtractTargetRef(targetRefReq v1alpha1.CrossVersionObjectReference) (
	v1alpha1.CrossVersionObjectReference, *errortypes.CustomError,
) {
	convertedTargetRef := v1alpha1.CrossVersionObjectReference{}
	if targetRefReq.Name == "" {
		return convertedTargetRef, errortypes.WorkloadNameIsEmptyError()
	}
	if ok := general.SliceContains(TargetRefKinds, targetRefReq.Kind); !ok {
		return convertedTargetRef, errortypes.WorkloadsUnsupportedError(targetRefReq.Kind, TargetRefKinds)
	}
	convertedTargetRef.Kind = targetRefReq.Kind
	convertedTargetRef.Name = targetRefReq.Name
	convertedTargetRef.APIVersion = targetRefReq.APIVersion
	return convertedTargetRef, nil
}

func ValidateAndExtractAlgorithmPolicy(algorithmPolicyReq v1alpha1.AlgorithmPolicy) (
	v1alpha1.AlgorithmPolicy, *errortypes.CustomError,
) {
	algorithmPolicy := v1alpha1.AlgorithmPolicy{
		Recommender: DefaultRecommenderType,
	}

	if algorithmPolicyReq.Algorithm == "" {
		algorithmPolicy.Algorithm = DefaultAlgorithmType
	} else {
		if ok := general.SliceContains(AlgorithmTypes, algorithmPolicyReq.Algorithm); !ok {
			return algorithmPolicy, errortypes.AlgorithmUnsupportedError(string(algorithmPolicyReq.Algorithm), AlgorithmTypes)
		}
		algorithmPolicy.Algorithm = algorithmPolicyReq.Algorithm
	}

	algorithmPolicy.Extensions = algorithmPolicyReq.Extensions
	return algorithmPolicy, nil
}

func ValidateAndExtractContainers(ctx context.Context, client dynamic.Interface, namespace string,
	targetRef v1alpha1.CrossVersionObjectReference,
	containerPolicies []v1alpha1.ContainerResourcePolicy,
	mapper *restmapper.DeferredDiscoveryRESTMapper) (
	[]Container, *errortypes.CustomError,
) {
	if len(containerPolicies) == 0 {
		return nil, errortypes.ContainerPoliciesNotFoundError()
	}

	resource, err := resourceutils.ConvertAndGetResource(ctx, client, namespace, targetRef, mapper)
	if err != nil {
		klog.ErrorS(err, "ConvertAndGetResource err")
		if k8sclient.IgnoreNotFound(err) == nil {
			return nil, errortypes.WorkloadNotFoundError(errortypes.WorkloadNotFoundMessage)
		}
		return nil, errortypes.WorkloadMatchedError(errortypes.WorkloadMatchedErrorMessage)
	}

	existContainerList, err := resourceutils.GetAllClaimedContainers(resource)
	if err != nil {
		klog.ErrorS(err, "get all claimed containers err")
		return nil, errortypes.ContainersMatchedError(errortypes.ContainersMatchedErrorMessage)
	}

	containers, validateErr := validateAndExtractContainers(containerPolicies, existContainerList)
	if validateErr != nil {
		return nil, validateErr
	}

	return containers, nil
}

func validateAndExtractContainers(containerPolicies []v1alpha1.ContainerResourcePolicy,
	existContainerList []string,
) ([]Container, *errortypes.CustomError) {
	resourcePoliciesMap := make(map[string]v1alpha1.ContainerResourcePolicy)

	for _, resourcePolicy := range containerPolicies {
		containerName := resourcePolicy.ContainerName
		if _, ok := resourcePoliciesMap[containerName]; ok {
			return nil, errortypes.ContainerDuplicateError(errortypes.ContainerDuplicateMessage, containerName)
		} else if containerName == "" {
			return nil, errortypes.ContainerNameEmptyError(errortypes.ContainerNameEmptyMessage)
		} else if !(containerName == ContainerPolicySelectAllFlag) && !general.SliceContains(existContainerList, containerName) {
			return nil, errortypes.ContainersNotFoundError(errortypes.ContainerNotFoundMessage, containerName)
		} else if len(resourcePolicy.ControlledResourcesPolicies) == 0 {
			return nil, errortypes.ControlledResourcesPoliciesEmptyError(errortypes.ControlledResourcesPoliciesEmptyMessage, containerName)
		}
		resourcePoliciesMap[containerName] = resourcePolicy
	}

	if defaultPolicy, exist := resourcePoliciesMap[ContainerPolicySelectAllFlag]; exist {
		for _, container := range existContainerList {
			if _, ok := resourcePoliciesMap[container]; !ok {
				defaultPolicy.ContainerName = container
				resourcePoliciesMap[container] = defaultPolicy
			}
		}
		delete(resourcePoliciesMap, "*")
	}

	containers := make([]Container, 0, len(resourcePoliciesMap))
	for _, containerResourcePolicy := range resourcePoliciesMap {
		container := Container{
			ContainerName:    containerResourcePolicy.ContainerName,
			ContainerConfigs: []ContainerConfig{},
		}
		for _, resourcesPolicy := range containerResourcePolicy.ControlledResourcesPolicies {
			if ok := general.SliceContains(ResourceNames, resourcesPolicy.ResourceName); !ok {
				return containers, errortypes.ResourceNameUnsupportedError(errortypes.ResourceNameUnsupportedMessage, ResourceNames)
			}
			if resourcesPolicy.ControlledValues != nil {
				if ok := general.SliceContains(SupportControlledValues, *resourcesPolicy.ControlledValues); !ok {
					return containers, errortypes.ControlledValuesUnsupportedError(errortypes.ResourceNameUnsupportedMessage, SupportControlledValues)
				}
			}

			containerConfig := ContainerConfig{
				ControlledResource: resourcesPolicy.ResourceName,
			}
			resourceBufferPercent := resourcesPolicy.BufferPercent
			if resourceBufferPercent == nil {
				containerConfig.ResourceBufferPercent = DefaultUsageBuffer
			} else if *resourceBufferPercent > MaxUsageBuffer || *resourceBufferPercent < MinUsageBuffer {
				return containers, errortypes.ResourceBuffersUnsupportedError(errortypes.ResourceBuffersUnsupportedMessage)
			} else {
				containerConfig.ResourceBufferPercent = *resourceBufferPercent
			}

			container.ContainerConfigs = append(container.ContainerConfigs, containerConfig)
		}
		containers = append(containers, container)
	}
	return containers, nil
}
