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
	"errors"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/poweraware/advisor/action/strategy/assess"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

// DVFS: Dynamic Voltage Frequency Scaling, is a technique servers use to manage the power consumption.
// the limit of dvfs effect a voluntary dvfs plan is allowed
const voluntaryDVFSEffectMaximum = 10

// dvfsTracker keeps track and accumulates the effect lowering power or cpu frequency by means of dvfs
type dvfsTracker struct {
	dvfsAccumEffect int
	isEffectCurrent bool
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

// adjustTargetWatt adjusts the target value taking into account the limiting indication.
func (d *dvfsTracker) adjustTargetWatt(actualWatt, desiredWatt int) (int, error) {
	if d.getDVFSAllowPercent() <= 0 {
		return 0, errors.New("no room for dvfs")
	}

	if !d.isEffectCurrent {
		return actualWatt, errors.New("unknown current effect")
	}

	// fresh effect can be used only once to avoid misinformation of outdated effect
	d.isEffectCurrent = false
	return d.assessor.AssessTarget(actualWatt, desiredWatt, d.getDVFSAllowPercent()), nil
}

func (d *dvfsTracker) isCapperAvailable() bool {
	return d.capperProber != nil && d.capperProber.IsCapperReady()
}

func (d *dvfsTracker) isInitialized() bool {
	return d.assessor.IsInitialized()
}

func (d *dvfsTracker) tryInit() {
	if err := d.assessor.Init(); err != nil {
		general.Errorf("pap: failed to init dvfsTracker: %v", err)
		return
	}

	general.Infof("pap: succeeded init dvfsTracker")
}

func (d *dvfsTracker) update(currPower int) {
	d.updateTrackedEffect(currPower)
	d.assessor.Update(currPower)
	general.InfofV(6, "pap: dvfs effect: %d, is current: %v", d.dvfsAccumEffect, d.isEffectCurrent)
}

func (d *dvfsTracker) updateTrackedEffect(currPower int) {
	if !d.isInitialized() {
		d.tryInit()
		return
	}

	val, err := d.assessor.AssessEffect(currPower, d.inDVFS, d.isCapperAvailable())
	if err != nil {
		general.Errorf("pap: failed to assess effect: %v", err)
		d.isEffectCurrent = false
		return
	}

	d.isEffectCurrent = true
	d.dvfsAccumEffect = val
}

func (d *dvfsTracker) dvfsEnter() {
	d.inDVFS = true
}

func (d *dvfsTracker) dvfsExit() {
	d.inDVFS = false
}

func (d *dvfsTracker) clear() {
	d.dvfsAccumEffect = 0
	d.isEffectCurrent = false
	d.inDVFS = false
	d.assessor.Clear()
}
