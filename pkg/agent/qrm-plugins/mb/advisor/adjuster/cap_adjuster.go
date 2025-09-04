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

package adjuster

// defaultPortionPercent to limit quota no more than the target value
// as certain value set in resctrl FS schemata would generally result in slight more mb traffic then the set value and
// not the other way around
const defaultPortionPercent = 100

// capAdjuster ensures the resultant settings never exceed the targets by specific portion
type capAdjuster struct {
	percentProportionLimit int
	inner                  Adjuster
}

func (p *capAdjuster) AdjustOutgoingTargets(targets []int, currents []int) []int {
	results := p.inner.AdjustOutgoingTargets(targets, currents)
	for i := range results {
		if results[i] > targets[i]*p.percentProportionLimit/100 {
			results[i] = targets[i] * p.percentProportionLimit / 100
		}
	}
	return results
}

var _ Adjuster = &capAdjuster{}
