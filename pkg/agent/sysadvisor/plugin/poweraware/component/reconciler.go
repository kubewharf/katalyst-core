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
	"context"
	"time"

	"github.com/pkg/errors"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/poweraware/component/capper"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
)

type PowerReconciler interface {
	// Reconcile returns true if CPU frequency capping is involved
	// this return is important as the cpu freq capping should be released
	// when the alert is gone
	Reconcile(ctx context.Context, desired *types.PowerSpec, actual int) (bool, error)
}

type powerReconciler struct {
	dryRun      bool
	priorAction PowerAction

	evictor  LoadEvictor
	capper   capper.PowerCapper
	strategy PowerStrategy
}

func (p *powerReconciler) Reconcile(ctx context.Context, desired *types.PowerSpec, actual int) (bool, error) {
	alertTimeLimit, err := types.GetPowerAlertResponseTimeLimit(desired.Alert)
	if err != nil {
		return false, errors.Wrap(err, "GetPowerAlertResponseTimeLimit failed")
	}
	deadline := desired.AlertTime.Add(alertTimeLimit)
	ttl := deadline.Sub(time.Now())
	action := p.strategy.RecommendAction(actual, desired.Budget, desired.Alert, desired.InternalOp, ttl)

	if p.dryRun {
		if p.priorAction == action {
			// to throttle duplicate logs
			return false, nil
		}
		klog.Infof("pap: dryRun: %s", action)
		p.priorAction = action
		return false, nil
	}

	klog.V(6).Infof("pap: reconcile action %#v", action)

	// todo: suppress actions too often??
	switch action.op {
	case types.InternalOpFreqCap:
		p.capper.Cap(ctx, action.arg, actual)
		return true, nil
	case types.InternalOpEvict:
		p.evictor.Evict(ctx, action.arg)
		return false, nil
	default:
		// no op
		return false, nil
	}
}

var _ PowerReconciler = &powerReconciler{}
