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

package component

import (
	"math"
	"time"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
)

// defaultDecayB is the recommended base b in decay formula: a*b^(-x)
const defaultDecayB = math.E / 2

type PowerStrategy interface {
	RecommendAction(currentWatt, desiredWatt int,
		alert types.PowerAlert, internalOp types.InternalOp, ttl time.Duration,
	) PowerAction
}

type ruleBasedPowerStrategy struct {
	coefficient linearDecay
}

func (p ruleBasedPowerStrategy) RecommendAction(actualWatt, desiredWatt int,
	alert types.PowerAlert, internalOp types.InternalOp, ttl time.Duration,
) PowerAction {
	if actualWatt <= desiredWatt {
		return PowerAction{op: types.InternalOpPause, arg: 0}
	}

	// stale request; ignore and return no-op; hopefully next time it will rectify
	if ttl <= -time.Minute*5 || types.InternalOpPause == internalOp {
		return PowerAction{op: types.InternalOpPause, arg: 0}
	}

	if ttl <= time.Minute*2 {
		return PowerAction{op: types.InternalOpFreqCap, arg: desiredWatt}
	}

	op := internalOp
	if types.InternalOpAuto == op {
		op = p.autoAction(actualWatt, desiredWatt, ttl)
	}

	if types.InternalOpFreqCap == op {
		return PowerAction{op: types.InternalOpFreqCap, arg: desiredWatt}
	} else if types.InternalOpEvict == op {
		return PowerAction{
			op:  types.InternalOpEvict,
			arg: p.coefficient.calcExcessiveInPercent(desiredWatt, actualWatt, ttl),
		}
	}

	return PowerAction{op: types.InternalOpPause, arg: 0}
}

func (p ruleBasedPowerStrategy) autoAction(actualWatt, desiredWatt int, ttl time.Duration) types.InternalOp {
	// upstream caller guarantees actual > desired
	ttlS0, _ := types.GetPowerAlertResponseTimeLimit(types.PowerAlertS0)
	if ttl <= ttlS0 {
		return types.InternalOpFreqCap
	}

	ttlF1, _ := types.GetPowerAlertResponseTimeLimit(types.PowerAlertF1)
	if ttl <= ttlF1 {
		return types.InternalOpEvict
	}

	// todo: consider throttle action, after load throttle is enabled
	return types.InternalOpPause
}

type linearDecay struct {
	b float64
}

func (d linearDecay) calcExcessiveInPercent(target, curr int, ttl time.Duration) int {
	// exponential decaying formula: a*b^(-t)
	a := 100 - target*100/curr
	decay := math.Pow(d.b, float64(-int(ttl.Minutes())/10))
	return int(float64(a) * decay)
}

var _ PowerStrategy = &ruleBasedPowerStrategy{}
