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

	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

// HealthzChecker periodically checks the running states
type HealthzChecker struct {
	// if unhealthyReason is none-empty, it means some check failed
	unhealthyReason *atomic.String
}

func NewHealthzChecker() *HealthzChecker {
	return &HealthzChecker{
		unhealthyReason: atomic.NewString(""),
	}
}

func (h *HealthzChecker) Run(ctx context.Context) {
	go wait.Until(h.check, time.Second*3, ctx.Done())
}

// CheckHealthy returns whether the component is healthy.
func (h *HealthzChecker) CheckHealthy() (bool, string) {
	if reason := h.unhealthyReason.Load(); reason != "" {
		return false, reason
	}

	return true, ""
}

// Since readiness check in kubernetes supports to config with graceful seconds both for
// failed-to-success and success-to-failed, so we don't need to record the detailed state,
// state transition time, and state lasting period here.
//
// for more information about readiness check, please refer to
// https://github.com/kubernetes/api/blob/ec40acc5b8d728b4554800cdf96fb735949a434b/core/v1/types.go#L221
func (h *HealthzChecker) check() {
	responses := general.CheckHealthz()

	// every time we call healthz check functions, we will override the healthz map instead of
	// replacing. If some checks must be ensured to exist, this strategy might not work.
	unhealthyReasons := make(map[string]general.HealthzCheckResponse)
	for name, resp := range responses {
		if resp.State != general.HealthzCheckStateReady {
			unhealthyReasons[string(name)] = resp
		}
	}

	if len(unhealthyReasons) > 0 {
		reasons, _ := json.Marshal(unhealthyReasons)
		h.unhealthyReason.Store(string(reasons))
	} else {
		h.unhealthyReason.Store("")
	}
}
