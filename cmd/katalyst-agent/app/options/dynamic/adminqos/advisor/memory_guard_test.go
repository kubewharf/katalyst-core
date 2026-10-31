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

package advisor

import (
	"testing"

	"github.com/stretchr/testify/require"
	cliflag "k8s.io/component-base/cli/flag"

	configv1alpha1 "github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	advisorconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/adminqos/advisor"
)

func TestNewMemoryGuardOptions(t *testing.T) {
	t.Parallel()

	require.Equal(t, string(configv1alpha1.CriticalWatermarkSourceLow), NewMemoryGuardOptions().CriticalWatermarkSource)
}

func TestMemoryGuardOptionsAddFlags(t *testing.T) {
	t.Parallel()

	fss := &cliflag.NamedFlagSets{}
	NewMemoryGuardOptions().AddFlags(fss)

	flag := fss.FlagSet("memory-guard").Lookup("memory-advisor-critical-watermark-source")
	require.NotNil(t, flag)
	require.NoError(t, flag.Value.Set(string(configv1alpha1.CriticalWatermarkSourceHigh)))
	require.Equal(t, string(configv1alpha1.CriticalWatermarkSourceHigh), flag.Value.String())
	require.NoError(t, flag.Value.Set(""))
	require.Empty(t, flag.Value.String())
}

func TestMemoryGuardOptionsApplyTo(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		source  string
		want    configv1alpha1.CriticalWatermarkSource
		wantErr bool
	}{
		{name: "low", source: string(configv1alpha1.CriticalWatermarkSourceLow), want: configv1alpha1.CriticalWatermarkSourceLow},
		{name: "high", source: string(configv1alpha1.CriticalWatermarkSourceHigh), want: configv1alpha1.CriticalWatermarkSourceHigh},
		{name: "empty", source: "", want: configv1alpha1.CriticalWatermarkSourceLow},
		{name: "uppercase", source: "LOW", wantErr: true},
		{name: "invalid", source: "critical", wantErr: true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			options := NewMemoryGuardOptions()
			options.CriticalWatermarkSource = tt.source
			conf := advisorconfig.NewMemoryGuardConfiguration()

			err := options.ApplyTo(conf)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, conf.CriticalWatermarkSource)
		})
	}
}
