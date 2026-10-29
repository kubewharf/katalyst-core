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

package vpa

import (
	"fmt"

	core "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/json"
	"k8s.io/client-go/discovery"
	"k8s.io/klog/v2"

	apis "github.com/kubewharf/katalyst-api/pkg/apis/autoscaling/v1alpha1"
	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	katalystutil "github.com/kubewharf/katalyst-core/pkg/util"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

type podResizeMode int

const (
	podResizeModeLegacy podResizeMode = iota
	podResizeModeNative
)

func (m podResizeMode) String() string {
	if m == podResizeModeNative {
		return "native"
	}
	return "legacy"
}

// discoverPodResizeMode discovers the pod resize mode based on the api server resources available.
// If the api server does not support the native resize mode, it returns the legacy mode.
func discoverPodResizeMode(client discovery.DiscoveryInterface) (podResizeMode, error) {
	resources, err := client.ServerResourcesForGroupVersion(core.SchemeGroupVersion.String())
	if apierrors.IsNotFound(err) {
		return podResizeModeLegacy, nil
	}
	if err != nil {
		return podResizeModeLegacy, fmt.Errorf("discover core v1 resources: %w", err)
	}

	for _, apiResource := range resources.APIResources {
		if apiResource.Name != "pods/resize" {
			continue
		}
		for _, verb := range apiResource.Verbs {
			if verb == "update" {
				return podResizeModeNative, nil
			}
		}
	}
	return podResizeModeLegacy, nil
}

func applyNativeResizePolicies(pod *core.Pod, policies map[string]apis.ContainerResourcePolicy) {
	for i := range pod.Spec.Containers {
		policy, ok := policies[pod.Spec.Containers[i].Name]
		if !ok {
			continue
		}

		restartPolicy := core.NotRequired
		if policy.ResourceResizePolicy == apis.ResourceResizePolicyRestart {
			restartPolicy = core.RestartContainer
		}
		pod.Spec.Containers[i].ResizePolicy = []core.ContainerResizePolicy{
			{ResourceName: core.ResourceCPU, RestartPolicy: restartPolicy},
			{ResourceName: core.ResourceMemory, RestartPolicy: restartPolicy},
		}
	}
}

func (vc *VPAController) resizePodNative(pod *core.Pod, resources map[string]core.ResourceRequirements,
	policies map[string]apis.ContainerResourcePolicy,
) error {
	podCopy := pod.DeepCopy()
	native.ApplyPodResources(resources, podCopy)
	applyNativeResizePolicies(podCopy, policies)

	if apiequality.Semantic.DeepEqual(pod.Spec.Containers, podCopy.Spec.Containers) {
		klog.V(5).Infof("pod %s/%s has no need to update resources", pod.Namespace, pod.Name)
		return nil
	}

	_, err := vc.podUpdater.UpdatePodResize(vc.ctx, podCopy, metav1.UpdateOptions{})
	return err
}

func (vc *VPAController) resizePodLegacy(pod *core.Pod, resources map[string]core.ResourceRequirements,
	policies map[string]apis.ContainerResourcePolicy,
) error {
	marshalledResourceAnnotation, err := json.Marshal(resources)
	if err != nil {
		return err
	}

	annotationPolicy, err := katalystutil.GenerateVPAPodResizePolicyAnnotations(pod, policies)
	if err != nil {
		return err
	}

	marshalledPolicyAnnotation, err := json.Marshal(annotationPolicy)
	if err != nil {
		return err
	}

	podCopy := pod.DeepCopy()
	if native.PodResourceDiff(pod, resources) {
		if len(resources) > 0 {
			podCopy.Annotations[apiconsts.PodAnnotationInplaceUpdateResourcesKey] = string(marshalledResourceAnnotation)
		} else {
			delete(podCopy.Annotations, apiconsts.PodAnnotationInplaceUpdateResourcesKey)
		}
		if len(annotationPolicy) > 0 {
			podCopy.Annotations[apiconsts.PodAnnotationInplaceUpdateResizePolicyKey] = string(marshalledPolicyAnnotation)
		} else {
			delete(podCopy.Annotations, apiconsts.PodAnnotationInplaceUpdateResizePolicyKey)
		}
	}

	if apiequality.Semantic.DeepEqual(pod.Annotations, podCopy.Annotations) {
		klog.V(5).Infof("pod %s/%s has no need to update resources", pod.Namespace, pod.Name)
		return nil
	}

	return vc.podUpdater.PatchPod(vc.ctx, pod, podCopy)
}
