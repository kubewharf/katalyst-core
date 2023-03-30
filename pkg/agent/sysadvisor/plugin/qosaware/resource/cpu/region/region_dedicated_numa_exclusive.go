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

// todo: implement provision and headroom policy for this region

package region

import (
	"fmt"

	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

type QoSRegionDedicatedNumaExclusive struct {
	*QoSRegionBase
}

// NewQoSRegionDedicatedNumaExclusive returns a region instance for dedicated cores
// with numa binding and numa exclusive container
func NewQoSRegionDedicatedNumaExclusive(name string, ownerPoolName string, conf *config.Configuration,
	metaCache *metacache.MetaCache, metaServer *metaserver.MetaServer, emitter metrics.MetricEmitter) QoSRegion {

	r := &QoSRegionDedicatedNumaExclusive{
		QoSRegionBase: NewQoSRegionBase(name, ownerPoolName, types.QoSRegionTypeDedicatedNumaExclusive, metaCache, metaServer, emitter),
	}

	return r
}

func (r *QoSRegionDedicatedNumaExclusive) AddContainer(ci *types.ContainerInfo) error {
	if ci == nil {
		return fmt.Errorf("container info nil")
	}

	r.podSet.Insert(ci.PodUID, ci.ContainerName)

	if len(r.containerTopologyAwareAssignment) <= 0 {
		r.containerTopologyAwareAssignment = ci.TopologyAwareAssignments
	} else {
		// Sanity check: all containers in the region share the same cpuset
		// Do not return error when sanity check fails to prevent unnecessary stall
		if !r.containerTopologyAwareAssignment.Equals(ci.TopologyAwareAssignments) {
			klog.Warningf("[qosaware-cpu] sanity check failed")
		}
	}

	return nil
}

func (r *QoSRegionDedicatedNumaExclusive) TryUpdateProvision() {
}

func (r *QoSRegionDedicatedNumaExclusive) TryUpdateHeadroom() {
}

func (r *QoSRegionDedicatedNumaExclusive) GetProvision() (types.ControlKnob, error) {
	return nil, fmt.Errorf("not supported")
}

func (r *QoSRegionDedicatedNumaExclusive) GetHeadroom() (resource.Quantity, error) {
	return *resource.NewQuantity(int64(0), resource.DecimalSI), nil
}
