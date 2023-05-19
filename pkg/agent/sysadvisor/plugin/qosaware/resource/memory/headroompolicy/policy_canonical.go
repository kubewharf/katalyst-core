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
	"fmt"
	"math"

	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/errors"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/helper"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/sysadvisor/qosaware/resource/memory/headroom"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

type PolicyCanonical struct {
	*PolicyBase

	// memoryHeadroom is valid to be used iff updateStatus successes
	memoryHeadroom float64
	updateStatus   types.PolicyUpdateStatus

	qosConfig             *generic.QoSConfiguration
	policyCanonicalConfig *headroom.MemoryPolicyCanonicalConfiguration
}

func NewPolicyCanonical(conf *config.Configuration, _ interface{}, metaReader metacache.MetaReader,
	metaServer *metaserver.MetaServer, _ metrics.MetricEmitter) HeadroomPolicy {
	p := PolicyCanonical{
		PolicyBase:            NewPolicyBase(metaReader, metaServer),
		updateStatus:          types.PolicyUpdateFailed,
		qosConfig:             conf.QoSConfiguration,
		policyCanonicalConfig: conf.MemoryHeadroomPolicyConfiguration.MemoryPolicyCanonicalConfiguration,
	}

	return &p
}

// estimateNonReclaimedQoSMemoryRequirement estimates the memory requirement of all containers that are not reclaimed
func (p *PolicyCanonical) estimateNonReclaimedQoSMemoryRequirement() (float64, error) {
	var (
		memoryEstimation float64 = 0
		containerCnt     float64 = 0
		errList          []error
	)

	f := func(podUID string, containerName string, ci *types.ContainerInfo) bool {
		containerEstimation, err := helper.EstimateContainerMemoryUsage(ci, p.metaReader, p.essentials.EnableReclaim)
		if err != nil {
			errList = append(errList, err)
			return true
		}

		general.Infof("pod %v container %v estimation %.2e", ci.PodName, containerName, containerEstimation)
		memoryEstimation += containerEstimation
		containerCnt += 1
		return true
	}
	p.metaReader.RangeContainer(f)
	general.Infof("memory requirement estimation: %.2e, #container %v", memoryEstimation, containerCnt)

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

	var (
		memoryEstimateRequirement float64
		memoryBuffer              float64
	)

	maxAllocatableMemory := float64(p.essentials.Total - p.essentials.ReservedForAllocate)
	memoryEstimateRequirement, err = p.estimateNonReclaimedQoSMemoryRequirement()
	if err != nil {
		return err
	}
	memoryHeadroomWithoutBuffer := math.Max(maxAllocatableMemory-memoryEstimateRequirement, 0)

	if p.policyCanonicalConfig.Enabled {
		memoryBuffer, err = p.calculateMemoryBuffer(memoryEstimateRequirement)
		if err != nil {
			return err
		}
	}

	p.memoryHeadroom = math.Max(memoryHeadroomWithoutBuffer+memoryBuffer, 0)
	p.memoryHeadroom = math.Min(p.memoryHeadroom, maxAllocatableMemory)
	general.Infof("without buffer memory headroom: %.2e, final memory headroom: %.2e, memory buffer: %.2e, max memory allocatable: %.2e",
		memoryHeadroomWithoutBuffer, p.memoryHeadroom, memoryBuffer, maxAllocatableMemory)

	return nil
}

func (p *PolicyCanonical) GetHeadroom() (resource.Quantity, error) {
	if p.updateStatus != types.PolicyUpdateSucceeded {
		return resource.Quantity{}, fmt.Errorf("last update failed")
	}

	return *resource.NewQuantity(int64(p.memoryHeadroom), resource.BinarySI), nil
}
