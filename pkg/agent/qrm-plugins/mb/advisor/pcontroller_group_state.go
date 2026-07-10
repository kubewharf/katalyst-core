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

package advisor

type capRecoveryModerator interface {
	Moderate(suggestedCap int) int
}

type nilCapRecover struct{}

// Moderate returns the suggested cap unchanged when no recovery moderation is configured.
func (n nilCapRecover) Moderate(suggestedCap int) int {
	return suggestedCap
}

type groupPCtrlState struct {
	pCtrl    pController
	ccdCapMB int

	recover capRecoveryModerator
}

// updateCCDCap applies recovery moderation before storing the next CCD cap.
func (g *groupPCtrlState) updateCCDCap(suggestedCap int) {
	moderatedCap := g.recover.Moderate(suggestedCap)
	g.ccdCapMB = moderatedCap
}

// newGroupPCtrlState initializes P-controller state with the maximum CCD cap as the starting value.
func newGroupPCtrlState(Kp float64, target, maxValue int) *groupPCtrlState {
	return &groupPCtrlState{
		pCtrl: pController{
			kp:     Kp,
			target: target,
		},
		ccdCapMB: maxValue,
		recover:  &nilCapRecover{},
	}
}
