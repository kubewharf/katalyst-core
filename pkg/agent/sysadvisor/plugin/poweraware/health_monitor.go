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

package poweraware

import (
	"context"
	"time"

	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/poweraware/advisor"
	powermetric "github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/poweraware/metric"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

type healthMonitor struct {
	advisor advisor.PowerAwareAdvisor
	emitter metrics.MetricEmitter
	ctx     context.Context
	cancel  context.CancelFunc
}

func newHealthMonitor(advisor advisor.PowerAwareAdvisor, emitter metrics.MetricEmitter) *healthMonitor {
	ctx, cancel := context.WithCancel(context.Background())
	return &healthMonitor{
		advisor: advisor,
		emitter: emitter,
		ctx:     ctx,
		cancel:  cancel,
	}
}

func (h *healthMonitor) Start() {
	go wait.Until(func() {
		powermetric.EmitPowerAdvisorHealth(h.emitter, h.advisor.HealthStatus())
	}, 30*time.Second, h.ctx.Done())
}

func (h *healthMonitor) Stop() {
	if h.cancel != nil {
		h.cancel()
	}
}
