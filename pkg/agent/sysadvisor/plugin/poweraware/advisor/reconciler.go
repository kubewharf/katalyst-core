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

import (
	"context"
	"time"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/poweraware/advisor/action"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/poweraware/advisor/action/strategy"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/poweraware/capper"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/poweraware/evictor"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/poweraware/spec"
	metrictypes "github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/types"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

type PowerReconciler interface {
	// Reconcile returns true if CPU frequency capping is involved
	// this return is important as the cpu freq capping should be released when the alert is gone
	Reconcile(ctx context.Context, desired *spec.PowerSpec, actual int) (bool, error)
	OnDVFSReset()
}

type powerReconciler struct {
	dryRun      bool
	priorAction action.PowerAction

	evictor  evictor.PercentageEvictor
	capper   capper.PowerCapper
	strategy strategy.PowerActionStrategy
	emitter  metrics.MetricEmitter
}

func (p *powerReconciler) OnDVFSReset() {
	p.strategy.OnDVFSReset()
}

func (p *powerReconciler) Reconcile(ctx context.Context, desired *spec.PowerSpec, actual int) (bool, error) {
	alertTimeLimit, err := spec.GetPowerAlertResponseTimeLimit(desired.Alert)
	if err != nil {
		general.InfofV(6, "pap: failed to get response time limit of alert: %v", err)
		// ok to ignore this mal-formatted alert for now; hopefully next iteration it will be correct
		return false, nil
	}
	deadline := desired.AlertTime.Add(alertTimeLimit)
	ttl := deadline.Sub(time.Now())
	actionPlan := p.strategy.RecommendAction(actual, desired.Budget, desired.Alert, desired.InternalOp, ttl)

	if p.dryRun {
		if p.priorAction == actionPlan {
			// to throttle duplicate logs
			return false, nil
		}
		general.Infof("pap: dryRun: %s", actionPlan)
		p.emitPowerAdvice(actionPlan, "dryRun")
		p.priorAction = actionPlan
		return false, nil
	}

	general.InfofV(6, "pap: reconcile action %#v", actionPlan)
	p.emitPowerAdvice(actionPlan, "real")

	switch actionPlan.Op {
	case spec.InternalOpFreqCap:
		p.capper.Cap(ctx, actionPlan.Arg, actual)
		general.Infof("pap: req to cap target %d, actual %d watts", actionPlan.Arg, actual)
		return true, nil
	case spec.InternalOpEvict:
		p.evictor.Evict(ctx, actionPlan.Arg)
		general.Infof("pap: req to evict target percentage %d", actionPlan.Arg)
		return false, nil
	default:
		// todo: add feature of pod suppressions (with their resource usage)
		return false, nil
	}
}

func newReconciler(dryRun bool, metricsReader metrictypes.MetricsReader, emitter metrics.MetricEmitter, evictor evictor.PercentageEvictor, capper capper.PowerCapper) PowerReconciler {
	return &powerReconciler{
		dryRun:      dryRun,
		priorAction: action.PowerAction{},
		evictor:     evictor,
		capper:      capper,
		strategy:    strategy.NewEvictFirstStrategy(emitter, evictor, metricsReader, capper),
		emitter:     emitter,
	}
}
