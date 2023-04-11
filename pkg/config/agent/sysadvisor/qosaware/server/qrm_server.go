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

import "github.com/kubewharf/katalyst-core/pkg/config/dynamic"

// QRMServerConfiguration stores configurations of qrm servers in qos aware plugin
type QRMServerConfiguration struct {
	QRMServers []string
}

// NewQRMServerConfiguration creates new qrm server configurations
func NewQRMServerConfiguration() *QRMServerConfiguration {
	return &QRMServerConfiguration{}
}

// ApplyConfiguration is used to set configuration based on conf.
func (c *QRMServerConfiguration) ApplyConfiguration(*QRMServerConfiguration, *dynamic.DynamicConfigCRD) {
}
