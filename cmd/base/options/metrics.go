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
	"time"

	"github.com/spf13/pflag"

	"github.com/kubewharf/katalyst-core/pkg/config/generic"
)

type MetricsOptions struct {
	EmitterPrometheusGCTimeout time.Duration
}

func NewMetricsOptions() *MetricsOptions {
	return &MetricsOptions{
		EmitterPrometheusGCTimeout: time.Minute * 5,
	}
}

// AddFlags adds flags  to the specified FlagSet.
func (o *MetricsOptions) AddFlags(fs *pflag.FlagSet) {
	fs.DurationVar(&o.EmitterPrometheusGCTimeout, "metrics-prom-gc-timeout",
		o.EmitterPrometheusGCTimeout, "the time duration to trigger gc logic for prometheus metrics emitter")
}

func (o *MetricsOptions) ApplyTo(c *generic.MetricsConfiguration) error {
	c.EmitterPrometheusGCTimeout = o.EmitterPrometheusGCTimeout
	return nil
}
