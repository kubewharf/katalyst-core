/*
Copyright 2026 The Katalyst Authors.

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

package utils

import (
	"errors"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/utils/topology"
)

// IsTopologyConvergenceDeadline reports whether err wraps one of the topology
// engine's explicit replayable deadline sentinels. A generic context deadline is
// intentionally excluded: it does not prove that topology exhausted its own
// convergence/adjustment budget.
func IsTopologyConvergenceDeadline(err error) bool {
	return errors.Is(err, topology.ErrConvergenceDeadlineExceeded) ||
		errors.Is(err, topology.ErrAdjustmentDeadlineExceeded)
}
