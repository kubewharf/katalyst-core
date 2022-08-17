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

package options

import (
	"k8s.io/apimachinery/pkg/util/errors"
	cliflag "k8s.io/component-base/cli/flag"

	webhookconfig "github.com/kubewharf/katalyst-core/pkg/config/webhook"
)

type WebhookOptions struct {
	*VPAOptions
	*PodOptions
}

func NewWebhooksOptions() *WebhookOptions {
	return &WebhookOptions{
		VPAOptions: NewVPAOptions(),
		PodOptions: NewPodOptions(),
	}
}

func (o *WebhookOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	o.VPAOptions.AddFlags(fss)
	o.PodOptions.AddFlags(fss)
}

// ApplyTo fills up config with options
func (o *WebhookOptions) ApplyTo(c *webhookconfig.WebhooksConfiguration) error {
	var errList []error

	errList = append(errList, o.VPAOptions.ApplyTo(c.VPAConfig))
	errList = append(errList, o.PodOptions.ApplyTo(c.PodConfig))
	return errors.NewAggregate(errList)
}

func (o *WebhookOptions) Config() (*webhookconfig.WebhooksConfiguration, error) {
	c := webhookconfig.NewWebhooksConfiguration()
	if err := o.ApplyTo(c); err != nil {
		return nil, err
	}
	return c, nil
}
