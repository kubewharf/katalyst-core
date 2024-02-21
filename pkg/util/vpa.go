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

	"github.com/pkg/errors"
	core "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/json"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	apis "github.com/kubewharf/katalyst-api/pkg/apis/autoscaling/v1alpha1"
	workload "github.com/kubewharf/katalyst-api/pkg/apis/workload/v1alpha1"
	autoscalelister "github.com/kubewharf/katalyst-api/pkg/client/listers/autoscaling/v1alpha1"
	workloadlister "github.com/kubewharf/katalyst-api/pkg/client/listers/workload/v1alpha1"
	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

// checkTargetRefMatch returns whether the given workload matches the target reference
func checkTargetRefMatch(reference apis.CrossVersionObjectReference, workload *unstructured.Unstructured) bool {
	return reference.Kind == workload.GetKind() && reference.Name == workload.GetName()
}

// generateObjectTargetReferenceKey is to generate a unique key by target reference
func generateObjectTargetReferenceKey(reference apis.CrossVersionObjectReference) string {
	return fmt.Sprintf("%s.%s.%s", reference.APIVersion, reference.Kind, reference.Name)
}

// generateWorkloadReferenceKey is to generate a unique key by workload
func generateWorkloadReferenceKey(workload *unstructured.Unstructured) string {
	return fmt.Sprintf("%s.%s.%s", workload.GetAPIVersion(), workload.GetKind(), workload.GetName())
}

// objectTargetReferenceIndex is used to generate informer index for target reference
func objectTargetReferenceIndex(reference apis.CrossVersionObjectReference) ([]string, error) {
	var keys []string
	key := generateObjectTargetReferenceKey(reference)
	keys = append(keys, key)
	return keys, nil
}

/*
 helper functions to get vpa-related objects with indexed informer
*/

// getVPAForWorkloadWithIndex is used to get vpa that should manage the given workload
func getVPAForWorkloadWithIndex(workload *unstructured.Unstructured, vpaIndexer cache.Indexer) (*apis.KatalystVerticalPodAutoscaler, error) {
	objs, err := vpaIndexer.ByIndex(consts.TargetReferenceIndex, generateWorkloadReferenceKey(workload))
	if err != nil {
		return nil, errors.Wrapf(err, "vpa for workload %s/%s not exist", workload.GetNamespace(), workload.GetName())
	} else if len(objs) > 1 || len(objs) == 0 {
		return nil, fmt.Errorf("vpa for workload %s/%s invalid", workload.GetNamespace(), workload.GetName())
	}

	vpa, ok := objs[0].(*apis.KatalystVerticalPodAutoscaler)
	if !ok {
		return nil, fmt.Errorf("invalid spd")
	}
	return vpa, nil
}

/*
 helper functions to build indexed informer for vpa-related objects
*/

// VPATargetReferenceIndex is used to construct informer index for target reference in VPA
func VPATargetReferenceIndex(obj interface{}) ([]string, error) {
	vpa, ok := obj.(*apis.KatalystVerticalPodAutoscaler)
	if !ok {
		return nil, fmt.Errorf("failed to reflect a obj to vpa")
	}
	return objectTargetReferenceIndex(vpa.Spec.TargetRef)
}

/*
 helper functions to get vpa-related objects
*/

// GetPodListForVPA is used to get pods that should be managed by the given vpa,
// we'll always get through workload
func GetPodListForVPA(vpa *apis.KatalystVerticalPodAutoscaler, podIndexer cache.Indexer, podLabelIndexKeyList []string,
	workloadLister cache.GenericLister, podLister corelisters.PodLister) ([]*core.Pod, error) {
	workloadObj, err := GetWorkloadForVPA(vpa, workloadLister)
	if err != nil {
		return nil, err
	}

	return native.GetPodListForWorkload(workloadObj, podIndexer, podLabelIndexKeyList, podLister)
}

// GetVPAForPod is used to get vpa that should manage the given vpa,
// we'll always get through workload, and we will find it recursively since
// we don't know in which level the owner will be.
func GetVPAForPod(pod *core.Pod, vpaIndexer cache.Indexer, workloadListerMap map[schema.GroupVersionKind]cache.GenericLister,
	vpaLister autoscalelister.KatalystVerticalPodAutoscalerLister) (*apis.KatalystVerticalPodAutoscaler, error) {
	for _, owner := range pod.GetOwnerReferences() {
		gvk := schema.FromAPIVersionAndKind(owner.APIVersion, owner.Kind)
		if _, ok := workloadListerMap[gvk]; ok {
			if workloadObj, err := workloadListerMap[gvk].ByNamespace(pod.GetNamespace()).Get(owner.Name); err == nil {
				var targetVPA *apis.KatalystVerticalPodAutoscaler
				native.VisitUnstructuredAncestors(workloadObj.(*unstructured.Unstructured),
					workloadListerMap, func(owner *unstructured.Unstructured) bool {
						vpa, err := GetVPAForWorkload(owner, vpaIndexer, vpaLister)
						if err != nil {
							return true
						}

						targetVPA = vpa
						return false
					})
				if targetVPA != nil {
					return targetVPA, nil
				}
			}
		}
	}

	return nil, apierrors.NewNotFound(apis.Resource(apis.ResourceNameKatalystVPA), "vpa for pod")
}

// GetWorkloadForVPA is used to get workload that should be managed by the given vpa
func GetWorkloadForVPA(vpa *apis.KatalystVerticalPodAutoscaler, workloadLister cache.GenericLister) (runtime.Object, error) {
	return workloadLister.ByNamespace(vpa.Namespace).Get(vpa.Spec.TargetRef.Name)
}

// GetVPAForWorkload is used to get vpa that should manage the given workload
func GetVPAForWorkload(workload *unstructured.Unstructured, vpaIndexer cache.Indexer,
	vpaLister autoscalelister.KatalystVerticalPodAutoscalerLister) (*apis.KatalystVerticalPodAutoscaler, error) {
	if vpaName, ok := workload.GetAnnotations()[apiconsts.WorkloadAnnotationVPANameKey]; ok {
		vpa, err := vpaLister.KatalystVerticalPodAutoscalers(workload.GetNamespace()).Get(vpaName)
		if err == nil && checkTargetRefMatch(vpa.Spec.TargetRef, workload) {
			return vpa, nil
		}
		return nil, apierrors.NewNotFound(apis.Resource(apis.ResourceNameKatalystVPA), "matched target refer vpa")
	}

	if vpaIndexer != nil {
		if vpa, err := getVPAForWorkloadWithIndex(workload, vpaIndexer); err == nil {
			return vpa, nil
		}
	}

	vpaList, err := vpaLister.List(labels.Everything())
	if err != nil {
		return nil, err
	}

	for _, vpa := range vpaList {
		if checkTargetRefMatch(vpa.Spec.TargetRef, workload) {
			return vpa, nil
		}
	}
	return nil, apierrors.NewNotFound(apis.Resource(apis.ResourceNameKatalystVPA), "vpa for workload")
}

// GetSPDForVPA is used to get spd that matches with the workload belongs to the given vpa
// the preference is indexer --> GetSPDForWorkload
func GetSPDForVPA(vpa *apis.KatalystVerticalPodAutoscaler, spdIndexer cache.Indexer, workloadLister cache.GenericLister,
	spdLister workloadlister.ServiceProfileDescriptorLister) (*workload.ServiceProfileDescriptor, error) {
	workloadObj, err := GetWorkloadForVPA(vpa, workloadLister)
	if err != nil {
		return nil, err
	}

	return GetSPDForWorkload(workloadObj.(*unstructured.Unstructured), spdIndexer, spdLister)
}

/*
 helper functions to generate vpa related fields
*/

// GenerateVPAResourceMap returns the map of pod resources and container resources;
// the returned pod resources map use GeneratePodContainerName as key, and apis.ContainerResources as value
// the returned container resources map use GenerateContainerName as key, and apis.ContainerResources as value
func GenerateVPAResourceMap(vpa *apis.KatalystVerticalPodAutoscaler) (map[consts.PodContainerName]apis.ContainerResources, map[consts.ContainerName]apis.ContainerResources, error) {
	if vpa == nil {
		return make(map[consts.PodContainerName]apis.ContainerResources), make(map[consts.ContainerName]apis.ContainerResources), nil
	}

	podResources := make(map[consts.PodContainerName]apis.ContainerResources)
	if vpa.Status.PodResources != nil {
		for _, podResource := range vpa.Status.PodResources {
			if podResource.PodName == nil {
				err := fmt.Errorf("pod resource's PodName is nil")
				klog.Errorf(err.Error())
				return nil, nil, err
			}

			for _, containerResource := range podResource.ContainerResources {
				if containerResource.ContainerName == nil {
					err := fmt.Errorf("container resource's ContainerName is nil")
					klog.Errorf(err.Error())
					return nil, nil, err
				}

				key := native.GeneratePodContainerName(*podResource.PodName, *containerResource.ContainerName)
				podResources[key] = *containerResource.DeepCopy()
			}
		}
	}

	containerResources := make(map[consts.ContainerName]apis.ContainerResources)
	if vpa.Status.ContainerResources != nil {
		for _, containerResource := range vpa.Status.ContainerResources {
			if containerResource.ContainerName == nil {
				err := fmt.Errorf("container resource's ContainerName is nil")
				klog.Errorf(err.Error())
				return nil, nil, err
			}

			containerResources[native.GenerateContainerName(*containerResource.ContainerName)] = *containerResource.DeepCopy()
		}
	}
	return podResources, containerResources, nil
}

// GenerateVPAPolicyMap returns vpa resource policy at container-level
func GenerateVPAPolicyMap(vpa *apis.KatalystVerticalPodAutoscaler) (map[string]apis.ContainerResourcePolicy, error) {
	if vpa == nil {
		err := fmt.Errorf("vpa is nil")
		klog.Errorf(err.Error())
		return nil, err
	}

	containerPolicies := make(map[string]apis.ContainerResourcePolicy)
	for _, containerPolicy := range vpa.Spec.ResourcePolicy.ContainerPolicies {
		if containerPolicy.ContainerName == nil {
			err := fmt.Errorf("containerPolicy's ContainerName is nil")
			klog.Errorf(err.Error())
			return nil, err
		}

		containerPolicies[*containerPolicy.ContainerName] = containerPolicy
	}
	return containerPolicies, nil
}

// GenerateVPAPodResizeResourceAnnotations returns mapping from containerName to resourceRequirements
// by merging the given podResources and containerResources; and podResources is always superior to containerResources
func GenerateVPAPodResizeResourceAnnotations(pod *core.Pod, podResources map[consts.PodContainerName]apis.ContainerResources,
	containerResources map[consts.ContainerName]apis.ContainerResources) (map[string]core.ResourceRequirements, error) {
	if pod == nil {
		err := fmt.Errorf("a nil pod in pods list")
		klog.Error(err.Error())
		return nil, err
	}

	annotationResources := make(map[string]core.ResourceRequirements)
	oldAnnotation := pod.Annotations[apiconsts.PodAnnotationInplaceUpdateResourcesKey]
	if len(oldAnnotation) > 0 {
		err := json.Unmarshal([]byte(oldAnnotation), &annotationResources)
		if err != nil {
			return nil, err
		}
	}

	for _, container := range pod.Spec.Containers {
		if containerResource, ok := containerResources[native.GenerateContainerName(container.Name)]; ok {
			annotationResources[container.Name] = ConvertVPAContainerResourceToResourceRequirements(containerResource)
		}
		if containerResource, ok := podResources[native.GeneratePodContainerName(pod.Name, container.Name)]; ok {
			annotationResources[container.Name] = ConvertVPAContainerResourceToResourceRequirements(containerResource)
		}
	}

	return annotationResources, nil
}

// GenerateVPAPodResizePolicyAnnotations returns mapping from containerName to policy
func GenerateVPAPodResizePolicyAnnotations(pod *core.Pod, containerPolicies map[string]apis.ContainerResourcePolicy) (map[string]string, error) {
	if pod == nil {
		err := fmt.Errorf("pod is nil")
		klog.Error(err.Error())
		return nil, err
	}

	annotationPolicies := make(map[string]string)

	for _, container := range pod.Spec.Containers {
		if containerPolicy, ok := containerPolicies[container.Name]; ok && containerPolicy.ResourceResizePolicy == apis.ResourceResizePolicyRestart {
			annotationPolicies[container.Name] = apiconsts.PodAnnotationInplaceUpdateResizePolicyRestart
		}
	}

	return annotationPolicies, nil
}

// ConvertVPAContainerResourceToResourceRequirements converts target value in apis.ContainerResources to core.ResourceRequirements
func ConvertVPAContainerResourceToResourceRequirements(containerResource apis.ContainerResources) core.ResourceRequirements {
	result := core.ResourceRequirements{}
	if containerResource.Requests != nil {
		result.Requests = containerResource.Requests.Target
	}
	if containerResource.Limits != nil {
		result.Limits = containerResource.Limits.Target
	}
	return result
}

// ConvertVPAContainerResourceToRecommendedContainerResources converts apis.ContainerResources to apis.RecommendedContainerResources
func ConvertVPAContainerResourceToRecommendedContainerResources(resource apis.ContainerResources) apis.RecommendedContainerResources {
	recommendation := apis.RecommendedContainerResources{ContainerName: resource.ContainerName}
	if resource.Requests != nil {
		recommendation.Requests = &apis.RecommendedRequestResources{
			Resources: resource.Requests.Target,
		}
	}
	if resource.Limits != nil {
		recommendation.Limits = &apis.RecommendedRequestResources{
			Resources: resource.Limits.Target,
		}
	}
	return recommendation
}

/*
 helper functions to do validation works
*/

// CheckWorkloadEnableVPA is used to check whether the object enables VPA functionality
func CheckWorkloadEnableVPA(runtimeObject runtime.Object) bool {
	object, err := meta.Accessor(runtimeObject)
	if err != nil {
		klog.Errorf("convert runtimeObject failed, err %v", err)
		return false
	}

	return object.GetAnnotations()[apiconsts.WorkloadAnnotationVPAEnabledKey] == apiconsts.WorkloadAnnotationVPAEnabled
}

func CheckVPARecommendationMatchVPA(vpaRec *apis.VerticalPodAutoscalerRecommendation, vpa *apis.KatalystVerticalPodAutoscaler) bool {
	for _, ow := range vpaRec.OwnerReferences {
		if ow.UID == vpa.UID {
			return true
		}
	}
	return false
}

// CheckVPAStatusLegal checks following things and return a msg
// 1. recommended resource mustn't change pod qos class
// 2. recommended resource mustn't scale up in VPA 1.0 todo: remove this in VPA 2.0
// 3. recommended resource mustn't be negative
// if vpa status is illegal, we'll refuse update its status
func CheckVPAStatusLegal(vpa *apis.KatalystVerticalPodAutoscaler, pods []*core.Pod) (bool, string, error) {
	if vpa == nil {
		return false, "", fmt.Errorf("vpa is nil")
	}

	podResources, containerResources, err := GenerateVPAResourceMap(vpa)
	if err != nil {
		klog.Errorf("failed to get resource from VPA %s", vpa.Name)
		return false, "", err
	}

	for _, pod := range pods {
		if pod == nil {
			return false, "", fmt.Errorf("pod is nil")
		}
		podResource, err := GenerateVPAPodResizeResourceAnnotations(pod, podResources, containerResources)
		if err != nil {
			return false, "", err
		}

		qosChange, err := native.CheckQosClassChanged(podResource, pod)
		if err != nil {
			return false, "", err
		}
		if qosChange {
			return false, "qos changed", nil
		}

		podCopy := &core.Pod{}
		podCopy.Spec.Containers = native.DeepCopyPodContainers(pod)
		native.ApplyPodResources(podResource, podCopy)
		zeroQuantity := resource.MustParse("0")
		for _, container := range podCopy.Spec.Containers {
			resourceNames := []core.ResourceName{core.ResourceCPU, core.ResourceMemory}
			for _, resourceName := range resourceNames {
				// check if resource are not negative
				limit := container.Resources.Limits[resourceName]
				request := container.Resources.Requests[resourceName]
				if native.IsResourceGreaterThan(zeroQuantity, request) {
					return false, fmt.Sprintf("resource %s request value is negative", resourceName), nil
				} else if native.IsResourceGreaterThan(zeroQuantity, limit) {
					return false, fmt.Sprintf("resource %s limit value is negative", resourceName), nil
				}
				if !limit.IsZero() && native.IsResourceGreaterThan(request, limit) {
					return false, fmt.Sprintf("resource %s request greater than limit", resourceName), nil
				}
			}
		}
	}
	return true, "", nil
}

// CheckPodSpecUpdated checks pod spec whether is updated to expected resize resource in annotation
func CheckPodSpecUpdated(pod *core.Pod) bool {
	podCopy := &core.Pod{}
	podCopy.Spec.Containers = native.DeepCopyPodContainers(pod)
	annotationResource, err := GenerateVPAPodResizeResourceAnnotations(pod, nil, nil)
	if err != nil {
		klog.Errorf("failed to exact pod %v resize resource annotation from container resource: %v", pod.Name, err)
		return false
	}

	native.ApplyPodResources(annotationResource, podCopy)
	return apiequality.Semantic.DeepEqual(pod.Spec.Containers, podCopy.Spec.Containers)
}
