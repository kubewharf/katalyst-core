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

	workloadapis "github.com/kubewharf/katalyst-api/pkg/apis/workload/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// TestFetchGetterUnregisteredDefaultFallsBackToPassthrough pins the nil-guard
// contract: when neither the requested getter nor the configured default getter
// is registered, fetchGetter must return a non-nil passthrough getter that keeps
// the default target, instead of a nil function that would panic on invocation.
func TestFetchGetterUnregisteredDefaultFallsBackToPassthrough(t *testing.T) {
	t.Parallel()

	f := newDedicatedIndicatorFixture(t, 0, types.TopologyAwareAssignment{
		0: machine.NewCPUSet(0),
	})
	dynamicConf := f.conf.GetDynamicConfiguration()
	// Default getter name that is NOT present in indicatorTargetGetters.
	dynamicConf.IndicatorTargetDefaultGetter = "not-registered-default"
	dynamicConf.IndicatorTargetGetters = map[string]string{}
	f.region.SetEssentials(types.ResourceEssentials{DynamicConfiguration: dynamicConf})

	getter, getterName := f.region.fetchGetter(workloadapis.ServiceSystemIndicatorNameCPUSchedWait)
	require.NotNil(t, getter, "fetchGetter must never return a nil function")
	require.Equal(t, "passthrough", getterName)

	// The passthrough getter returns the configured default target unchanged.
	require.Equal(t, 42.0, getter(workloadapis.ServiceSystemIndicatorNameCPUSchedWait, 42.0))
}

// TestFetchGetterRegisteredDefaultIsUsed pins the happy path: when the default
// getter IS registered, it is returned and applied normally.
func TestFetchGetterRegisteredDefaultIsUsed(t *testing.T) {
	t.Parallel()

	f := newDedicatedIndicatorFixture(t, 0, types.TopologyAwareAssignment{
		0: machine.NewCPUSet(0),
	})
	dynamicConf := f.conf.GetDynamicConfiguration()
	dynamicConf.IndicatorTargetDefaultGetter = "registered-default"
	dynamicConf.IndicatorTargetGetters = map[string]string{}
	f.region.SetEssentials(types.ResourceEssentials{DynamicConfiguration: dynamicConf})
	f.region.indicatorTargetGetters["registered-default"] = func(
		_ workloadapis.ServiceSystemIndicatorName, target float64,
	) float64 {
		return target * 2
	}

	getter, getterName := f.region.fetchGetter(workloadapis.ServiceSystemIndicatorNameCPUSchedWait)
	require.NotNil(t, getter)
	require.Equal(t, "registered-default", getterName)
	require.Equal(t, 84.0, getter(workloadapis.ServiceSystemIndicatorNameCPUSchedWait, 42.0))
}
