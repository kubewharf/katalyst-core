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

package reclaimpolicy

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kubewharf/katalyst-core/pkg/metaserver/spd"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

// PodReclaimProfilingProvider is the minimal SPD-facing dependency required
// for pod reclaim policy evaluation.
type PodReclaimProfilingProvider interface {
	ServiceBusinessPerformanceLevel(ctx context.Context, podMeta metav1.ObjectMeta) (spd.PerformanceLevel, error)
	ServiceBaseline(ctx context.Context, podMeta metav1.ObjectMeta) (bool, error)
}

// EvaluatePodReclaimPolicy checks whether a pod is eligible for reclaim.
//
// The policy is intentionally pod-level and side-effect free so callers can
// share the same decision logic without coupling to QRM or SysAdvisor internals.
func EvaluatePodReclaimPolicy(
	ctx context.Context,
	profilingProvider PodReclaimProfilingProvider,
	podMeta metav1.ObjectMeta,
	nodeEnableReclaim bool,
) (bool, error) {
	if !nodeEnableReclaim {
		general.Infof("node reclaim disabled")
		return false, nil
	}

	if profilingProvider == nil {
		return false, fmt.Errorf("pod reclaim profiling provider is nil")
	}

	pLevel, err := profilingProvider.ServiceBusinessPerformanceLevel(ctx, podMeta)
	if err != nil && !spd.IsSPDNameOrResourceNotFound(err) {
		return false, err
	} else if err != nil {
		return true, nil
	} else if pLevel == spd.PerformanceLevelPoor {
		general.InfoS("performance level is poor, reclaim disabled", "pod", podMeta.Name, "namespace", podMeta.Namespace, "uid", podMeta.UID)
		return false, nil
	}

	baseline, err := profilingProvider.ServiceBaseline(ctx, podMeta)
	if err != nil && !spd.IsSPDNameOrResourceNotFound(err) {
		return false, err
	} else if err != nil {
		return true, nil
	} else if baseline {
		general.InfoS("pod is regarded as baseline, reclaim disabled", "pod", podMeta.Name, "namespace", podMeta.Namespace, "uid", podMeta.UID)
		return false, nil
	}

	return true, nil
}
