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

package irqtuning

import (
	"testing"

	"github.com/stretchr/testify/assert"
	cliflag "k8s.io/component-base/cli/flag"

	v1alpha1 "github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options/dynamic/irqtuning/coresadjust"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options/dynamic/irqtuning/coresexclusion"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options/dynamic/irqtuning/loadbalance"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options/dynamic/irqtuning/netoverload"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options/dynamic/irqtuning/rpsexcludeirqcore"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options/dynamic/irqtuning/throughputclassswitch"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/irqtuning"
	irqdynamicconf "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/irqtuning"
)

func TestNewIRQTuningOptions(t *testing.T) {
	t.Parallel()
	options := NewIRQTuningOptions()

	assert.False(t, options.EnableTuner)
	assert.Equal(t, string(v1alpha1.TuningPolicyBalance), options.TuningPolicy)
	assert.Equal(t, 5, options.TuningInterval)
	assert.False(t, options.EnableRPS)
	assert.Equal(t, 0.0, options.EnableRPSCPUVSNicsQueue)
	assert.Equal(t, string(v1alpha1.NICAffinityPolicyCompleteMap), options.NICAffinityPolicy)
	assert.False(t, options.ReniceKsoftirqd)
	assert.Equal(t, -20, options.KsoftirqdNice)
	assert.Equal(t, 50, options.CoresExpectedCPUUtil)
	assert.NotNil(t, options.RPSExcludeIRQCoresThreshold)
	assert.NotNil(t, options.ThroughputClassSwitchOptions)
	assert.NotNil(t, options.CoreNetOverLoadThreshold)
	assert.NotNil(t, options.LoadBalanceOptions)
	assert.NotNil(t, options.CoresAdjustOptions)
	assert.NotNil(t, options.CoresExclusionOptions)
}

func TestIRQTuningOptions_AddFlags(t *testing.T) {
	t.Parallel()
	fss := cliflag.NamedFlagSets{}
	options := NewIRQTuningOptions()
	options.AddFlags(&fss)

	// Verify that flags are added correctly
	flagSet := fss.FlagSet("irq-tuning")
	assert.NotNil(t, flagSet.Lookup("enable-tuner"))
	assert.NotNil(t, flagSet.Lookup("tuning-policy"))
	assert.NotNil(t, flagSet.Lookup("tuning-interval"))
	assert.NotNil(t, flagSet.Lookup("enable-rps"))
	assert.NotNil(t, flagSet.Lookup("enable-rps-cpu-vs-nics-queue"))
	assert.NotNil(t, flagSet.Lookup("nic-affinity-policy"))
	assert.NotNil(t, flagSet.Lookup("renice-ksoftirqd"))
	assert.NotNil(t, flagSet.Lookup("ksoftirqd-nice"))
	assert.NotNil(t, flagSet.Lookup("cores-expected-cpu-util"))
}

func TestIRQTuningOptions_ApplyTo(t *testing.T) {
	t.Parallel()
	type testCase struct {
		name     string
		options  *IRQTuningOptions
		expected *irqdynamicconf.IRQTuningConfiguration
		wantErr  bool
	}

	cases := []testCase{
		{
			name:    "default options",
			options: NewIRQTuningOptions(),
			expected: &irqdynamicconf.IRQTuningConfiguration{
				EnableTuner:             false,
				TuningPolicy:            v1alpha1.TuningPolicyBalance,
				TuningInterval:          5,
				EnableRPS:               false,
				EnableRPSCPUVSNicsQueue: 0,
				NICAffinityPolicy:       v1alpha1.NICAffinityPolicyCompleteMap,
				ReniceKsoftirqd:         false,
				KsoftirqdNice:           -20,
				CoresExpectedCPUUtil:    50,
			},
			wantErr: false,
		},
		{
			name: "custom options",
			options: &IRQTuningOptions{
				EnableTuner:                  true,
				TuningPolicy:                 string(v1alpha1.TuningPolicyExclusive),
				TuningInterval:               10,
				EnableRPS:                    true,
				EnableRPSCPUVSNicsQueue:      2.5,
				NICAffinityPolicy:            string(v1alpha1.NICAffinityPolicyOverallBalance),
				ReniceKsoftirqd:              true,
				KsoftirqdNice:                -10,
				CoresExpectedCPUUtil:         70,
				RPSExcludeIRQCoresThreshold:  rpsexcludeirqcore.NewRPSExcludeIRQCoresThreshold(),
				ThroughputClassSwitchOptions: throughputclassswitch.NewThroughputClassSwitchOptions(),
				CoreNetOverLoadThreshold:     netoverload.NewIRQCoreNetOverloadThresholdOptions(),
				LoadBalanceOptions:           loadbalance.NewIRQLoadBalanceOptions(),
				CoresAdjustOptions:           coresadjust.NewIRQCoresAdjustOptions(),
				CoresExclusionOptions:        coresexclusion.NewIRQCoresExclusionOptions(),
			},
			expected: &irqdynamicconf.IRQTuningConfiguration{
				EnableTuner:             true,
				TuningPolicy:            v1alpha1.TuningPolicyExclusive,
				TuningInterval:          10,
				EnableRPS:               true,
				EnableRPSCPUVSNicsQueue: 2.5,
				NICAffinityPolicy:       v1alpha1.NICAffinityPolicyOverallBalance,
				ReniceKsoftirqd:         true,
				KsoftirqdNice:           -10,
				CoresExpectedCPUUtil:    70,
			},
			wantErr: false,
		},
	}

	for _, tc := range cases {
		c := irqtuning.NewIRQTuningConfiguration()
		err := tc.options.ApplyTo(c)

		if tc.wantErr {
			assert.Error(t, err, tc.name)
		} else {
			assert.NoError(t, err, tc.name)
			assert.Equal(t, tc.expected.EnableTuner, c.EnableTuner, tc.name)
			assert.Equal(t, tc.expected.TuningPolicy, c.TuningPolicy, tc.name)
			assert.Equal(t, tc.expected.TuningInterval, c.TuningInterval, tc.name)
			assert.Equal(t, tc.expected.EnableRPS, c.EnableRPS, tc.name)
			assert.Equal(t, tc.expected.EnableRPSCPUVSNicsQueue, c.EnableRPSCPUVSNicsQueue, tc.name)
			assert.Equal(t, tc.expected.NICAffinityPolicy, c.NICAffinityPolicy, tc.name)
			assert.Equal(t, tc.expected.ReniceKsoftirqd, c.ReniceKsoftirqd, tc.name)
			assert.Equal(t, tc.expected.KsoftirqdNice, c.KsoftirqdNice, tc.name)
			assert.Equal(t, tc.expected.CoresExpectedCPUUtil, c.CoresExpectedCPUUtil, tc.name)
		}
	}
}
