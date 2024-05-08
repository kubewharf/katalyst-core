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

package node

import (
	"context"
	"fmt"
	"time"

	kubewebhook "github.com/slok/kubewebhook/pkg/webhook"
	whcontext "github.com/slok/kubewebhook/pkg/webhook/context"
	"github.com/slok/kubewebhook/pkg/webhook/mutating"
	admissionv1beta1 "k8s.io/api/admission/v1beta1"
	core "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"

	katalystbase "github.com/kubewharf/katalyst-core/cmd/base"
	webhookconsts "github.com/kubewharf/katalyst-core/cmd/katalyst-webhook/app/webhook"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	webhookconfig "github.com/kubewharf/katalyst-core/pkg/config/webhook"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

type WebhookNodeMutator interface {
	MutateNode(node *core.Node, admissionRequest *admissionv1beta1.AdmissionRequest) error
	Name() string
}

// WebhookNode is the implementation of Kubernetes Webhook
// any implementation should at least implement the interface of mutating.Mutator of validating.Validator
type WebhookNode struct {
	ctx context.Context

	metricEmitter metrics.MetricEmitter

	mutators []WebhookNodeMutator
}

func NewWebhookNode(
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

	wn := &WebhookNode{
		ctx:           ctx,
		metricEmitter: metricEmitter,
		mutators:      []WebhookNodeMutator{},
	}

	wn.mutators = append(
		wn.mutators,
		NewWebhookNodeAllocatableMutator(),
	)

	cfg := mutating.WebhookConfig{
		Name: "nodeMutator",
		Obj:  &core.Node{},
	}
	webhook, err := mutating.NewWebhook(cfg, wn, nil, nil, nil)
	if err != nil {
		return nil, nil, err
	}

	return webhook, wn.Run, nil
}

func (wn *WebhookNode) Run() bool {
	klog.Infof("node webhook run")

	return true
}

func (wn *WebhookNode) Mutate(ctx context.Context, obj metav1.Object) (bool, error) {
	klog.V(5).Info("webhookNode notice an obj to be mutated")

	node, ok := obj.(*core.Node)
	if !ok || node == nil {
		err := fmt.Errorf("failed to convert obj to node: %v", obj)
		klog.Error(err)
		return false, err
	}

	admissionRequest := whcontext.GetAdmissionRequest(ctx)
	if admissionRequest == nil {
		err := fmt.Errorf("failed to get admission request from ctx")
		klog.Error(err)
		return false, err
	}

	klog.V(5).Infof("begin to mutate node %s", node.Name)
	for _, mutator := range wn.mutators {
		start := time.Now()
		err := mutator.MutateNode(node, admissionRequest)
		if err != nil {
			klog.Errorf("failed to mutate node %s: %v", node.Name, err)
			wn.emitMetrics(false, start, mutator)
			return false, err
		}
		wn.emitMetrics(true, start, mutator)
	}
	klog.Infof("node %s was mutated", node.Name)
	return true, nil
}

func (wn *WebhookNode) emitMetrics(success bool, start time.Time, mutator WebhookNodeMutator) {
	_ = wn.metricEmitter.StoreInt64("node_mutator_request_total", 1, metrics.MetricTypeNameCount, metrics.MetricTag{Key: "type", Val: mutator.Name()})
	_ = wn.metricEmitter.StoreFloat64("node_mutator_latency", time.Since(start).Seconds(), metrics.MetricTypeNameCount, metrics.MetricTag{Key: "type", Val: mutator.Name()})
	if !success {
		_ = wn.metricEmitter.StoreInt64("node_mutator_request_fail", 1, metrics.MetricTypeNameCount, metrics.MetricTag{Key: "type", Val: mutator.Name()})
	}
}
