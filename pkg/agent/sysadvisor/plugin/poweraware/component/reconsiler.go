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

	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
)

type PowerReconciler interface {
	Reconcile(ctx context.Context, desired *types.PowerSpec, actual int)
}

type powerReconciler struct {
	dryRun      bool
	priorAction PowerAction

	evictor  LoadEvictor
	capper   PowerCapper
	strategy PowerStrategy
}

func (p *powerReconciler) Reconcile(ctx context.Context, desired *types.PowerSpec, actual int) {
	alertTimeLimit, err := types.GetPowerAlertResponseTimeLimit(desired.Alert)
	if err != nil {
		// not to log error, as there would be too many such logs - denial of service risk
		// todo: report to metric dashboard
		return
	}
	deadline := desired.AlertTime.Add(alertTimeLimit)
	ttl := deadline.Sub(time.Now())
	action := p.strategy.RecommendAction(actual, desired.Budget, desired.Alert, desired.InternalOp, ttl)

	if p.dryRun {
		if p.priorAction == action {
			// to throttle duplicate logs
			return
		}
		klog.Infof("pap: dryRun: %s", action)
		p.priorAction = action
		return
	}

	// todo: suppress actions too often??
	switch action.op {
	case types.InternalOpFreqCap:
		p.capper.Cap(ctx, action.arg, actual)
	case types.InternalOpEvict:
		p.evictor.Evict(ctx, action.arg)
	default:
		// no op
		return
	}
}

var _ PowerReconciler = &powerReconciler{}
