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

	kubewebhook "github.com/slok/kubewebhook/pkg/webhook"
	whcontext "github.com/slok/kubewebhook/pkg/webhook/context"
	"github.com/slok/kubewebhook/pkg/webhook/mutating"
	core "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	autoscalelister "github.com/kubewharf/katalyst-api/pkg/client/listers/autoscaling/v1alpha1"
	workloadlister "github.com/kubewharf/katalyst-api/pkg/client/listers/workload/v1alpha1"
	katalystbase "github.com/kubewharf/katalyst-core/cmd/base"
	webhookconsts "github.com/kubewharf/katalyst-core/cmd/katalyst-webhook/app/webhook"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	webhookconfig "github.com/kubewharf/katalyst-core/pkg/config/webhook"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util"
)

const podWebhookName = "pod"

type WebhookPodMutator interface {
	MutatePod(pod *core.Pod, namespace string) (allowed bool, err error)
}

// WebhookPod is the implementation of Kubernetes Webhook
// any implementation should at least implement the interface of mutating.Mutator of validating.Validator
type WebhookPod struct {
	ctx context.Context

	vpaIndexer cache.Indexer
	spdIndexer cache.Indexer

	vpaLister      autoscalelister.KatalystVerticalPodAutoscalerLister
	spdLister      workloadlister.ServiceProfileDescriptorLister
	workloadLister map[schema.GroupVersionKind]cache.GenericLister

	syncedFunc    []cache.InformerSynced
	metricEmitter metrics.MetricEmitter

	mutators []WebhookPodMutator
}

// NewWebhookPod makes the webhook pf Pod
func NewWebhookPod(
	ctx context.Context,
	webhookCtx *katalystbase.GenericContext,
	_ *generic.GenericConfiguration,
	_ *webhookconfig.GenericWebhookConfiguration,
	_ *webhookconfig.WebhooksConfiguration,
) (kubewebhook.Webhook, webhookconsts.GenericStartFunc, error) {
	metricEmitter := webhookCtx.EmitterPool.GetDefaultMetricsEmitter()
	if metricEmitter == nil {
		metricEmitter = metrics.DummyMetrics{}
	}

	vpaInformer := webhookCtx.InternalInformerFactory.Autoscaling().V1alpha1().KatalystVerticalPodAutoscalers()
	spdInformer := webhookCtx.InternalInformerFactory.Workload().V1alpha1().ServiceProfileDescriptors()

	// build indexer: workload --> vpa
	if _, ok := vpaInformer.Informer().GetIndexer().GetIndexers()[consts.TargetReferenceIndex]; !ok {
		err := vpaInformer.Informer().GetIndexer().AddIndexers(cache.Indexers{
			consts.TargetReferenceIndex: util.VPATargetReferenceIndex,
		})
		if err != nil {
			klog.Errorf("[pod webhook] failed to add vpa target reference index")
			return nil, nil, err
		}
	}

	// build index: workload ---> spd
	if _, ok := spdInformer.Informer().GetIndexer().GetIndexers()[consts.OwnerReferenceIndex]; !ok {
		err := spdInformer.Informer().AddIndexers(cache.Indexers{
			consts.TargetReferenceIndex: util.SPDTargetReferenceIndex,
		})
		if err != nil {
			klog.Errorf("[pod webhook] failed to add target reference index for spd")
			return nil, nil, err
		}
	}

	wp := &WebhookPod{
		ctx:            ctx,
		vpaIndexer:     vpaInformer.Informer().GetIndexer(),
		spdIndexer:     spdInformer.Informer().GetIndexer(),
		vpaLister:      vpaInformer.Lister(),
		spdLister:      spdInformer.Lister(),
		workloadLister: make(map[schema.GroupVersionKind]cache.GenericLister),
		metricEmitter:  metricEmitter,
		syncedFunc: []cache.InformerSynced{
			vpaInformer.Informer().HasSynced,
		},
		mutators: []WebhookPodMutator{},
	}

	workloadInformers := webhookCtx.DynamicResourcesManager.GetDynamicInformers()
	for _, wf := range workloadInformers {
		wp.workloadLister[wf.GVK] = wf.Informer.Lister()
		wp.syncedFunc = append(wp.syncedFunc, wf.Informer.Informer().HasSynced)
	}

	wp.mutators = append(wp.mutators,
		NewWebhookPodResourceMutator(ctx, wp.vpaIndexer, wp.vpaLister, wp.workloadLister),
		NewWebhookPodSPDReferenceMutator(ctx, wp.spdIndexer, wp.spdLister, wp.workloadLister),
	)

	cfg := mutating.WebhookConfig{
		Name: "podMutator",
		Obj:  &core.Pod{},
	}
	webhook, err := mutating.NewWebhook(cfg, wp, nil, nil, nil)
	if err != nil {
		return nil, nil, err
	}

	return webhook, wp.Run, nil
}

func (wp *WebhookPod) Run() bool {
	if !cache.WaitForCacheSync(wp.ctx.Done(), wp.syncedFunc...) {
		klog.Errorf("unable to sync caches for %s webhook")
		return false
	}
	klog.Infof("Caches are synced for %s webhook", podWebhookName)

	return true
}

func (wp *WebhookPod) Mutate(ctx context.Context, obj metav1.Object) (bool, error) {
	klog.V(5).Info("notice an obj to be mutated")
	pod, ok := obj.(*core.Pod)
	if !ok {
		err := fmt.Errorf("failed to convert obj to pod: %v", obj)
		klog.Error(err.Error())
		return false, err
	} else if pod == nil {
		err := fmt.Errorf("pod can't be nil")
		klog.Error(err.Error())
		return false, err
	}
	ar := whcontext.GetAdmissionRequest(ctx)
	if ar == nil {
		err := fmt.Errorf("failed to get admission request from ctx")
		klog.Error(err.Error())
		return false, err
	}

	klog.V(5).Infof("begin to mutate pod %s", pod.Name)
	for _, mutator := range wp.mutators {
		mutated, err := mutator.MutatePod(pod, ar.Namespace)
		if err != nil {
			klog.Errorf("failed to mutate pod %s: %v", pod.Name, err)
			return false, err
		}
		if !mutated {
			klog.Infof("pod %s mutation isn't allowed", pod.Name)
			return false, nil
		}
	}

	klog.Infof("pod %s was mutated ", pod.Name)
	return true, nil
}
