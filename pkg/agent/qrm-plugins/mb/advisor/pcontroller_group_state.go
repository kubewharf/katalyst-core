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
	Moderate(raises int) (int, bool)
}

type fullCapRecover struct{}

// Moderate returns the suggested cap unchanged when no recovery moderation is configured.
func (n fullCapRecover) Moderate(raises int) (int, bool) {
	return raises, true
}

// coolDownRecover withholds before cool-down is done
type coolDownRecover struct {
	coolCount         int
	coolDownThreshold int
}

func (c *coolDownRecover) Moderate(raises int) (int, bool) {
	c.coolCount++
	if c.coolCount < c.coolDownThreshold {
		return 0, false
	}

	return raises, true
}

type reducedRecover struct {
	reducerPCT int
}

func (r *reducedRecover) Moderate(raises int) (int, bool) {
	return raises * r.reducerPCT / 100, true
}

type pipelineRecover struct {
	recovers []capRecoveryModerator
}

func (p *pipelineRecover) Moderate(raises int) (int, bool) {
	for _, r := range p.recovers {
		moderated, ok := r.Moderate(raises)
		if !ok {
			return moderated, false
		}
		raises = moderated
	}

	return raises, true
}

const (
	defaultCoolDowns  = 30
	defaultReducerPCT = 2
)

func newRecoverModerator(mode string) capRecoveryModerator {
	if mode == "slow" {
		return &pipelineRecover{
			recovers: []capRecoveryModerator{
				&coolDownRecover{coolDownThreshold: defaultCoolDowns},
				&reducedRecover{reducerPCT: defaultReducerPCT},
			},
		}
	}

	return fullCapRecover{}
}

type groupPCtrlState struct {
	pCtrl    pController
	ccdCapMB int

	recover capRecoveryModerator
}

// updateCCDCap applies recovery moderation if applicable
func (g *groupPCtrlState) updateCCDCap(suggestedCap int) {
	if suggestedCap <= g.ccdCapMB {
		g.ccdCapMB = suggestedCap
		return
	}

	// raising up is under moderation
	if suggestedCap > g.ccdCapMB {
		if moderatedCap, ok := g.recover.Moderate(suggestedCap - g.ccdCapMB); ok {
			g.ccdCapMB += moderatedCap
		}
	}
}

// newGroupPCtrlState initializes P-controller state with the maximum CCD cap as the starting value.
func newGroupPCtrlState(Kp float64, target, maxValue int, recoverMode string) *groupPCtrlState {
	return &groupPCtrlState{
		pCtrl: pController{
			kp:     Kp,
			target: target,
		},
		ccdCapMB: maxValue,
		recover:  newRecoverModerator(recoverMode),
	}
}
