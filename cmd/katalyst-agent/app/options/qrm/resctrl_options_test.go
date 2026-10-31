/*
Copyright 2026 The Katalyst Authors.

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

	"github.com/stretchr/testify/require"
	cliflag "k8s.io/component-base/cli/flag"

	qrmresctrl "github.com/kubewharf/katalyst-core/pkg/config/agent/qrm/resctrl"
)

func TestResctrlOptionsApplyToValidatesSharedPoolMappings(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		mapping map[string]int
		wantErr string
	}{
		{name: "empty", mapping: map[string]int{}},
		{name: "non-default pool", mapping: map[string]int{"batch": 30}},
		{name: "NUMA pool", mapping: map[string]int{"share-NUMA0": 3}},
		{name: "default share", mapping: map[string]int{"share": 30}, wantErr: "default share pool must not be configured"},
		{name: "empty pool", mapping: map[string]int{"": 30}, wantErr: "shared subgroup pool name must not be empty"},
		{name: "negative subgroup", mapping: map[string]int{"batch": -1}, wantErr: `shared subgroup for pool "batch" must be non-negative`},
		{name: "fixed CLOS", mapping: map[string]int{"dedicated": 1}, wantErr: "conflicts with a reserved clos id"},
		{name: "default physical CLOS", mapping: map[string]int{"shared-50": 1}, wantErr: "conflicts with a reserved clos id"},
		{name: "explicit physical CLOS", mapping: map[string]int{"share-50": 1}, wantErr: "conflicts with a reserved clos id"},
		{name: "legacy physical CLOS", mapping: map[string]int{"shared-30": 1}, wantErr: "conflicts with a reserved clos id"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			options := NewResctrlOptions()
			options.CPUSetPoolToSharedSubgroup = tt.mapping
			err := options.ApplyTo(qrmresctrl.NewResctrlConfig())
			if tt.wantErr == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tt.wantErr)
			}
		})
	}
}

func TestResctrlOptionsDoesNotRegisterRemovedDefaultSharedSubgroupFlag(t *testing.T) {
	t.Parallel()
	options := NewResctrlOptions()
	fss := cliflag.NamedFlagSets{}
	options.AddFlags(&fss)

	require.Nil(t, fss.FlagSet("resctrl").Lookup("resctrl-default-shared-subgroup"))
}

func TestResctrlOptionsDoesNotRegisterRuntimeSwitches(t *testing.T) {
	t.Parallel()
	options := NewResctrlOptions()

	fss := cliflag.NamedFlagSets{}
	options.AddFlags(&fss)
	fs := fss.FlagSet("resctrl")
	require.Nil(t, fs.Lookup("disable-rdt"))
	require.Nil(t, fs.Lookup("enable-resctrl-group-lifecycle-management"))

	conf := qrmresctrl.NewResctrlConfig()
	require.NoError(t, options.ApplyTo(conf))
	require.False(t, conf.EnableResctrlHint)
}

func TestQRMPluginsOptionsApplyToKeepsResctrlFlagsCompatible(t *testing.T) {
	t.Parallel()

	options := NewQRMPluginsOptions()
	fss := cliflag.NamedFlagSets{}
	options.AddFlags(&fss)
	fs := fss.FlagSet("resctrl")

	require.NoError(t, fs.Parse([]string{
		"--pod-admit-resctrl-layout-hint=true",
		"--resctrl-skip-cleanup-closids=system,legacy",
	}))

	conf := qrmresctrl.NewResctrlConfig()
	require.NoError(t, options.ResctrlOptions.ApplyTo(conf))
	require.True(t, conf.EnableResctrlHint)
	require.ElementsMatch(t, []string{"system", "legacy"}, conf.SkipCleanupClosIDs.UnsortedList())
	require.Empty(t, conf.DefaultClosIDs)
}
