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

	"github.com/stretchr/testify/require"
	cliflag "k8s.io/component-base/cli/flag"

	apiconfig "github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	dynamicqrm "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/adminqos/qrm"
)

func TestFragMemOptionsDefaultsAndFlags(t *testing.T) {
	t.Parallel()

	as := require.New(t)
	options := NewFragMemOptions()
	as.False(options.EnableFragMem)
	as.Equal(80, options.MemFragScoreAsync)
	as.Equal("madvise", options.THPDefaultConfig)
	as.Equal(85, options.THPHighOrderScoreThreshold)
	as.Empty(options.THPDefragPolicy)

	defaultConf := dynamicqrm.NewFragMemConfiguration()
	as.NoError(options.ApplyTo(defaultConf))
	as.Empty(defaultConf.THPDefragPolicy)

	fss := cliflag.NamedFlagSets{}
	options.AddFlags(&fss)
	fs := fss.FlagSet("memory_resource_plugin")
	as.NotNil(fs.Lookup("enable-setting-mem-compaction"))
	as.NotNil(fs.Lookup("qrm-memory-frag-score-async"))
	as.NotNil(fs.Lookup("qrm-memory-thp-default-config"))
	as.NotNil(fs.Lookup("qrm-memory-thp-high-order-score-threshold"))
	as.NotNil(fs.Lookup("qrm-memory-thp-defrag-policy"))
	as.Empty(fs.Lookup("qrm-memory-thp-defrag-policy").DefValue)
	as.NoError(fs.Parse([]string{
		"--enable-setting-mem-compaction=true",
		"--qrm-memory-frag-score-async=70",
		"--qrm-memory-thp-default-config=always",
		"--qrm-memory-thp-high-order-score-threshold=90",
		"--qrm-memory-thp-defrag-policy= Defer+Madvise ",
	}))

	conf := dynamicqrm.NewFragMemConfiguration()
	as.NoError(options.ApplyTo(conf))
	as.True(conf.EnableFragMem)
	as.Equal(70, conf.MemFragScoreAsync)
	as.Equal(apiconfig.THPModeAlways, conf.THPDefaultConfig)
	as.Equal(90, conf.THPHighOrderScoreThreshold)
	as.Equal(apiconfig.THPDefragPolicyDeferMadvise, conf.THPDefragPolicy)
}

func TestFragMemOptionsApplyToRejectsInvalidDefragPolicy(t *testing.T) {
	t.Parallel()

	for _, policy := range []string{"force", "deny", "advise", "within_size", "invalid"} {
		policy := policy
		t.Run(policy, func(t *testing.T) {
			t.Parallel()

			options := NewFragMemOptions()
			options.EnableFragMem = true
			options.THPDefragPolicy = policy
			conf := dynamicqrm.FragMemConfiguration{
				MemFragScoreAsync: 42,
				THPDefragPolicy:   apiconfig.THPDefragPolicyNever,
			}
			before := conf

			err := options.ApplyTo(&conf)
			require.Error(t, err)
			require.Contains(t, err.Error(), "invalid THP defrag policy")
			require.Equal(t, before, conf, "invalid options must not partially modify the configuration")
		})
	}
}
