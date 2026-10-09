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

package region

import (
	"testing"

	"github.com/stretchr/testify/require"

	configapi "github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// TestQoSRegionUpdateStatusGetStatusRace pins the C10 lock discipline:
// UpdateStatus writes r.regionStatus (BoundType + the OvershootStatus map) while
// GetStatus reads/clones it. Both must hold the region mutex, so they can run
// concurrently without a data race. Run under -race this is the regression guard:
// before the fix UpdateStatus wrote regionStatus lock-free.
func TestQoSRegionUpdateStatusGetStatusRace(t *testing.T) {
	t.Parallel()

	f := newDedicatedIndicatorFixture(t, 0, types.TopologyAwareAssignment{
		0: machine.NewCPUSet(0),
	})
	dynamicConf := f.conf.GetDynamicConfiguration()
	f.region.SetEssentials(types.ResourceEssentials{DynamicConfiguration: dynamicConf})

	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
				f.region.UpdateStatus()
			}
		}
	}()
	for i := 0; i < 200; i++ {
		_ = f.region.GetStatus()
	}
	close(stop)
	<-done

	// Sanity: a status was produced and is cloneable without error.
	status := f.region.GetStatus()
	require.NotEmpty(t, status)
}

// TestQoSRegionUpdateStatusPublishesBoundType verifies the functional contract:
// after UpdateStatus, GetStatus observes the BoundType derived from the control
// essentials, proving the locked write is visible through the locked read.
func TestQoSRegionUpdateStatusPublishesBoundType(t *testing.T) {
	t.Parallel()

	f := newDedicatedIndicatorFixture(t, 0, types.TopologyAwareAssignment{
		0: machine.NewCPUSet(0),
	})
	dynamicConf := f.conf.GetDynamicConfiguration()
	f.region.SetEssentials(types.ResourceEssentials{
		DynamicConfiguration: dynamicConf,
		ResourceUpperBound:   90,
		ResourceLowerBound:   4,
	})
	f.region.ControlEssentials = types.ControlEssentials{
		ControlKnobs: types.ControlKnob{
			configapi.ControlKnobNonReclaimedCPURequirement: {Value: 3},
		},
	}

	f.region.UpdateStatus()
	status := f.region.GetStatus()

	// value 3 <= lower bound 4 => BoundLower.
	require.Equal(t, types.BoundLower, status.BoundType)
}
