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

package memory

import (
	"testing"

	"github.com/spf13/pflag"
	"github.com/stretchr/testify/require"

	memoryconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/sysadvisor/qosaware/resource/memory"
)

func TestReclaimedMemoryLimitMargin(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		value   string
		want    int64
		invalid bool
	}{
		{name: "default is disabled"},
		{name: "explicit zero", value: "0"},
		{name: "binary quantity", value: "1Gi", want: 1 << 30},
		{name: "byte quantity", value: "513", want: 513},
		{name: "maximum bytes", value: "9223372036854775807", want: 9223372036854775807},
		{name: "negative rejected", value: "-1Gi", invalid: true},
		{name: "overflow rejected", value: "9223372036854775808", invalid: true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			opts := NewMemoryAdvisorOptions()
			flags := pflag.NewFlagSet(tc.name, pflag.ContinueOnError)
			opts.AddFlags(flags)
			if tc.value != "" {
				require.NoError(t, flags.Parse([]string{"--memory-advisor-reclaimed-memory-limit-margin=" + tc.value}))
			}
			conf := memoryconfig.NewMemoryAdvisorConfiguration()
			conf.ReclaimedMemoryLimitMargin = 17
			err := opts.ApplyTo(conf)
			if tc.invalid {
				require.Error(t, err)
				require.Equal(t, int64(17), conf.ReclaimedMemoryLimitMargin)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, conf.ReclaimedMemoryLimitMargin)
		})
	}
}
