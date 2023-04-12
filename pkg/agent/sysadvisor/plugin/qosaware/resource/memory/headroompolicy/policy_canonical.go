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

	// memoryHeadroom is valid to be used iff updateStatus successes
	memoryHeadroom float64
	updateStatus   types.PolicyUpdateStatus
}

func NewPolicyCanonical(_ *config.Configuration, _ interface{}, metaReader metacache.MetaReader,
	metaServer *metaserver.MetaServer, _ metrics.MetricEmitter) HeadroomPolicy {
	p := PolicyCanonical{
		PolicyBase: NewPolicyBase(metaReader, metaServer),

		updateStatus: types.PolicyUpdateFailed,
	}

	return &p
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
		containerEstimation := 0.0
		if !p.EnableReclaim {
			containerEstimation = ci.MemoryRequest
		} else {
			containerEstimation, err = helper.EstimateContainerResourceUsage(ci, v1.ResourceMemory, p.MetaReader)
			if err != nil {
				errList = append(errList, err)
				return true
			}
		}
		klog.Infof("[qosaware-memory-headroom] pod %v container %v estimation %.2e", ci.PodName, containerName, containerEstimation)
		memoryEstimation += containerEstimation
		containerCnt += 1
		return true
	}
	p.MetaReader.RangeContainer(f)
	klog.Infof("[qosaware-memory-headroom] memory requirement estimation: %.2e, #container %v", memoryEstimation, containerCnt)

	p.memoryHeadroom = math.Max(float64(p.Total-p.ReservedForAllocate)-memoryEstimation, 0)

	return errors.NewAggregate(errList)
}

func (p *PolicyCanonical) GetHeadroom() (resource.Quantity, error) {
	if p.updateStatus != types.PolicyUpdateSucceeded {
		return resource.Quantity{}, fmt.Errorf("last update failed")
	}

	return *resource.NewQuantity(int64(p.memoryHeadroom), resource.DecimalSI), nil
}
