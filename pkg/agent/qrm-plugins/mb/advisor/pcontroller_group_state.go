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
	Moderate(toRaise int) (int, bool)
	Accept(newValue, currValue, target, observed int)
}

type fullCapRecover struct{}

func (n fullCapRecover) Accept(newValue, currValue, target, observed int) {
}

// Moderate returns the suggested cap unchanged when no recovery moderation is configured.
func (n fullCapRecover) Moderate(toRaise int) (int, bool) {
	return toRaise, true
}

// coolDown withholds before cool-down is done
type coolDown struct {
	count             int
	coolDownThreshold int
}

func (c *coolDown) isCool() bool {
	return c.count >= c.coolDownThreshold
}

func (c *coolDown) reset() {
	c.count = 0
}

func (c *coolDown) countOneMore() {
	c.count++
}

// lagRecover moderates cap raise in cool-down fashion
type lagRecover struct {
	cooler coolDown
}

func (c *lagRecover) Accept(newValue, currValue, target, observed int) {
	// turn on warm whenever observed not below target
	if observed >= target {
		c.cooler.reset()
		return
	}

	c.cooler.countOneMore()
}

func (c *lagRecover) Moderate(toRaise int) (int, bool) {
	if c.cooler.isCool() {
		return toRaise, true
	}

	return 0, false
}

type reducedRecover struct {
	reducerPCT int
}

func (r *reducedRecover) Accept(newValue, currValue, target, observed int) {
}

func (r *reducedRecover) Moderate(toRaise int) (int, bool) {
	moderated := toRaise * r.reducerPCT / 100
	if toRaise > 0 && moderated == 0 {
		moderated = 1
	}
	return moderated, true
}

// lagAdjustedBaselineRecover allows limited raise above tracked baseline which is adjusted in cool-down pattern
type lagAdjustedBaselineRecover struct {
	cooler      coolDown
	baseline    int
	floatingPCT int
}

func (f *lagAdjustedBaselineRecover) Moderate(toRaise int) (int, bool) {
	ceil := f.getCeiling()
	if toRaise > ceil {
		return ceil, true
	}

	return toRaise, true
}

func (f *lagAdjustedBaselineRecover) getCeiling() int {
	ceil := f.baseline * f.floatingPCT / 100
	if ceil == 0 {
		ceil = 1
	}
	return ceil
}

func (f *lagAdjustedBaselineRecover) Accept(newValue, currValue, target, observed int) {
	if newValue < f.baseline {
		f.baseline = newValue
		f.cooler.reset()
		return
	}

	if observed >= target {
		f.cooler.reset()
		return
	}

	if !f.cooler.isCool() {
		f.cooler.countOneMore()
		return
	}

	// now it is ok to raise the baseline once
	f.baseline += f.getCeiling()
	f.cooler.reset()
}

type pipelineRecover struct {
	recovers []capRecoveryModerator
}

func (p *pipelineRecover) Accept(newValue, currValue, target, observed int) {
	for _, r := range p.recovers {
		r.Accept(newValue, currValue, target, observed)
	}
}

func (p *pipelineRecover) Moderate(toRaise int) (int, bool) {
	for _, r := range p.recovers {
		moderated, ok := r.Moderate(toRaise)
		if !ok {
			return moderated, false
		}
		toRaise = moderated
	}

	return toRaise, true
}

const (
	defaultCoolDowns  = 30
	defaultReducerPCT = 10
	defaultCeilPCT    = 2

	recoveryModeSlowCoolDown = "slow-cool-down"
)

func newRecoverModerator(mode string, maxValue int) capRecoveryModerator {
	if mode == recoveryModeSlowCoolDown {
		return &pipelineRecover{
			recovers: []capRecoveryModerator{
				&lagRecover{cooler: coolDown{coolDownThreshold: defaultCoolDowns}},
				&lagAdjustedBaselineRecover{floatingPCT: defaultCeilPCT, baseline: maxValue, cooler: coolDown{coolDownThreshold: defaultCoolDowns}},
				&reducedRecover{reducerPCT: defaultReducerPCT},
			},
		}
	}

	// default is full-recover
	return fullCapRecover{}
}

type groupPCtrlState struct {
	pCtrl    pController
	ccdCapMB int

	recover capRecoveryModerator
}

// updateCCDCap updates ccd cap under the applicable recovery moderation
func (g *groupPCtrlState) updateCCDCap(suggestedCap, observedMax int) {
	newCap := suggestedCap

	// raising up is under moderation
	if suggestedCap > g.ccdCapMB {
		if moderatedRaise, ok := g.recover.Moderate(suggestedCap - g.ccdCapMB); ok {
			newCap = g.ccdCapMB + moderatedRaise
		} else {
			newCap = g.ccdCapMB
		}
	}
	// make sure moderator update itself to reflect situation changes
	g.recover.Accept(newCap, g.ccdCapMB, g.pCtrl.target, observedMax)

	g.ccdCapMB = newCap
}

// newGroupPCtrlState initializes P-controller state with the maximum CCD cap as the starting value.
func newGroupPCtrlState(Kp float64, target, maxValue int, recoverMode string) *groupPCtrlState {
	return &groupPCtrlState{
		pCtrl: pController{
			kp:     Kp,
			target: target,
		},
		ccdCapMB: maxValue,
		recover:  newRecoverModerator(recoverMode, maxValue),
	}
}
