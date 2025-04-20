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

package featuregatenegotiation

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/advisorsvc"
	"github.com/kubewharf/katalyst-core/pkg/agent/utilcomponent/featuregatenegotiation/finders"
)

func Test_GetWantedButNotSupportedFeatureGates(t *testing.T) {
	t.Run("empty wanted feature gates", func(t *testing.T) {
		t.Parallel()
		wanted := make(map[string]*advisorsvc.FeatureGate)
		supported := map[string]*advisorsvc.FeatureGate{
			"gate1": {Name: "gate1"},
		}

		result := GetWantedButNotSupportedFeatureGates(wanted, supported)
		assert.Empty(t, result)
	})

	t.Run("empty supported feature gates", func(t *testing.T) {
		t.Parallel()
		wanted := map[string]*advisorsvc.FeatureGate{
			"gate1": {Name: "gate1"},
			"gate2": {Name: "gate2"},
		}
		supported := make(map[string]*advisorsvc.FeatureGate)

		result := GetWantedButNotSupportedFeatureGates(wanted, supported)
		require.Len(t, result, 2)
		assert.Contains(t, result, "gate1")
		assert.Contains(t, result, "gate2")
	})

	t.Run("some wanted gates not supported", func(t *testing.T) {
		t.Parallel()
		wanted := map[string]*advisorsvc.FeatureGate{
			"gate1": {Name: "gate1"},
			"gate2": {Name: "gate2"},
			"gate3": {Name: "gate3"},
		}
		supported := map[string]*advisorsvc.FeatureGate{
			"gate1": {Name: "gate1"},
			"gate3": {Name: "gate3"},
		}

		result := GetWantedButNotSupportedFeatureGates(wanted, supported)
		require.Len(t, result, 1)
		assert.Contains(t, result, "gate2")
		assert.NotContains(t, result, "gate1")
		assert.NotContains(t, result, "gate3")
	})

	t.Run("all wanted gates are supported", func(t *testing.T) {
		t.Parallel()
		wanted := map[string]*advisorsvc.FeatureGate{
			"gate1": {Name: "gate1"},
			"gate2": {Name: "gate2"},
		}
		supported := map[string]*advisorsvc.FeatureGate{
			"gate1": {Name: "gate1"},
			"gate2": {Name: "gate2"},
		}

		result := GetWantedButNotSupportedFeatureGates(wanted, supported)
		assert.Empty(t, result)
	})
}

func Test_GenerateSupportedWantedFeatureGates(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name               string
		wantedFeatureGates map[string]*advisorsvc.FeatureGate
		featureGateType    string
		expectResult       map[string]*advisorsvc.FeatureGate
		expectError        bool
		errorMsg           string
	}{
		{
			name: "error - wrong feature gate type",
			wantedFeatureGates: map[string]*advisorsvc.FeatureGate{
				"fg1": {Name: "fg1", Type: "wrong_type"},
			},
			featureGateType: finders.FeatureGateTypeCPU,
			expectError:     true,
			errorMsg:        "unexpected feature gate fg1",
		},
		{
			name: "error - must-mutually-supported feature gate not supported",
			wantedFeatureGates: map[string]*advisorsvc.FeatureGate{
				"fg1": {Name: "fg1", Type: finders.FeatureGateTypeCPU, MustMutuallySupported: true},
			},
			featureGateType: finders.FeatureGateTypeCPU,
			expectError:     true,
			errorMsg:        "feature gate fg1 is not supported by sysadvisor",
		},
		{
			name: "success - non-must-mutually-supported feature gate not supported",
			wantedFeatureGates: map[string]*advisorsvc.FeatureGate{
				"fg1": {Name: "fg1", Type: finders.FeatureGateTypeCPU, MustMutuallySupported: false},
			},
			featureGateType: finders.FeatureGateTypeCPU,
			expectResult:    map[string]*advisorsvc.FeatureGate{},
		},
		{
			name:               "success - empty input",
			wantedFeatureGates: nil,
			featureGateType:    finders.FeatureGateTypeCPU,
			expectResult:       map[string]*advisorsvc.FeatureGate{},
		},
	}

	for _, testcase := range tests {
		tt := testcase
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result, err := GenerateSupportedWantedFeatureGates(tt.wantedFeatureGates, tt.featureGateType)

			if tt.expectError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorMsg)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.expectResult, result)
		})
	}
}
