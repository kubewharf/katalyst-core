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
	kubewebhook "github.com/slok/kubewebhook/pkg/webhook"
)

// GenericStartFunc is used to determine whether the
// component has been successfully started
type GenericStartFunc func() bool

type WebhookWrapper struct {
	Name      string
	StartFunc GenericStartFunc
	Webhook   kubewebhook.Webhook
}
