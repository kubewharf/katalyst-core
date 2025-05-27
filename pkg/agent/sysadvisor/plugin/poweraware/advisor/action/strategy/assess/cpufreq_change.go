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

const minMHZ = 800

type cpuFreqChangeAssessor struct {
	initFreqMhz int
}

func (c *cpuFreqChangeAssessor) Clear() {
	c.initFreqMhz = 0
}

func (c *cpuFreqChangeAssessor) AssessEffect(current int) (int, error) {
	if c.initFreqMhz < minMHZ {
		return 0, fmt.Errorf("invalid initial frequency %d mhz", c.initFreqMhz)
	}

	if current < minMHZ {
		return 0, fmt.Errorf("invalid current frequency %d mhz", current)
	}

	if current >= c.initFreqMhz {
		return 0, nil
	}

	return 100 - current*100/c.initFreqMhz, nil
}

func (c *cpuFreqChangeAssessor) Update(actual int) {
	// no need to keep track of the change, as we always compare with the initial frequency
}

func (c *cpuFreqChangeAssessor) AssessTarget(actualWatt, desiredWatt int, maxDecreasePercent int) int {
	// keep as is if there is no room to decrease
	if maxDecreasePercent <= 0 {
		return actualWatt
	}

	// when there is decrease room for cpu frequency, lower the power in smaller portion to avoid the misstep
	lowerLimit := (100 - maxDecreasePercent/2) * actualWatt / 100
	if lowerLimit > desiredWatt {
		return lowerLimit
	}

	return desiredWatt
}

func NewCPUFreqChangeAssessor(initMhz int) Assessor {
	return &cpuFreqChangeAssessor{
		initFreqMhz: initMhz,
	}
}
