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

package adminqos

import (
	"testing"

	cliflag "k8s.io/component-base/cli/flag"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/adminqos"
)

func TestNewAdminQoSOptions(t *testing.T) {
	t.Parallel()
	options := NewAdminQoSOptions()

	// Verify that all sub-options are initialized
	if options.ReclaimedResourceOptions == nil {
		t.Errorf("ReclaimedResourceOptions is nil")
	}
	if options.QRMPluginOptions == nil {
		t.Errorf("QRMPluginOptions is nil")
	}
	if options.EvictionOptions == nil {
		t.Errorf("EvictionOptions is nil")
	}
	if options.AdvisorOptions == nil {
		t.Errorf("AdvisorOptions is nil")
	}
}

func TestAdminQoSOptions_AddFlags(t *testing.T) {
	t.Parallel()
	options := NewAdminQoSOptions()
	fss := &cliflag.NamedFlagSets{}

	options.AddFlags(fss)

	// Verify that all sub-options add flags
	reclaimedResourceFlagSet := fss.FlagSet("reclaimed-resource")
	if reclaimedResourceFlagSet == nil {
		t.Errorf("reclaimed-resource flag set not found")
	}

	evictionFlagSet := fss.FlagSet("eviction")
	if evictionFlagSet == nil {
		t.Errorf("eviction flag set not found")
	}
}

func TestAdminQoSOptions_ApplyTo(t *testing.T) {
	t.Parallel()
	options := NewAdminQoSOptions()
	config := adminqos.NewAdminQoSConfiguration()

	// Apply options to config
	err := options.ApplyTo(config)
	if err != nil {
		t.Errorf("ApplyTo failed: %v", err)
	}

	// Verify that config is updated
	if config.ReclaimedResourceConfiguration == nil {
		t.Errorf("ReclaimedResourceConfiguration is nil after ApplyTo")
	}
	if config.QRMPluginConfiguration == nil {
		t.Errorf("QRMPluginConfiguration is nil after ApplyTo")
	}
	if config.EvictionConfiguration == nil {
		t.Errorf("EvictionConfiguration is nil after ApplyTo")
	}
	if config.AdvisorConfiguration == nil {
		t.Errorf("AdvisorConfiguration is nil after ApplyTo")
	}
	if config.FineGrainedResourceConfiguration == nil {
		t.Errorf("FineGrainedResourceConfiguration is nil after ApplyTo")
	}
}
