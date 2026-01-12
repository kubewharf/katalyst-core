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
	"testing"

	cliflag "k8s.io/component-base/cli/flag"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/adminqos/qrm"
)

func TestNewQRMPluginOptions(t *testing.T) {
	t.Parallel()
	options := NewQRMPluginOptions()

	// Verify that all sub-options are initialized
	if options.CPUPluginOptions == nil {
		t.Errorf("CPUPluginOptions is nil")
	}
}

func TestQRMPluginOptions_AddFlags(t *testing.T) {
	t.Parallel()
	options := NewQRMPluginOptions()
	fss := &cliflag.NamedFlagSets{}

	options.AddFlags(fss)

	// Verify that all sub-options add flags
	cpuPluginFlagSet := fss.FlagSet("qrm-cpu-plugin")
	if cpuPluginFlagSet == nil {
		t.Errorf("qrm-cpu-plugin flag set not found")
	}
}

func TestQRMPluginOptions_ApplyTo(t *testing.T) {
	t.Parallel()
	options := NewQRMPluginOptions()
	config := qrm.NewQRMPluginConfiguration()

	// Apply options to config
	err := options.ApplyTo(config)
	if err != nil {
		t.Errorf("ApplyTo failed: %v", err)
	}

	// Verify that config is updated
	if config.CPUPluginConfiguration == nil {
		t.Errorf("CPUPluginConfiguration is nil after ApplyTo")
	}
}
