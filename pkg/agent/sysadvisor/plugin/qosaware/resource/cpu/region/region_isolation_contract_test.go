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

package region

import (
	"testing"

	"github.com/stretchr/testify/require"

	configapi "github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// recordingIsolationPolicy is a ProvisionPolicy stand-in that records whether
// Update() was invoked and echoes the current ResourceEssentials as the isolation
// upper/lower knobs, mirroring PolicyBase.GetControlKnobAdjusted for isolation.
type recordingIsolationPolicy struct {
	updateCalled bool
	resourceEss  types.ResourceEssentials
}

func (f *recordingIsolationPolicy) SetEssentials(re types.ResourceEssentials, _ types.ControlEssentials) {
	f.resourceEss = re
}
func (f *recordingIsolationPolicy) SetPodSet(types.PodSet)               {}
func (f *recordingIsolationPolicy) SetBindingNumas(machine.CPUSet, bool) {}
func (f *recordingIsolationPolicy) Update() error                        { f.updateCalled = true; return nil }
func (f *recordingIsolationPolicy) GetMetaInfo() string                  { return "recording-isolation" }
func (f *recordingIsolationPolicy) GetControlKnobAdjusted() (types.ControlKnob, error) {
	return types.ControlKnob{
		configapi.ControlKnobNonIsolatedUpperCPUSize: {Value: f.resourceEss.ResourceUpperBound},
		configapi.ControlKnobNonIsolatedLowerCPUSize: {Value: f.resourceEss.ResourceLowerBound},
	}, nil
}

func newTestIsolationRegion(t *testing.T) *QoSRegionIsolation {
	t.Helper()
	conf, err := options.NewOptions().Config()
	require.NoError(t, err)
	conf.GenericSysAdvisorConfiguration.StateFileDirectory = t.TempDir()
	// no provision/headroom policies configured; tests inject their own.
	conf.CPUAdvisorConfiguration.ProvisionPolicies = nil
	conf.CPUAdvisorConfiguration.HeadroomPolicies = nil

	metaCache := metacache.NewDummyMetaCacheImp()
	// Build a minimal MetaServer directly instead of metaserver.NewMetaServer so
	// the test does not touch process-global cgroup-path / containerd state; this
	// keeps the isolation region tests safe to run in parallel.
	metaServer := &metaserver.MetaServer{
		MetaAgent: &agent.MetaAgent{
			KatalystMachineInfo: &machine.KatalystMachineInfo{
				CPUTopology: &machine.CPUTopology{
					NumCPUs:      8,
					NumCores:     4,
					NumSockets:   1,
					NumNUMANodes: 1,
				},
			},
		},
	}

	ci := types.ContainerInfo{
		QoSLevel:            "shared_cores",
		OriginOwnerPoolName: "isolation-default",
	}
	return NewQoSRegionIsolation(&ci, "iso-test", conf, nil, commonstate.FakedNUMAID, metaCache, metaServer, metrics.DummyMetrics{}).(*QoSRegionIsolation)
}

// policy update episode (and therefore the CPU regulator ramp) for isolation
// regions is an established feature. updateProvisionPolicy must mark policies
// Succeeded without invoking policy.Update().
func TestQoSRegionIsolationDoesNotRunPolicyUpdate(t *testing.T) {
	t.Parallel()

	iso := newTestIsolationRegion(t)
	fake := &recordingIsolationPolicy{}
	iso.provisionPolicies = []*internalProvisionPolicy{{
		name:                "recording",
		policy:              fake,
		internalPolicyState: internalPolicyState{updateStatus: types.PolicyUpdateFailed},
	}}

	iso.updateProvisionPolicy()

	require.False(t, fake.updateCalled, "isolation must not run policy.Update()")
	require.Equal(t, types.PolicyUpdateSucceeded, iso.provisionPolicies[0].updateStatus)
}

// TestQoSRegionIsolationProvisionSerialCycle pins the P4 single-period serial
// contract: within one advisor cycle, TryUpdateProvision() (writer) is followed
// by GetProvision() (reader). The isolation upper/lower knobs are taken straight
// from the resource bounds with no regulator ramp, so a bound change across
// cycles is reflected directly. Both methods take the region mutex, so running
// them serially under -race is race-free.
func TestQoSRegionIsolationProvisionSerialCycle(t *testing.T) {
	t.Parallel()

	iso := newTestIsolationRegion(t)
	fake := &recordingIsolationPolicy{}
	iso.provisionPolicies = []*internalProvisionPolicy{{
		name:                "recording",
		policy:              fake,
		internalPolicyState: internalPolicyState{updateStatus: types.PolicyUpdateFailed},
	}}

	setBounds := func(upper, lower float64) {
		iso.SetEssentials(types.ResourceEssentials{
			DynamicConfiguration: iso.conf.GetDynamicConfiguration(),
			ResourceUpperBound:   upper,
			ResourceLowerBound:   lower,
		})
	}

	// Cycle 1.
	setBounds(80, 10)
	iso.TryUpdateProvision()
	knob, err := iso.GetProvision()
	require.NoError(t, err)
	require.Equal(t, 80.0, knob[configapi.ControlKnobNonIsolatedUpperCPUSize].Value)
	require.Equal(t, 10.0, knob[configapi.ControlKnobNonIsolatedLowerCPUSize].Value)

	// Cycle 2: a large bound jump must be reflected directly (no regulator ramp
	// would clamp it to MaxRampUpStep) — this is the "isolation does not run the
	// regulator" behavior.
	setBounds(200, 5)
	iso.TryUpdateProvision()
	knob, err = iso.GetProvision()
	require.NoError(t, err)
	require.Equal(t, 200.0, knob[configapi.ControlKnobNonIsolatedUpperCPUSize].Value,
		"isolation upper bound must jump directly: no CPU regulator ramp")
	require.Equal(t, 5.0, knob[configapi.ControlKnobNonIsolatedLowerCPUSize].Value)
}
