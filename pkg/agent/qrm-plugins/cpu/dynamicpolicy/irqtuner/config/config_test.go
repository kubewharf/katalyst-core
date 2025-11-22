//go:build linux
// +build linux

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

package config

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	dynconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/irqtuning"
)

func Test_String(t *testing.T) {
	t.Parallel()

	conf := &IrqTuningConfig{
		Interval:        5,
		EnableIrqTuning: false,
		IrqTuningPolicy: IrqTuningBalanceFair,
		NormalThroughputNics: []NicInfo{
			{
				NicName: "eth0",
			},
			{
				NicName:   "eth2",
				NetNSName: "ns2",
			},
		},
	}

	expectedLines := []string{
		"    NormalThroughputNics:",
		"        NicName: eth0",
		"        NetNSName: ",
		"        NicName: eth2",
		"        NetNSName: ns2",
	}

	t.Run("format config string", func(t *testing.T) {
		msg := conf.String()
		lines := strings.Split(msg, "\n")
		for _, line := range expectedLines {
			assert.Contains(t, lines, line)
		}
	})
}

func Test_ValidateIrqTuningDynamicConfig(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name          string
		conf          *dynconfig.Configuration
		expectedError error
	}{{
		name: "Scenario 1: succeed to validate normal throughput nics conf",
		conf: &dynconfig.Configuration{
			IRQTuningConfiguration: &irqtuning.IRQTuningConfiguration{
				TuningInterval:       5,
				KsoftirqdNice:        0,
				CoresExpectedCPUUtil: 50,
				NormalThroughputNics: []string{"eth0", "ns2/eth2"},
			},
		},
		expectedError: nil,
	}, {
		name: "Scenario 2: failed to validate normal throughput nics conf",
		conf: &dynconfig.Configuration{
			IRQTuningConfiguration: &irqtuning.IRQTuningConfiguration{
				TuningInterval:       5,
				KsoftirqdNice:        0,
				CoresExpectedCPUUtil: 50,
				NormalThroughputNics: []string{"eth0", "ns2/eth5/xyz"},
			},
		},
		expectedError: errors.New("invalid nic: ns2/eth5/xyz"),
	}}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateIrqTuningDynamicConfig(tc.conf)
			assert.Equal(t, err, tc.expectedError)
		})
	}
}

func Test_ConvertDynamicConfigToIrqTuningConfig(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name                         string
		conf                         *dynconfig.Configuration
		expectedNormalThroughputNics []NicInfo
	}{{
		name: "Scenario 1: succeed to ConvertDynamicConfigToIrqTuningConfig",
		conf: &dynconfig.Configuration{
			IRQTuningConfiguration: &irqtuning.IRQTuningConfiguration{
				TuningInterval:       5,
				TuningPolicy:         v1alpha1.TuningPolicyBalance,
				NICAffinityPolicy:    v1alpha1.NICAffinityPolicyCompleteMap,
				KsoftirqdNice:        0,
				CoresExpectedCPUUtil: 50,
				NormalThroughputNics: []string{"eth0", "ns2/eth2"},
			},
		},
		expectedNormalThroughputNics: []NicInfo{
			{
				NicName: "eth0",
			},
			{
				NicName:   "eth2",
				NetNSName: "ns2",
			},
		},
	}, {
		name: "Scenario 2: parse invalid NormalThroughput Nic",
		conf: &dynconfig.Configuration{
			IRQTuningConfiguration: &irqtuning.IRQTuningConfiguration{
				TuningInterval:       5,
				TuningPolicy:         v1alpha1.TuningPolicyBalance,
				NICAffinityPolicy:    v1alpha1.NICAffinityPolicyCompleteMap,
				KsoftirqdNice:        0,
				CoresExpectedCPUUtil: 50,
				NormalThroughputNics: []string{"eth0", "ns2/eth5/xyz"},
			},
		},
		expectedNormalThroughputNics: []NicInfo{
			{
				NicName: "eth0",
			},
		},
	}}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			conf := ConvertDynamicConfigToIrqTuningConfig(tc.conf)
			assert.Equal(t, conf.NormalThroughputNics, tc.expectedNormalThroughputNics)
		})
	}
}
