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

package webhook

import (
	"k8s.io/apiserver/pkg/server"
)

type GenericWebhookConfiguration struct {
	// Webhooks is the list of webhooks to enable or disable
	// '*' means "all enabled by default webhooks"
	// 'foo' means "enable 'foo'"
	// '-foo' means "disable 'foo'"
	// first item for a particular name wins
	Webhooks []string

	// A selector to restrict the list of returned objects by their labels. this selector is
	// used in informer factory.
	LabelSelector string

	// ServerPort indicates the insecure port that webhook implementation is listening on
	ServerPort string

	SecureServing *server.SecureServingInfo

	DynamicGVResources []string
}

type WebhooksConfiguration struct {
	*VPAConfig
	*PodConfig
}

func NewGenericWebhookConfiguration() *GenericWebhookConfiguration {
	return &GenericWebhookConfiguration{}
}

func NewWebhooksConfiguration() *WebhooksConfiguration {
	return &WebhooksConfiguration{
		VPAConfig: NewVPAConfig(),
		PodConfig: NewPodConfig(),
	}
}
