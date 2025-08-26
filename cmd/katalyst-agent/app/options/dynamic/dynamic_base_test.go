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

package dynamic

import (
	"testing"

	cliflag "k8s.io/component-base/cli/flag"

	dynamicconf "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
)

func TestNewDynamicOptions(t *testing.T) {
	t.Parallel()
	options := NewDynamicOptions()

	// Verify that all sub-options are initialized
	if options.AdminQoSOptions == nil {
		t.Errorf("AdminQoSOptions is nil")
	}
	if options.TransparentMemoryOffloadingOptions == nil {
		t.Errorf("TransparentMemoryOffloadingOptions is nil")
	}
	if options.StrategyGroupOptions == nil {
		t.Errorf("StrategyGroupOptions is nil")
	}
	if options.IRQTuningOptions == nil {
		t.Errorf("IRQTuningOptions is nil")
	}
}

func TestDynamicOptions_AddFlags(t *testing.T) {
	t.Parallel()
	options := NewDynamicOptions()
	fss := &cliflag.NamedFlagSets{}

	options.AddFlags(fss)

	// Verify that flag sets are created for each sub-option
	adminQosFlagSet := fss.FlagSet("admin_qos")
	if adminQosFlagSet == nil {
		t.Errorf("admin_qos flag set not found")
	}

	// Note: TMO options use the flag set from its default implementation
	// which may not be directly visible here

	strategyGroupFlagSet := fss.FlagSet("strategy_group")
	if strategyGroupFlagSet == nil {
		t.Errorf("strategy_group flag set not found")
	}

	irqTuningFlagSet := fss.FlagSet("irq-tuning")
	if irqTuningFlagSet == nil {
		t.Errorf("irq-tuning flag set not found")
	}
}

func TestDynamicOptions_ApplyTo(t *testing.T) {
	t.Parallel()
	options := NewDynamicOptions()
	config := dynamicconf.NewConfiguration()

	// Apply options to config
	err := options.ApplyTo(config)
	if err != nil {
		t.Errorf("ApplyTo failed: %v", err)
	}

	// Verify that config is updated
	if config.AdminQoSConfiguration == nil {
		t.Errorf("AdminQoSConfiguration is nil after ApplyTo")
	}
	if config.TransparentMemoryOffloadingConfiguration == nil {
		t.Errorf("TransparentMemoryOffloadingConfiguration is nil after ApplyTo")
	}
	if config.StrategyGroupConfiguration == nil {
		t.Errorf("StrategyGroupConfiguration is nil after ApplyTo")
	}
	if config.IRQTuningConfiguration == nil {
		t.Errorf("IRQTuningConfiguration is nil after ApplyTo")
	}
}
