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

package qosawarenoderesources

import (
	"context"
	"fmt"
	"math"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kubeschedulerconfig "k8s.io/kubernetes/pkg/scheduler/apis/config"
	"k8s.io/kubernetes/pkg/scheduler/framework"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/feature"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/noderesources"

	"github.com/kubewharf/katalyst-api/pkg/apis/scheduling/config"
	"github.com/kubewharf/katalyst-api/pkg/apis/scheduling/config/validation"
	"github.com/kubewharf/katalyst-core/pkg/scheduler/cache"
	"github.com/kubewharf/katalyst-core/pkg/scheduler/eventhandlers"
	"github.com/kubewharf/katalyst-core/pkg/scheduler/util"
)

// BalancedAllocation is a score plugin that calculates the difference between the cpu and memory fraction
// of capacity, and prioritizes the host based on how close the two metrics are to each other.
type BalancedAllocation struct {
	handle framework.Handle
	resourceAllocationScorer
	nativeBalancedAllocation *noderesources.BalancedAllocation
}

var _ = framework.ScorePlugin(&BalancedAllocation{})

// BalancedAllocationName is the name of the plugin used in the plugin registry and configurations.
const BalancedAllocationName = "QoSAwareNodeResourcesBalancedAllocation"

// Name returns name of the plugin. It is used in logs, etc.
func (ba *BalancedAllocation) Name() string {
	return BalancedAllocationName
}

// Score invoked at the score extension point.
func (ba *BalancedAllocation) Score(ctx context.Context, state *framework.CycleState, pod *v1.Pod, nodeName string) (int64, *framework.Status) {
	if util.IsReclaimedPod(pod) {
		extendedNodeInfo, err := cache.GetCache().GetNodeInfo(nodeName)
		if err != nil {
			return 0, framework.AsStatus(fmt.Errorf("getting node %q error: %w", nodeName, err))
		}

		// ba.score favors nodes with balanced resource usage rate.
		// It calculates the standard deviation for those resources and prioritizes the node based on how close the usage of those resources is to each other.
		// Detail: score = (1 - std) * MaxNodeScore, where std is calculated by the root square of Σ((fraction(i)-mean)^2)/len(resources)
		// The algorithm is partly inspired by:
		// "Wei Huang et al. An Energy Efficient Virtual Machine Placement Algorithm with Balanced Resource Utilization"
		return ba.score(pod, extendedNodeInfo, nodeName)
	}

	return ba.nativeBalancedAllocation.Score(ctx, state, pod, nodeName)
}

// ScoreExtensions of the Score plugin.
func (ba *BalancedAllocation) ScoreExtensions() framework.ScoreExtensions {
	return nil
}

// NewBalancedAllocation initializes a new plugin and returns it.
func NewBalancedAllocation(baArgs runtime.Object, h framework.Handle) (framework.Plugin, error) {
	args, ok := baArgs.(*config.QoSAwareNodeResourcesBalancedAllocationArgs)
	if !ok {
		return nil, fmt.Errorf("want args to be of type NodeQoSResourcesBalancedAllocationArgs, got %T", baArgs)
	}

	if err := validation.ValidateQoSAwareNodeResourcesBalancedAllocationArgs(nil, args); err != nil {
		return nil, err
	}

	resToWeightMap := make(resourceToWeightMap)
	for _, resource := range args.ReclaimedResources {
		resToWeightMap[v1.ResourceName(resource.Name)] = resource.Weight
	}

	nativeBalancedAllocation, err := newNativeBalancedAllocation(args, h)
	if err != nil {
		return nil, err
	}

	eventhandlers.RegisterCommonPodHandler()
	eventhandlers.RegisterCommonCNRHandler()

	return &BalancedAllocation{
		handle: h,
		resourceAllocationScorer: resourceAllocationScorer{
			Name:                BalancedAllocationName,
			scorer:              balancedResourceScorer,
			useRequested:        true,
			resourceToWeightMap: resToWeightMap,
		},
		nativeBalancedAllocation: nativeBalancedAllocation,
	}, nil
}

func newNativeBalancedAllocation(args *config.QoSAwareNodeResourcesBalancedAllocationArgs, h framework.Handle) (*noderesources.BalancedAllocation, error) {
	nativeBalancedAllocationPlugin, err := noderesources.NewBalancedAllocation(
		&kubeschedulerconfig.NodeResourcesBalancedAllocationArgs{
			Resources: args.Resources,
		}, h, feature.Features{},
	)
	if err != nil {
		return nil, err
	}

	nativeBalancedAllocation, ok := nativeBalancedAllocationPlugin.(*noderesources.BalancedAllocation)
	if !ok {
		return nil, fmt.Errorf("assert nativeBalancedAllocation type error, got %T", nativeBalancedAllocationPlugin)
	}

	return nativeBalancedAllocation, nil
}

func balancedResourceScorer(requested, allocatable resourceToValueMap) int64 {
	var resourceToFractions []float64
	var totalFraction float64
	for name, value := range requested {
		fraction := float64(value) / float64(allocatable[name])
		if fraction > 1 {
			fraction = 1
		}
		totalFraction += fraction
		resourceToFractions = append(resourceToFractions, fraction)
	}

	std := 0.0

	// For most cases, resources are limited to cpu and memory, the std could be simplified to std := (fraction1-fraction2)/2
	// len(fractions) > 2: calculate std based on the well-known formula - root square of Σ((fraction(i)-mean)^2)/len(fractions)
	// Otherwise, set the std to zero is enough.
	if len(resourceToFractions) == 2 {
		std = math.Abs((resourceToFractions[0] - resourceToFractions[1]) / 2)
	} else if len(resourceToFractions) > 2 {
		mean := totalFraction / float64(len(resourceToFractions))
		var sum float64
		for _, fraction := range resourceToFractions {
			sum = sum + (fraction-mean)*(fraction-mean)
		}
		std = math.Sqrt(sum / float64(len(resourceToFractions)))
	}

	// STD (standard deviation) is always a positive value. 1-deviation lets the score to be higher for node which has least deviation and
	// multiplying it with `MaxNodeScore` provides the scaling factor needed.
	return int64((1 - std) * float64(framework.MaxNodeScore))
}
