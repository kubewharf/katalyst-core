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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	apis "github.com/kubewharf/katalyst-api/pkg/apis/autoscaling/v1alpha1"
	apiworkload "github.com/kubewharf/katalyst-api/pkg/apis/workload/v1alpha1"
	workloadlister "github.com/kubewharf/katalyst-api/pkg/client/listers/workload/v1alpha1"
	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

/*
 helper functions to get spd-related objects with indexed informer
*/

// GetSPDForWorkload is used to get spd that should manage the given workload
func getSPDFroWorkloadWithIndex(workload *unstructured.Unstructured, spdIndexer cache.Indexer) (*apiworkload.ServiceProfileDescriptor, error) {
	objs, err := spdIndexer.ByIndex(consts.TargetReferenceIndex, generateWorkloadReferenceKey(workload))
	if err != nil {
		return nil, errors.Wrapf(err, "spd for workload %s/%s not exist", workload.GetNamespace(), workload.GetName())
	} else if len(objs) > 1 || len(objs) == 0 {
		return nil, fmt.Errorf("spd for workload %s/%s invalid", workload.GetNamespace(), workload.GetName())
	}

	spd, ok := objs[0].(*apiworkload.ServiceProfileDescriptor)
	if !ok {
		return nil, fmt.Errorf("invalid spd")
	}
	return spd, nil
}

/*
 helper functions to build indexed informer for spd-related objects
*/

// SPDTargetReferenceIndex is used to construct informer index for target reference in SPD
func SPDTargetReferenceIndex(obj interface{}) ([]string, error) {
	spd, ok := obj.(*apiworkload.ServiceProfileDescriptor)
	if !ok || spd == nil {
		return nil, fmt.Errorf("failed to reflect a obj to spd")
	}
	return objectTargetReferenceIndex(spd.Spec.TargetRef)
}

/*
 helper functions to get spd-related objects
*/

// GetWorkloadForSPD is used to get workload that should be managed the given spd
func GetWorkloadForSPD(spd *apiworkload.ServiceProfileDescriptor, lister cache.GenericLister) (runtime.Object, error) {
	return lister.ByNamespace(spd.Namespace).Get(spd.Spec.TargetRef.Name)
}

// GetSPDForWorkload is used to get spd that should manage the given workload
// the preference is annotation ---> indexer --> lister
func GetSPDForWorkload(workload *unstructured.Unstructured, spdIndexer cache.Indexer,
	spdLister workloadlister.ServiceProfileDescriptorLister) (*apiworkload.ServiceProfileDescriptor, error) {
	if !CheckWorkloadSPDEnabled(workload) {
		return nil, fmt.Errorf("workload not enable spd")
	}

	if spdName, ok := workload.GetAnnotations()[apiconsts.WorkloadAnnotationSPDNameKey]; ok {
		spd, err := spdLister.ServiceProfileDescriptors(workload.GetNamespace()).Get(spdName)
		if err == nil && checkTargetRefMatch(spd.Spec.TargetRef, workload) {
			return spd, nil
		}
		return nil, apierrors.NewNotFound(apis.Resource(apiworkload.ResourceNameServiceProfileDescriptors), "matched target refer spd")
	}

	if spdIndexer != nil {
		if spd, err := getSPDFroWorkloadWithIndex(workload, spdIndexer); err == nil {
			return spd, nil
		}
	}

	spdList, err := spdLister.List(labels.Everything())
	if err != nil {
		return nil, err
	}

	for _, spd := range spdList {
		if checkTargetRefMatch(spd.Spec.TargetRef, workload) {
			return spd, nil
		}
	}
	return nil, apierrors.NewNotFound(apiworkload.Resource(apiworkload.ResourceNameServiceProfileDescriptors), "spd for workload")
}

// GetSPDForPod is used to get spd that should manage the given vpa,
// we'll try to find by annotation for pod, and then go through workload if not exist,
// and we will find it recursively since we don't know in which level the owner will be.
func GetSPDForPod(pod *core.Pod, spdIndexer cache.Indexer, workloadListerMap map[schema.GroupVersionKind]cache.GenericLister,
	spdLister workloadlister.ServiceProfileDescriptorLister) (*apiworkload.ServiceProfileDescriptor, error) {
	// different with vpa, we will store spd name in pod name, so we will check whether it's still valid
	if spaName, ok := pod.GetAnnotations()[apiconsts.PodAnnotationSPDNameKey]; ok {
		spd, err := spdLister.ServiceProfileDescriptors(pod.GetNamespace()).Get(spaName)
		if err == nil && CheckSPDMatchWithPod(pod, spd, workloadListerMap) {
			return spd, nil
		}
	}

	for _, owner := range pod.GetOwnerReferences() {
		gvk := schema.FromAPIVersionAndKind(owner.APIVersion, owner.Kind)
		if _, ok := workloadListerMap[gvk]; ok {
			if workloadObj, err := workloadListerMap[gvk].ByNamespace(pod.GetNamespace()).Get(owner.Name); err == nil {
				var targetSPD *apiworkload.ServiceProfileDescriptor
				native.VisitUnstructuredAncestors(workloadObj.(*unstructured.Unstructured),
					workloadListerMap, func(owner *unstructured.Unstructured) bool {
						spd, err := GetSPDForWorkload(owner, spdIndexer, spdLister)
						if err != nil {
							return true
						}

						targetSPD = spd
						return false
					})
				if targetSPD != nil {
					return targetSPD, nil
				}
			}
		}
	}

	spdList, err := spdLister.List(labels.Everything())
	if err != nil {
		return nil, err
	}

	for _, spd := range spdList {
		if CheckSPDMatchWithPod(pod, spd, workloadListerMap) {
			return spd, nil
		}
	}
	return nil, apierrors.NewNotFound(apiworkload.Resource(apiworkload.ResourceNameServiceProfileDescriptors), "spd for pod")
}

// GetPodListForSPD is used to get pods that should be managed by the given spd,
// we'll always get through workload
func GetPodListForSPD(spd *apiworkload.ServiceProfileDescriptor, podIndexer cache.Indexer, podLabelIndexKeyList []string,
	workloadLister cache.GenericLister, podLister corelisters.PodLister) ([]*core.Pod, error) {
	workloadObj, err := GetWorkloadForSPD(spd, workloadLister)
	if err != nil {
		return nil, err
	}

	return native.GetPodListForWorkload(workloadObj, podIndexer, podLabelIndexKeyList, podLister)
}

/*
 helper functions to do validation works
*/

// CheckWorkloadSPDEnabled checks if the given workload is enabled with service profiling.
func CheckWorkloadSPDEnabled(workload metav1.Object) bool {
	return workload.GetAnnotations()[apiconsts.WorkloadAnnotationSPDEnableKey] == apiconsts.WorkloadAnnotationSPDEnabled
}

// CheckSPDMatchWithPod checks whether the given pod and spd matches with each other
func CheckSPDMatchWithPod(pod *core.Pod, spd *apiworkload.ServiceProfileDescriptor, workloadListerMap map[schema.GroupVersionKind]cache.GenericLister) bool {
	gvk := schema.FromAPIVersionAndKind(spd.Spec.TargetRef.APIVersion, spd.Spec.TargetRef.Kind)
	if _, ok := workloadListerMap[gvk]; !ok {
		return false
	}

	workloadObj, err := GetWorkloadForSPD(spd, workloadListerMap[gvk])
	if err != nil {
		klog.Errorf("failed to get workload for spd %v: %v", spd.Name, err)
		return false
	}

	if !CheckWorkloadSPDEnabled(workloadObj.(metav1.Object)) {
		return false
	}

	selector, err := native.GetUnstructuredSelector(workloadObj.(*unstructured.Unstructured))
	if err != nil || selector == nil {
		klog.Errorf("failed to get workload selector %v: %v", workloadObj, err)
		return false
	}

	return selector.Matches(labels.Set(pod.Labels))
}
