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

package provision

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/util/sets"

	configapi "github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
)

func TestRestrictProvisionControlKnob(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		originControlKnob map[types.CPUProvisionPolicyName]types.ControlKnob
		wantControlKnob   map[types.CPUProvisionPolicyName]types.ControlKnob
	}{
		{
			name:              "no restriction",
			originControlKnob: map[types.CPUProvisionPolicyName]types.ControlKnob{"p1": {"c1": types.ControlKnobItem{Value: 8}}, "p2": {"c1": types.ControlKnobItem{Value: 10}}},
			wantControlKnob:   map[types.CPUProvisionPolicyName]types.ControlKnob{"p1": {"c1": types.ControlKnobItem{Value: 8}}, "p2": {"c1": types.ControlKnobItem{Value: 10}}},
		},
		{
			name:              "restricted by p2",
			originControlKnob: map[types.CPUProvisionPolicyName]types.ControlKnob{"p1": {configapi.ControlKnobReclaimedCoresCPUQuota: types.ControlKnobItem{Value: 16}}, "p2": {configapi.ControlKnobReclaimedCoresCPUQuota: types.ControlKnobItem{Value: 10}}},
			wantControlKnob:   map[types.CPUProvisionPolicyName]types.ControlKnob{"p1": {configapi.ControlKnobReclaimedCoresCPUQuota: types.ControlKnobItem{Value: 10}}, "p2": {configapi.ControlKnobReclaimedCoresCPUQuota: types.ControlKnobItem{Value: 10}}},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			conf, err := options.NewOptions().Config()
			require.NoError(t, err)
			require.NotNil(t, conf)

			conf.RestrictRefPolicy = map[types.CPUProvisionPolicyName]types.CPUProvisionPolicyName{"p1": "p2"}

			m := &Manager{
				conf:                conf,
				restrictedCtrlKnobs: sets.NewString(string(configapi.ControlKnobReclaimedCoresCPUQuota)),
			}

			restrictedControlKnobs := m.restrictProvisionControlKnob(tt.originControlKnob)
			assert.Equal(t, tt.wantControlKnob, restrictedControlKnobs)
		})
	}
}
