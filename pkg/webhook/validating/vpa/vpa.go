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
	"context"
	"fmt"

	kubewebhook "github.com/slok/kubewebhook/pkg/webhook"
	"github.com/slok/kubewebhook/pkg/webhook/validating"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	apis "github.com/kubewharf/katalyst-api/pkg/apis/autoscaling/v1alpha1"
	apiListers "github.com/kubewharf/katalyst-api/pkg/client/listers/autoscaling/v1alpha1"
	katalystbase "github.com/kubewharf/katalyst-core/cmd/base"
	webhookconsts "github.com/kubewharf/katalyst-core/cmd/katalyst-webhook/app/webhook"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	webhookconfig "github.com/kubewharf/katalyst-core/pkg/config/webhook"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

const (
	vpaWebhookName = "vpa"
)

// WebhookVPA is the implementation of Kubernetes Webhook
// any implementation should at least implement the interface of mutating.Mutator of validating.Validator
type WebhookVPA struct {
	ctx    context.Context
	dryRun bool

	validators    []WebhookVPAValidator
	metricEmitter metrics.MetricEmitter

	// vpaListerSynced returns true if the VerticalPodAutoscaler store has been synced at least once.
	vpaListerSynced cache.InformerSynced
	// vpaLister can list/get VerticalPodAutoscaler from the shared informer's store
	vpaLister apiListers.KatalystVerticalPodAutoscalerLister
}

type WebhookVPAValidator interface {
	ValidateVPA(vpa *apis.KatalystVerticalPodAutoscaler) (valid bool, message string, err error)
}

func NewWebhookVPA(ctx context.Context, webhookCtx *katalystbase.GenericContext,
	genericConf *generic.GenericConfiguration, _ *webhookconfig.GenericWebhookConfiguration,
	_ *webhookconfig.WebhooksConfiguration, metricsEmitter metrics.MetricEmitter,
) (kubewebhook.Webhook, webhookconsts.GenericStartFunc, error) {
	wa := &WebhookVPA{
		ctx:    ctx,
		dryRun: genericConf.DryRun,
	}

	wa.metricEmitter = metricsEmitter
	if metricsEmitter == nil {
		wa.metricEmitter = metrics.DummyMetrics{}
	}

	wa.vpaListerSynced = webhookCtx.InternalInformerFactory.Autoscaling().V1alpha1().KatalystVerticalPodAutoscalers().Informer().HasSynced
	wa.vpaLister = webhookCtx.InternalInformerFactory.Autoscaling().V1alpha1().KatalystVerticalPodAutoscalers().Lister()

	wa.validators = []WebhookVPAValidator{
		NewWebhookVPAPodNameContainerNameValidator(),
		NewWebhookVPAOverlapValidator(wa.vpaLister),
		NewWebhookVPAPolicyValidator(),
	}

	cfg := validating.WebhookConfig{
		Name: "vpaValidator",
		Obj:  &apis.KatalystVerticalPodAutoscaler{},
	}

	webhook, err := validating.NewWebhook(cfg, wa, nil, nil, nil)
	if err != nil {
		return nil, wa.Run, err
	}
	return webhook, wa.Run, nil
}

func (wa *WebhookVPA) Run() bool {
	if !cache.WaitForCacheSync(wa.ctx.Done(), wa.vpaListerSynced) {
		klog.Errorf("unable to sync caches for %s webhook", vpaWebhookName)
		return false
	}
	klog.Infof("Caches are synced for %s webhook", vpaWebhookName)

	return true
}

func (wa *WebhookVPA) Validate(_ context.Context, obj metav1.Object) (bool, validating.ValidatorResult, error) {
	klog.V(5).Info("notice an obj to be validated")
	vpa, ok := obj.(*apis.KatalystVerticalPodAutoscaler)
	if !ok {
		err := fmt.Errorf("failed to convert obj to vpa: %v", obj)
		klog.Error(err.Error())
		return false, validating.ValidatorResult{}, err
	}
	if vpa == nil {
		err := fmt.Errorf("vpa can't be nil")
		klog.Error(err.Error())
		return false, validating.ValidatorResult{}, err
	}

	klog.V(5).Infof("begin to validate vpa %s", vpa.Name)

	for _, validator := range wa.validators {
		succeed, msg, err := validator.ValidateVPA(vpa)
		if err != nil {
			klog.Errorf("an err occurred when validating vpa %s", vpa.Name)
			_ = wa.metricEmitter.StoreInt64("vpa_webhook_error", 1, metrics.MetricTypeNameCount,
				metrics.MetricTag{Key: "name", Val: vpa.Name})
			return false, validating.ValidatorResult{}, err
		} else if !succeed {
			klog.Infof("vpa %s didn't pass the webhook", vpa.Name)
			_ = wa.metricEmitter.StoreInt64("vpa_webhook_fail", 1, metrics.MetricTypeNameCount,
				metrics.MetricTag{Key: "name", Val: vpa.Name})
			if !wa.dryRun {
				return false, validating.ValidatorResult{Valid: false, Message: msg}, nil
			}
		}
	}

	_ = wa.metricEmitter.StoreInt64("vpa_webhook_succeed", 1, metrics.MetricTypeNameCount,
		metrics.MetricTag{Key: "name", Val: vpa.Name})
	klog.Infof("vpa %s passed the validation webhook", vpa.Name)
	return false, validating.ValidatorResult{Valid: true, Message: "validation succeed"}, nil
}
