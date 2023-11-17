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

	workloadlister "github.com/kubewharf/katalyst-api/pkg/client/listers/workload/v1alpha1"
	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	katalystutil "github.com/kubewharf/katalyst-core/pkg/util"
)

type WebhookPodSPDReferenceMutator struct {
	ctx context.Context

	spdIndexer cache.Indexer

	spdLister      workloadlister.ServiceProfileDescriptorLister
	workloadLister map[schema.GroupVersionKind]cache.GenericLister
}

// NewWebhookPodSPDReferenceMutator will maintain spd reference annotations for pod
func NewWebhookPodSPDReferenceMutator(
	ctx context.Context,
	spdIndexer cache.Indexer,
	spdLister workloadlister.ServiceProfileDescriptorLister,
	workloadLister map[schema.GroupVersionKind]cache.GenericLister,
) *WebhookPodSPDReferenceMutator {
	s := WebhookPodSPDReferenceMutator{
		ctx:            ctx,
		spdIndexer:     spdIndexer,
		spdLister:      spdLister,
		workloadLister: workloadLister,
	}
	return &s
}

func (s *WebhookPodSPDReferenceMutator) MutatePod(pod *core.Pod, namespace string) (mutated bool, err error) {
	if pod == nil {
		err := fmt.Errorf("pod is nil")
		klog.Error(err.Error())
		return false, err
	}

	pod.Namespace = namespace
	spd, err := katalystutil.GetSPDForPod(pod, s.spdIndexer, s.workloadLister, s.spdLister)
	if err != nil || spd == nil {
		klog.Warningf("didn't to find spd of pod %v/%v, err: %v", pod.Namespace, pod.Name, err)
		return false, nil
	}

	if pod.Annotations == nil {
		pod.Annotations = make(map[string]string)
	}
	pod.Annotations[apiconsts.PodAnnotationSPDNameKey] = spd.Name
	return true, nil

}
