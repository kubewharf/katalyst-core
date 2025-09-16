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

package helper

import (
	"context"
	"fmt"

	v1 "k8s.io/api/core/v1"

	"github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/spd"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

// PodEnableReclaim checks whether the pod can be reclaimed,
// if node does not enable reclaim, it will return false directly,
// if node enable reclaim, it will check whether the pod is degraded or baseline.
func PodEnableReclaim(ctx context.Context, metaServer *metaserver.MetaServer,
	podUID string, nodeEnableReclaim bool,
) (bool, error) {
	if !nodeEnableReclaim {
		general.Infof("node reclaim disabled")
		return false, nil
	}

	if metaServer == nil {
		return false, fmt.Errorf("metaServer is nil")
	}

	pod, err := metaServer.GetPod(ctx, podUID)
	if err != nil {
		return false, err
	}

	// get current service performance level of the pod
	pLevel, err := metaServer.ServiceBusinessPerformanceLevel(ctx, pod.ObjectMeta)
	if err != nil && !spd.IsSPDNameOrResourceNotFound(err) {
		return false, err
	} else if err != nil {
		return true, nil
	} else if pLevel == spd.PerformanceLevelPoor {
		// if performance level is poor, it can not be reclaimed
		general.InfoS("performance level is poor, reclaim disabled", "podUID", podUID)
		return false, nil
	}

	// check whether current pod is service baseline
	baseline, err := metaServer.ServiceBaseline(ctx, pod.ObjectMeta)
	if err != nil && !spd.IsSPDNameOrResourceNotFound(err) {
		return false, err
	} else if err != nil {
		return true, nil
	} else if baseline {
		// if pod is baseline, it can not be reclaimed
		general.InfoS("pod is regarded as baseline, reclaim disabled", "podUID", podUID)
		return false, nil
	}

	return true, nil
}

// PodDisableReclaimLevel returns the disable reclaim level for a pod.
// The disable reclaim level indicates at which granularity level the reclaim should be disabled.
// It fetches the ReclaimResourceIndicators from the metaServer to determine the level.
// If there's an error fetching the pod or the indicators, or if the pod is considered as baseline,
// it returns DisableReclaimLevelPod as the default level.
func PodDisableReclaimLevel(
	ctx context.Context,
	metaServer *metaserver.MetaServer,
	podUID string,
) v1alpha1.DisableReclaimLevel {
	// Default to DisableReclaimLevelPod if we can't determine a more specific level
	disableReclaimLevel := v1alpha1.DisableReclaimLevelPod

	// Try to get the pod by its UID
	pod, err := metaServer.GetPod(ctx, podUID)
	if err != nil {
		// If we can't get the pod, return the default level
		return disableReclaimLevel
	}

	// Initialize ReclaimResourceIndicators to store the result
	indicators := v1alpha1.ReclaimResourceIndicators{}

	// Try to get the service extended indicators for the pod
	// This will tell us if the pod should have reclaim disabled and at what level
	baseline, err := metaServer.ServiceExtendedIndicator(ctx, pod.ObjectMeta, &indicators)
	if err != nil {
		// Log the error but continue with default behavior
		general.Errorf("failed to fetch indicators: %v", err)
	}

	// If the pod is not baseline (baseline is false), use the DisableReclaimLevel from indicators
	if !baseline && indicators.DisableReclaimLevel != nil {
		disableReclaimLevel = *indicators.DisableReclaimLevel
	}

	// Return the determined disable reclaim level
	return disableReclaimLevel
}

func PodPerformanceScore(ctx context.Context, metaServer *metaserver.MetaServer, podUID string) (float64, error) {
	if metaServer == nil {
		return 0, fmt.Errorf("metaServer is nil")
	}
	pod, err := metaServer.GetPod(ctx, podUID)
	if err != nil {
		return 0, err
	}

	return metaServer.ServiceBusinessPerformanceScore(ctx, pod.ObjectMeta)
}

func PodIsDaemonSet(pod *v1.Pod) bool {
	if pod != nil && pod.OwnerReferences != nil {
		for _, ownerReference := range pod.OwnerReferences {
			if ownerReference.Kind == "DaemonSet" {
				return true
			}
		}
	}
	return false
}

func PodMatchKind(pod *v1.Pod, kind string) bool {
	if pod != nil && pod.OwnerReferences != nil {
		for _, ownerReference := range pod.OwnerReferences {
			if ownerReference.Kind == kind {
				return true
			}
		}
	}
	return false
}

func FilterPodsByKind(pods []*v1.Pod, skippedPodKinds []string) []*v1.Pod {
	var filteredPods []*v1.Pod
	for _, pod := range pods {
		skipped := false
		for _, kind := range skippedPodKinds {
			if matchKind := PodMatchKind(pod, kind); matchKind {
				skipped = true
				break
			}
		}

		if !skipped {
			filteredPods = append(filteredPods, pod)
		}
	}
	return filteredPods
}

func IsValidQosLevel(qoslevel string) bool {
	if qoslevel == string(consts.QoSLevelReclaimedCores) || qoslevel == string(consts.QoSLevelSharedCores) ||
		qoslevel == string(consts.QoSLevelDedicatedCores) || qoslevel == string(consts.QoSLevelSystemCores) {
		return true
	}
	return false
}
