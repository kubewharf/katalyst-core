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

package global

import (
	cliflag "k8s.io/component-base/cli/flag"

	"github.com/kubewharf/katalyst-core/pkg/agent/audit/sink"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/global"
)

const DefaultBufferSize = 1000

type AuditOptions struct {
	Sinks      []string
	BufferSize int
}

func NewAuditOptions() *AuditOptions {
	return &AuditOptions{
		Sinks:      []string{sink.SinkNameLogBased},
		BufferSize: DefaultBufferSize,
	}
}

// AddFlags adds flags to the specified FlagSet.
func (o *AuditOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("audit")
	fs.StringSliceVar(&o.Sinks, "sinks", o.Sinks, "the sinks to send audit data")
	fs.IntVar(&o.BufferSize, "buffer-size", o.BufferSize, "buffer size for write audit data")
}

// ApplyTo fills up config with options
func (o *AuditOptions) ApplyTo(conf *global.AuditConfiguration) error {
	conf.Sinks = o.Sinks
	conf.BufferSize = o.BufferSize
	return nil
}
