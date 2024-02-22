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

package headroompolicy

import (
	"context"
	"fmt"
	"math"

	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/helper"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

type PolicyCanonical struct {
	*PolicyBase

	// memoryHeadroom is valid to be used iff updateStatus successes
	memoryHeadroom float64
	updateStatus   types.PolicyUpdateStatus

	conf *config.Configuration
}

func NewPolicyCanonical(conf *config.Configuration, _ interface{}, metaReader metacache.MetaReader,
	metaServer *metaserver.MetaServer, _ metrics.MetricEmitter) HeadroomPolicy {
	p := PolicyCanonical{
		PolicyBase:   NewPolicyBase(metaReader, metaServer),
		updateStatus: types.PolicyUpdateFailed,
		conf:         conf,
	}

	return &p
}

func (p *PolicyCanonical) Name() types.MemoryHeadroomPolicyName {
	return types.MemoryHeadroomPolicyCanonical
}

// estimateNonReclaimedQoSMemoryRequirement estimates the memory requirement of all containers that are not reclaimed
func (p *PolicyCanonical) estimateNonReclaimedQoSMemoryRequirement() (float64, error) {
	var (
		memoryEstimation float64 = 0
		containerCnt     float64 = 0
		errList          []error
	)

	f := func(podUID string, containerName string, ci *types.ContainerInfo) bool {
		var containerEstimation = float64(0)
		defer func() {
			containerCnt += 1
			memoryEstimation += containerEstimation
		}()
		// when ramping up, estimation of cpu should be set as cpu request
		enableReclaim, err := helper.PodEnableReclaim(context.Background(), p.metaServer, podUID, p.essentials.EnableReclaim && !ci.RampUp)
		if err != nil {
			errList = append(errList, err)
			return true
		}

		if ci.IsDedicatedNumaExclusive() && !enableReclaim {
			if ci.ContainerType == v1alpha1.ContainerType_MAIN {
				bindingNumas := machine.GetCPUAssignmentNUMAs(ci.TopologyAwareAssignments)
				for _, numaID := range bindingNumas.ToSliceInt() {
					memoryCap, ok := p.metaServer.MemoryDetails[numaID]
					if !ok {
						errList = append(errList, fmt.Errorf("get memory capacity of numa %v failed", numaID))
						return true
					}
					containerEstimation += float64(memoryCap)
				}
				general.InfoS("occupied memory exclusively",
					"podName", ci.PodName, "containerName", containerName,
					"value", general.FormatMemoryQuantity(containerEstimation))
			} else {
				containerEstimation = 0
			}
			return true
		}

		containerEstimation, err = helper.EstimateContainerMemoryUsage(ci, p.metaReader, enableReclaim)
		if err != nil {
			errList = append(errList, err)
			return true
		}

		general.InfoS("memory estimation", "podName",
			ci.PodName, "containerName", containerName, "value", general.FormatMemoryQuantity(containerEstimation))
		return true
	}
	p.metaReader.RangeContainer(f)

	general.InfoS("memoryEstimation details", "memory requirement estimation", general.FormatMemoryQuantity(memoryEstimation),
		"#container", containerCnt)

	return memoryEstimation, errors.NewAggregate(errList)
}

func (p *PolicyCanonical) Update() (err error) {
	defer func() {
		if err != nil {
			p.updateStatus = types.PolicyUpdateFailed
		} else {
			p.updateStatus = types.PolicyUpdateSucceeded
		}
	}()

	dynamicConfig := p.conf.GetDynamicConfiguration()

	var (
		memoryEstimateRequirement float64
		utilBasedBuffer           float64
	)

	maxAllocatableMemory := p.essentials.ResourceUpperBound - p.essentials.ReservedForAllocate
	memoryEstimateRequirement, err = p.estimateNonReclaimedQoSMemoryRequirement()
	if err != nil {
		return err
	}
	memoryHeadroomWithoutBuffer := math.Max(maxAllocatableMemory-memoryEstimateRequirement, 0)

	if dynamicConfig.MemoryUtilBasedConfiguration.Enable {
		utilBasedBuffer, err = p.calculateUtilBasedBuffer(dynamicConfig, memoryEstimateRequirement)
		if err != nil {
			return err
		}
	}

	p.memoryHeadroom = math.Max(memoryHeadroomWithoutBuffer+utilBasedBuffer, 0)
	p.memoryHeadroom = math.Min(p.memoryHeadroom, maxAllocatableMemory)

	general.InfoS("memory details",
		"without buffer memory headroom", general.FormatMemoryQuantity(memoryHeadroomWithoutBuffer),
		"final memory headroom", general.FormatMemoryQuantity(p.memoryHeadroom),
		"memory buffer", general.FormatMemoryQuantity(utilBasedBuffer),
		"max memory allocatable", general.FormatMemoryQuantity(maxAllocatableMemory),
	)

	return nil
}

func (p *PolicyCanonical) GetHeadroom() (resource.Quantity, error) {
	if p.updateStatus != types.PolicyUpdateSucceeded {
		return resource.Quantity{}, fmt.Errorf("last update failed")
	}

	return *resource.NewQuantity(int64(p.memoryHeadroom), resource.BinarySI), nil
}
