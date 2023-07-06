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

package server

import (
	"github.com/spf13/pflag"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/sysadvisor/qosaware/server"
)

// QRMServerOptions holds the configurations for qrm servers in qos aware plugin
type QRMServerOptions struct {
	QRMServers []string
}

// NewQRMServerOptions creates a new Options with a default config
func NewQRMServerOptions() *QRMServerOptions {
	return &QRMServerOptions{
		QRMServers: []string{"cpu", "memory"},
	}
}

// AddFlags adds flags to the specified FlagSet.
func (o *QRMServerOptions) AddFlags(fs *pflag.FlagSet) {
	fs.StringSliceVar(&o.QRMServers, "qrm-servers", o.QRMServers, "active dimensions for qrm servers")
}

// ApplyTo fills up config with options
func (o *QRMServerOptions) ApplyTo(c *server.QRMServerConfiguration) error {
	c.QRMServers = o.QRMServers
	return nil
}
