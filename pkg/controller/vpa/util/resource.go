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
	"sort"

	core "k8s.io/api/core/v1"
	"k8s.io/klog/v2"

	apis "github.com/kubewharf/katalyst-api/pkg/apis/autoscaling/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	katalystutil "github.com/kubewharf/katalyst-core/pkg/util"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

/*
 helper functions to sort slice-organized resources
*/

func sortPodResources(podResources []apis.PodResources) {
	sort.SliceStable(podResources, func(i, j int) bool {
		if podResources[i].PodName == nil {
			return true
		}
		return *podResources[i].PodName > *podResources[j].PodName
	})

	for _, pr := range podResources {
		sortContainerResources(pr.ContainerResources)
	}
}

func sortContainerResources(containerResources []apis.ContainerResources) {
	sort.SliceStable(containerResources, func(i, j int) bool {
		if containerResources[i].ContainerName == nil {
			return true
		}
		return *containerResources[i].ContainerName > *containerResources[j].ContainerName
	})
}

func sortRecommendedPodResources(recommendedPodResources []apis.RecommendedPodResources) {
	sort.SliceStable(recommendedPodResources, func(i, j int) bool {
		if recommendedPodResources[i].PodName == nil {
			return true
		}
		return *recommendedPodResources[i].PodName > *recommendedPodResources[j].PodName
	})

	for _, pr := range recommendedPodResources {
		sortRecommendedContainerResources(pr.ContainerRecommendations)
	}
}

func sortRecommendedContainerResources(recommendedContainerResources []apis.RecommendedContainerResources) {
	sort.SliceStable(recommendedContainerResources, func(i, j int) bool {
		if recommendedContainerResources[i].ContainerName == nil {
			return true
		}
		return *recommendedContainerResources[i].ContainerName > *recommendedContainerResources[j].ContainerName
	})
}

/*
 helper functions to generate status for vpa
*/

// mergeResourceAndRecommendation merge RecommendedContainerResources into ContainerResources,
// it works both for requests and limits
func mergeResourceAndRecommendation(containerResource apis.ContainerResources,
	containerRecommendation apis.RecommendedContainerResources,
) apis.ContainerResources {
	mergeFn := func(resource *apis.ContainerResourceList, recommendation *apis.RecommendedRequestResources) *apis.ContainerResourceList {
		if recommendation == nil {
			return resource
		}

		if resource == nil {
			return &apis.ContainerResourceList{
				UncappedTarget: recommendation.Resources,
				Target:         recommendation.Resources,
			}
		}

		resourceCopy := resource.DeepCopy()
		resourceCopy.UncappedTarget = recommendation.Resources
		resourceCopy.Target = recommendation.Resources
		return resourceCopy
	}

	containerResource.Requests = mergeFn(containerResource.Requests, containerRecommendation.Requests)
	containerResource.Limits = mergeFn(containerResource.Limits, containerRecommendation.Limits)
	return containerResource
}

// mergeResourceAndCurrent merge pod current resources into ContainerResources,
// it works both for requests and limits
func mergeResourceAndCurrent(containerResource apis.ContainerResources,
	containerCurrent apis.ContainerResources,
) apis.ContainerResources {
	mergeFn := func(resource *apis.ContainerResourceList, current *apis.ContainerResourceList) *apis.ContainerResourceList {
		if current == nil {
			return resource
		}

		if resource == nil {
			return nil
		}

		resourceCopy := resource.DeepCopy()
		resourceCopy.Current = current.Current
		return resourceCopy
	}

	containerResource.Requests = mergeFn(containerResource.Requests, containerCurrent.Requests)
	containerResource.Limits = mergeFn(containerResource.Limits, containerCurrent.Limits)
	return containerResource
}

// cropResources change containerResources with containerPolicies
func cropResources(podResources map[consts.PodContainerName]apis.ContainerResources,
	containerResources map[consts.ContainerName]apis.ContainerResources, containerPolicies map[string]apis.ContainerResourcePolicy,
) error {
	for key, containerResource := range podResources {
		_, containerName, err := native.ParsePodContainerName(key)
		if err != nil {
			return err
		}

		if policy, ok := containerPolicies[containerName]; ok {
			podResources[key] = cropResourcesWithPolicies(containerResource, policy)
		}
	}

	for key, containerResource := range containerResources {
		containerName := native.ParseContainerName(key)
		if policy, ok := containerPolicies[containerName]; ok {
			containerResources[key] = cropResourcesWithPolicies(containerResource, policy)
		}
	}
	return nil
}

// cropResourcesWithPolicies check policy.ControllerValues and crop final recommendation to obey resource policy
func cropResourcesWithPolicies(resource apis.ContainerResources, policy apis.ContainerResourcePolicy) apis.ContainerResources {
	cropRequests := func() {
		if resource.Requests != nil {
			resource.Requests.LowerBound = policy.MinAllowed.DeepCopy()
			resource.Requests.UpperBound = policy.MaxAllowed.DeepCopy()
			resource.Requests.Target = cropResourcesWithBounds(resource.Requests.UncappedTarget,
				policy.MinAllowed, policy.MaxAllowed, policy.ControlledResources)
		}
	}

	cropLimits := func() {
		if resource.Limits != nil {
			resource.Limits.LowerBound = policy.MinAllowed.DeepCopy()
			resource.Limits.UpperBound = policy.MaxAllowed.DeepCopy()
			resource.Limits.Target = cropResourcesWithBounds(resource.Limits.UncappedTarget,
				policy.MinAllowed, policy.MaxAllowed, policy.ControlledResources)
		}
	}

	resource = *resource.DeepCopy()
	switch policy.ControlledValues {
	case apis.ContainerControlledValuesRequestsOnly:
		cropRequests()
		resource.Limits = nil
	case apis.ContainerControlledValuesLimitsOnly:
		cropLimits()
		resource.Requests = nil
	case apis.ContainerControlledValuesRequestsAndLimits:
		cropRequests()
		cropLimits()
	}
	return resource
}

// cropResourcesWithBounds limit uncappedValue between lowBound and upBound and
// filter out resources which aren't in controlledResource
func cropResourcesWithBounds(uncappedValue core.ResourceList, lowBound core.ResourceList, upBound core.ResourceList,
	controlledResource []core.ResourceName,
) core.ResourceList {
	finalValue := make(core.ResourceList)

	for _, resourceName := range controlledResource {
		resourceValue, ok := uncappedValue[resourceName]
		if !ok {
			continue
		}

		finalValue[resourceName] = resourceValue

		if minValue, ok := lowBound[resourceName]; ok {
			if resourceValue.Cmp(minValue) < 0 {
				finalValue[resourceName] = minValue.DeepCopy()
			}
		}

		if maxValue, ok := upBound[resourceName]; ok {
			if resourceValue.Cmp(maxValue) > 0 {
				finalValue[resourceName] = maxValue.DeepCopy()
			}
		}
	}

	return finalValue
}

// GetVPAResourceStatusWithRecommendation updates resource recommendation results from vpaRec to vpa
func GetVPAResourceStatusWithRecommendation(vpa *apis.KatalystVerticalPodAutoscaler, recPodResources []apis.RecommendedPodResources,
	recContainerResources []apis.RecommendedContainerResources,
) ([]apis.PodResources, []apis.ContainerResources, error) {
	containerPolicies, err := katalystutil.GenerateVPAPolicyMap(vpa)
	if err != nil {
		return nil, nil, err
	}

	vpaPodResources, vpaContainerResources, err := katalystutil.GenerateVPAResourceMap(vpa)
	if err != nil {
		return nil, nil, err
	}

	podResources := make(map[consts.PodContainerName]apis.ContainerResources)
	containerResources := make(map[consts.ContainerName]apis.ContainerResources)

	for _, podRec := range recPodResources {
		if podRec.PodName == nil {
			klog.Errorf("recommended pod resource's podName can't be nil")
			continue
		}

		for _, containerRec := range podRec.ContainerRecommendations {
			if containerRec.ContainerName == nil {
				klog.Errorf("recommended pod resource's containerName can't be nil")
				continue
			}

			key := native.GeneratePodContainerName(*podRec.PodName, *containerRec.ContainerName)
			if _, ok := podResources[key]; ok {
				klog.Errorf("recommended pod %s already exists", key)
				continue
			}

			podResources[key] = mergeResourceAndRecommendation(apis.ContainerResources{
				ContainerName: containerRec.ContainerName,
			}, containerRec)

			// if vpa status already had current pod resources, then merge it
			if res, ok := vpaPodResources[key]; ok {
				podResources[key] = mergeResourceAndCurrent(podResources[key], res)
			}
		}
	}

	for _, containerRec := range recContainerResources {
		if containerRec.ContainerName == nil {
			klog.Errorf("recommended container resource's containerName can't be nil")
			continue
		}

		key := native.GenerateContainerName(*containerRec.ContainerName)
		if _, ok := containerResources[key]; ok {
			klog.Errorf("recommended container %s already exists", key)
			continue
		}

		containerResources[key] = mergeResourceAndRecommendation(apis.ContainerResources{
			ContainerName: containerRec.ContainerName,
		}, containerRec)

		// if vpa status already had current container resources, then merge it
		if res, ok := vpaContainerResources[key]; ok {
			containerResources[key] = mergeResourceAndCurrent(containerResources[key], res)
		}
	}

	// crop resources limit resource recommendation with boundaries
	if err := cropResources(podResources, containerResources, containerPolicies); err != nil {
		return nil, nil, fmt.Errorf("failed to set container resource by policies: %v", err)
	}

	podResourcesMap := make(map[string]*apis.PodResources)
	for key, containerResource := range podResources {
		podName, _, err := native.ParsePodContainerName(key)
		if err != nil {
			return nil, nil, err
		}

		if _, ok := podResourcesMap[podName]; !ok {
			podResourcesMap[podName] = &apis.PodResources{
				PodName:            &podName,
				ContainerResources: make([]apis.ContainerResources, 0),
			}
		}
		podResourcesMap[podName].ContainerResources = append(podResourcesMap[podName].ContainerResources, containerResource)
	}

	var (
		finalPodResources       = make([]apis.PodResources, 0, len(podResourcesMap))
		finalContainerResources = make([]apis.ContainerResources, 0, len(containerResources))
	)
	for _, podResource := range podResourcesMap {
		finalPodResources = append(finalPodResources, *podResource)
	}
	for _, containerResource := range containerResources {
		finalContainerResources = append(finalContainerResources, containerResource)
	}

	// sort both finalPodResources and finalContainerResources to make sure result stable
	sortPodResources(finalPodResources)
	sortContainerResources(finalContainerResources)

	return finalPodResources, finalContainerResources, nil
}

// GetVPAResourceStatusWithCurrent updates pod current resource results from vpaRec to vpa
func GetVPAResourceStatusWithCurrent(vpa *apis.KatalystVerticalPodAutoscaler, pods []*core.Pod) ([]apis.PodResources, []apis.ContainerResources, error) {
	podResources, containerResources, err := katalystutil.GenerateVPAResourceMap(vpa)
	if err != nil {
		return nil, nil, err
	}

	updateContainerResourcesCurrent := func(targetResourceNames map[consts.ContainerName][]core.ResourceName,
		containerName consts.ContainerName,
		target core.ResourceList, current *core.ResourceList,
		specResource core.ResourceList,
	) {
		// if pod apply strategy is 'Pod', we not need update container current,
		// because each pod has different recommendation.
		if vpa.Spec.UpdatePolicy.PodApplyStrategy == apis.PodApplyStrategyStrategyPod {
			*current = nil
			return
		}

		_, ok := targetResourceNames[containerName]
		if !ok {
			*current = target.DeepCopy()
			resourceNames := make([]core.ResourceName, 0, len(target))
			for resourceName := range target {
				resourceNames = append(resourceNames, resourceName)
			}
			sort.SliceStable(resourceNames, func(i, j int) bool {
				return string(resourceNames[i]) < string(resourceNames[j])
			})
			targetResourceNames[containerName] = resourceNames
		}

		specCurrent := cropResourcesWithBounds(specResource, nil, nil, targetResourceNames[containerName])
		if !native.ResourcesEqual(target, specCurrent) &&
			(native.ResourcesEqual(target, *current) ||
				!native.ResourcesLess(*current, specCurrent, targetResourceNames[containerName])) {
			*current = specCurrent
		}
	}

	for containerName := range containerResources {
		// get container resource current requests
		if containerResources[containerName].Requests != nil {
			targetResourceNames := make(map[consts.ContainerName][]core.ResourceName)
			func(pods []*core.Pod) {
				for _, pod := range pods {
					for _, container := range pod.Spec.Containers {
						if container.Name != string(containerName) {
							continue
						}

						updateContainerResourcesCurrent(targetResourceNames, containerName, containerResources[containerName].Requests.Target,
							&containerResources[containerName].Requests.Current, container.Resources.Requests)
					}
				}
			}(pods)
		}

		// get container resource current limits
		if containerResources[containerName].Limits != nil {
			targetResourceNames := make(map[consts.ContainerName][]core.ResourceName)
			func(pods []*core.Pod) {
				for _, pod := range pods {
					for _, container := range pod.Spec.Containers {
						if container.Name != string(containerName) {
							continue
						}

						updateContainerResourcesCurrent(targetResourceNames, containerName, containerResources[containerName].Limits.Target,
							&containerResources[containerName].Limits.Current, container.Resources.Limits)
					}
				}
			}(pods)
		}
	}

	getPodContainerResourcesCurrent := func(target core.ResourceList, specResource core.ResourceList) core.ResourceList {
		resourceNames := make([]core.ResourceName, 0, len(target))
		for resourceName := range target {
			resourceNames = append(resourceNames, resourceName)
		}

		return cropResourcesWithBounds(specResource, nil, nil, resourceNames)
	}

	for podContainerName := range podResources {
		podName, containerName, err := native.ParsePodContainerName(podContainerName)
		if err != nil {
			return nil, nil, err
		}

		// find pod matched pod & container current requests
		if podResources[podContainerName].Requests != nil {
			func(pods []*core.Pod) {
				for _, pod := range pods {
					if pod.Name != podName {
						continue
					}

					for _, container := range pod.Spec.Containers {
						if container.Name != containerName {
							continue
						}

						podResources[podContainerName].Requests.Current = getPodContainerResourcesCurrent(podResources[podContainerName].Requests.Target, container.Resources.Requests)
						return
					}
				}
			}(pods)
		}

		// find pod matched pod & container current limits
		if podResources[podContainerName].Limits != nil {
			func(pods []*core.Pod) {
				for _, pod := range pods {
					if pod.Name != podName {
						continue
					}

					for _, container := range pod.Spec.Containers {
						if container.Name != containerName {
							continue
						}

						podResources[podContainerName].Limits.Current = getPodContainerResourcesCurrent(podResources[podContainerName].Limits.Target, container.Resources.Limits)
						return
					}
				}
			}(pods)
		}
	}

	podResourcesMap := make(map[string]*apis.PodResources)
	for key, containerResource := range podResources {
		podName, _, err := native.ParsePodContainerName(key)
		if err != nil {
			return nil, nil, err
		}

		if _, ok := podResourcesMap[podName]; !ok {
			podResourcesMap[podName] = &apis.PodResources{
				PodName:            &podName,
				ContainerResources: make([]apis.ContainerResources, 0),
			}
		}
		podResourcesMap[podName].ContainerResources = append(podResourcesMap[podName].ContainerResources, containerResource)
	}

	var (
		finalPodResources       = make([]apis.PodResources, 0, len(podResourcesMap))
		finalContainerResources = make([]apis.ContainerResources, 0, len(containerResources))
	)
	for _, podResource := range podResourcesMap {
		finalPodResources = append(finalPodResources, *podResource)
	}
	for _, containerResource := range containerResources {
		finalContainerResources = append(finalContainerResources, containerResource)
	}

	// sort both finalPodResources and finalContainerResources to make sure result stable
	sortPodResources(finalPodResources)
	sortContainerResources(finalContainerResources)

	return finalPodResources, finalContainerResources, nil
}

/*
 helper functions to generate status for vpaRec
*/

// GetVPARecResourceStatus updates resource recommendation results from vpa status to vpaRec status
func GetVPARecResourceStatus(vpaPodResources []apis.PodResources, vpaContainerResources []apis.ContainerResources) (
	[]apis.RecommendedPodResources, []apis.RecommendedContainerResources, error,
) {
	recPodResources := make(map[consts.PodContainerName]apis.RecommendedContainerResources)
	for _, podResource := range vpaPodResources {
		if podResource.PodName == nil {
			continue
		}

		for _, containerResource := range podResource.ContainerResources {
			if containerResource.ContainerName == nil {
				klog.Errorf("vpa pod resource's podName can't be nil")
				continue
			}

			key := native.GeneratePodContainerName(*podResource.PodName, *containerResource.ContainerName)
			if _, ok := recPodResources[key]; ok {
				klog.Errorf("vpa pod %s already exists", key)
				continue
			}
			recPodResources[key] = katalystutil.ConvertVPAContainerResourceToRecommendedContainerResources(containerResource)
		}
	}

	recContainerResources := make(map[consts.ContainerName]apis.RecommendedContainerResources)
	for _, containerResource := range vpaContainerResources {
		if containerResource.ContainerName == nil {
			continue
		}

		key := native.GenerateContainerName(*containerResource.ContainerName)
		if _, ok := recContainerResources[key]; ok {
			klog.Errorf("vpa container %s already exists", key)
			continue
		}
		recContainerResources[key] = katalystutil.ConvertVPAContainerResourceToRecommendedContainerResources(containerResource)
	}

	podResourcesMap := make(map[string]*apis.RecommendedPodResources)
	for key, containerResource := range recPodResources {
		podName, _, err := native.ParsePodContainerName(key)
		if err != nil {
			return nil, nil, err
		}

		if _, ok := podResourcesMap[podName]; !ok {
			podResourcesMap[podName] = &apis.RecommendedPodResources{
				PodName:                  &podName,
				ContainerRecommendations: make([]apis.RecommendedContainerResources, 0),
			}
		}
		podResourcesMap[podName].ContainerRecommendations = append(podResourcesMap[podName].ContainerRecommendations, containerResource)
	}

	var (
		finalPodResources       = make([]apis.RecommendedPodResources, 0, len(podResourcesMap))
		finalContainerResources = make([]apis.RecommendedContainerResources, 0, len(recContainerResources))
	)
	for _, podResource := range podResourcesMap {
		finalPodResources = append(finalPodResources, *podResource)
	}
	for _, containerResource := range recContainerResources {
		finalContainerResources = append(finalContainerResources, containerResource)
	}

	// sort both finalPodResources and finalContainerResources to make sure result stable
	sortRecommendedPodResources(finalPodResources)
	sortRecommendedContainerResources(finalContainerResources)

	return finalPodResources, finalContainerResources, nil
}
