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

package pod

import (
	"context"
	"fmt"

	core "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	apis "github.com/kubewharf/katalyst-api/pkg/apis/autoscaling/v1alpha1"
	autoscalelister "github.com/kubewharf/katalyst-api/pkg/client/listers/autoscaling/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/util"
	katalystutil "github.com/kubewharf/katalyst-core/pkg/util"
)

var supportedResources = []core.ResourceName{core.ResourceCPU, core.ResourceMemory}

type WebhookPodResourceMutator struct {
	ctx context.Context

	vpaIndexer cache.Indexer

	vpaLister      autoscalelister.KatalystVerticalPodAutoscalerLister
	workloadLister map[schema.GroupVersionKind]cache.GenericLister
}

// NewWebhookPodResourceMutator will mutate pod resource according to vpa
func NewWebhookPodResourceMutator(
	ctx context.Context,
	vpaIndexer cache.Indexer,
	vpaLister autoscalelister.KatalystVerticalPodAutoscalerLister,
	workloadLister map[schema.GroupVersionKind]cache.GenericLister,
) *WebhookPodResourceMutator {
	r := WebhookPodResourceMutator{
		ctx:            ctx,
		vpaIndexer:     vpaIndexer,
		vpaLister:      vpaLister,
		workloadLister: workloadLister,
	}
	return &r
}

// isVPANeededInWebhook returns if vpa is needed in webhook by checking its PodMatchingStrategy
func (r *WebhookPodResourceMutator) isVPANeededInWebhook(vpa *apis.KatalystVerticalPodAutoscaler) bool {
	if vpa == nil {
		return false
	}
	return vpa.Spec.UpdatePolicy.PodMatchingStrategy != apis.PodMatchingStrategyForHistoricalPod
}

func (r *WebhookPodResourceMutator) mergeRecommendationIntoPodResource(pod *core.Pod, recommendations map[string]core.ResourceRequirements) error {
	merge := func(resource core.ResourceList, recommendation core.ResourceList) core.ResourceList {
		for _, name := range supportedResources {
			if value, ok := recommendation[name]; ok {
				resource[name] = value
			}
		}
		return resource
	}

	if pod == nil {
		err := fmt.Errorf("pod is nil")
		klog.Error(err.Error())
		return err
	}
	for i := 0; i < len(pod.Spec.Containers); i++ {
		container := &pod.Spec.Containers[i]
		container.Resources.Limits = merge(container.Resources.Limits, recommendations[container.Name].Limits)
		container.Resources.Requests = merge(container.Resources.Requests, recommendations[container.Name].Requests)
	}
	return nil
}

func (r *WebhookPodResourceMutator) MutatePod(pod *core.Pod, namespace string) (mutated bool, err error) {
	if pod == nil {
		err := fmt.Errorf("pod is nil")
		klog.Error(err.Error())
		return false, err
	}

	pod.Namespace = namespace
	vpa, err := katalystutil.GetVPAForPod(pod, r.vpaIndexer, r.workloadLister, r.vpaLister)
	if err != nil || vpa == nil {
		klog.Warningf("didn't to find vpa of pod %v/%v, err: %v", pod.Namespace, pod.Name, err)
		return false, nil
	}

	gvk := schema.FromAPIVersionAndKind(vpa.Spec.TargetRef.APIVersion, vpa.Spec.TargetRef.Kind)
	workloadLister, ok := r.workloadLister[gvk]
	if !ok {
		klog.Errorf("vpa %s/%s without workload lister", vpa.Namespace, vpa.Name)
		return false, nil
	}

	workload, err := katalystutil.GetWorkloadForVPA(vpa, workloadLister)
	if err != nil {
		klog.Warning("didn't to find workload of pod %v/%v, err: %v", pod.Namespace, pod.Name, err)
		return false, nil
	}

	// check if pod has been enabled with vpa functionality
	if !katalystutil.CheckWorkloadEnableVPA(workload) {
		klog.V(6).Infof("VPA of pod %s is not supported", pod.Name)
		return false, nil
	}

	needVPA := r.isVPANeededInWebhook(vpa)
	if !needVPA {
		return false, nil
	}

	podResources, containerResources, err := util.GenerateVPAResourceMap(vpa)
	if err != nil {
		klog.Errorf("failed to get container resource from VPA %s", vpa.Name)
		return false, err
	}

	annotationResource, err := util.GenerateVPAPodResizeResourceAnnotations(pod, podResources, containerResources)
	if err != nil {
		klog.Errorf("failed to exact pod %v resize resource annotation from container resource: %v", pod, err)
		return false, err
	}

	isLegal, msg, err := katalystutil.CheckVPAStatusLegal(vpa, []*core.Pod{pod})
	if err != nil {
		klog.Errorf("failed to check pod %v status whether is legal: %v", pod, err)
		return false, err
	} else if !isLegal {
		klog.Errorf("vpa status is illegal: %s", msg)
		return false, nil
	}

	err = r.mergeRecommendationIntoPodResource(pod, annotationResource)
	if err != nil {
		klog.Error("failed to merge recommendation to pod resource")
		return false, err
	}

	return true, nil
}
