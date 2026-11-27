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
	"fmt"
	"strconv"
	"strings"

	apiconfig "github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/crd"
)

// ParseTHPDefragPolicy normalizes and validates a THP defrag policy.
// An empty policy leaves the host's current defrag policy unchanged.
func ParseTHPDefragPolicy(policy string) (apiconfig.THPDefragPolicy, error) {
	normalizedPolicy := apiconfig.THPDefragPolicy(strings.TrimSpace(strings.ToLower(policy)))
	if normalizedPolicy == "" {
		return "", nil
	}

	if err := validateTHPDefragPolicy(normalizedPolicy); err != nil {
		return "", err
	}
	return normalizedPolicy, nil
}

func validateTHPDefragPolicy(policy apiconfig.THPDefragPolicy) error {
	switch policy {
	case apiconfig.THPDefragPolicyAlways, apiconfig.THPDefragPolicyDefer, apiconfig.THPDefragPolicyDeferMadvise,
		apiconfig.THPDefragPolicyMadvise, apiconfig.THPDefragPolicyNever:
		return nil
	default:
		return fmt.Errorf("invalid THP defrag policy %q, expected one of %q/%q/%q/%q/%q", policy,
			apiconfig.THPDefragPolicyAlways, apiconfig.THPDefragPolicyDefer, apiconfig.THPDefragPolicyDeferMadvise,
			apiconfig.THPDefragPolicyMadvise, apiconfig.THPDefragPolicyNever)
	}
}

// ParseVMCompactionProactiveness parses an optional startup target.
// An empty value leaves the host value unchanged unless configured by AQC.
func ParseVMCompactionProactiveness(input string) (*int64, error) {
	if input == "" {
		return nil, nil
	}
	value, err := strconv.ParseInt(strings.TrimSpace(input), 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid vm.compaction_proactiveness %q: must be an integer in [0, 100]", input)
	}
	if err := validateVMCompactionProactiveness(value); err != nil {
		return nil, err
	}
	return &value, nil
}

func validateVMCompactionProactiveness(value int64) error {
	if value < 0 || value > 100 {
		return fmt.Errorf("invalid vm.compaction_proactiveness %d: must be an integer in [0, 100]", value)
	}
	return nil
}

type FragMemConfiguration struct {
	EnableFragMem              bool
	MemFragScoreAsync          int
	THPDefaultConfig           apiconfig.THPMode
	THPHighOrderScoreThreshold int
	THPStaticEnableConfig      *THPStaticEnableConfiguration
	// THPDefragPolicy is applied when non-empty and EnableFragMem is true, independently of THP mode tuning.
	// An empty policy leaves the host's current defrag policy unchanged.
	THPDefragPolicy apiconfig.THPDefragPolicy
	// VMCompactionProactiveness sets vm.compaction_proactiveness in [0, 100],
	// independently of EnableFragMem. Nil preserves the host value;
	// the actual value is reported regardless.
	VMCompactionProactiveness *int64
}

type THPStaticEnableConfiguration struct {
	Enable bool
	THP    apiconfig.THPMode
	THPShm apiconfig.THPShmMode
}

func NewFragMemConfiguration() *FragMemConfiguration {
	return &FragMemConfiguration{}
}

func getFragMemConfig(conf *crd.DynamicConfigCRD) *apiconfig.FragMemConfig {
	if aqc := conf.AdminQoSConfiguration; aqc != nil &&
		aqc.Spec.Config.QRMPluginConfig != nil &&
		aqc.Spec.Config.QRMPluginConfig.MemoryPluginConfig != nil {
		return aqc.Spec.Config.QRMPluginConfig.MemoryPluginConfig.FragMemConfig
	}
	return nil
}

// ValidateConfiguration checks explicit AQC settings before the configuration is applied.
// An explicit empty THP policy is invalid; omitted fields preserve startup settings.
func (c *FragMemConfiguration) ValidateConfiguration(conf *crd.DynamicConfigCRD) error {
	if config := getFragMemConfig(conf); config != nil {
		if config.THPDefragPolicy != nil {
			if err := validateTHPDefragPolicy(*config.THPDefragPolicy); err != nil {
				return err
			}
		}
		if config.VMCompactionProactiveness != nil {
			if err := validateVMCompactionProactiveness(*config.VMCompactionProactiveness); err != nil {
				return err
			}
		}
	}
	return nil
}

func (c *FragMemConfiguration) ApplyConfiguration(conf *crd.DynamicConfigCRD) {
	if config := getFragMemConfig(conf); config != nil {
		if config.EnableFragMem != nil {
			c.EnableFragMem = *config.EnableFragMem
		}
		if config.MemFragScoreAsync != nil {
			c.MemFragScoreAsync = int(*config.MemFragScoreAsync)
		}
		if config.THPDefaultConfig != nil {
			c.THPDefaultConfig = apiconfig.THPMode(strings.TrimSpace(strings.ToLower(*config.THPDefaultConfig)))
		}
		if config.THPDefragPolicy != nil {
			c.THPDefragPolicy = *config.THPDefragPolicy
		}
		if config.VMCompactionProactiveness != nil {
			value := *config.VMCompactionProactiveness
			c.VMCompactionProactiveness = &value
		}
		if config.THPHighOrderScoreThreshold != nil {
			c.THPHighOrderScoreThreshold = int(*config.THPHighOrderScoreThreshold)
		}
		if config.THPStaticEnableConfig != nil {
			c.THPStaticEnableConfig = &THPStaticEnableConfiguration{}
			if config.THPStaticEnableConfig.Enable != nil {
				c.THPStaticEnableConfig.Enable = *config.THPStaticEnableConfig.Enable
			}
			if config.THPStaticEnableConfig.THP != nil {
				c.THPStaticEnableConfig.THP = apiconfig.THPMode(strings.TrimSpace(strings.ToLower(string(*config.THPStaticEnableConfig.THP))))
			}
			if config.THPStaticEnableConfig.THPShm != nil {
				c.THPStaticEnableConfig.THPShm = apiconfig.THPShmMode(strings.TrimSpace(strings.ToLower(string(*config.THPStaticEnableConfig.THPShm))))
			}
		} else {
			c.THPStaticEnableConfig = nil
		}
	}
}
