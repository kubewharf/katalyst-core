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

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/helper"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

type PolicyCanonical struct {
	*PolicyBase

	// MemoryHeadroom is valid to be used iff updateStatus successes
	MemoryHeadroom float64
	updateStatus   types.PolicyUpdateStatus
}

func NewPolicyCanonical(_ *config.Configuration, _ interface{}, metaReader metacache.MetaReader,
	metaServer *metaserver.MetaServer, _ metrics.MetricEmitter) HeadroomPolicy {
	p := PolicyCanonical{
		PolicyBase:   NewPolicyBase(metaReader, metaServer),
		updateStatus: types.PolicyUpdateFailed,
	}

	return &p
}

func (p *PolicyCanonical) estimateContainerMemoryUsage(ci *types.ContainerInfo) (float64, error) {
	return helper.EstimateContainerResourceUsage(ci, v1.ResourceMemory, p.metaReader, p.essentials.EnableReclaim)
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
		memoryEstimation float64 = 0
		containerCnt     float64 = 0
		errList          []error
	)

	f := func(podUID string, containerName string, ci *types.ContainerInfo) bool {
		containerEstimation, err := p.estimateContainerMemoryUsage(ci)
		if err != nil {
			errList = append(errList, err)
			return true
		}

		klog.Infof("[qosaware-memory-headroom] pod %v container %v estimation %.2e", ci.PodName, containerName, containerEstimation)
		memoryEstimation += containerEstimation
		containerCnt += 1
		return true
	}
	p.metaReader.RangeContainer(f)
	klog.Infof("[qosaware-memory-headroom] memory requirement estimation: %.2e, #container %v", memoryEstimation, containerCnt)

	p.MemoryHeadroom = math.Max(float64(p.essentials.Total-p.essentials.ReservedForAllocate)-memoryEstimation, 0)

	return errors.NewAggregate(errList)
}

func (p *PolicyCanonical) GetHeadroom() (resource.Quantity, error) {
	if p.updateStatus != types.PolicyUpdateSucceeded {
		return resource.Quantity{}, fmt.Errorf("last update failed")
	}

	return *resource.NewQuantity(int64(p.MemoryHeadroom), resource.DecimalSI), nil
}
