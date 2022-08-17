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
	"fmt"

	"k8s.io/apimachinery/pkg/util/errors"
	apiserveroptions "k8s.io/apiserver/pkg/server/options"
	cliflag "k8s.io/component-base/cli/flag"
	componentbaseconfig "k8s.io/component-base/config"

	webhookconfig "github.com/kubewharf/katalyst-core/pkg/config/webhook"
)

// GenericWebhookOptions holds the configurations for webhook based configurations.
type GenericWebhookOptions struct {
	DryRun bool

	Webhooks           []string
	LabelSelector      string
	ServerPort         string
	DynamicGVResources []string

	SecureServing *apiserveroptions.SecureServingOptions
	componentbaseconfig.ClientConnectionConfiguration
}

// NewGenericWebhookOptions creates a new Options with a default config.
func NewGenericWebhookOptions() *GenericWebhookOptions {
	o := &GenericWebhookOptions{
		DryRun:        true,
		SecureServing: apiserveroptions.NewSecureServingOptions(),
	}

	o.SecureServing.ServerCert.PairName = "katalyst-webhook"
	return o
}

// AddFlags adds flags  to the specified FlagSet.
func (o *GenericWebhookOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("webhook")

	fs.StringSliceVar(&o.Webhooks, "webhooks", o.Webhooks, fmt.Sprintf(""+
		"A list of webhooks to enable. '*' enables all on-by-default webhooks, 'foo' enables the webhook "+
		"named 'foo', '-foo' disables the webhook named 'foo'"))

	fs.StringVar(&o.ServerPort, "server-port", o.ServerPort, fmt.Sprintf(""+
		"HTTP port that webhooks will be listening on."))
	fs.StringVar(&o.LabelSelector, "label-selector", o.LabelSelector, fmt.Sprintf(""+
		"A selector to restrict the list of returned objects by their labels. this selector is used in informer factory."))

	fs.BoolVar(&o.DryRun, "dry-run", o.DryRun, "A bool to enable and disable dry-run.")

	fs.Float32Var(&o.QPS, "kube-api-qps", o.QPS, "QPS to use while talking with kubernetes apiserver.")
	fs.Int32Var(&o.Burst, "kube-api-burst", o.Burst, "Burst to use while talking with kubernetes apiserver.")

	fs.StringSliceVar(&o.DynamicGVResources, "dynamic-resources", o.DynamicGVResources, fmt.Sprintf(""+
		"A list of workloads to be list and watched. "+
		"DynamicGVResources should be in the format of `resource.version.group.com` like 'deployments.v1.apps'."))

	o.SecureServing.AddFlags(fs)
}

// ApplyTo fills up config with options
func (o *GenericWebhookOptions) ApplyTo(c *webhookconfig.GenericWebhookConfiguration) error {
	c.ClientConnection.QPS = o.QPS
	c.ClientConnection.Burst = o.Burst

	c.DryRun = o.DryRun
	c.Webhooks = o.Webhooks
	c.ServerPort = o.ServerPort
	c.LabelSelector = o.LabelSelector
	c.DynamicGVResources = o.DynamicGVResources

	if errList := o.SecureServing.Validate(); len(errList) > 0 {
		return errors.NewAggregate(errList)
	}

	err := o.SecureServing.ApplyTo(&c.SecureServing)
	if err != nil {
		return err
	}
	return nil
}

func (o *GenericWebhookOptions) Config() (*webhookconfig.GenericWebhookConfiguration, error) {
	c := webhookconfig.NewGenericWebhookConfiguration()
	if err := o.ApplyTo(c); err != nil {
		return nil, err
	}
	return c, nil
}
