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

package validating

import (
	"context"

	katalyst "github.com/kubewharf/katalyst-core/cmd/base"
	webhookconsts "github.com/kubewharf/katalyst-core/cmd/katalyst-webhook/app/webhook"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	webhookconfig "github.com/kubewharf/katalyst-core/pkg/config/webhook"
	"github.com/kubewharf/katalyst-core/pkg/webhook/validating/vpa"
)

const (
	VPAWebhookName = "vpa"
)

func StartVPAWebhook(ctx context.Context, webhookCtx *katalyst.GenericContext,
	genericConf *generic.GenericConfiguration, webhookGenericConf *webhookconfig.GenericWebhookConfiguration,
	webhookConf *webhookconfig.WebhooksConfiguration, name string,
) (*webhookconsts.WebhookWrapper, error) {
	v, run, err := vpa.NewWebhookVPA(ctx, webhookCtx, genericConf, webhookGenericConf, webhookConf, webhookCtx.EmitterPool.GetDefaultMetricsEmitter())
	if err != nil {
		return nil, err
	}
	return &webhookconsts.WebhookWrapper{
		Name:      name,
		StartFunc: run,
		Webhook:   v,
	}, nil
}
