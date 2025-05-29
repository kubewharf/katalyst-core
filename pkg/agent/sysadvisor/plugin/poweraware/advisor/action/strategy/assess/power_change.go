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

package assess

import "fmt"

// powerChangeAssessor assesses change effect by power consumption
type powerChangeAssessor struct {
	accumulatedEffect int
	prevPower         int
}

func (p *powerChangeAssessor) AssessTarget(actualWatt, desiredWatt int, maxDecreasePercent int) int {
	lowerLimit := (100 - maxDecreasePercent) * actualWatt / 100
	if lowerLimit > desiredWatt {
		return lowerLimit
	}

	return desiredWatt
}

func (p *powerChangeAssessor) Update(currPower int) {
	p.prevPower = currPower
}

func (p *powerChangeAssessor) AssessEffect(currentPower int) (int, error) {
	if currentPower <= 0 {
		return 0, fmt.Errorf("invalid cuurent value %d", currentPower)
	}

	// if actual power is more than previous, likely previous round dvfs took no effect;
	// not to take into account
	if currentPower >= p.prevPower {
		return p.accumulatedEffect, nil
	}

	change := (p.prevPower - currentPower) * 100 / p.prevPower
	p.accumulatedEffect += change
	return p.accumulatedEffect, nil
}

func (p *powerChangeAssessor) Clear() {
	p.prevPower = 0
}

func NewPowerChangeAssessor(effect, preValue int) Assessor {
	return &powerChangeAssessor{
		accumulatedEffect: effect,
		prevPower:         preValue,
	}
}
