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

package bulkhead

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	cliflag "k8s.io/component-base/cli/flag"

	bulkheadconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/qrm/bulkhead"
)

func TestBulkheadOptionsAddFlagsMaxCPUsDrainRatio(t *testing.T) {
	t.Parallel()

	options := NewBulkheadOptions()
	fss := &cliflag.NamedFlagSets{}
	options.AddFlags(fss)

	fs := fss.FlagSet("cpu_resource_plugin")
	flag := fs.Lookup("qrm-cpu-bulkhead-max-cpus-drain-ratio")
	require.NotNil(t, flag)
	require.Equal(t, "0", flag.DefValue)

	require.NoError(t, fs.Set("qrm-cpu-bulkhead-max-cpus-drain-ratio", "0.25"))
	require.Equal(t, 0.25, options.MaxCPUsDrainRatio)
}

func TestBulkheadOptionsDefaultDisablesPartitionRelPaths(t *testing.T) {
	t.Parallel()

	options := NewBulkheadOptions()
	require.Empty(t, options.BulkheadPartitionRelPaths)

	conf := bulkheadconfig.NewBulkheadConfiguration()
	require.NoError(t, options.ApplyTo(conf))
	require.Empty(t, conf.BulkheadPartitionRelPaths)
}

func TestBulkheadOptionsExposePreserveReclaimCPUSetWhenTopologyDisabled(t *testing.T) {
	t.Parallel()

	options := NewBulkheadOptions()
	require.False(t, options.PreserveReclaimCPUSetWhenTopologyDisabled)

	fss := &cliflag.NamedFlagSets{}
	options.AddFlags(fss)
	fs := fss.FlagSet("cpu_resource_plugin")
	flag := fs.Lookup("qrm-cpu-bulkhead-preserve-reclaim-cpuset-when-topology-disabled")
	require.NotNil(t, flag)
	require.Equal(t, "false", flag.DefValue)

	require.NoError(t, fs.Set("qrm-cpu-bulkhead-preserve-reclaim-cpuset-when-topology-disabled", "true"))
	conf := bulkheadconfig.NewBulkheadConfiguration()
	require.NoError(t, options.ApplyTo(conf))
	require.True(t, conf.PreserveReclaimCPUSetWhenTopologyDisabled)

	require.NoError(t, fs.Set("qrm-cpu-bulkhead-preserve-reclaim-cpuset-when-topology-disabled", "false"))
	require.NoError(t, options.ApplyTo(conf))
	require.False(t, conf.PreserveReclaimCPUSetWhenTopologyDisabled)
}

func TestBulkheadOptionsDefaultsReclaimSiblingSystem(t *testing.T) {
	t.Parallel()

	options := NewBulkheadOptions()
	require.Equal(t, []string{"system"}, options.BulkheadReclaimSiblingRelPaths)
	require.True(t, options.EnableBulkheadReclaimSiblings)
	require.Equal(t, "system", options.BulkheadSystemRelPath)

	conf := bulkheadconfig.NewBulkheadConfiguration()
	require.NoError(t, options.ApplyTo(conf))
	require.Equal(t, []string{"system"}, conf.BulkheadReclaimSiblingRelPaths)
}

func TestBulkheadOptionsCanExplicitlyEnablePartitionRelPaths(t *testing.T) {
	t.Parallel()

	options := NewBulkheadOptions()
	fss := &cliflag.NamedFlagSets{}
	options.AddFlags(fss)

	fs := fss.FlagSet("cpu_resource_plugin")
	require.NoError(t, fs.Set("qrm-cpu-bulkhead-partition-rel-paths", "kubepods,system"))

	conf := bulkheadconfig.NewBulkheadConfiguration()
	require.NoError(t, options.ApplyTo(conf))
	require.Equal(t, []string{"kubepods", "system"}, conf.BulkheadPartitionRelPaths)
}

func TestBulkheadOptionsApplyToValidatesMaxCPUsDrainRatio(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		ratio   float64
		wantErr bool
	}{
		{name: "zero_disables_limit", ratio: 0},
		{name: "fraction", ratio: 0.25},
		{name: "one", ratio: 1},
		{name: "negative", ratio: -0.01, wantErr: true},
		{name: "greater_than_one", ratio: 1.01, wantErr: true},
		{name: "nan", ratio: math.NaN(), wantErr: true},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			options := NewBulkheadOptions()
			options.MaxCPUsDrainRatio = tc.ratio
			conf := bulkheadconfig.NewBulkheadConfiguration()

			err := options.ApplyTo(conf)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.ratio, conf.TopologyDrainSelection.MaxCPUsDrainRatio)
		})
	}
}

func TestBulkheadOptionsExposeConvergenceDeadlineAndDeadlockProbeBudget(t *testing.T) {
	t.Parallel()

	options := NewBulkheadOptions()
	fss := &cliflag.NamedFlagSets{}
	options.AddFlags(fss)
	fs := fss.FlagSet("cpu_resource_plugin")

	require.NotNil(t, fs.Lookup("qrm-cpu-bulkhead-topology-convergence-deadline"))
	require.NotNil(t, fs.Lookup("qrm-cpu-bulkhead-deadlock-probe-operations"))
	require.NoError(t, fs.Set("qrm-cpu-bulkhead-topology-convergence-deadline", "750ms"))
	require.NoError(t, fs.Set("qrm-cpu-bulkhead-deadlock-probe-operations", "123"))

	conf := bulkheadconfig.NewBulkheadConfiguration()
	require.NoError(t, options.ApplyTo(conf))
	require.Equal(t, 750*time.Millisecond, conf.TopologyConvergenceBudget.DeadlineDuration)
	require.Equal(t, 123, conf.TopologyConvergenceBudget.MaxDeadlockProbeOperations)
}

func TestBulkheadOptionsExposeAdmissionBudget(t *testing.T) {
	t.Parallel()

	options := NewBulkheadOptions()
	fss := &cliflag.NamedFlagSets{}
	options.AddFlags(fss)
	fs := fss.FlagSet("cpu_resource_plugin")

	require.NotNil(t, fs.Lookup("qrm-cpu-bulkhead-admission-leaf-defer"))
	require.NotNil(t, fs.Lookup("qrm-cpu-bulkhead-admission-max-required-writes"))
	require.NotNil(t, fs.Lookup("qrm-cpu-bulkhead-admission-safe-duration"))
	require.NoError(t, fs.Set("qrm-cpu-bulkhead-admission-leaf-defer", "false"))
	require.NoError(t, fs.Set("qrm-cpu-bulkhead-admission-max-required-writes", "64"))
	require.NoError(t, fs.Set("qrm-cpu-bulkhead-admission-safe-duration", "750ms"))

	conf := bulkheadconfig.NewBulkheadConfiguration()
	require.NoError(t, options.ApplyTo(conf))
	require.False(t, conf.EnableAdmissionLeafDefer)
	require.Equal(t, 64, conf.AdmissionMaxRequiredWrites)
	require.Equal(t, 750*time.Millisecond, conf.AdmissionSafeDuration)
}

func TestBulkheadOptionsRejectInvalidAdmissionBudget(t *testing.T) {
	t.Parallel()

	options := NewBulkheadOptions()
	options.AdmissionMaxRequiredWrites = -1
	require.Error(t, options.ApplyTo(bulkheadconfig.NewBulkheadConfiguration()))

	options = NewBulkheadOptions()
	options.AdmissionSafeDuration = 0
	require.Error(t, options.ApplyTo(bulkheadconfig.NewBulkheadConfiguration()))
}

func TestBulkheadOptionsDefaultDeadlockProbeBudgetUsesAuto(t *testing.T) {
	t.Parallel()

	options := NewBulkheadOptions()
	require.Equal(t, 0, options.MaxDeadlockProbeOperations)

	conf := bulkheadconfig.NewBulkheadConfiguration()
	require.NoError(t, options.ApplyTo(conf))
	require.Equal(t, 0, conf.TopologyConvergenceBudget.MaxDeadlockProbeOperations)
}

func TestBulkheadOptionsRejectInvalidConvergenceDeadlineAndProbeBudget(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		deadline time.Duration
		probe    int
	}{
		{name: "zero_deadline", deadline: 0, probe: 1},
		{name: "negative_deadline", deadline: -time.Second, probe: 1},
		{name: "negative_probe", deadline: time.Second, probe: -1},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			options := NewBulkheadOptions()
			options.TopologyConvergenceDeadline = tc.deadline
			options.MaxDeadlockProbeOperations = tc.probe
			require.Error(t, options.ApplyTo(bulkheadconfig.NewBulkheadConfiguration()))
		})
	}
}
