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

package strategy

import (
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/poweraware/advisor/action/strategy/assess"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

// DVFS: Dynamic Voltage Frequency Scaling, is a technique servers use to manage the power consumption.
// the limit of dvfs effect a voluntary dvfs plan is allowed
const voluntaryDVFSEffectMaximum = 10

// dvfsTracker keeps track and accumulates the effect lowering power or cpu frequency by means of dvfs
type dvfsTracker struct {
	dvfsAccumEffect int
	inDVFS          bool

	capperProber CapperProber
	assessor     assess.Assessor
}

func (d *dvfsTracker) getDVFSAllowPercent() int {
	leftPercentage := voluntaryDVFSEffectMaximum - d.dvfsAccumEffect
	if leftPercentage < 0 {
		leftPercentage = 0
	}
	return leftPercentage
}

// adjustTargetWatt yields the target value taking into account the limiting indicator value.
func (d *dvfsTracker) adjustTargetWatt(actualWatt, desiredWatt int) int {
	return d.assessor.AssessTarget(actualWatt, desiredWatt, d.getDVFSAllowPercent())
}

func (d *dvfsTracker) isCapperAvailable() bool {
	return d.capperProber != nil && d.capperProber.IsCapperReady()
}

func (d *dvfsTracker) update(currPower int) {
	// only accumulate when dvfs is engaged
	if d.inDVFS && d.isCapperAvailable() {
		val, err := d.assessor.AssessEffect(currPower)
		if err != nil {
			general.Errorf("pap: failed to get accumulated effect: %v", err)
		} else {
			d.dvfsAccumEffect = val
		}
	}

	d.assessor.Update(currPower)
}

func (d *dvfsTracker) dvfsEnter() {
	d.inDVFS = true
}

func (d *dvfsTracker) dvfsExit() {
	d.inDVFS = false
}

func (d *dvfsTracker) clear() {
	d.dvfsAccumEffect = 0
	d.inDVFS = false
	d.assessor.Clear()
}
