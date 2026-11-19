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

package qrm

import (
	cliflag "k8s.io/component-base/cli/flag"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/adminqos/qrm"
)

// DynamicRDTOptions is the standalone startup options for RDT on/off. It seeds
// only the dynamic RDTConfig.DisableRDT; the static ResctrlOptions carries no
// RDT switch. When true, managed non-skip CLOS directories are cleaned and new
// CLOS creation is rejected.
type DynamicRDTOptions struct {
	DisableRDT bool
}

func NewDynamicRDTOptions() *DynamicRDTOptions {
	return &DynamicRDTOptions{}
}

func (o *DynamicRDTOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("dynamic-qrm")
	fs.BoolVar(&o.DisableRDT, "disable-rdt",
		o.DisableRDT, "if set true, RDT CLOS injection is disabled: managed non-skip CLOS directories are cleaned and new CLOS creation is rejected")
}

func (o *DynamicRDTOptions) ApplyTo(c *qrm.QRMPluginConfiguration) error {
	c.RDTConfig.DisableRDT = o.DisableRDT
	return nil
}
