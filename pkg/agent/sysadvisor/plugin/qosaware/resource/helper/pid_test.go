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

package helper

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
)

func TestPIDControllerAdjustHandlesZeroCurrent(t *testing.T) {
	t.Parallel()

	controller := NewPIDController("test", types.FirstOrderPIDParams{
		Kpn:                  0.01,
		Kdp:                  1,
		AdjustmentUpperBound: 10,
		AdjustmentLowerBound: -2,
		DeadbandLowerPct:     0.8,
	}, "")
	controller.SetEssentials(types.ResourceEssentials{
		ResourceLowerBound: 4,
		ResourceUpperBound: 90,
	})

	controlKnob := controller.Adjust(40, 460, 0, false)
	require.Equal(t, 38.0, controlKnob)

	controlKnob = controller.Adjust(controlKnob, 460, 0, false)
	require.Equal(t, 36.0, controlKnob)

	controlKnob = controller.Adjust(controlKnob, 460, 4, false)
	require.Equal(t, 34.0, controlKnob)
}
