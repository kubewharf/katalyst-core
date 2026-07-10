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

func (n nilCapRecover) Moderate(suggestedCap int) int {
	return suggestedCap
}

type groupPCtrlState struct {
	pCtrl    pController
	ccdCapMB int

	recover capRecoveryModerator
}

func (g *groupPCtrlState) updateCCDCap(suggestedCap int) {
	moderatedCap := g.recover.Moderate(suggestedCap)
	g.ccdCapMB = moderatedCap
}

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
