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

type feedbackAdjuster struct {
	prevValues []int
}

func feedback(x0, x1, y1 int) (y0 int) {
	// assuming x0:y0 == x1:y1 to calculate x1 = x0/y0*y1
	// where x[i] is the effective value to set, and y[i] the resultant usage being controlled by x[i]
	if x1 == 0 || x0 == 0 {
		return y1
	}
	return int(float64(x0*y1) / float64(x1))
}

func (f *feedbackAdjuster) AdjustOutgoingTargets(targets []int, currents []int) []int {
	if len(targets) == 0 {
		return nil
	}

	result := targets
	if len(f.prevValues) != 0 && len(f.prevValues) == len(currents) && len(targets) == len(currents) {
		for i := range targets {
			v := feedback(f.prevValues[i], currents[i], targets[i])
			result[i] = v
		}
	}

	f.prevValues = result

	return result
}
