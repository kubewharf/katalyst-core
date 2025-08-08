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

type Adjuster interface {
	// AdjustOutgoingTargets yields the value to set in order to have the target for each domain,
	// taking into account of with currents the observed usage controlled by previous set values.
	AdjustOutgoingTargets(targets []int, currents []int) []int
}

func New() Adjuster {
	return &capAdjuster{
		inner: &feedbackAdjuster{},
	}
}
