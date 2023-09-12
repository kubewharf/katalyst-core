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

package inference

import (
	"time"

	cliflag "k8s.io/component-base/cli/flag"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/sysadvisor/inference"
)

const (
	defaultInferenceSyncPeriod = 5 * time.Second
)

// InferencePluginOptions holds the configurations for inference plugin.
type InferencePluginOptions struct {
	SyncPeriod time.Duration
}

// NewInferencePluginOptions creates a new Options with a default config.
func NewInferencePluginOptions() *InferencePluginOptions {
	return &InferencePluginOptions{
		SyncPeriod: defaultInferenceSyncPeriod,
	}
}

// AddFlags adds flags to the specified FlagSet.
func (o *InferencePluginOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("inference_plugin")

	fs.DurationVar(&o.SyncPeriod, "inference-sync-period", o.SyncPeriod, "Period for inference plugin to sync")
}

// ApplyTo fills up config with options
func (o *InferencePluginOptions) ApplyTo(c *inference.InferencePluginConfiguration) error {
	c.SyncPeriod = o.SyncPeriod
	return nil
}
