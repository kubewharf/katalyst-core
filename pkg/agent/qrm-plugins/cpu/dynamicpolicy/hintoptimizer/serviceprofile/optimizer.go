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

package serviceprofile

import (
	"context"
	"fmt"
	"math"
	"sort"
	"time"

	pkgerrors "github.com/pkg/errors"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/hintoptimizer"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/qrm"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	agentpod "github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/spd"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

const (
	defaultQuantitiesSize = 24

	// MaxNUMAScore is the maximum score a numa is expected to return.
	MaxNUMAScore float64 = 100.

	// MinNUMAScore is the minimum score a numa is expected to return.
	MinNUMAScore float64 = 0
)

const (
	metricNameServiceProfileOptimizeHintsFailed  = "service_profile_optimize_hints_failed"
	metricNameServiceProfileOptimizeHintsSuccess = "service_profile_optimize_hints_success"
	metricNameServiceProfileOptimizeHintsLatency = "service_profile_optimize_hints_latency"
)

type NUMAScore struct {
	Score float64
	ID    int
}

type GetRequestFunc func(podUID string) (resource.Quantity, error)

type NormalizeNUMAScoresFunc func([]NUMAScore) error

type resourceFuncs struct {
	normalizeScoreFunc NormalizeNUMAScoresFunc
	defaultRequestFunc GetRequestFunc
}

type serviceProfileHintOptimizer struct {
	emitter               metrics.MetricEmitter
	profilingManager      spd.ServiceProfilingManager
	podFetcher            agentpod.PodFetcher
	resourceWeight        map[v1.ResourceName]float64
	resourceFuncs         map[v1.ResourceName]resourceFuncs
	defaultQuantitiesSize int
}

func NewServiceProfileHintOptimizer(
	emitter metrics.MetricEmitter,
	metaServer *metaserver.MetaServer,
	conf qrm.ServiceProfileHintOptimizerConfig,
) hintoptimizer.HintOptimizer {
	p := &serviceProfileHintOptimizer{
		emitter:               emitter,
		profilingManager:      metaServer,
		podFetcher:            metaServer,
		resourceWeight:        conf.ResourceWeights,
		defaultQuantitiesSize: defaultQuantitiesSize,
	}

	p.resourceFuncs = map[v1.ResourceName]resourceFuncs{
		v1.ResourceCPU: {
			defaultRequestFunc: p.getCPURequest,
			normalizeScoreFunc: spreadNormalizeScore,
		},
		v1.ResourceMemory: {
			defaultRequestFunc: p.getMemoryRequest,
			normalizeScoreFunc: spreadNormalizeScore,
		},
	}

	return p
}

func (p *serviceProfileHintOptimizer) OptimizeHints(
	req *pluginapi.ResourceRequest,
	hints []*pluginapi.TopologyHint,
	machineState state.NUMANodeMap,
) error {
	now := time.Now()
	defer func() {
		_ = p.emitter.StoreInt64(metricNameServiceProfileOptimizeHintsLatency, time.Since(now).Milliseconds(),
			metrics.MetricTypeNameRaw,
		)
	}()

	err := p.optimizeHints(req, hints, machineState)
	if err != nil {
		general.Warningf("optimize hints failed with error: %v", err)
		_ = p.emitter.StoreInt64(metricNameServiceProfileOptimizeHintsFailed, 1,
			metrics.MetricTypeNameRaw,
			p.generateRequestMetricTags(req)...,
		)
	}

	return nil
}

func (p *serviceProfileHintOptimizer) optimizeHints(
	req *pluginapi.ResourceRequest,
	hints []*pluginapi.TopologyHint,
	machineState state.NUMANodeMap,
) error {
	if len(hints) == 0 {
		return nil
	}

	general.Infof("%s/%s origin hints: %v", req.PodNamespace, req.PodName, hints)

	// Extract NUMA node IDs from the hints. Each hint should map to exactly one NUMA node.
	numaNodes, err := p.getNUMANodes(hints)
	if err != nil {
		return err
	}

	numaScores, err := p.score(req, numaNodes, machineState)
	if err != nil {
		if spd.IsSPDNameNotFound(err) {
			// SPD name isn't found, skip optimization.
			general.Infof("%v, skip optimization", err)
			return nil
		}
		return err
	}

	// Sort hints by aggregated NUMA scores in descending order.
	sort.Slice(hints, func(i, j int) bool {
		return numaScores[int(hints[i].Nodes[0])] > numaScores[int(hints[j].Nodes[0])]
	})

	// Mark the highest-ranked hint as preferred, and mark all other hints as non-preferred to
	// make sure that the highest-ranked hint is the only one that is preferred.
	hints[0].Preferred = true
	maxScore := numaScores[int(hints[0].Nodes[0])]
	for i := 1; i < len(hints); i++ {
		if numaScores[int(hints[i].Nodes[0])] == maxScore {
			hints[i].Preferred = true
		} else {
			hints[i].Preferred = false
		}
	}

	_ = p.emitter.StoreInt64(metricNameServiceProfileOptimizeHintsSuccess, 1,
		metrics.MetricTypeNameRaw,
		p.generateRequestMetricTags(req)...,
	)

	general.Infof("optimize hints %s/%s successfully, optimized hints: %v", req.PodNamespace, req.PodName, hints)
	return nil
}

func (p *serviceProfileHintOptimizer) generateRequestMetricTags(req *pluginapi.ResourceRequest) []metrics.MetricTag {
	return []metrics.MetricTag{
		{
			Key: "podName",
			Val: req.PodName,
		},
		{
			Key: "podNamespace",
			Val: req.PodNamespace,
		},
	}
}

// getNUMANodeScores returns a map of NUMA node IDs to their aggregated scores.
func (p *serviceProfileHintOptimizer) score(
	req *pluginapi.ResourceRequest,
	numaNodes []int,
	machineState state.NUMANodeMap,
) (map[int]float64, error) {
	numaScores := make(map[int]float64, len(numaNodes))
	for resourceName, weight := range p.resourceWeight {
		if weight == 0 {
			continue
		}

		scores, err := p.getNUMANodeScores(req, numaNodes, machineState, resourceName)
		if err != nil {
			return nil, err
		}

		general.Infof("%s/%s scores for resource %s before normalize: %v", req.PodNamespace, req.PodName, resourceName, scores)

		if funcs, ok := p.resourceFuncs[resourceName]; ok && funcs.normalizeScoreFunc != nil {
			err = funcs.normalizeScoreFunc(scores)
			if err != nil {
				return nil, err
			}
		}

		general.Infof("%s/%s scores for resource %s after normalize: %v", req.PodNamespace, req.PodName, resourceName, scores)

		for _, score := range scores {
			numaScores[score.ID] += score.Score * weight
		}
	}

	general.Infof("%s/%s aggregated scores: %v", req.PodNamespace, req.PodName, numaScores)

	return numaScores, nil
}

func (p *serviceProfileHintOptimizer) getNUMANodes(hints []*pluginapi.TopologyHint) ([]int, error) {
	numaNodes := make([]int, len(hints))
	for i, hint := range hints {
		if len(hint.Nodes) != 1 {
			return nil, fmt.Errorf("hint %d has invalid node count", i)
		}
		numaNodes[i] = int(hint.Nodes[0])
	}
	return numaNodes, nil
}

func (p *serviceProfileHintOptimizer) getNUMANodeScores(
	req *pluginapi.ResourceRequest,
	numaNodes []int,
	machineState state.NUMANodeMap,
	name v1.ResourceName,
) ([]NUMAScore, error) {
	request, err := p.getContainerServiceProfileRequest(req, name)
	if err != nil {
		return nil, pkgerrors.Wrapf(err, "getting %s/%s service profile request", req.PodNamespace, req.PodName)
	}

	// Calculate scores for each NUMA node.
	var scores []NUMAScore
	for _, numaID := range numaNodes {
		// Get the current allocated pod service profile state for the NUMA node.
		profileState, err := p.getNUMAAllocatedServiceProfileState(machineState, name, numaID)
		if err != nil {
			return nil, err
		}

		err = native.AddQuantities(profileState, request)
		if err != nil {
			return nil, fmt.Errorf("failed to add quantities: %v", err)
		}

		// Compute the NUMA node score based on the aggregated state.
		scores = append(scores, NUMAScore{
			Score: native.AggregateMaxQuantities(profileState).AsApproximateFloat64(),
			ID:    numaID,
		})
	}

	return scores, nil
}

func spreadNormalizeScore(scores []NUMAScore) error {
	// Determine the minimum and maximum scores among the NUMA nodes.
	minScore, maxScore := math.MaxFloat64, 0.0
	for _, score := range scores {
		if score.Score < minScore {
			minScore = score.Score
		}
		if score.Score > maxScore {
			maxScore = score.Score
		}
	}

	// Handle the edge case where all scores are zero.
	if maxScore == 0 {
		for i := range scores {
			scores[i].Score = MaxNUMAScore
		}
		return nil
	}

	// Normalize scores to a range of [MinNUMAScore, MaxNUMAScore], And invert the scores.
	// This is done to ensure that the highest-ranked NUMA node receives the highest score,
	// and the lowest-ranked NUMA node receives the lowest score.
	for i := range scores {
		s := scores[i].Score
		scores[i].Score = MaxNUMAScore * (maxScore + minScore - s) / maxScore
	}
	return nil
}

func (p *serviceProfileHintOptimizer) getContainerServiceProfileRequest(
	req *pluginapi.ResourceRequest,
	name v1.ResourceName,
) ([]resource.Quantity, error) {
	if req == nil {
		return nil, fmt.Errorf("request is nil")
	}

	// Fetch the service profile request from the MetaServer using the provided metadata.
	request, err := spd.GetContainerServiceProfileRequest(p.profilingManager, metav1.ObjectMeta{
		UID:         types.UID(req.PodUid),
		Namespace:   req.PodNamespace,
		Name:        req.PodName,
		Labels:      req.Labels,
		Annotations: req.Annotations,
	}, name)
	if err != nil && !errors.IsNotFound(err) {
		// Log non-"not found" errors. Errors like "SPD name not found" indicate the pod explicitly not require
		// profiling for optimization. In such cases, return the error to skip hint optimization.
		general.Warningf("GetContainerServiceProfileRequest for pod: %s/%s, container: %s failed with error: %v",
			req.PodNamespace, req.PodName, req.ContainerName, err)
		return nil, err
	}

	return p.getServiceProfileRequestWithDefault(req.PodUid, request, name)
}

func (p *serviceProfileHintOptimizer) getContainerServiceProfileState(
	allocationInfo *state.AllocationInfo,
	name v1.ResourceName,
) ([]resource.Quantity, error) {
	if allocationInfo == nil {
		return nil, fmt.Errorf("allocationInfo is nil")
	}

	// Fetch the service profile state from the MetaServer using allocation metadata.
	request, err := spd.GetContainerServiceProfileRequest(p.profilingManager, metav1.ObjectMeta{
		UID:         types.UID(allocationInfo.PodUid),
		Namespace:   allocationInfo.PodNamespace,
		Name:        allocationInfo.PodName,
		Labels:      allocationInfo.Labels,
		Annotations: allocationInfo.Annotations,
	}, name)
	if err != nil && !spd.IsSPDNameOrResourceNotFound(err) {
		// Log non-"spd name or resource not found" errors. Missing SPD name or resource is expected in some cases,
		// such as existing allocations without a corresponding service profile.
		general.Warningf("GetContainerServiceProfileRequest for pod: %s/%s, container: %s failed with error: %v",
			allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName, err)
		return nil, err
	}

	return p.getServiceProfileRequestWithDefault(allocationInfo.PodUid, request, name)
}

func (p *serviceProfileHintOptimizer) getServiceProfileRequestWithDefault(podUID string, request []resource.Quantity, name v1.ResourceName) ([]resource.Quantity, error) {
	// If the request is unavailable, fall back to the default request function.
	if request == nil && p.resourceFuncs[name].defaultRequestFunc != nil {
		quantity, err := p.resourceFuncs[name].defaultRequestFunc(podUID)
		if err != nil {
			return nil, err
		}
		request = native.DuplicateQuantities(quantity, p.defaultQuantitiesSize)
	}
	return request, nil
}

func (p *serviceProfileHintOptimizer) getNUMAAllocatedServiceProfileState(
	machineState state.NUMANodeMap,
	name v1.ResourceName,
	numaID int,
) ([]resource.Quantity, error) {
	if machineState == nil {
		return nil, fmt.Errorf("machineState is nil")
	} else if machineState[numaID] == nil {
		return nil, fmt.Errorf("machineState for NUMA node %d is nil", numaID)
	}

	profileState := native.DuplicateQuantities(resource.Quantity{}, p.defaultQuantitiesSize)
	for _, entries := range machineState[numaID].PodEntries {
		for _, allocationInfo := range entries {
			if !(allocationInfo.CheckSharedNUMABinding() && allocationInfo.CheckMainContainer()) {
				continue
			}

			request, err := p.getContainerServiceProfileState(allocationInfo, name)
			if err != nil {
				return nil, pkgerrors.Wrapf(err, "getting %s/%s service profile state", allocationInfo.PodNamespace, allocationInfo.PodName)
			}

			err = native.AddQuantities(profileState, request)
			if err != nil {
				return nil, err
			}
		}
	}

	return profileState, nil
}

func (p *serviceProfileHintOptimizer) getCPURequest(podUID string) (resource.Quantity, error) {
	var (
		err error
		pod *v1.Pod
	)

	ctx := context.Background()
	pod, err = p.podFetcher.GetPod(ctx, podUID)
	if err != nil {
		ctx = context.WithValue(ctx, agentpod.BypassCacheKey, agentpod.BypassCacheTrue)
		pod, err = p.podFetcher.GetPod(ctx, podUID)
		if err != nil {
			return resource.Quantity{}, err
		}
	}

	quantity := native.CPUQuantityGetter()(native.SumUpPodRequestResources(pod))
	return quantity, nil
}

func (p *serviceProfileHintOptimizer) getMemoryRequest(podUID string) (resource.Quantity, error) {
	var (
		err error
		pod *v1.Pod
	)

	ctx := context.Background()
	pod, err = p.podFetcher.GetPod(ctx, podUID)
	if err != nil {
		ctx = context.WithValue(ctx, agentpod.BypassCacheKey, agentpod.BypassCacheTrue)
		pod, err = p.podFetcher.GetPod(ctx, podUID)
		if err != nil {
			return resource.Quantity{}, err
		}
	}

	quantity := native.MemoryQuantityGetter()(native.SumUpPodRequestResources(pod))
	return quantity, nil
}
