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

package katalyst_base

import (
	"context"
	"encoding/json"
	"time"

	"go.uber.org/atomic"
	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

const (
	syncPeriod              = 30 * time.Second
	MetricNameUnhealthyRule = "unhealthy_healthz_check_rule"
)

// HealthzChecker periodically checks the running states
type HealthzChecker struct {
	// if unhealthyReason is none-empty, it means some check failed
	unhealthyReason *atomic.String
	emitter         metrics.MetricEmitter
}

func NewHealthzChecker(emitter metrics.MetricEmitter) *HealthzChecker {
	return &HealthzChecker{
		unhealthyReason: atomic.NewString(""),
		emitter:         emitter,
	}
}

func (h *HealthzChecker) Run(ctx context.Context) {
	go wait.Until(func() {
		results := general.GetRegisterReadinessCheckResult()
		for key, result := range results {
			if !result.Ready {
				_ = h.emitter.StoreInt64(MetricNameUnhealthyRule, 1, metrics.MetricTypeNameRaw,
					metrics.MetricTag{Key: "rule", Val: string(key)})
			}
		}
	}, syncPeriod, ctx.Done())
}

// CheckHealthy returns whether the component is healthy.
func (h *HealthzChecker) CheckHealthy() (bool, string) {
	results := general.GetRegisterReadinessCheckResult()
	healthy := true
	for _, result := range results {
		if !result.Ready {
			healthy = false
		}
	}

	resultBytes, err := json.Marshal(results)
	if err != nil {
		general.Errorf("marshal healthz content failed,err:%v", err)
	}

	return healthy, string(resultBytes)
}
