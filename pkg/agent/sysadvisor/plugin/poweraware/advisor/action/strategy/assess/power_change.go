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

type powerChangeAssessor struct {
	prevPower int
}

func (p *powerChangeAssessor) Update(actual, desired int) {
	p.prevPower = actual
}

func (p *powerChangeAssessor) AssessChange(current int) int {
	dvfsEffect := 0
	// if actual power is more than previous, likely previous round dvfs took no effect;
	// not to take into account
	if current > 0 && current < p.prevPower {
		dvfsEffect = (p.prevPower - current) * 100 / p.prevPower
	}
	return dvfsEffect
}

func (p *powerChangeAssessor) Clear() {
	p.prevPower = 0
}

func NewPowerChangeAssessor(value int) Assessor {
	return &powerChangeAssessor{
		prevPower: value,
	}
}
