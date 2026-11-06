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
	"strings"
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
	if options.MemoryPluginOptions == nil {
		t.Errorf("MemoryPluginOptions is nil")
	}
	if !options.EnableBulkheadCpusetMems {
		t.Errorf("EnableBulkheadCpusetMems default = false, want true")
	}
	if options.BulkheadCATDefaultAllowedBitUsages != "*" {
		t.Errorf("BulkheadCATDefaultAllowedBitUsages default = %q, want *", options.BulkheadCATDefaultAllowedBitUsages)
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
	memoryPluginFlagSet := fss.FlagSet("memory_resource_plugin")
	if memoryPluginFlagSet == nil {
		t.Errorf("memory_resource_plugin flag set not found")
	}
	for _, name := range []string{
		"enable-bulkhead",
		"enable-bulkhead-cpuset-topology",
		"enable-bulkhead-cpuset-mems",
		"enable-bulkhead-workqueue",
		"enable-bulkhead-system-service",
		"bind-irq-to-reclaimed-pool",
		"bulkhead-non-reclaim-pool-min-size",
		"bulkhead-default-cat-ways",
		"bulkhead-clos-cat-ways",
		"bulkhead-cat-default-allowed-bit-usages",
	} {
		if cpuPluginFlagSet.Lookup(name) == nil {
			t.Errorf("qrm-cpu-plugin flag %q not found", name)
		}
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
	if config.MemoryPluginConfiguration == nil {
		t.Errorf("MemoryPluginConfiguration is nil after ApplyTo")
	}
	if config.CPUPluginConfiguration.BulkheadConfig.NonReclaimPoolMinSize != 16 {
		t.Errorf("NonReclaimPoolMinSize = %d, want default 16", config.CPUPluginConfiguration.BulkheadConfig.NonReclaimPoolMinSize)
	}
	if !config.CPUPluginConfiguration.BulkheadConfig.EnableBulkheadCpusetMems {
		t.Errorf("EnableBulkheadCpusetMems = false, want default true")
	}
}

func TestQRMPluginOptions_ApplyToDynamicBulkheadConfiguration(t *testing.T) {
	t.Parallel()

	options := NewQRMPluginOptions()
	options.EnableBulkhead = true
	options.EnableBulkheadCpusetTopology = true
	options.EnableBulkheadCpusetMems = true
	options.EnableBulkheadWorkqueue = true
	options.EnableBulkheadSystemService = true
	options.BindIRQToReclaimedPool = true
	options.BulkheadNonReclaimPoolMinSize = 4
	config := qrm.NewQRMPluginConfiguration()

	err := options.ApplyTo(config)
	if err != nil {
		t.Errorf("ApplyTo failed: %v", err)
	}
	if !config.CPUPluginConfiguration.BulkheadConfig.Enable {
		t.Errorf("Enable = false, want true")
	}
	if !config.CPUPluginConfiguration.BulkheadConfig.EnableBulkheadCpusetTopology {
		t.Errorf("EnableBulkheadCpusetTopology = false, want true")
	}
	if !config.CPUPluginConfiguration.BulkheadConfig.EnableBulkheadCpusetMems {
		t.Errorf("EnableBulkheadCpusetMems = false, want true")
	}
	if !config.CPUPluginConfiguration.BulkheadConfig.EnableBulkheadWorkqueue {
		t.Errorf("EnableBulkheadWorkqueue = false, want true")
	}
	if !config.CPUPluginConfiguration.BulkheadConfig.EnableBulkheadSystemService {
		t.Errorf("EnableBulkheadSystemService = false, want true")
	}
	if !config.CPUPluginConfiguration.BindIRQToReclaimedPool {
		t.Errorf("BindIRQToReclaimedPool = false, want true")
	}
	if config.CPUPluginConfiguration.BulkheadConfig.NonReclaimPoolMinSize != 4 {
		t.Errorf("NonReclaimPoolMinSize = %d, want 4", config.CPUPluginConfiguration.BulkheadConfig.NonReclaimPoolMinSize)
	}
}

func TestQRMPluginOptions_ParseBulkheadNonReclaimPoolMinSize(t *testing.T) {
	t.Parallel()

	options := NewQRMPluginOptions()
	fss := &cliflag.NamedFlagSets{}
	options.AddFlags(fss)

	if err := fss.FlagSet("qrm-cpu-plugin").Parse([]string{"--bulkhead-non-reclaim-pool-min-size=4"}); err != nil {
		t.Fatalf("failed to parse flag: %v", err)
	}

	config := qrm.NewQRMPluginConfiguration()
	if err := options.ApplyTo(config); err != nil {
		t.Fatalf("ApplyTo failed: %v", err)
	}
	if config.CPUPluginConfiguration.BulkheadConfig.NonReclaimPoolMinSize != 4 {
		t.Fatalf("NonReclaimPoolMinSize = %d, want 4", config.CPUPluginConfiguration.BulkheadConfig.NonReclaimPoolMinSize)
	}
}

func TestQRMPluginOptions_ParseBulkheadCATWays(t *testing.T) {
	t.Parallel()

	options := NewQRMPluginOptions()
	fss := &cliflag.NamedFlagSets{}
	options.AddFlags(fss)

	if err := fss.FlagSet("qrm-cpu-plugin").Parse([]string{
		"--bulkhead-default-cat-ways=MaxCATWays",
		"--bulkhead-clos-cat-ways=share-00=MaxCATWays-MinCATWays,share-01=2",
	}); err != nil {
		t.Fatalf("failed to parse flags: %v", err)
	}
	if got := options.BulkheadDefaultCATWays.Value; got != "MaxCATWays" {
		t.Fatalf("BulkheadDefaultCATWays = %q, want MaxCATWays", got)
	}
	if got := options.BulkheadClosCATWays["share-00"]; got != "MaxCATWays-MinCATWays" {
		t.Fatalf("BulkheadClosCATWays[share-00] = %q, want MaxCATWays-MinCATWays", got)
	}
	if got := options.BulkheadClosCATWays["share-01"]; got != "2" {
		t.Fatalf("BulkheadClosCATWays[share-01] = %q, want 2", got)
	}

	config := qrm.NewQRMPluginConfiguration()
	if err := options.ApplyTo(config); err != nil {
		t.Fatalf("ApplyTo failed: %v", err)
	}
	rdt := config.CPUPluginConfiguration.BulkheadConfig.BulkheadRDTConfig
	if got := rdt.DefaultCATWays.String(); got != "MaxCATWays" {
		t.Fatalf("DefaultCATWays = %s, want MaxCATWays", got)
	}
	if got := rdt.ClosCATWays["share-00"].String(); got != "MaxCATWays-MinCATWays" {
		t.Fatalf("ClosCATWays[share-00] = %s, want MaxCATWays-MinCATWays", got)
	}
	if got := rdt.ClosCATWays["share-01"].String(); got != "2" {
		t.Fatalf("ClosCATWays[share-01] = %s, want 2", got)
	}
	if got := rdt.CATPolicy.DefaultPlacement.AllowedBitUsages; len(got) != 1 || got[0] != qrm.CATBitUsageAll {
		t.Fatalf("DefaultPlacement.AllowedBitUsages = %#v, want *", got)
	}
	options.BulkheadClosCATWays["share-01"] = "9"
	if got := rdt.ClosCATWays["share-01"].String(); got != "2" {
		t.Fatalf("ClosCATWays aliases options map: share-01 = %s, want 2", got)
	}
}

func TestQRMPluginOptions_ParseBulkheadCATDefaultAllowedBitUsages(t *testing.T) {
	t.Parallel()

	options := NewQRMPluginOptions()
	fss := &cliflag.NamedFlagSets{}
	options.AddFlags(fss)

	if err := fss.FlagSet("qrm-cpu-plugin").Parse([]string{"--bulkhead-cat-default-allowed-bit-usages=*"}); err != nil {
		t.Fatalf("failed to parse flags: %v", err)
	}
	config := qrm.NewQRMPluginConfiguration()
	if err := options.ApplyTo(config); err != nil {
		t.Fatalf("ApplyTo failed: %v", err)
	}
	got := config.CPUPluginConfiguration.BulkheadConfig.BulkheadRDTConfig.CATPolicy.DefaultPlacement.AllowedBitUsages
	if len(got) != 1 || got[0] != qrm.CATBitUsageAll {
		t.Fatalf("DefaultPlacement.AllowedBitUsages = %#v, want *", got)
	}
}

func TestQRMPluginOptions_RejectInvalidBulkheadCATDefaultAllowedBitUsages(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name            string
		value           string
		wantErrContains string
	}{
		{name: "unknown", value: "Q", wantErrContains: `unsupported cat bit usage "q"`},
		{name: "combined wildcard", value: "*,S", wantErrContains: `cat bit usage "*" must not be combined`},
		{name: "duplicate", value: "S,S", wantErrContains: `duplicate cat bit usage "s"`},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			options := NewQRMPluginOptions()
			fss := &cliflag.NamedFlagSets{}
			options.AddFlags(fss)
			if err := fss.FlagSet("qrm-cpu-plugin").Parse([]string{"--bulkhead-cat-default-allowed-bit-usages=" + tc.value}); err != nil {
				t.Fatalf("failed to parse flags: %v", err)
			}

			err := options.ApplyTo(qrm.NewQRMPluginConfiguration())
			if err == nil {
				t.Fatal("ApplyTo succeeded, want error")
			}
			if !strings.Contains(err.Error(), tc.wantErrContains) {
				t.Fatalf("ApplyTo error = %q, want substring %q", err, tc.wantErrContains)
			}
			if err.Error() != strings.ToLower(err.Error()) {
				t.Fatalf("ApplyTo error = %q, want lower-case error", err)
			}
		})
	}
}

func TestQRMPluginOptions_ApplyToRejectsLegacyCATWaysTokens(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name            string
		args            []string
		wantErrContains string
	}{
		{
			name:            "legacy CBMMask token",
			args:            []string{"--bulkhead-default-cat-ways=CBMMask"},
			wantErrContains: `cat ways expression operand "CBMMask" is invalid`,
		},
		{
			name:            "legacy MinCBMBits token",
			args:            []string{"--bulkhead-clos-cat-ways=share-00=MinCBMBits"},
			wantErrContains: `cat ways expression operand "MinCBMBits" is invalid`,
		},
		{
			name:            "legacy combined tokens",
			args:            []string{"--bulkhead-clos-cat-ways=share-00=CBMMask-MinCBMBits"},
			wantErrContains: `cat ways expression operand "CBMMask" is invalid`,
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			options := NewQRMPluginOptions()
			fss := &cliflag.NamedFlagSets{}
			options.AddFlags(fss)
			if err := fss.FlagSet("qrm-cpu-plugin").Parse(tc.args); err != nil {
				t.Fatalf("failed to parse flags: %v", err)
			}

			err := options.ApplyTo(qrm.NewQRMPluginConfiguration())
			if err == nil {
				t.Fatal("ApplyTo succeeded, want error")
			}
			if !strings.Contains(err.Error(), tc.wantErrContains) {
				t.Fatalf("ApplyTo error = %q, want substring %q", err, tc.wantErrContains)
			}
		})
	}
}

func TestQRMPluginOptions_ParseInvalidBulkheadClosCATWays(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name            string
		value           string
		wantErrContains string
	}{
		{name: "invalid expression", value: "reclaim=invalid", wantErrContains: "invalid bulkhead-clos-cat-ways for clos"},
		{name: "zero ways", value: "reclaim=0", wantErrContains: "cat ways expression operand"},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			options := NewQRMPluginOptions()
			fss := &cliflag.NamedFlagSets{}
			options.AddFlags(fss)

			fs := fss.FlagSet("qrm-cpu-plugin")
			if err := fs.Parse([]string{"--bulkhead-clos-cat-ways=" + tc.value}); err != nil {
				t.Fatalf("failed to parse flag: %v", err)
			}
			err := options.ApplyTo(qrm.NewQRMPluginConfiguration())
			if err == nil {
				t.Fatal("ApplyTo succeeded, want error")
			}
			if !strings.Contains(err.Error(), tc.wantErrContains) {
				t.Fatalf("ApplyTo error = %q, want substring %q", err, tc.wantErrContains)
			}
		})
	}
}

func TestQRMPluginOptions_ParseExplicitZeroBulkheadDefaultCATWays(t *testing.T) {
	t.Parallel()

	options := NewQRMPluginOptions()
	fss := &cliflag.NamedFlagSets{}
	options.AddFlags(fss)

	if err := fss.FlagSet("qrm-cpu-plugin").Parse([]string{"--bulkhead-default-cat-ways=0"}); err != nil {
		t.Fatalf("failed to parse flag: %v", err)
	}

	err := options.ApplyTo(qrm.NewQRMPluginConfiguration())
	if err == nil {
		t.Fatal("ApplyTo succeeded, want error")
	}
	if !strings.Contains(err.Error(), "cat ways expression operand") {
		t.Fatalf("ApplyTo error = %q, want positive-value error", err)
	}
	if err.Error() != strings.ToLower(err.Error()) {
		t.Fatalf("ApplyTo error = %q, want lower-case error", err)
	}
}

func TestCPUPluginOptions_ParseExplicitZeroFromFirstNamedFlagSets(t *testing.T) {
	t.Parallel()

	options := NewCPUPluginOptions()
	firstFSS := &cliflag.NamedFlagSets{}
	secondFSS := &cliflag.NamedFlagSets{}
	options.AddFlags(firstFSS)
	options.AddFlags(secondFSS)

	if err := firstFSS.FlagSet("qrm-cpu-plugin").Parse([]string{"--bulkhead-default-cat-ways=0"}); err != nil {
		t.Fatalf("failed to parse flag from first NamedFlagSets: %v", err)
	}

	err := options.ApplyTo(qrm.NewCPUPluginConfiguration())
	if err == nil {
		t.Fatal("ApplyTo succeeded, want error")
	}
	if !strings.Contains(err.Error(), "cat ways expression operand") {
		t.Fatalf("ApplyTo error = %q, want positive-value error", err)
	}
}

func TestQRMPluginOptions_ValidateBulkheadCATWays(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name            string
		defaultCATWays  string
		closCATWays     map[string]string
		wantErrContains string
	}{
		{name: "negative default", defaultCATWays: "-1", wantErrContains: "invalid bulkhead-default-cat-ways"},
		{name: "empty clos", closCATWays: map[string]string{"": "2"}, wantErrContains: "bulkhead-clos-cat-ways contains an empty clos"},
		{name: "zero ways", closCATWays: map[string]string{"reclaim": "0"}, wantErrContains: "invalid bulkhead-clos-cat-ways for clos"},
		{name: "negative ways", closCATWays: map[string]string{"reclaim": "-1"}, wantErrContains: "invalid bulkhead-clos-cat-ways for clos"},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			options := NewQRMPluginOptions()
			options.BulkheadDefaultCATWays.Value = tc.defaultCATWays
			options.BulkheadClosCATWays = tc.closCATWays

			err := options.ApplyTo(qrm.NewQRMPluginConfiguration())
			if err == nil {
				t.Fatal("ApplyTo succeeded, want error")
			}
			if !strings.Contains(err.Error(), tc.wantErrContains) {
				t.Fatalf("ApplyTo error = %q, want substring %q", err, tc.wantErrContains)
			}
			if err.Error() != strings.ToLower(err.Error()) {
				t.Fatalf("ApplyTo error = %q, want lower-case error", err)
			}
		})
	}

	t.Run("zero values preserve compatibility", func(t *testing.T) {
		t.Parallel()

		options := NewQRMPluginOptions()
		config := qrm.NewQRMPluginConfiguration()
		if err := options.ApplyTo(config); err != nil {
			t.Fatalf("ApplyTo failed: %v", err)
		}
		rdt := config.CPUPluginConfiguration.BulkheadConfig.BulkheadRDTConfig
		if rdt.DefaultCATWays.Configured() {
			t.Fatalf("DefaultCATWays = %s, want unconfigured", rdt.DefaultCATWays.String())
		}
		if rdt.ClosCATWays != nil {
			t.Fatalf("ClosCATWays = %v, want nil", rdt.ClosCATWays)
		}
	})
}
