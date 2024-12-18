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

// DVFS: Dynamic Voltage Frequency Scaling, is a technique servers use to manage the power consumption.
// the limit of dvfs effect a voluntary dvfs plan is allowed
const voluntaryDVFSEffectMaximum = 10

// dvfsTracker keeps track and accumulates the effect lowering power by means of dvfs
type dvfsTracker struct {
	dvfsAccumEffect int
	inDVFS          bool
	prevPower       int
}

func (d *dvfsTracker) getDVFSAllowPercent() int {
	leftPercentage := voluntaryDVFSEffectMaximum - d.dvfsAccumEffect
	if leftPercentage < 0 {
		leftPercentage = 0
	}
	return leftPercentage
}

func (d *dvfsTracker) update(actualWatt, desiredWatt int) {
	// only accumulate when dvfs is engaged
	if d.prevPower >= 0 && d.inDVFS {
		// if actual power is more than previous, likely previous round dvfs took no effect; not to take into account
		if actualWatt < d.prevPower {
			dvfsEffect := (d.prevPower - actualWatt) * 100 / d.prevPower
			d.dvfsAccumEffect += dvfsEffect
		}
	}

	d.prevPower = actualWatt
}

func (d *dvfsTracker) dvfsEnter() {
	d.inDVFS = true
}

func (d *dvfsTracker) dvfsExit() {
	d.inDVFS = false
}

func (d *dvfsTracker) clear() {
	d.dvfsAccumEffect = 0
	d.prevPower = 0
	d.inDVFS = false
}
